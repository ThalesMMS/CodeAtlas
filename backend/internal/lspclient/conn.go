package lspclient

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"sync"
)

// ClientState is the transport lifecycle state.
type ClientState int

const (
	StateNew ClientState = iota
	StateRunning
	StateClosing
	StateClosed
	StateFailed
)

func (s ClientState) String() string {
	switch s {
	case StateRunning:
		return "running"
	case StateClosing:
		return "closing"
	case StateClosed:
		return "closed"
	case StateFailed:
		return "failed"
	default:
		return "new"
	}
}

// ErrClosed is returned when sending on a closing/closed/failed transport.
var ErrClosed = errors.New("lspclient: transport is closed")

// NotificationHandler handles a server→client notification.
type NotificationHandler func(params json.RawMessage)

// ServerRequestHandler handles a server→client request, returning a result or an
// error to send back.
type ServerRequestHandler func(ctx context.Context, params json.RawMessage) (any, error)

// RPCError is a JSON-RPC error response.
type RPCError struct {
	Code    int             `json:"code"`
	Message string          `json:"message"`
	Data    json.RawMessage `json:"data,omitempty"`
}

func (e *RPCError) Error() string { return fmt.Sprintf("jsonrpc error %d: %s", e.Code, e.Message) }

type wireMessage struct {
	JSONRPC string           `json:"jsonrpc"`
	ID      *json.RawMessage `json:"id,omitempty"`
	Method  string           `json:"method,omitempty"`
	Params  json.RawMessage  `json:"params,omitempty"`
	Result  json.RawMessage  `json:"result,omitempty"`
	Error   *RPCError        `json:"error,omitempty"`
}

type pending struct {
	ch chan response
}

type response struct {
	result json.RawMessage
	err    error
}

type notificationDispatch struct {
	handler NotificationHandler
	params  json.RawMessage
}

// conn is the JSON-RPC protocol layer over an io.Reader/io.WriteCloser. It is
// independent of process management and so is fully testable over pipes.
type conn struct {
	reader   *bufio.Reader
	writer   io.Writer
	closer   io.Closer
	maxBytes int64

	writeMu sync.Mutex // serializes all writes

	mu              sync.Mutex
	state           ClientState
	nextID          int64
	pending         map[int64]*pending
	notifyHandlers  map[string]NotificationHandler
	requestHandlers map[string]ServerRequestHandler
	notifications   chan notificationDispatch
	closeErr        error
	done            chan struct{}
}

func newConn(reader io.Reader, writer io.Writer, closer io.Closer, maxBytes int64) *conn {
	return &conn{
		reader:          bufio.NewReader(reader),
		writer:          writer,
		closer:          closer,
		maxBytes:        maxBytes,
		state:           StateNew,
		pending:         make(map[int64]*pending),
		notifyHandlers:  make(map[string]NotificationHandler),
		requestHandlers: make(map[string]ServerRequestHandler),
		notifications:   make(chan notificationDispatch, 64),
		done:            make(chan struct{}),
	}
}

func (c *conn) start() {
	c.mu.Lock()
	c.state = StateRunning
	c.mu.Unlock()
	go c.notificationLoop()
	go c.readLoop()
}

func (c *conn) notificationLoop() {
	for {
		select {
		case notification := <-c.notifications:
			notification.handler(notification.params)
		case <-c.done:
			return
		}
	}
}

func (c *conn) State() ClientState {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.state
}

func (c *conn) onNotification(method string, handler NotificationHandler) {
	c.mu.Lock()
	c.notifyHandlers[method] = handler
	c.mu.Unlock()
}

func (c *conn) onRequest(method string, handler ServerRequestHandler) {
	c.mu.Lock()
	c.requestHandlers[method] = handler
	c.mu.Unlock()
}

// Notify sends a client→server notification (no id, no response).
func (c *conn) Notify(_ context.Context, method string, params any) error {
	raw, err := marshalParams(params)
	if err != nil {
		return err
	}
	return c.writeMessage(wireMessage{JSONRPC: "2.0", Method: method, Params: raw})
}

// Request sends a client→server request and waits for the correlated response,
// applying defaultTimeout only when the caller's ctx has no earlier deadline.
func (c *conn) Request(ctx context.Context, method string, params any, result any) error {
	raw, err := marshalParams(params)
	if err != nil {
		return err
	}
	c.mu.Lock()
	if c.state != StateRunning {
		c.mu.Unlock()
		return ErrClosed
	}
	id := c.nextID
	c.nextID++
	slot := &pending{ch: make(chan response, 1)}
	c.pending[id] = slot
	c.mu.Unlock()

	idRaw := json.RawMessage(fmt.Sprintf("%d", id))
	if err := c.writeMessage(wireMessage{JSONRPC: "2.0", ID: &idRaw, Method: method, Params: raw}); err != nil {
		c.removePending(id)
		return err
	}

	select {
	case res := <-slot.ch:
		if res.err != nil {
			return res.err
		}
		if result != nil && len(res.result) > 0 {
			return json.Unmarshal(res.result, result)
		}
		return nil
	case <-ctx.Done():
		// Invalidate the pending entry race-safely and best-effort cancel upstream.
		// The cancel notification is fired asynchronously so a stuck/slow writer
		// never blocks the caller's cancellation; a late response is discarded.
		c.removePending(id)
		go func() { _ = c.Notify(context.Background(), "$/cancelRequest", cancelParams{ID: id}) }()
		return fmt.Errorf("lspclient: request %q canceled: %w", method, ctx.Err())
	case <-c.done:
		return c.transportErr()
	}
}

type cancelParams struct {
	ID int64 `json:"id"`
}

