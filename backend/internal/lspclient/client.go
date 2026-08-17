package lspclient

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"io"
	"os/exec"
	"sync"
	"time"
)

// ProcessConfig configures the language-server subprocess. Executable/Args come
// from validated configuration and are passed to exec directly — never through a
// shell and never concatenated into a command line.
type ProcessConfig struct {
	Executable      string
	Args            []string
	WorkingDir      string
	Env             []string // exact environment to pass; nil inherits the parent environment
	MaxMessageBytes int64
	RequestTimeout  time.Duration
	ShutdownTimeout time.Duration
}

func (c ProcessConfig) withDefaults() ProcessConfig {
	if c.MaxMessageBytes <= 0 {
		c.MaxMessageBytes = 32 << 20
	}
	if c.RequestTimeout <= 0 {
		c.RequestTimeout = 30 * time.Second
	}
	if c.ShutdownTimeout <= 0 {
		c.ShutdownTimeout = 5 * time.Second
	}
	return c
}

// Client is a JSON-RPC client bound to a language-server subprocess.
type Client struct {
	config ProcessConfig

	mu        sync.Mutex
	cmd       *exec.Cmd
	conn      *conn
	stderr    *boundedBuffer
	pid       int
	closeOnce sync.Once

	notifyHandlers  map[string]NotificationHandler
	requestHandlers map[string]ServerRequestHandler
}

// NewClient builds a client; call Start to launch the process.
func NewClient(config ProcessConfig) *Client {
	return &Client{
		config:          config.withDefaults(),
		stderr:          newBoundedBuffer(64 << 10),
		notifyHandlers:  make(map[string]NotificationHandler),
		requestHandlers: make(map[string]ServerRequestHandler),
	}
}

// PID returns the subprocess PID for observability (0 before Start).
func (c *Client) PID() int {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.pid
}

// Stderr returns the bounded, captured stderr (logs only; never trusted as
// protocol).
func (c *Client) Stderr() string { return c.stderr.String() }

// Start launches the subprocess and begins the read loop. The caller context
// bounds launch only; the subprocess lifetime is owned by Client and ends at
// Close. On any startup failure no process or goroutine is left alive.
func (c *Client) Start(ctx context.Context) error {
	if c.config.Executable == "" {
		return errors.New("lspclient: no executable configured")
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	cmd := exec.Command(c.config.Executable, c.config.Args...)
	cmd.Dir = c.config.WorkingDir
	cmd.Env = c.config.Env

	stdin, err := cmd.StdinPipe()
	if err != nil {
		return err
	}
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		_ = stdin.Close()
		return err
	}
	stderr, err := cmd.StderrPipe()
	if err != nil {
		_ = stdin.Close()
		_ = stdout.Close()
		return err
	}
	if err := cmd.Start(); err != nil {
		_ = stdin.Close()
		_ = stdout.Close()
		_ = stderr.Close()
		return fmt.Errorf("lspclient: start %q: %w", c.config.Executable, err)
	}

	conn := newConn(stdout, stdin, stdin, c.config.MaxMessageBytes)
	c.mu.Lock()
	c.cmd = cmd
	c.pid = cmd.Process.Pid
	c.conn = conn
	for method, handler := range c.notifyHandlers {
		conn.onNotification(method, handler)
	}
	for method, handler := range c.requestHandlers {
		conn.onRequest(method, handler)
	}
	c.mu.Unlock()

	go c.drainStderr(stderr)
	conn.start()
	return nil
}

func (c *Client) drainStderr(reader io.Reader) {
	scanner := bufio.NewScanner(reader)
	scanner.Buffer(make([]byte, 0, 4096), 1<<20)
	for scanner.Scan() {
		c.stderr.WriteLine(scanner.Text())
	}
}

// State reports the transport state.
func (c *Client) State() ClientState {
	c.mu.Lock()
	conn := c.conn
	c.mu.Unlock()
	if conn == nil {
		return StateNew
	}
	return conn.State()
}

// Request sends a request, applying the configured RequestTimeout only when the
// caller's context has no earlier deadline.
func (c *Client) Request(ctx context.Context, method string, params, result any) error {
	conn := c.connOrNil()
	if conn == nil {
		return ErrClosed
	}
	if _, ok := ctx.Deadline(); !ok && c.config.RequestTimeout > 0 {
		var cancel context.CancelFunc
		ctx, cancel = context.WithTimeout(ctx, c.config.RequestTimeout)
		defer cancel()
	}
	return conn.Request(ctx, method, params, result)
}

// Notify sends a notification.
func (c *Client) Notify(ctx context.Context, method string, params any) error {
	conn := c.connOrNil()
	if conn == nil {
		return ErrClosed
	}
	return conn.Notify(ctx, method, params)
}

// OnNotification registers a server→client notification handler.
func (c *Client) OnNotification(method string, handler NotificationHandler) {
	c.mu.Lock()
	c.notifyHandlers[method] = handler
	conn := c.conn
	c.mu.Unlock()
	if conn != nil {
		conn.onNotification(method, handler)
	}
}

// OnRequest registers a server→client request handler.
func (c *Client) OnRequest(method string, handler ServerRequestHandler) {
	c.mu.Lock()
	c.requestHandlers[method] = handler
	conn := c.conn
	c.mu.Unlock()
	if conn != nil {
		conn.onRequest(method, handler)
	}
}

func (c *Client) connOrNil() *conn {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.conn
}

// Close tears down the transport and process. It is idempotent and safe to call
// concurrently. The LSP shutdown/exit handshake is the adapter's responsibility;
// Close is the hard backstop.
func (c *Client) Close(_ context.Context) error {
	c.closeOnce.Do(func() {
		c.mu.Lock()
		conn := c.conn
		cmd := c.cmd
		c.mu.Unlock()
		if conn != nil {
			conn.closeTransport()
		}
		if cmd != nil && cmd.Process != nil {
			done := make(chan struct{})
			go func() { _ = cmd.Wait(); close(done) }()
			select {
			case <-done:
			case <-time.After(c.config.ShutdownTimeout):
				_ = cmd.Process.Kill()
				<-done
			}
		}
	})
	return nil
}

// boundedBuffer keeps the most recent lines of stderr up to a byte ceiling.
type boundedBuffer struct {
	mu    sync.Mutex
	lines []string
	bytes int
	max   int
}

func newBoundedBuffer(max int) *boundedBuffer { return &boundedBuffer{max: max} }

func (b *boundedBuffer) WriteLine(line string) {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.lines = append(b.lines, line)
	b.bytes += len(line) + 1
	for b.bytes > b.max && len(b.lines) > 1 {
		b.bytes -= len(b.lines[0]) + 1
		b.lines = b.lines[1:]
	}
}

func (b *boundedBuffer) String() string {
	b.mu.Lock()
	defer b.mu.Unlock()
	out := ""
	for i, line := range b.lines {
		if i > 0 {
			out += "\n"
		}
		out += line
	}
	return out
}
