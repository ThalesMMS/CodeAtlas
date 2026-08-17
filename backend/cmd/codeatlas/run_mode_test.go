package main

import (
	"context"
	"encoding/json"
	"errors"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/ThalesMMS/CodeAtlas/internal/desktop"
)

type modeWindow struct {
	title      string
	navigated  string
	html       string
	runs       int
	destroys   int
	terminates int
}

func (w *modeWindow) SetTitle(value string)                { w.title = value }
func (w *modeWindow) SetSize(_, _ int, _ desktop.SizeHint) {}
func (w *modeWindow) Navigate(value string)                { w.navigated = value }
func (w *modeWindow) SetHTML(value string)                 { w.html = value }
func (w *modeWindow) Dispatch(f func())                    { f() }
func (w *modeWindow) Run()                                 { w.runs++ }
func (w *modeWindow) Terminate()                           { w.terminates++ }
func (w *modeWindow) Destroy()                             { w.destroys++ }

type countingFactory struct {
	window     desktop.Window
	newCalls   int
	fatalCalls int
}

func (f *countingFactory) New() (desktop.Window, error) {
	f.newCalls++
	return f.window, nil
}

func (f *countingFactory) ShowFatal(string, string) { f.fatalCalls++ }

func TestRunSelectedModeHeadlessNeverCreatesWindow(t *testing.T) {
	factory := &countingFactory{}
	ran := false
	server := func(context.Context, func(net.Addr)) error {
		ran = true
		return nil
	}
	err := runSelectedMode(context.Background(), desktop.Mode{Enabled: false}, factory, server)
	if err != nil {
		t.Fatal(err)
	}
	if !ran {
		t.Fatal("headless server did not run")
	}
	if factory.newCalls != 0 {
		t.Fatalf("window creations = %d", factory.newCalls)
	}
}

func TestRunSelectedModeDesktopUsesController(t *testing.T) {
	window := &modeWindow{}
	factory := &countingFactory{window: window}
	server := func(ctx context.Context, listening func(net.Addr)) error {
		listening(staticModeAddr("0.0.0.0:43210"))
		<-ctx.Done()
		return nil
	}
	err := runSelectedMode(context.Background(), desktop.Mode{Enabled: true}, factory, server)
	if err != nil {
		t.Fatal(err)
	}
	if factory.newCalls != 1 || window.navigated != "http://127.0.0.1:43210" || window.runs != 1 || window.destroys != 1 {
		t.Fatalf("factory=%d navigate=%q runs=%d destroys=%d", factory.newCalls, window.navigated, window.runs, window.destroys)
	}
}

func TestDesktopConfigurationErrorUsesFatalPage(t *testing.T) {
	window := &modeWindow{}
	wantErr := errors.New("invalid configuration <workspace>")
	err := runSelectedMode(context.Background(), desktop.Mode{Enabled: true}, &countingFactory{window: window}, func(context.Context, func(net.Addr)) error {
		return wantErr
	})
	if !errors.Is(err, wantErr) {
		t.Fatalf("Run error = %v, want %v", err, wantErr)
	}
	if !strings.Contains(window.html, "could not start") || strings.Contains(window.html, "<workspace>") {
		t.Fatalf("fatal HTML = %q", window.html)
	}
}

func TestStoreCommandPreparesConsoleWithoutWindow(t *testing.T) {
	events := make([]string, 0, 2)
	factoryCalls := 0
	exit := runProcess(
		[]string{"store", "verify", "index.db"},
		func() error { events = append(events, "console"); return nil },
		func(args []string) int {
			events = append(events, "store:"+strings.Join(args, ","))
			return 23
		},
		func() desktop.WindowFactory { factoryCalls++; return &countingFactory{} },
	)
	if exit != 23 || factoryCalls != 0 || strings.Join(events, "|") != "console|store:verify,index.db" {
		t.Fatalf("exit=%d factoryCalls=%d events=%v", exit, factoryCalls, events)
	}
}

type staticModeAddr string

