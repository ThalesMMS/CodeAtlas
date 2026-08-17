package desktop

import (
	"context"
	"errors"
	"net"
	"strings"
	"sync"
	"testing"
	"time"
)

type fakeWindow struct {
	mu          sync.Mutex
	events      []string
	html        string
	runStarted  chan struct{}
	closeWindow chan struct{}
	dispatch    chan func()
	htmlSet     chan struct{}
	closeOnce   sync.Once
	runOnce     sync.Once
	htmlOnce    sync.Once
	destroys    int
}

func newFakeWindow() *fakeWindow {
	return &fakeWindow{
		runStarted:  make(chan struct{}),
		closeWindow: make(chan struct{}),
		dispatch:    make(chan func(), 8),
		htmlSet:     make(chan struct{}),
	}
}

func (w *fakeWindow) record(event string) {
	w.mu.Lock()
	defer w.mu.Unlock()
	w.events = append(w.events, event)
}

func (w *fakeWindow) SetTitle(string)              { w.record("title") }
func (w *fakeWindow) SetSize(_, _ int, _ SizeHint) { w.record("size") }
func (w *fakeWindow) Navigate(value string)        { w.record("navigate:" + value) }
func (w *fakeWindow) SetHTML(value string) {
	w.mu.Lock()
	w.html = value
	w.events = append(w.events, "html")
	w.mu.Unlock()
	w.htmlOnce.Do(func() { close(w.htmlSet) })
}
func (w *fakeWindow) Dispatch(f func()) {
	w.record("dispatch")
	w.dispatch <- f
}
func (w *fakeWindow) Run() {
	w.record("run")
	w.runOnce.Do(func() { close(w.runStarted) })
	for {
		select {
		case f := <-w.dispatch:
			f()
		case <-w.closeWindow:
			return
		}
	}
}
func (w *fakeWindow) Terminate() {
	w.record("terminate")
	w.close()
}
func (w *fakeWindow) Destroy() {
	w.mu.Lock()
	defer w.mu.Unlock()
	w.destroys++
}
func (w *fakeWindow) close() { w.closeOnce.Do(func() { close(w.closeWindow) }) }

func (w *fakeWindow) snapshot() ([]string, string, int) {
	w.mu.Lock()
	defer w.mu.Unlock()
	return append([]string(nil), w.events...), w.html, w.destroys
}

type fakeFactory struct {
	window       Window
	err          error
	newCalls     int
	fatalCalls   int
	fatalTitle   string
	fatalMessage string
}

func (f *fakeFactory) New() (Window, error) {
	f.newCalls++
	return f.window, f.err
}

func (f *fakeFactory) ShowFatal(title, message string) {
	f.fatalCalls++
	f.fatalTitle = title
	f.fatalMessage = message
}

func waitFor(t *testing.T, channel <-chan struct{}, message string) {
	t.Helper()
	select {
	case <-channel:
	case <-time.After(3 * time.Second):
		t.Fatal(message)
	}
}

func runController(controller Controller) <-chan error {
	done := make(chan error, 1)
	go func() { done <- controller.Run(context.Background()) }()
	return done
}

func TestControllerNavigatesBeforeWindowRun(t *testing.T) {
	window := newFakeWindow()
	factory := &fakeFactory{window: window}
	server := func(ctx context.Context, listening func(net.Addr)) error {
		listening(staticAddr("0.0.0.0:43123"))
		<-ctx.Done()
		return nil
	}
	done := runController(Controller{Factory: factory, Server: server})
	waitFor(t, window.runStarted, "window Run did not start")
	window.close()
	if err := <-done; err != nil {
		t.Fatal(err)
	}

	events, _, destroys := window.snapshot()
	navigate, run := -1, -1
	for i, event := range events {
		if strings.HasPrefix(event, "navigate:http://127.0.0.1:43123") {
			navigate = i
		}
		if event == "run" {
			run = i
		}
	}
	if navigate < 0 || run < 0 || navigate >= run {
		t.Fatalf("events = %v, want navigation before run", events)
	}
	if destroys != 1 {
		t.Fatalf("Destroy calls = %d, want 1", destroys)
	}
}

func TestControllerWindowCloseCancelsAndWaitsForServer(t *testing.T) {
	window := newFakeWindow()
	serverReturned := make(chan struct{})
	server := func(ctx context.Context, listening func(net.Addr)) error {
		listening(staticAddr("127.0.0.1:43124"))
		<-ctx.Done()
		close(serverReturned)
		return nil
	}
	done := runController(Controller{Factory: &fakeFactory{window: window}, Server: server})
	waitFor(t, window.runStarted, "window Run did not start")
	window.close()
	if err := <-done; err != nil {
		t.Fatal(err)
	}
	waitFor(t, serverReturned, "controller returned before server shutdown")
}