func (c *conn) writeMessage(msg wireMessage) error {
	payload, err := json.Marshal(msg)
	if err != nil {
		return err
	}
	c.writeMu.Lock()
	defer c.writeMu.Unlock()
	c.mu.Lock()
	if c.state == StateClosed || c.state == StateFailed {
		c.mu.Unlock()
		return ErrClosed
	}
	c.mu.Unlock()
	if err := writeMessage(c.writer, payload); err != nil {
		c.fail(fmt.Errorf("lspclient: write: %w", err))
		return ErrClosed
	}
	return nil
}

func (c *conn) readLoop() {
	for {
		payload, err := readMessage(c.reader, c.maxBytes)
		if err != nil {
			// A framing/transport loss is terminal: never try to resync forever.
			c.fail(fmt.Errorf("lspclient: read: %w", err))
			return
		}
		var msg wireMessage
		if err := json.Unmarshal(payload, &msg); err != nil {
			c.fail(fmt.Errorf("lspclient: invalid JSON: %w", err))
			return
		}
		c.dispatch(msg)
	}
}

func (c *conn) dispatch(msg wireMessage) {
	switch {
	case msg.Method != "" && msg.ID == nil:
		c.dispatchNotification(msg)
	case msg.Method != "" && msg.ID != nil:
		c.dispatchServerRequest(msg)
	case msg.ID != nil:
		c.dispatchResponse(msg)
	default:
		// An id-less, method-less message is meaningless; drop it.
	}
}

func (c *conn) dispatchNotification(msg wireMessage) {
	c.mu.Lock()
	handler := c.notifyHandlers[msg.Method]
	c.mu.Unlock()
	if handler == nil {
		return
	}
	// One dispatcher preserves wire order. The bounded queue lets the read loop
	// continue through ordinary bursts while applying backpressure to a server that
	// publishes faster than handlers can consume.
	select {
	case c.notifications <- notificationDispatch{handler: handler, params: copyRaw(msg.Params)}:
	case <-c.done:
	}
}

func (c *conn) dispatchServerRequest(msg wireMessage) {
	c.mu.Lock()
	handler := c.requestHandlers[msg.Method]
	c.mu.Unlock()
	id := *msg.ID
	params := copyRaw(msg.Params)
	go func() {
		if handler == nil {
			_ = c.writeMessage(wireMessage{JSONRPC: "2.0", ID: &id, Error: &RPCError{Code: -32601, Message: "method not found"}})
			return
		}
		result, err := handler(context.Background(), params)
		if err != nil {
			_ = c.writeMessage(wireMessage{JSONRPC: "2.0", ID: &id, Error: &RPCError{Code: -32603, Message: err.Error()}})
			return
		}
		raw, mErr := marshalParams(result)
		if mErr != nil {
			_ = c.writeMessage(wireMessage{JSONRPC: "2.0", ID: &id, Error: &RPCError{Code: -32603, Message: "marshal result"}})
			return
		}
		_ = c.writeMessage(wireMessage{JSONRPC: "2.0", ID: &id, Result: raw})
	}()
}

func (c *conn) dispatchResponse(msg wireMessage) {
	var id int64
	if err := json.Unmarshal(*msg.ID, &id); err != nil {
		return // non-integer id never matches a client request; discard
	}
	slot := c.takePending(id)
	if slot == nil {
		return // unknown/late id: discard without panic
	}
	var resErr error
	if msg.Error != nil {
		resErr = msg.Error
	}
	slot.ch <- response{result: copyRaw(msg.Result), err: resErr}
}

func (c *conn) removePending(id int64) {
	c.mu.Lock()
	delete(c.pending, id)
	c.mu.Unlock()
}

func (c *conn) takePending(id int64) *pending {
	c.mu.Lock()
	defer c.mu.Unlock()
	slot := c.pending[id]
	delete(c.pending, id)
	return slot
}

// fail marks the transport failed and completes every pending request with the
// error. It is idempotent.
func (c *conn) fail(err error) {
	c.mu.Lock()
	if c.state == StateFailed || c.state == StateClosed {
		c.mu.Unlock()
		return
	}
	c.state = StateFailed
	c.closeErr = err
	waiters := c.pending
	c.pending = make(map[int64]*pending)
	close(c.done)
	c.mu.Unlock()
	for _, slot := range waiters {
		slot.ch <- response{err: err}
	}
	if c.closer != nil {
		_ = c.closer.Close()
	}
}

// closeTransport marks closed and completes pending with ErrClosed. Idempotent.
func (c *conn) closeTransport() {
	c.mu.Lock()
	if c.state == StateClosed || c.state == StateFailed {
		c.mu.Unlock()
		return
	}
	c.state = StateClosed
	c.closeErr = ErrClosed
	waiters := c.pending
	c.pending = make(map[int64]*pending)
	close(c.done)
	c.mu.Unlock()
	for _, slot := range waiters {
		slot.ch <- response{err: ErrClosed}
	}
	if c.closer != nil {
		_ = c.closer.Close()
	}
}

func (c *conn) transportErr() error {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.closeErr != nil {
		return c.closeErr
	}
	return ErrClosed
}

func marshalParams(params any) (json.RawMessage, error) {
	if params == nil {
		return nil, nil
	}
	if raw, ok := params.(json.RawMessage); ok {
		return raw, nil
	}
	return json.Marshal(params)
}

// copyRaw copies a json.RawMessage so it does not alias the read loop's buffer.
func copyRaw(raw json.RawMessage) json.RawMessage {
	if len(raw) == 0 {
		return nil
	}
	out := make(json.RawMessage, len(raw))
	copy(out, raw)
	return out
}
