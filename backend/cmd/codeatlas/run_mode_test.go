package main

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/ThalesMMS/CodeAtlas/internal/desktop"
	"github.com/ThalesMMS/CodeAtlas/internal/settings"
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
func (w *modeWindow) Bind(string, any) error               { return nil }

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

func TestRunSelectedModeHeadlessRerunsServerAfterRestartRequest(t *testing.T) {
	runs := 0
	server := func(context.Context, func(net.Addr)) error {
		runs++
		if runs == 1 {
			return desktop.ErrRestartRequested
		}
		return nil
	}
	err := runSelectedMode(context.Background(), desktop.Mode{Enabled: false}, &countingFactory{}, server)
	if err != nil {
		t.Fatal(err)
	}
	if runs != 2 {
		t.Fatalf("server runs = %d, want 2", runs)
	}
}

func TestRunSelectedModeHeadlessStopsRestartLoopWhenCancelled(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	runs := 0
	server := func(context.Context, func(net.Addr)) error {
		runs++
		cancel()
		return desktop.ErrRestartRequested
	}
	err := runSelectedMode(ctx, desktop.Mode{Enabled: false}, &countingFactory{}, server)
	if !errors.Is(err, desktop.ErrRestartRequested) || runs != 1 {
		t.Fatalf("err = %v runs = %d, want a single run once the parent context is cancelled", err, runs)
	}
}