func TestControllerEarlyServerFailureShowsFatalPageUntilClose(t *testing.T) {
	window := newFakeWindow()
	wantErr := errors.New("bind failed api_key=sk-DESKTOP-SECRET")
	done := runController(Controller{
		Factory: &fakeFactory{window: window},
		Server:  func(context.Context, func(net.Addr)) error { return wantErr },
	})
	waitFor(t, window.runStarted, "fatal window did not start")
	_, html, _ := window.snapshot()
	if strings.Contains(html, "sk-DESKTOP-SECRET") || !strings.Contains(html, "could not start") {
		t.Fatalf("fatal HTML = %q", html)
	}
	select {
	case <-done:
		t.Fatal("controller returned before the user closed the fatal page")
	default:
	}
	window.close()
	if err := <-done; !errors.Is(err, wantErr) {
		t.Fatalf("Run error = %v, want %v", err, wantErr)
	}
}

func TestControllerRuntimeFailureDispatchesFatalPageAndKeepsWindowOpen(t *testing.T) {
	window := newFakeWindow()
	fail := make(chan struct{})
	wantErr := errors.New("runtime failed")
	server := func(_ context.Context, listening func(net.Addr)) error {
		listening(staticAddr("127.0.0.1:43125"))
		<-fail
		return wantErr
	}
	done := runController(Controller{Factory: &fakeFactory{window: window}, Server: server})
	waitFor(t, window.runStarted, "window Run did not start")
	close(fail)
	waitFor(t, window.htmlSet, "runtime failure was not displayed")
	select {
	case <-done:
		t.Fatal("controller returned before the user closed the runtime error page")
	default:
	}
	window.close()
	if err := <-done; !errors.Is(err, wantErr) {
		t.Fatalf("Run error = %v, want %v", err, wantErr)
	}
}

func TestControllerParentCancellationClosesRuntimeErrorPage(t *testing.T) {
	window := newFakeWindow()
	ctx, cancel := context.WithCancel(context.Background())
	fail := make(chan struct{})
	done := make(chan error, 1)
	go func() {
		done <- (Controller{
			Factory: &fakeFactory{window: window},
			Server: func(_ context.Context, listening func(net.Addr)) error {
				listening(staticAddr("127.0.0.1:43127"))
				<-fail
				return errors.New("runtime failed")
			},
		}).Run(ctx)
	}()
	waitFor(t, window.runStarted, "window Run did not start")
	close(fail)
	waitFor(t, window.htmlSet, "runtime failure was not displayed")
	cancel()
	select {
	case err := <-done:
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("Run error = %v, want context cancellation", err)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("parent cancellation did not close the runtime error page")
	}
}

func TestControllerParentCancellationTerminatesWindowAndReturnsCancellation(t *testing.T) {
	window := newFakeWindow()
	ctx, cancel := context.WithCancel(context.Background())
	serverReturned := make(chan struct{})
	server := func(ctx context.Context, listening func(net.Addr)) error {
		listening(staticAddr("127.0.0.1:43126"))
		<-ctx.Done()
		close(serverReturned)
		return nil
	}
	done := make(chan error, 1)
	go func() { done <- (Controller{Factory: &fakeFactory{window: window}, Server: server}).Run(ctx) }()
	waitFor(t, window.runStarted, "window Run did not start")
	cancel()
	if err := <-done; !errors.Is(err, context.Canceled) {
		t.Fatalf("Run error = %v, want context cancellation", err)
	}
	waitFor(t, serverReturned, "server did not stop after parent cancellation")
	events, _, _ := window.snapshot()
	if !containsEvent(events, "terminate") {
		t.Fatalf("events = %v, want terminate", events)
	}
}

func TestControllerWindowFactoryFailureUsesFatalDialogWithoutServer(t *testing.T) {
	wantErr := errors.New("webview unavailable")
	factory := &fakeFactory{err: wantErr}
	serverRan := false
	err := (Controller{
		Factory: factory,
		Server:  func(context.Context, func(net.Addr)) error { serverRan = true; return nil },
	}).Run(context.Background())
	if !errors.Is(err, wantErr) {
		t.Fatalf("Run error = %v, want %v", err, wantErr)
	}
	if serverRan || factory.newCalls != 1 || factory.fatalCalls != 1 {
		t.Fatalf("serverRan=%v newCalls=%d fatalCalls=%d", serverRan, factory.newCalls, factory.fatalCalls)
	}
}

func containsEvent(events []string, want string) bool {
	for _, event := range events {
		if event == want {
			return true
		}
	}
	return false
}
