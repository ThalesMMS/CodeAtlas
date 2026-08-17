package lspclient

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"strings"
	"sync"
	"testing"
	"time"
)

type testServer struct {
	reader *bufio.Reader
	writer io.Writer
	mu     sync.Mutex
}

type countingReader struct {
	reader    io.Reader
	bytesRead int
}

func (r *countingReader) Read(buffer []byte) (int, error) {
	read, err := r.reader.Read(buffer)
	r.bytesRead += read
	return read, err
}

func (s *testServer) read(t *testing.T) wireMessage {
	t.Helper()
	payload, err := readMessage(s.reader, 1<<20)
	if err != nil {
		t.Fatalf("server read: %v", err)
	}
	var msg wireMessage
	if err := json.Unmarshal(payload, &msg); err != nil {
		t.Fatalf("server decode: %v", err)
	}
	return msg
}

func (s *testServer) send(t *testing.T, msg wireMessage) {
	t.Helper()
	payload, _ := json.Marshal(msg)
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := writeMessage(s.writer, payload); err != nil {
		t.Fatalf("server write: %v", err)
	}
}

func newTestConn(t *testing.T) (*conn, *testServer, *io.PipeWriter) {
	t.Helper()
	clientReader, serverWriter := io.Pipe() // server → client
	serverReader, clientWriter := io.Pipe() // client → server
	c := newConn(clientReader, clientWriter, clientWriter, 1<<20)
	c.start()
	server := &testServer{reader: bufio.NewReader(serverReader), writer: serverWriter}
	t.Cleanup(func() { c.closeTransport() })
	return c, server, serverWriter
}

func TestRequestResponseRoundTrip(t *testing.T) {
	t.Parallel()
	c, server, _ := newTestConn(t)
	go func() {
		msg := server.read(t)
		result, _ := json.Marshal(map[string]string{"echo": msg.Method})
		server.send(t, wireMessage{JSONRPC: "2.0", ID: msg.ID, Result: result})
	}()
	var out map[string]string
	if err := c.Request(context.Background(), "ping", map[string]int{"x": 1}, &out); err != nil {
		t.Fatalf("Request: %v", err)
	}
	if out["echo"] != "ping" {
		t.Fatalf("unexpected result: %v", out)
	}
}

func TestOutOfOrderResponsesCorrelate(t *testing.T) {
	t.Parallel()
	c, server, _ := newTestConn(t)
	go func() {
		first := server.read(t)
		second := server.read(t)
		// Respond to the second request before the first, echoing each request's
		// method so correlation is verifiable regardless of which was sent first.
		server.send(t, wireMessage{JSONRPC: "2.0", ID: second.ID, Result: json.RawMessage(`"` + second.Method + `"`)})
		server.send(t, wireMessage{JSONRPC: "2.0", ID: first.ID, Result: json.RawMessage(`"` + first.Method + `"`)})
	}()
	var wg sync.WaitGroup
	results := make([]string, 2)
	for i, method := range []string{"alpha", "beta"} {
		wg.Add(1)
		go func(index int, m string) {
			defer wg.Done()
			_ = c.Request(context.Background(), m, nil, &results[index])
		}(i, method)
	}
	wg.Wait()
	// Each request received exactly its own method back, despite the reversed order.
	if results[0] != "alpha" || results[1] != "beta" {
		t.Fatalf("responses mis-correlated: %v", results)
	}
}