func (a staticModeAddr) Network() string { return "tcp" }
func (a staticModeAddr) String() string  { return string(a) }

type blockingModeWindow struct {
	navigated chan string
	closed    chan struct{}
	dispatch  chan func()
	closeOnce sync.Once
}

func newBlockingModeWindow() *blockingModeWindow {
	return &blockingModeWindow{navigated: make(chan string, 1), closed: make(chan struct{}), dispatch: make(chan func(), 4)}
}

func (*blockingModeWindow) SetTitle(string)                      {}
func (*blockingModeWindow) SetSize(_, _ int, _ desktop.SizeHint) {}
func (w *blockingModeWindow) Navigate(value string)              { w.navigated <- value }
func (w *blockingModeWindow) SetHTML(value string)               { w.navigated <- "fatal:" + value }
func (w *blockingModeWindow) Dispatch(f func())                  { w.dispatch <- f }
func (w *blockingModeWindow) Terminate()                         { w.close() }
func (*blockingModeWindow) Destroy()                             {}
func (w *blockingModeWindow) close()                             { w.closeOnce.Do(func() { close(w.closed) }) }
func (w *blockingModeWindow) Run() {
	for {
		select {
		case f := <-w.dispatch:
			f()
		case <-w.closed:
			return
		}
	}
}

func TestDesktopFirstRunReachesSettingsBootstrap(t *testing.T) {
	configRoot := t.TempDir()
	workspace := t.TempDir()
	t.Setenv("APPDATA", configRoot)
	t.Setenv("HOME", configRoot)
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(configRoot, "xdg"))
	t.Setenv("CODEATLAS_LLM_BASE_URL", "")
	t.Setenv("CODEATLAS_LLM_API_KEY", "")
	t.Setenv("CODEATLAS_LLM_MODEL", "")
	t.Setenv("CODEATLAS_ENABLE_EMBEDDINGS", "false")
	t.Setenv("CODEATLAS_GOPLS", "false")
	t.Setenv("CODEATLAS_TYPESCRIPT_LSP", "false")
	t.Setenv("CODEATLAS_SWIFT_LSP", "false")
	t.Setenv("CODEATLAS_PYTHON_LSP", "false")
	t.Setenv("CODEATLAS_RUST_LSP", "false")

	window := newBlockingModeWindow()
	done := make(chan error, 1)
	go func() {
		done <- runSelectedMode(context.Background(), desktop.Mode{Enabled: true}, &countingFactory{window: window}, func(ctx context.Context, listening func(net.Addr)) error {
			return runConfigured(ctx, []string{"-workspace", workspace, "-listen", "127.0.0.1:0", "-db", filepath.Join(workspace, "first-run.db")}, listening)
		})
	}()

	var baseURL string
	select {
	case baseURL = <-window.navigated:
	case <-time.After(10 * time.Second):
		t.Fatal("desktop did not navigate to the bound listener")
	}
	if strings.HasPrefix(baseURL, "fatal:") {
		t.Fatalf("desktop startup failed: %s", baseURL)
	}

	deadline := time.Now().Add(10 * time.Second)
	state := ""
	for time.Now().Before(deadline) {
		response, err := http.Get(baseURL + "/api/health/ready")
		if err == nil {
			var status struct {
				State string `json:"state"`
			}
			_ = json.NewDecoder(response.Body).Decode(&status)
			response.Body.Close()
			state = status.State
			if state == "AWAITING_CONFIGURATION" {
				break
			}
		}
		time.Sleep(10 * time.Millisecond)
	}
	window.close()
	if state != "AWAITING_CONFIGURATION" {
		t.Fatalf("readiness state = %q, want AWAITING_CONFIGURATION", state)
	}
	select {
	case err := <-done:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("desktop process did not stop after window close")
	}
	if _, err := os.Stat(filepath.Join(configRoot, "CodeAtlas", "settings.json")); err == nil {
		t.Fatal("first run unexpectedly persisted settings")
	}
}