func TestRestartOutcomeMapsRestartCause(t *testing.T) {
	ctx, cancel := context.WithCancelCause(context.Background())
	if err := restartOutcome(ctx, nil); err != nil {
		t.Fatalf("restartOutcome without cancellation = %v", err)
	}
	other := errors.New("runtime failed")
	if err := restartOutcome(ctx, other); !errors.Is(err, other) {
		t.Fatalf("restartOutcome preserved error = %v, want %v", err, other)
	}
	cancel(desktop.ErrRestartRequested)
	if err := restartOutcome(ctx, context.Canceled); !errors.Is(err, desktop.ErrRestartRequested) {
		t.Fatalf("restartOutcome after restart cancellation = %v", err)
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

func TestDefaultDesktopWorkspaceUsesSettingsDirectory(t *testing.T) {
	settingsPath := filepath.Join(t.TempDir(), "CodeAtlas", "settings.json")
	workspace, err := defaultDesktopWorkspace(settingsPath)
	if err != nil {
		t.Fatal(err)
	}
	want := filepath.Join(filepath.Dir(settingsPath), "workspace")
	if workspace != want {
		t.Fatalf("default desktop workspace = %q, want %q", workspace, want)
	}
	info, err := os.Stat(workspace)
	if err != nil {
		t.Fatal(err)
	}
	if !info.IsDir() {
		t.Fatalf("default desktop workspace is not a directory: %q", workspace)
	}
}

func TestApplyDesktopWorkspaceDefaultPreservesOtherSettings(t *testing.T) {
	environment := settings.Environment{settings.FieldLLMModel: "model"}
	resolved := settings.Resolved{
		Values:  settings.Values{Workspace: "."},
		Sources: map[settings.FieldKey]settings.Source{settings.FieldWorkspace: settings.SourceDefault},
	}
	workspace := filepath.Join(t.TempDir(), "workspace")
	adjustedEnvironment, adjusted := applyDesktopWorkspaceDefault(environment, resolved, workspace)
	if adjusted.Values.Workspace != workspace {
		t.Fatalf("resolved workspace = %q, want %q", adjusted.Values.Workspace, workspace)
	}
	if adjustedEnvironment[settings.FieldWorkspace] != workspace || adjustedEnvironment[settings.FieldLLMModel] != "model" {
		t.Fatalf("adjusted environment = %#v", adjustedEnvironment)
	}
	if _, ok := environment[settings.FieldWorkspace]; ok {
		t.Fatal("default workspace mutated the original environment")
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
func (w *blockingModeWindow) SetHTML(value string) {
	if strings.Contains(value, "Restarting CodeAtlas") {
		w.navigated <- "restarting:" + value
		return
	}
	w.navigated <- "fatal:" + value
}
func (w *blockingModeWindow) Dispatch(f func())    { w.dispatch <- f }
func (w *blockingModeWindow) Terminate()           { w.close() }
func (*blockingModeWindow) Destroy()               {}
func (*blockingModeWindow) Bind(string, any) error { return nil }
func (w *blockingModeWindow) close()               { w.closeOnce.Do(func() { close(w.closed) }) }
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

var settingsTokenPattern = regexp.MustCompile(`name="codeatlas-settings-token" content="([^"]+)"`)

func TestDesktopSettingsRestartReopensWindowOnNewComposition(t *testing.T) {
	configRoot := t.TempDir()
	workspace := t.TempDir()
	t.Setenv("APPDATA", configRoot)
	t.Setenv("HOME", configRoot)
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(configRoot, "xdg"))
	t.Setenv("CODEATLAS_LLM_BASE_URL", "")
	t.Setenv("CODEATLAS_LLM_API_KEY", "")
	t.Setenv("CODEATLAS_LLM_MODEL", "")
	t.Setenv("CODEATLAS_GOPLS", "false")
	t.Setenv("CODEATLAS_TYPESCRIPT_LSP", "false")
	t.Setenv("CODEATLAS_SWIFT_LSP", "false")
	t.Setenv("CODEATLAS_PYTHON_LSP", "false")
	t.Setenv("CODEATLAS_RUST_LSP", "false")

	window := newBlockingModeWindow()
	var runs int
	var mu sync.Mutex
	done := make(chan error, 1)
	go func() {
		done <- runSelectedMode(context.Background(), desktop.Mode{Enabled: true}, &countingFactory{window: window}, func(ctx context.Context, listening func(net.Addr)) error {
			mu.Lock()
			runs++
			mu.Unlock()
			return runConfigured(ctx, []string{"-workspace", workspace, "-listen", "127.0.0.1:0", "-db", filepath.Join(workspace, "restart.db")}, listening)
		})
	}()

	baseURL := waitForNavigation(t, window)
	client := &http.Client{Timeout: 5 * time.Second}
	page, err := client.Get(baseURL + "/")
	if err != nil {
		t.Fatal(err)
	}
	pageBody, _ := io.ReadAll(page.Body)
	page.Body.Close()
	match := settingsTokenPattern.FindSubmatch(pageBody)
	if match == nil {
		t.Fatalf("settings token not embedded in %q", string(pageBody[:min(len(pageBody), 300)]))
	}
	token := string(match[1])

	snapshotResponse, err := settingsGet(client, baseURL, token)
	if err != nil {
		t.Fatal(err)
	}
	if !snapshotResponse.RestartSupported {
		t.Fatal("desktop composition did not advertise restart support")
	}

	request, _ := http.NewRequest(http.MethodPost, baseURL+"/api/settings/restart", strings.NewReader(`{"revision":0}`))
	request.Header.Set("X-CodeAtlas-Settings-Token", token)
	request.Header.Set("Content-Type", "application/json")
	request.Header.Set("Origin", baseURL)
	response, err := client.Do(request)
	if err != nil {
		t.Fatal(err)
	}
	responseBody, _ := io.ReadAll(response.Body)
	response.Body.Close()
	if response.StatusCode != http.StatusAccepted {
		t.Fatalf("restart status = %d body=%s", response.StatusCode, responseBody)
	}

	restartedURL := waitForNavigation(t, window)
	if strings.HasPrefix(restartedURL, "fatal:") {
		t.Fatalf("restart failed: %s", restartedURL)
	}
	if _, err := settingsGet(client, restartedURL, ""); err == nil {
		t.Fatal("restarted composition accepted the request without a settings token")
	}
	deadline := time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) {
		if health, err := client.Get(restartedURL + "/api/health/ready"); err == nil {
			health.Body.Close()
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	window.close()
	select {
	case err := <-done:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(15 * time.Second):
		t.Fatal("desktop process did not stop after window close")
	}
	mu.Lock()
	defer mu.Unlock()
	if runs != 2 {
		t.Fatalf("composition runs = %d, want 2", runs)
	}
}

func waitForNavigation(t *testing.T, window *blockingModeWindow) string {
	t.Helper()
	for {
		select {
		case value := <-window.navigated:
			if strings.HasPrefix(value, "fatal:") {
				t.Fatalf("desktop startup failed: %s", value)
			}
			if strings.HasPrefix(value, "restarting:") {
				continue
			}
			return value
		case <-time.After(20 * time.Second):
			t.Fatal("desktop did not navigate to a bound listener")
		}
	}
}

type restartSnapshotResponse struct {
	Revision         uint64 `json:"revision"`
	RestartSupported bool   `json:"restartSupported"`
}

func settingsGet(client *http.Client, baseURL, token string) (restartSnapshotResponse, error) {
	request, _ := http.NewRequest(http.MethodGet, baseURL+"/api/settings", nil)
	request.Header.Set("X-CodeAtlas-Settings-Token", token)
	response, err := client.Do(request)
	if err != nil {
		return restartSnapshotResponse{}, err
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		return restartSnapshotResponse{}, errors.New("settings request failed with status " + response.Status)
	}
	var snapshot restartSnapshotResponse
	return snapshot, json.NewDecoder(response.Body).Decode(&snapshot)
}