func TestServerNotificationReachesHandler(t *testing.T) {
	t.Parallel()
	c, server, _ := newTestConn(t)
	got := make(chan string, 1)
	c.onNotification("window/logMessage", func(params json.RawMessage) {
		got <- string(params)
	})
	server.send(t, wireMessage{JSONRPC: "2.0", Method: "window/logMessage", Params: json.RawMessage(`{"message":"hi"}`)})
	select {
	case params := <-got:
		if !strings.Contains(params, "hi") {
			t.Fatalf("notification params lost: %s", params)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("notification handler not invoked")
	}
}

func TestServerNotificationsRunSequentiallyInWireOrder(t *testing.T) {
	t.Parallel()
	c, server, _ := newTestConn(t)
	firstStarted := make(chan struct{})
	releaseFirst := make(chan struct{})
	completed := make(chan int, 2)
	c.onNotification("textDocument/publishDiagnostics", func(params json.RawMessage) {
		var payload struct {
			Revision int `json:"revision"`
		}
		_ = json.Unmarshal(params, &payload)
		if payload.Revision == 1 {
			close(firstStarted)
			<-releaseFirst
		}
		completed <- payload.Revision
	})

	server.send(t, wireMessage{JSONRPC: "2.0", Method: "textDocument/publishDiagnostics", Params: json.RawMessage(`{"revision":1}`)})
	<-firstStarted
	server.send(t, wireMessage{JSONRPC: "2.0", Method: "textDocument/publishDiagnostics", Params: json.RawMessage(`{"revision":2}`)})
	select {
	case revision := <-completed:
		t.Fatalf("notification %d completed while revision 1 was blocked", revision)
	case <-time.After(50 * time.Millisecond):
	}
	close(releaseFirst)
	if first, second := <-completed, <-completed; first != 1 || second != 2 {
		t.Fatalf("completion order = %d,%d, want 1,2", first, second)
	}
}

func TestServerRequestGetsResponse(t *testing.T) {
	t.Parallel()
	c, server, _ := newTestConn(t)
	c.onRequest("workspace/configuration", func(_ context.Context, _ json.RawMessage) (any, error) {
		return []map[string]bool{{"enabled": true}}, nil
	})
	id := json.RawMessage(`99`)
	server.send(t, wireMessage{JSONRPC: "2.0", ID: &id, Method: "workspace/configuration", Params: json.RawMessage(`{}`)})
	reply := server.read(t)
	if reply.ID == nil || string(*reply.ID) != "99" || !strings.Contains(string(reply.Result), "enabled") {
		t.Fatalf("unexpected server-request reply: %+v", reply)
	}
}

func TestCancellationSendsCancelAndFailsFast(t *testing.T) {
	t.Parallel()
	c, server, _ := newTestConn(t)
	ctx, cancel := context.WithCancel(context.Background())
	go func() {
		server.read(t) // receive the request, never respond
		cancel()       // then cancel the client context
	}()
	err := c.Request(ctx, "slow/op", nil, nil)
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("expected context.Canceled, got %v", err)
	}
	// The client should have sent a best-effort $/cancelRequest.
	cancelMsg := server.read(t)
	if cancelMsg.Method != "$/cancelRequest" {
		t.Fatalf("expected $/cancelRequest, got %q", cancelMsg.Method)
	}
}

func TestTransportFailureCompletesPending(t *testing.T) {
	t.Parallel()
	c, _, serverWriter := newTestConn(t)
	errCh := make(chan error, 1)
	go func() { errCh <- c.Request(context.Background(), "x", nil, nil) }()
	time.Sleep(20 * time.Millisecond)
	serverWriter.Close() // server → client EOF: transport fails
	select {
	case err := <-errCh:
		if err == nil {
			t.Fatal("pending request should fail on transport loss")
		}
	case <-time.After(2 * time.Second):
		t.Fatal("pending request not completed after transport failure")
	}
	if c.State() != StateFailed {
		t.Fatalf("state = %v, want failed", c.State())
	}
	if err := c.Request(context.Background(), "y", nil, nil); !errors.Is(err, ErrClosed) {
		t.Fatalf("request after failure = %v, want ErrClosed", err)
	}
}

func TestReadMessageRejectsOversizedAndMalformed(t *testing.T) {
	t.Parallel()
	// Oversized declared length is rejected before allocation.
	oversized := bufio.NewReader(strings.NewReader("Content-Length: 999999\r\n\r\n"))
	if _, err := readMessage(oversized, 16); !errors.Is(err, errMessageTooLarge) {
		t.Fatalf("oversized = %v, want errMessageTooLarge", err)
	}
	// Missing Content-Length is rejected.
	noLen := bufio.NewReader(strings.NewReader("Content-Type: x\r\n\r\n"))
	if _, err := readMessage(noLen, 1024); !errors.Is(err, errNoContentLength) {
		t.Fatalf("no content-length = %v", err)
	}
	// A valid frame round-trips.
	var buf bytes.Buffer
	if err := writeMessage(&buf, []byte(`{"ok":true}`)); err != nil {
		t.Fatal(err)
	}
	payload, err := readMessage(bufio.NewReader(&buf), 1024)
	if err != nil || string(payload) != `{"ok":true}` {
		t.Fatalf("round-trip failed: %q %v", payload, err)
	}
}

func TestReadHeaderLineEnforcesLimitWhileReading(t *testing.T) {
	t.Parallel()
	source := &countingReader{reader: strings.NewReader(strings.Repeat("x", maxHeaderLineBytes+(1<<20)))}
	reader := bufio.NewReaderSize(source, 128)

	if _, err := readHeaderLine(reader); !errors.Is(err, errHeaderTooLong) {
		t.Fatalf("readHeaderLine() error = %v, want errHeaderTooLong", err)
	}
	if source.bytesRead > maxHeaderLineBytes {
		t.Fatalf("readHeaderLine() consumed %d bytes before rejecting, limit = %d", source.bytesRead, maxHeaderLineBytes)
	}
}

func TestReadHeaderLineAcceptsExactLimit(t *testing.T) {
	t.Parallel()
	content := strings.Repeat("x", maxHeaderLineBytes-1)
	line, err := readHeaderLine(bufio.NewReaderSize(strings.NewReader(content+"\n"), 128))
	if err != nil {
		t.Fatalf("readHeaderLine() error = %v", err)
	}
	if line != content {
		t.Fatalf("readHeaderLine() length = %d, want %d", len(line), len(content))
	}
}
