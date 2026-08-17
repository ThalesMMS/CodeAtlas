package httpapi

import (
	"encoding/json"
	"fmt"
	"strconv"
	"strings"
	"sync"

	"github.com/ThalesMMS/CodeAtlas/internal/domain"
)

const defaultSSEHistory = 512

type sseEvent struct {
	ID       string
	Type     string
	Data     []byte
	sequence uint64
}

// sseBroker merges index and job events into one globally ordered stream and
// retains a bounded replay window for EventSource reconnections.
type sseBroker struct {
	mu          sync.Mutex
	sequence    uint64
	history     []sseEvent
	capacity    int
	subscribers map[chan sseEvent]struct{}
	closed      bool
	onDrop      func()
}

func (b *sseBroker) SetDropObserver(observer func()) {
	b.mu.Lock()
	b.onDrop = observer
	b.mu.Unlock()
}

func newSSEBroker(capacity int) *sseBroker {
	if capacity <= 0 {
		capacity = defaultSSEHistory
	}
	return &sseBroker{capacity: capacity, subscribers: make(map[chan sseEvent]struct{})}
}

func (b *sseBroker) PublishIndex(event domain.IndexEvent) {
	b.publish("index", func(id string) ([]byte, error) {
		event.ID = id
		return json.Marshal(event)
	})
}

func (b *sseBroker) PublishJob(event domain.JobEvent) {
	b.publish("job", func(id string) ([]byte, error) {
		event.ID = id
		return json.Marshal(event)
	})
}

func (b *sseBroker) publish(eventType string, encode func(string) ([]byte, error)) {
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.closed {
		return
	}
	nextSequence := b.sequence + 1
	id := fmt.Sprintf("evt-%d", nextSequence)
	data, err := encode(id)
	if err != nil {
		return
	}
	b.sequence = nextSequence
	event := sseEvent{ID: id, Type: eventType, Data: data, sequence: nextSequence}
	b.history = append(b.history, event)
	if overflow := len(b.history) - b.capacity; overflow > 0 {
		copy(b.history, b.history[overflow:])
		b.history = b.history[:b.capacity]
	}
	for subscriber := range b.subscribers {
		select {
		case subscriber <- event:
		default:
			delete(b.subscribers, subscriber)
			close(subscriber)
			if b.onDrop != nil {
				b.onDrop()
			}
		}
	}
}

// Subscribe atomically installs a live subscriber and returns any retained
// events after lastEventID. resync is true when the requested history is no
// longer available or the ID is malformed/ahead of the server.
func (b *sseBroker) Subscribe(lastEventID string) (<-chan sseEvent, []sseEvent, bool, func()) {
	b.mu.Lock()
	defer b.mu.Unlock()
	channel := make(chan sseEvent, 64)
	if b.closed {
		close(channel)
		return channel, nil, false, func() {}
	}
	b.subscribers[channel] = struct{}{}
	replay, resync := b.replayLocked(strings.TrimSpace(lastEventID))
	return channel, replay, resync, func() {
		b.mu.Lock()
		defer b.mu.Unlock()
		if _, exists := b.subscribers[channel]; exists {
			delete(b.subscribers, channel)
			close(channel)
		}
	}
}

func (b *sseBroker) replayLocked(lastEventID string) ([]sseEvent, bool) {
	if lastEventID == "" {
		return nil, false
	}
	last, ok := parseSSEEventID(lastEventID)
	if !ok || last > b.sequence {
		return nil, true
	}
	if len(b.history) > 0 && last+1 < b.history[0].sequence {
		return nil, true
	}
	replay := make([]sseEvent, 0)
	for _, event := range b.history {
		if event.sequence > last {
			replay = append(replay, event)
		}
	}
	return replay, false
}

func parseSSEEventID(id string) (uint64, bool) {
	if !strings.HasPrefix(id, "evt-") {
		return 0, false
	}
	value, err := strconv.ParseUint(strings.TrimPrefix(id, "evt-"), 10, 64)
	return value, err == nil
}

func (b *sseBroker) Close() {
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.closed {
		return
	}
	b.closed = true
	for subscriber := range b.subscribers {
		delete(b.subscribers, subscriber)
		close(subscriber)
	}
}
