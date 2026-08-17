package httpapi

import (
	"bufio"
	"context"
	"io"
	"math"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/ThalesMMS/CodeAtlas/internal/domain"
	"github.com/ThalesMMS/CodeAtlas/internal/observability"
)

func TestSSEBrokerMergesAndReplaysGlobalOrder(t *testing.T) {
	t.Parallel()
	broker := newSSEBroker(4)
	defer broker.Close()
	broker.PublishIndex(domain.IndexEvent{Type: "index.changed"})
	broker.PublishJob(domain.JobEvent{Type: "job.running"})
	broker.PublishIndex(domain.IndexEvent{Type: "index.done"})

	channel, replay, resync, cancel := broker.Subscribe("evt-1")
	defer cancel()
	if resync || len(replay) != 2 {
		t.Fatalf("replay/resync = %v/%v, want two replayed events", replay, resync)
	}
	if replay[0].ID != "evt-2" || replay[0].Type != "job" || replay[1].ID != "evt-3" || replay[1].Type != "index" {
		t.Fatalf("replay = %+v, want globally ordered job then index", replay)
	}

	broker.PublishJob(domain.JobEvent{Type: "job.succeeded"})
	select {
	case event := <-channel:
		if event.ID != "evt-4" || event.Type != "job" {
			t.Fatalf("live event = %+v, want evt-4 job", event)
		}
	case <-time.After(time.Second):
		t.Fatal("live event was not delivered after replay subscription")
	}
}

func TestSSEBrokerRequestsResyncOutsideReplayWindow(t *testing.T) {
	t.Parallel()
	broker := newSSEBroker(2)
	defer broker.Close()
	for index := 0; index < 4; index++ {
		broker.PublishIndex(domain.IndexEvent{Type: "changed"})
	}
	_, replay, resync, cancel := broker.Subscribe("evt-1")
	defer cancel()
	if !resync || len(replay) != 0 {
		t.Fatalf("replay/resync = %v/%v, want resync without partial replay", replay, resync)
	}
}

func TestSSEBrokerRecordsSlowSubscriberDrops(t *testing.T) {
	t.Parallel()
	broker := newSSEBroker(1)
	defer broker.Close()
	metrics := observability.NewMetrics()
	broker.SetDropObserver(metrics.SSEEventDropped)
	events, _, _, cancel := broker.Subscribe("")
	defer cancel()
	for index := 0; index < 65; index++ {
		broker.PublishIndex(domain.IndexEvent{Type: "changed"})
	}
	if got := metrics.Snapshot().SSEEventsDropped; got != 1 {
		t.Fatalf("sseEventsDropped = %d, want 1", got)
	}
	broker.mu.Lock()
	subscribers := len(broker.subscribers)
	broker.mu.Unlock()
	if subscribers != 0 {
		t.Fatalf("slow subscriber remains registered: %d", subscribers)
	}
	for range events {
	}
}

func TestSSEBrokerSkipsEventsThatCannotBeEncoded(t *testing.T) {
	t.Parallel()
	broker := newSSEBroker(4)
	defer broker.Close()
	events, _, _, cancel := broker.Subscribe("")
	defer cancel()
	invalidPercent := math.NaN()
	broker.PublishJob(domain.JobEvent{Type: "job.progress", Job: domain.JobSnapshot{
		Progress: domain.Progress{Percent: &invalidPercent},
	}})

	broker.mu.Lock()
	sequence, history := broker.sequence, len(broker.history)
	broker.mu.Unlock()
	if sequence != 0 || history != 0 {
		t.Fatalf("failed encoding advanced broker state: sequence=%d history=%d", sequence, history)
	}
	select {
	case event := <-events:
		t.Fatalf("failed encoding was broadcast: %+v", event)
	default:
	}

	broker.PublishIndex(domain.IndexEvent{Type: "index.changed"})
	select {
	case event := <-events:
		if event.ID != "evt-1" {
			t.Fatalf("first valid event ID = %q, want evt-1", event.ID)
		}
	case <-time.After(time.Second):
		t.Fatal("valid event was not delivered after encoding failure")
	}
}

func TestEventsHandlerClearsServerWriteTimeout(t *testing.T) {
	t.Parallel()
	broker := newSSEBroker(8)
	defer broker.Close()
	server := &Server{events: broker}
	testServer := httptest.NewUnstartedServer(http.HandlerFunc(func(response http.ResponseWriter, request *http.Request) {
		server.handleEvents(&statusRecorder{ResponseWriter: response}, request)
	}))
	testServer.Config.WriteTimeout = 40 * time.Millisecond
	testServer.Start()
	defer testServer.Close()

	request, err := http.NewRequestWithContext(context.Background(), http.MethodGet, testServer.URL, nil)
	if err != nil {
		t.Fatal(err)
	}
	response, err := testServer.Client().Do(request)
	if err != nil {
		t.Fatal(err)
	}
	defer response.Body.Close()
	reader := bufio.NewReader(response.Body)
	readUntilBlankLine(t, reader)

	time.Sleep(3 * testServer.Config.WriteTimeout)
	broker.PublishIndex(domain.IndexEvent{Type: "after-timeout"})
	block := readUntilBlankLine(t, reader)
	if !strings.Contains(block, "id: evt-1") || !strings.Contains(block, "event: index") {
		t.Fatalf("event after server WriteTimeout = %q", block)
	}
}

func TestEventsHandlerHonorsLastEventID(t *testing.T) {
	t.Parallel()
	broker := newSSEBroker(8)
	defer broker.Close()
	broker.PublishIndex(domain.IndexEvent{Type: "one"})
	broker.PublishJob(domain.JobEvent{Type: "two"})
	broker.PublishIndex(domain.IndexEvent{Type: "three"})
	server := &Server{events: broker}
	testServer := httptest.NewServer(http.HandlerFunc(server.handleEvents))
	defer testServer.Close()

	request, err := http.NewRequest(http.MethodGet, testServer.URL, nil)
	if err != nil {
		t.Fatal(err)
	}
	request.Header.Set("Last-Event-ID", "evt-1")
	response, err := testServer.Client().Do(request)
	if err != nil {
		t.Fatal(err)
	}
	defer response.Body.Close()
	reader := bufio.NewReader(response.Body)
	readUntilBlankLine(t, reader) // connected
	second := readUntilBlankLine(t, reader)
	third := readUntilBlankLine(t, reader)
	if !strings.Contains(second, "id: evt-2") || !strings.Contains(second, "event: job") {
		t.Fatalf("first replay = %q", second)
	}
	if !strings.Contains(third, "id: evt-3") || !strings.Contains(third, "event: index") {
		t.Fatalf("second replay = %q", third)
	}
}

func readUntilBlankLine(t *testing.T, reader *bufio.Reader) string {
	t.Helper()
	result := strings.Builder{}
	done := make(chan error, 1)
	go func() {
		for {
			line, err := reader.ReadString('\n')
			if err != nil {
				done <- err
				return
			}
			result.WriteString(line)
			if line == "\n" {
				done <- nil
				return
			}
		}
	}()
	select {
	case err := <-done:
		if err != nil && err != io.EOF {
			t.Fatal(err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("timed out reading SSE block")
	}
	return result.String()
}
