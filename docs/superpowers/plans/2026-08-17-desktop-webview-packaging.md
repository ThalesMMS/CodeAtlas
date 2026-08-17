# Desktop WebView Packaging Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Package CodeAtlas as an integrated native WebView application on Windows and macOS while preserving its existing foreground server mode.

**Architecture:** A build-tagged `internal/desktop` package wraps `webview_go` behind a fakeable window interface. The native UI loop stays on the operating system's main thread while `app.Run` serves the existing embedded frontend in a background goroutine; listener notification provides the exact local URL, and window closure cancels the server gracefully. Normal builds exclude WebView code, while Windows packaging produces a GUI executable and macOS packaging produces a local `.app` bundle.

**Tech Stack:** Go 1.23+, cgo, `github.com/webview/webview_go` at `v0.0.0-20240831120633-6173450d4dd6`, Windows WebView2, macOS WebKit/Cocoa, PowerShell/batch, Bash, Node.js 26 test runner.

**Spec:** `docs/superpowers/specs/2026-08-17-desktop-webview-packaging-design.md`

## Global Constraints

- Execute this plan only after all tasks in `docs/superpowers/plans/2026-08-17-runtime-settings.md` have been implemented, verified, and committed; both plans modify `backend/cmd/codeatlas/main.go`, `backend/internal/app/runtime.go`, packaging documentation, and first-run behavior.
- Packaged first run without a provider must reach the runtime Settings UI; the desktop layer must not parse `.env`, persist settings, access the keyring, or add a competing configuration form.
- Normal builds use `-tags fts5`; desktop builds use `-tags "fts5 desktop"` and `CGO_ENABLED=1`.
- Desktop builds default `-desktop=true`; normal builds default `-desktop=false`; explicit `-desktop=false` always remains available.
- `store` and other CLI-only commands never create a native window.
- The production WebView is titled `CodeAtlas`, resizable, starts at `1280x800`, has a `900x600` minimum, and disables developer tools.
- Closing the final window cancels and waits for the existing graceful server/indexer/LSP shutdown.
- Windows output is `dist/codeatlas.exe` with the GUI subsystem and WebView2; macOS output is `dist/CodeAtlas.app` using system WebKit.
- Windows/macOS server launchers invoke the same desktop-tagged binary with `-desktop=false` and forward every argument.
- Do not create a Windows installer, DMG, updater, signature, notarization flow, custom icon, Linux desktop package, or single-instance lock.
- Never copy `.env`, settings JSON, credentials, API keys, or frontend source files into `dist`; the production frontend remains embedded in the Go binary.
- Fatal HTML is local, escaped, script-free, remote-resource-free, bounded, and passed through `observability.RedactString` before display.
- Production does not download or execute a WebView2 installer. Missing WebView2 receives an actionable native error.
- Existing uncommitted `README.md`, `build_package_and_run.cmd`, `build_package_and_run.sh`, and `frontend/tests/packaging-scripts.test.cjs` are in-scope packaging work but must be reviewed before staging. Never stage `.env` or unrelated user changes.
- Native macOS execution requires a macOS host. Windows verification may prove the bundle contract but must not be reported as native macOS runtime verification.

---

## File Structure

New focused units:

- `backend/internal/desktop/mode.go` — parses/removes only the desktop flag and exposes remaining application arguments.
- `backend/internal/desktop/default_desktop.go` / `default_headless.go` — compile-time default without importing WebView in normal builds.
- `backend/internal/desktop/controller.go` — owns server/window coordination against interfaces only.
- `backend/internal/desktop/navigation.go` — converts listener addresses into safe loopback navigation URLs.
- `backend/internal/desktop/errorpage.go` — creates escaped, redacted local fatal HTML.
- `backend/internal/desktop/window.go` — native-window interface and size hints used by controller tests.
- `backend/internal/desktop/window_webview.go` — `webview_go` adapter, compiled only for desktop Windows/macOS.
- `backend/internal/desktop/window_unavailable.go` — non-desktop/unsupported factory that never links native UI code.
- `backend/internal/desktop/webview2_windows.go` — injectable WebView2 Runtime preflight.
- `backend/internal/desktop/dialog_windows.go` / `dialog_darwin.go` / `dialog_stub.go` — last-resort fatal dialogs.
- `backend/internal/desktop/console_windows.go` / `console_other.go` — foreground-console preparation for GUI-subsystem headless commands.
- `backend/cmd/codeatlas/run_mode.go` — connects the composition root to the desktop controller without expanding `main.go` further.
- `packaging/windows/codeatlas-server.cmd` — source-controlled Windows server launcher template.
- `packaging/macos/CodeAtlas.Info.plist` — source-controlled bundle metadata.
- `packaging/macos/codeatlas-server` — source-controlled macOS server launcher template.

Files modified together retain their current responsibilities: `app/runtime.go` only gains listener notification; `config` only gains argument-based loading; the build scripts only assemble platform artifacts.

---

### Task 1: Notify desktop callers when the HTTP listener is bound

**Files:**
- Modify: `backend/internal/app/runtime.go`
- Modify: `backend/internal/app/runtime_test.go`

**Interfaces:**
- Produces: `RuntimeDeps.OnListening func(net.Addr)`
- Guarantees: callback runs once after successful `Listen` and before `Server.Serve`
- Preserves: nil callback behavior and every existing headless lifecycle test

- [ ] **Step 1: Write the failing listener-order test**

Add a handler and callback that record their order without sleeps:

```go
func TestRunNotifiesActualListenerBeforeServing(t *testing.T) {
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil { t.Fatal(err) }
	notified := make(chan net.Addr, 1)
	served := make(chan struct{}, 1)
	ctx, cancel := context.WithCancel(context.Background())
	server := &http.Server{Handler: http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		served <- struct{}{}
		w.WriteHeader(http.StatusNoContent)
	})}
	done := make(chan error, 1)
	go func() {
		done <- app.Run(ctx, minimalRuntimeDeps(server, listener, func(addr net.Addr) {
			notified <- addr
		}))
	}()
	addr := <-notified
	if addr.String() != listener.Addr().String() {
		t.Fatalf("notified address = %q, want %q", addr, listener.Addr())
	}
	response, err := http.Get("http://" + addr.String())
	if err != nil { t.Fatal(err) }
	response.Body.Close()
	<-served
	cancel()
	if err := <-done; err != nil { t.Fatal(err) }
}
```

Make `minimalRuntimeDeps` use successful local probes, no-op migration/indexing, and a blocking `RunIndexer` so the test exercises the real shutdown path.

- [ ] **Step 2: Run the focused test and prove failure**

```powershell
Set-Location backend
go test ./internal/app -run TestRunNotifiesActualListenerBeforeServing -count=1
```

Expected: compile failure because `RuntimeDeps.OnListening` does not exist.

- [ ] **Step 3: Add the notification hook at the listener boundary**

Extend `RuntimeDeps`:

```go
OnListening func(net.Addr)
```

Immediately after `deps.Listen()` succeeds and before starting `Server.Serve`:

```go
if deps.OnListening != nil {
	deps.OnListening(listener.Addr())
}
```

Do not move binding into the desktop package and do not notify after readiness/bootstrap.

- [ ] **Step 4: Run the complete app lifecycle suite**

```powershell
Set-Location backend
go test ./internal/app -count=1
```

Expected: listener-order and existing failure-matrix tests pass.

- [ ] **Step 5: Commit the runtime seam**

```powershell
git add backend/internal/app/runtime.go backend/internal/app/runtime_test.go
git commit -m "feat: expose runtime listener readiness"
```

---

### Task 2: Add deterministic desktop-mode and argument parsing

**Files:**
- Create: `backend/internal/desktop/mode.go`
- Create: `backend/internal/desktop/mode_test.go`
- Create: `backend/internal/desktop/default_desktop.go`
- Create: `backend/internal/desktop/default_headless.go`
- Modify: `backend/internal/config/config.go`
- Modify: `backend/internal/config/config_test.go`

**Interfaces:**
- Produces: `desktop.Mode{Enabled bool, Args []string}`
- Produces: `desktop.ParseMode(args []string, defaultEnabled bool) (Mode, error)`
- Produces: `desktop.DefaultEnabled() bool`
- Produces: `config.LoadArgs(args []string) (Config, error)` while retaining `config.Load()` as an `os.Args[1:]` wrapper

- [ ] **Step 1: Write failing mode-parser tests**

Use a table covering desktop defaults, explicit values, argument preservation, `--`, invalid booleans, and duplicate last-value-wins behavior:

```go
func TestParseMode(t *testing.T) {
	cases := []struct {
		name string
		args []string
		defaultEnabled bool
		wantEnabled bool
		wantArgs []string
		wantErr bool
	}{
		{"desktop build default", []string{"-listen", "127.0.0.1:0"}, true, true, []string{"-listen", "127.0.0.1:0"}, false},
		{"disable", []string{"-desktop=false", "-workspace", `C:\code`}, true, false, []string{"-workspace", `C:\code`}, false},
		{"enable long", []string{"--desktop=true"}, false, true, nil, false},
		{"bare bool", []string{"-desktop"}, false, true, nil, false},
		{"last wins", []string{"-desktop=false", "--desktop=true"}, false, true, nil, false},
		{"after terminator preserved", []string{"--", "-desktop=false"}, true, true, []string{"--", "-desktop=false"}, false},
		{"invalid", []string{"-desktop=window"}, true, false, nil, true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			original := slices.Clone(tc.args)
			got, err := ParseMode(tc.args, tc.defaultEnabled)
			if (err != nil) != tc.wantErr { t.Fatalf("error = %v", err) }
			if got.Enabled != tc.wantEnabled || !slices.Equal(got.Args, tc.wantArgs) {
				t.Fatalf("mode = %#v, want enabled=%v args=%v", got, tc.wantEnabled, tc.wantArgs)
			}
			if !slices.Equal(tc.args, original) { t.Fatal("ParseMode mutated its input") }
		})
	}
}
```

Add `TestLoadArgsDoesNotReadProcessArguments`: set `os.Args` to invalid values, call `config.LoadArgs` with explicit valid flags/environment, and prove only the passed slice is parsed.

- [ ] **Step 2: Run and prove failure**

```powershell
Set-Location backend
go test ./internal/desktop ./internal/config -run 'TestParseMode|TestLoadArgsDoesNotReadProcessArguments' -count=1
```

Expected: `internal/desktop` and `config.LoadArgs` are undefined.

- [ ] **Step 3: Implement the pure desktop flag parser**

Use `strconv.ParseBool`, copy preserved arguments, stop interpreting at `--`, and accept exactly `-desktop`, `--desktop`, `-desktop=<bool>`, and `--desktop=<bool>`:

```go
type Mode struct {
	Enabled bool
	Args []string
}

func ParseMode(args []string, defaultEnabled bool) (Mode, error) {
	mode := Mode{Enabled: defaultEnabled, Args: make([]string, 0, len(args))}
	parseFlags := true
	for _, arg := range args {
		if !parseFlags {
			mode.Args = append(mode.Args, arg)
			continue
		}
		if arg == "--" {
			parseFlags = false
			mode.Args = append(mode.Args, arg)
			continue
		}
		if arg == "-desktop" || arg == "--desktop" {
			mode.Enabled = true
			continue
		}
		name, value, hasValue := strings.Cut(arg, "=")
		if hasValue && (name == "-desktop" || name == "--desktop") {
			enabled, err := strconv.ParseBool(value)
			if err != nil { return mode, fmt.Errorf("invalid -desktop value: %w", err) }
			mode.Enabled = enabled
			continue
		}
		mode.Args = append(mode.Args, arg)
	}
	return mode, nil
}
```

The returned error names `-desktop` but never includes unrelated argument values.

- [ ] **Step 4: Add build-tagged defaults**

`default_desktop.go`:

```go
//go:build desktop && (windows || darwin)

package desktop

func DefaultEnabled() bool { return true }
```

`default_headless.go`:

```go
//go:build !desktop || (!windows && !darwin)

package desktop

func DefaultEnabled() bool { return false }
```

- [ ] **Step 5: Refactor configuration parsing to an explicit FlagSet**

Keep `Load()` for callers, but move flag definitions and parsing into:

```go
func Load() (Config, error) { return LoadArgs(os.Args[1:]) }

func LoadArgs(args []string) (Config, error) {
	flags := flag.NewFlagSet("codeatlas", flag.ContinueOnError)
	flags.SetOutput(io.Discard)
	workspaceFlag := flags.String("workspace", envOr("CODEATLAS_WORKSPACE", "."), "workspace directory to index")
	listenFlag := flags.String("listen", envOr("CODEATLAS_LISTEN", "127.0.0.1:8080"), "HTTP listen address")
	dbFlag := flags.String("db", envOr("CODEATLAS_DB", ""), "SQLite database path")
	if err := flags.Parse(args); err != nil { return Config{}, err }
	return configFromParsedFlags(*workspaceFlag, *listenFlag, *dbFlag)
}
```

Move the existing post-parse environment/settings resolution and validation body into unexported `configFromParsedFlags(workspace, listen, database string) (Config, error)` without changing precedence or validation. Retain current `Load()` tests and remove global `flag.CommandLine` reset helpers only after their replacements pass.

- [ ] **Step 6: Run normal and desktop-tag tests**

```powershell
Set-Location backend
go test ./internal/desktop ./internal/config -count=1
go test -tags desktop ./internal/desktop -count=1
```

Expected: parser tests pass in both tag modes; no native WebView code exists yet.

- [ ] **Step 7: Commit mode selection**

```powershell
git add backend/internal/desktop/mode.go backend/internal/desktop/mode_test.go backend/internal/desktop/default_desktop.go backend/internal/desktop/default_headless.go backend/internal/config/config.go backend/internal/config/config_test.go
git commit -m "feat: select desktop or server process mode"
```

---

### Task 3: Implement the native-window-independent desktop controller

**Files:**
- Create: `backend/internal/desktop/window.go`
- Create: `backend/internal/desktop/controller.go`
- Create: `backend/internal/desktop/controller_test.go`
- Create: `backend/internal/desktop/navigation.go`
- Create: `backend/internal/desktop/navigation_test.go`
- Create: `backend/internal/desktop/errorpage.go`
- Create: `backend/internal/desktop/errorpage_test.go`

**Interfaces:**
- Produces: `desktop.Window`, `WindowFactory`, `SizeHint`, and `ServerFunc`
- Produces: `desktop.Controller.Run(context.Context) error`
- Produces: `desktop.NavigationURL(net.Addr) (string, error)`
- Produces: `desktop.FatalHTML(title string, err error) string`
- Consumes: a server callback matching `func(context.Context, func(net.Addr)) error`

- [ ] **Step 1: Define tests around a deterministic fake window**

The fake records every call and blocks in `Run` until `closeWindow` is closed:

```go
type fakeWindow struct {
	mu sync.Mutex
	events []string
	runStarted chan struct{}
	closeWindow chan struct{}
	dispatch chan func()
}

func (w *fakeWindow) Run() {
	close(w.runStarted)
	for {
		select {
		case f := <-w.dispatch: f()
		case <-w.closeWindow: return
		}
	}
}
```

Add focused tests proving:

1. `Navigate` receives the listener URL before `Run` starts.
2. Closing the window cancels the server context and waits for server return.
3. Early bind failure calls `SetHTML(FatalHTML(...))`, runs the window, and returns the original error only after the user closes it.
4. Runtime failure after navigation dispatches fatal HTML and keeps the window open.
5. Parent cancellation calls `Terminate`, waits for the server, and returns the parent cancellation result without replacing the page.
6. `Destroy` runs exactly once on every created-window path.
7. `WindowFactory.New` failure calls `ShowFatal` once, never starts the server, and returns the creation error.

- [ ] **Step 2: Write navigation and redaction tests**

Table-test address conversion:

```go
cases := []struct{ address, want string }{
	{"127.0.0.1:8080", "http://127.0.0.1:8080"},
	{"0.0.0.0:9090", "http://127.0.0.1:9090"},
	{"[::]:8081", "http://[::1]:8081"},
	{"[::1]:8082", "http://[::1]:8082"},
}
```

Use a fake `net.Addr`. Reject missing/non-numeric ports. For fatal HTML, pass:

```text
api_key=sk-DESKTOP-SECRET https://user:password@example.invalid/v1 <script>alert(1)</script>
```

Assert the secret/password/script tag are absent, entities are escaped, the document contains no `<script`, and output is bounded to 8 KiB.

- [ ] **Step 3: Run and prove failure**

```powershell
Set-Location backend
go test ./internal/desktop -run 'TestController|TestNavigationURL|TestFatalHTML' -count=1
```

Expected: controller/window/navigation interfaces are undefined.

- [ ] **Step 4: Implement the interfaces and controller**

Use these exact contracts:

```go
type SizeHint int
const (
	SizeDefault SizeHint = iota
	SizeMinimum
)

type Window interface {
	SetTitle(string)
	SetSize(width, height int, hint SizeHint)
	Navigate(string)
	SetHTML(string)
	Dispatch(func())
	Run()
	Terminate()
	Destroy()
}

type WindowFactory interface {
	New() (Window, error)
	ShowFatal(title, message string)
}

type ServerFunc func(context.Context, func(net.Addr)) error

type Controller struct {
	Factory WindowFactory
	Server ServerFunc
}
```

`Controller.Run` creates/configures the window on its caller's thread, starts `Server` in one goroutine with a `sync.Once` listener callback feeding a buffered channel, then chooses early-listen versus early-error. After navigation, a monitor goroutine distinguishes runtime failure from parent cancellation and window closure. Close the `windowClosed` channel before canceling so expected graceful return cannot overwrite the page.

Configure the created window before starting the server:

```go
window.SetTitle("CodeAtlas")
window.SetSize(1280, 800, SizeDefault)
window.SetSize(900, 600, SizeMinimum)
```

- [ ] **Step 5: Implement navigation and safe error HTML**

Use `net.SplitHostPort`, map only unspecified hosts to loopback, and use `net.JoinHostPort`. Build the page with `html/template` or `html.EscapeString` after `observability.RedactString`; never interpolate raw `err.Error()`.

- [ ] **Step 6: Run controller tests with the race detector**

```powershell
Set-Location backend
go test ./internal/desktop -count=1
go test -race ./internal/desktop -count=1
```

Expected: lifecycle tests pass without races or goroutine leaks.

- [ ] **Step 7: Commit the controller**

```powershell
git add backend/internal/desktop/window.go backend/internal/desktop/controller.go backend/internal/desktop/controller_test.go backend/internal/desktop/navigation.go backend/internal/desktop/navigation_test.go backend/internal/desktop/errorpage.go backend/internal/desktop/errorpage_test.go
git commit -m "feat: coordinate desktop window lifecycle"
```

---

### Task 4: Add the `webview_go` and native platform adapters

**Files:**
- Create: `backend/internal/desktop/window_webview.go`
- Create: `backend/internal/desktop/window_unavailable.go`
- Create: `backend/internal/desktop/webview2_windows.go`
- Create: `backend/internal/desktop/webview2_windows_test.go`
- Create: `backend/internal/desktop/webview2_other.go`
- Create: `backend/internal/desktop/dialog_windows.go`
- Create: `backend/internal/desktop/dialog_darwin.go`
- Create: `backend/internal/desktop/dialog_stub.go`
- Create: `backend/internal/desktop/console_windows.go`
- Create: `backend/internal/desktop/console_other.go`
- Modify: `backend/go.mod`
- Modify: `backend/go.sum`
- Modify: `THIRD_PARTY_NOTICES.md`

**Interfaces:**
- Produces: `desktop.NativeFactory() WindowFactory`
- Produces: `desktop.PrepareHeadlessConsole() error`
- Implements: `Window` with `webview_go`
- Windows preflight result: `ErrWebView2Unavailable`

- [ ] **Step 1: Pin the native dependency**

```powershell
Set-Location backend
go get github.com/webview/webview_go@v0.0.0-20240831120633-6173450d4dd6
go mod tidy
go list -m github.com/webview/webview_go
```

Expected module line:

```text
github.com/webview/webview_go v0.0.0-20240831120633-6173450d4dd6
```

Do not use an unpinned `@master` query in committed files.

- [ ] **Step 2: Write failing injectable WebView2 preflight tests**

Define a registry reader function rather than touching the developer registry from unit tests. Cover per-user and machine installs, empty/zero `pv` values, missing keys, and unexpected access errors:

```go
func TestCheckWebView2(t *testing.T) {
	lookup := func(root registry.Key, path, name string) (string, error) {
		if root == registry.CURRENT_USER { return "126.0.0.0", nil }
		return "", registry.ErrNotExist
	}
	if err := checkWebView2(lookup); err != nil { t.Fatal(err) }
}
```

The production reader follows [Microsoft's WebView2 Runtime detection contract](https://learn.microsoft.com/microsoft-edge/webview2/concepts/distribution): check product ID `{F3017226-FE2A-4295-8BDF-00C3A9A7E4C5}` and `pv` under the documented 32/64-bit EdgeUpdate client locations in HKCU and HKLM. Not-found/empty/`0.0.0.0` across all locations returns `ErrWebView2Unavailable`; permission or malformed-data errors return a sanitized preflight error.

- [ ] **Step 3: Run and prove failure on Windows**

```powershell
Set-Location backend
go test -tags desktop ./internal/desktop -run TestCheckWebView2 -count=1
```

Expected: WebView2 checker is undefined.

- [ ] **Step 4: Implement the build-tagged `webview_go` wrapper**

`window_webview.go` uses:

```go
//go:build desktop && (windows || darwin)

type webviewWindow struct { native webview.WebView }

func (w *webviewWindow) SetHTML(value string) { w.native.SetHtml(value) }
func (w *webviewWindow) Dispatch(f func()) { w.native.Dispatch(f) }
func (w *webviewWindow) SetSize(width, height int, hint SizeHint) {
	nativeHint := webview.HintNone
	if hint == SizeMinimum { nativeHint = webview.HintMin }
	w.native.SetSize(width, height, nativeHint)
}
```

`NativeFactory.New` runs `platformWebViewPreflight()`, then calls `webview.New(false)`. It does not expose `Bind`, `Eval`, or any Go RPC bridge.

`window_unavailable.go` is selected for `!desktop || (!windows && !darwin)` and returns a stable unsupported error without importing `webview_go`.

- [ ] **Step 5: Implement last-resort fatal dialogs and console preparation**

On Windows, call `MessageBoxW` through `golang.org/x/sys/windows` lazy DLL procedures with UTF-16 strings that have already been redacted. Implement `PrepareHeadlessConsole` with kernel32 `AttachConsole(ATTACH_PARENT_PROCESS)`; treat `ERROR_ACCESS_DENIED` as already attached, then reopen `CONIN$` and `CONOUT$` into `os.Stdin`, `os.Stdout`, and `os.Stderr`.

On macOS, implement the fatal alert in a cgo file linked with CoreFoundation and call `CFUserNotificationDisplayAlert` using escaped `CFStringRef` values. `PrepareHeadlessConsole` is a no-op because a Mach-O executable launched from a terminal retains its streams.

The non-desktop stub prints the already-redacted message to stderr and keeps `PrepareHeadlessConsole` a no-op.

- [ ] **Step 6: Document all bundled native licenses**

Add to `THIRD_PARTY_NOTICES.md`:

```markdown
| `github.com/webview/webview_go` | `v0.0.0-20240831120633-6173450d4dd6` | MIT |
| `webview/webview` (bundled by `webview_go`) | upstream snapshot in pinned module | MIT |
| Microsoft WebView2 headers (bundled by `webview_go`) | upstream snapshot in pinned module | BSD-3-Clause |
```

Verify the license identifiers against the three `LICENSE` files inside the pinned module before committing.

- [ ] **Step 7: Prove normal builds exclude WebView and desktop builds compile**

```powershell
Set-Location backend
$deps = go list -tags fts5 -deps ./cmd/codeatlas
if ($deps -match 'github.com/webview/webview_go') { throw 'normal build imports webview_go' }
go test -tags fts5 ./internal/desktop -count=1
go test -tags "fts5 desktop" ./internal/desktop -count=1
go build -tags "fts5 desktop" -trimpath -ldflags "-H=windowsgui" -o "$env:TEMP\codeatlas-desktop-smoke.exe" ./cmd/codeatlas
```

Expected: normal dependency list excludes WebView; both package test modes and the Windows desktop compile succeed. Remove only the exact temporary smoke binary after recording the result.

- [ ] **Step 8: Commit native adapters**

```powershell
git add backend/internal/desktop/window_webview.go backend/internal/desktop/window_unavailable.go backend/internal/desktop/webview2_windows.go backend/internal/desktop/webview2_windows_test.go backend/internal/desktop/webview2_other.go backend/internal/desktop/dialog_windows.go backend/internal/desktop/dialog_darwin.go backend/internal/desktop/dialog_stub.go backend/internal/desktop/console_windows.go backend/internal/desktop/console_other.go backend/go.mod backend/go.sum THIRD_PARTY_NOTICES.md
git commit -m "feat: add native WebView desktop adapters"
```

---

### Task 5: Integrate desktop lifecycle with the composition root

**Files:**
- Create: `backend/cmd/codeatlas/run_mode.go`
- Create: `backend/cmd/codeatlas/run_mode_test.go`
- Modify: `backend/cmd/codeatlas/main.go`
- Modify: `backend/cmd/codeatlas/main_test.go`

**Interfaces:**
- Produces: `runSelectedMode(context.Context, desktop.Mode, desktop.WindowFactory, desktop.ServerFunc) error`
- Produces: `runConfigured(context.Context, []string, func(net.Addr)) error`
- Produces: `runComposition(context.Context, config.Config, func(net.Addr), *slog.Logger) error`
- Produces: `presentStartupError(desktopEnabled bool, factory desktop.WindowFactory, err error) int`
- Produces: `exitCode(error) int`
- Consumes: completed runtime-settings startup manager and recoverable provider bootstrap
- Preserves: `runStoreCommand`, exit codes, signals, LSP shutdown, and headless logging

- [ ] **Step 1: Write failing process-mode orchestration tests**

Define a counting factory and prove the headless path cannot create a window:

```go
func TestRunSelectedModeHeadlessNeverCreatesWindow(t *testing.T) {
	factory := &countingFactory{}
	ran := false
	server := func(context.Context, func(net.Addr)) error { ran = true; return nil }
	err := runSelectedMode(context.Background(), desktop.Mode{Enabled: false}, factory, server)
	if err != nil { t.Fatal(err) }
	if !ran { t.Fatal("headless server did not run") }
	if factory.newCalls != 0 { t.Fatalf("window creations = %d", factory.newCalls) }
}
```

Also add `TestRunSelectedModeDesktopUsesController`, `TestStoreCommandPreparesConsoleWithoutWindow`, and `TestDesktopConfigurationErrorUsesFatalPage` with channel/counter assertions rather than time delays. The configuration-error test passes a `ServerFunc` that returns a sentinel before listener notification and asserts the fake window receives escaped fatal HTML rather than only a native dialog. Add a first-run integration test after the runtime-settings implementation: start without LLM base/model using isolated user config roots, run with a fake window, assert the listener is navigated and readiness reaches `AWAITING_CONFIGURATION` rather than returning an error.

- [ ] **Step 2: Run and prove failure**

```powershell
Set-Location backend
go test ./cmd/codeatlas -run 'TestRunSelectedMode|TestStoreCommandPreparesConsole|TestDesktopConfigurationError|TestDesktopFirstRunReachesSettingsBootstrap' -count=1
```

Expected: mode runner and configuration extraction do not exist.

- [ ] **Step 3: Extract configuration and composition into the server callback**

Move the current configuration load, logger, runtime-settings manager, providers, LSP managers, services, API/server construction, deferred cleanup, and `app.Run` call into this signature:

```go
func runConfigured(ctx context.Context, args []string, onListening func(net.Addr)) error {
	logger := slog.New(slog.NewTextHandler(os.Stdout, &slog.HandlerOptions{Level: slog.LevelInfo}))
	cfg, err := config.LoadArgs(args)
	if err != nil {
		logger.Error("invalid configuration", "error", err)
		return fmt.Errorf("invalid configuration: %w", err)
	}
	return runComposition(ctx, cfg, onListening, logger)
}

func runSelectedMode(ctx context.Context, mode desktop.Mode, factory desktop.WindowFactory, server desktop.ServerFunc) error {
	if !mode.Enabled { return server(ctx, nil) }
	return (desktop.Controller{Factory: factory, Server: server}).Run(ctx)
}

func presentStartupError(desktopEnabled bool, factory desktop.WindowFactory, err error) int {
	message := observability.RedactString(err.Error())
	if desktopEnabled { factory.ShowFatal("CodeAtlas could not start", message) } else { fmt.Fprintln(os.Stderr, message) }
	return 1
}

func exitCode(err error) int {
	if err == nil { return 0 }
	return 1
}
```

Create `runComposition` by moving the complete post-configuration composition block from `main.go` verbatim under the declared signature. Assign its existing `app.RuntimeDeps` literal to local `deps`, keep every field and defer, set `deps.OnListening = onListening`, and call `app.Run(ctx, deps)`. Preserve the current `runtime stopped with an error` logger call before returning the error. Use the provided context instead of creating another signal context.

- [ ] **Step 4: Implement mode selection in `run()`**

The order is fixed:

```go
if len(os.Args) > 1 && os.Args[1] == "store" {
	_ = desktop.PrepareHeadlessConsole()
	return runStoreCommand(os.Args[2:])
}
factory := desktop.NativeFactory()
mode, err := desktop.ParseMode(os.Args[1:], desktop.DefaultEnabled())
if err != nil { return presentStartupError(mode.Enabled, factory, err) }
if !mode.Enabled {
	if err := desktop.PrepareHeadlessConsole(); err != nil { return 1 }
}
rootContext, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
defer stop()
server := func(ctx context.Context, listening func(net.Addr)) error {
	return runConfigured(ctx, mode.Args, listening)
}
return exitCode(runSelectedMode(rootContext, mode, factory, server))
```

Configuration/composition errors now occur inside `ServerFunc`: desktop mode presents them in the created WebView, while headless mode retains logger output and returns nonzero. `presentStartupError` is reserved for malformed `-desktop` syntax before a controller can start. Create the logger after console preparation in headless GUI-subsystem paths. Desktop native dialogs use only text passed through `observability.RedactString`.

- [ ] **Step 5: Run command/app integration suites**

```powershell
Set-Location backend
go test ./cmd/codeatlas ./internal/app ./internal/readiness ./internal/settings -count=1
go test -race ./cmd/codeatlas ./internal/desktop -count=1
```

Expected: headless behavior, store commands, recoverable first run, and desktop lifecycle all pass.

- [ ] **Step 6: Commit composition integration**

```powershell
git add backend/cmd/codeatlas/run_mode.go backend/cmd/codeatlas/run_mode_test.go backend/cmd/codeatlas/main.go backend/cmd/codeatlas/main_test.go
git commit -m "feat: launch CodeAtlas in desktop mode"
```

---

### Task 6: Produce the Windows GUI package and server launcher

**Files:**
- Create: `packaging/windows/codeatlas-server.cmd`
- Modify: `build_package_and_run.cmd`
- Modify: `frontend/tests/packaging-scripts.test.cjs`

**Interfaces:**
- Produces: `dist/codeatlas.exe`
- Produces: `dist/codeatlas-server.cmd`
- Windows build flags: `-tags "fts5 desktop" -trimpath -ldflags "-H=windowsgui"`

- [ ] **Step 1: Extend the Windows fixture with failing packaging assertions**

Update `fixture` to copy `packaging/windows/codeatlas-server.cmd`. Make fake `go.cmd` log the complete argument vector and copy the helper to the `-o` target. Assert:

```js
assert.match(log, /go\|.*args=build -tags "?fts5 desktop"? -trimpath -ldflags "?-H=windowsgui"?/);
assert.ok(fs.existsSync(path.join(f.root, 'dist', 'codeatlas.exe')));
assert.ok(fs.existsSync(path.join(f.root, 'dist', 'codeatlas-server.cmd')));
```

Execute the generated server launcher with `-listen 127.0.0.1:19091` and assert the helper receives `-desktop=false` before the forwarded arguments. Add a test where `gcc` exists but `g++` does not; packaging must fail before `npm ci` with a C++ compiler error.

- [ ] **Step 2: Run and prove failure**

```powershell
node --test frontend/tests/packaging-scripts.test.cjs --test-name-pattern="Windows"
```

Expected: missing desktop tags/linker flags/launcher/C++ validation.

- [ ] **Step 3: Add the source-controlled server launcher**

`packaging/windows/codeatlas-server.cmd`:

```bat
@echo off
setlocal
start "" /b /wait "%~dp0codeatlas.exe" -desktop=false %*
exit /b %ERRORLEVEL%
```

The packaging script copies this exact template; it does not construct batch source dynamically.

- [ ] **Step 4: Update Windows compiler and build selection**

Pair `gcc` with `g++` and `clang` with `clang++`, respecting explicit `CC` and `CXX`. Validate both with `where`, then set `CGO_ENABLED=1`, `CC`, and `CXX`. Build:

```bat
call go build -tags "fts5 desktop" -trimpath -ldflags "-H=windowsgui" -o "%ROOT%dist\codeatlas.exe" .\cmd\codeatlas
```

Copy the launcher to `dist`, then run the app with:

```bat
start "" /wait "%ROOT%dist\codeatlas.exe" %*
set "EXIT_CODE=%ERRORLEVEL%"
```

Retain the locked Node/npm checks, safe `.env` loading used by the repository script, default example workspace, error propagation, and arbitrary argument forwarding.

- [ ] **Step 5: Run packaging tests and a real compile smoke**

```powershell
node --test frontend/tests/packaging-scripts.test.cjs --test-name-pattern="Windows"
Set-Location backend
go build -tags "fts5 desktop" -trimpath -ldflags "-H=windowsgui" -o "$env:TEMP\codeatlas-windows-package.exe" ./cmd/codeatlas
```

Expected: fixture tests pass and the real GUI-subsystem executable builds. Delete only `$env:TEMP\codeatlas-windows-package.exe` after verification.

- [ ] **Step 6: Commit Windows packaging**

```powershell
git add packaging/windows/codeatlas-server.cmd build_package_and_run.cmd frontend/tests/packaging-scripts.test.cjs
git commit -m "build: package Windows desktop executable"
```

---

### Task 7: Produce the macOS application bundle and server launcher

**Files:**
- Create: `packaging/macos/CodeAtlas.Info.plist`
- Create: `packaging/macos/codeatlas-server`
- Modify: `build_package_and_run.sh`
- Modify: `frontend/tests/packaging-scripts.test.cjs`

**Interfaces:**
- Produces: `dist/CodeAtlas.app/Contents/MacOS/codeatlas`
- Produces: `dist/CodeAtlas.app/Contents/Resources/`
- Produces: `dist/CodeAtlas.app/Contents/Info.plist`
- Produces: `dist/codeatlas-server`

- [ ] **Step 1: Extend the macOS fixture with failing bundle assertions**

Copy the two `packaging/macos` templates into the fixture. Add a fake `open` command that accepts `-W <bundle> --args ...`, executes `<bundle>/Contents/MacOS/codeatlas`, and forwards only arguments after `--args`. Assert the helper runs with the configured workspace/listen values.

Require:

```js
const app = path.join(f.root, 'dist', 'CodeAtlas.app');
assert.ok(fs.existsSync(path.join(app, 'Contents', 'MacOS', 'codeatlas')));
assert.ok(fs.existsSync(path.join(app, 'Contents', 'Resources')));
assert.match(fs.readFileSync(path.join(app, 'Contents', 'Info.plist'), 'utf8'), /<string>CodeAtlas<\/string>/);
assert.ok(fs.existsSync(path.join(f.root, 'dist', 'codeatlas-server')));
```

Execute `codeatlas-server -listen 127.0.0.1:19092` and assert `-desktop=false` plus forwarded arguments. Add a missing `clang++`/`c++` case that fails before npm installation.

- [ ] **Step 2: Run and prove failure**

```powershell
node --test frontend/tests/packaging-scripts.test.cjs --test-name-pattern="macOS"
```

Expected: no `.app`, plist, server launcher, desktop tags, or fake-open flow.

- [ ] **Step 3: Add static bundle metadata and launcher templates**

`CodeAtlas.Info.plist` declares:

```xml
<?xml version="1.0" encoding="UTF-8"?>
<!DOCTYPE plist PUBLIC "-//Apple//DTD PLIST 1.0//EN" "http://www.apple.com/DTDs/PropertyList-1.0.dtd">
<plist version="1.0"><dict>
  <key>CFBundleDisplayName</key><string>CodeAtlas</string>
  <key>CFBundleExecutable</key><string>codeatlas</string>
  <key>CFBundleIdentifier</key><string>com.codeatlas.desktop</string>
  <key>CFBundleName</key><string>CodeAtlas</string>
  <key>CFBundlePackageType</key><string>APPL</string>
  <key>CFBundleShortVersionString</key><string>0.1.0</string>
  <key>CFBundleVersion</key><string>1</string>
</dict></plist>
```

`packaging/macos/codeatlas-server` resolves its own physical directory and:

```bash
#!/usr/bin/env bash
set -Eeuo pipefail
ROOT="$(cd -- "$(dirname -- "${BASH_SOURCE[0]}")" && pwd -P)"
exec "$ROOT/CodeAtlas.app/Contents/MacOS/codeatlas" -desktop=false "$@"
```

- [ ] **Step 4: Assemble and launch the application bundle**

Validate `clang`/`clang++` (or `cc`/`c++`), create only these exact paths, copy the plist, and build directly to `Contents/MacOS/codeatlas`:

```bash
CGO_ENABLED=1 CC="$CC" CXX="$CXX" go build -tags 'fts5 desktop' -trimpath \
  -o "$ROOT/dist/CodeAtlas.app/Contents/MacOS/codeatlas" ./cmd/codeatlas
```

Copy/chmod the server launcher and binary, then run:

```bash
open -W "$ROOT/dist/CodeAtlas.app" --args "$@"
```

Do not copy `.env`, frontend output, a provisioning profile, signature, icon, or credentials into the bundle.

- [ ] **Step 5: Run the complete packaging fixture suite**

```powershell
node --test frontend/tests/packaging-scripts.test.cjs
```

Expected: Windows and macOS happy paths, tool-version failures, compiler-pair failures, safe dotenv behavior, bundle structure, and both launchers pass.

- [ ] **Step 6: Commit macOS packaging**

```powershell
git add packaging/macos/CodeAtlas.Info.plist packaging/macos/codeatlas-server build_package_and_run.sh frontend/tests/packaging-scripts.test.cjs
git commit -m "build: package macOS CodeAtlas app bundle"
```

---

### Task 8: Document, verify, and smoke-test the packaged experience

**Files:**
- Modify: `README.md`
- Modify: `docs/MANIFEST.txt`
- Modify: `docs/SHA256SUMS.txt`
- Verify: `THIRD_PARTY_NOTICES.md`
- Verify: `build_package_and_run.cmd`
- Verify: `build_package_and_run.sh`
- Verify: `packaging/windows/codeatlas-server.cmd`
- Verify: `packaging/macos/CodeAtlas.Info.plist`
- Verify: `packaging/macos/codeatlas-server`

**Interfaces:**
- Documents: desktop/server entry points, first-run Settings, dependencies, and unsigned-bundle limits
- Verifies: automated Windows behavior and structural macOS bundle contract

- [ ] **Step 1: Update README from the current uncommitted packaging text**

Replace server-only wording with exact artifacts and behavior:

- `dist/codeatlas.exe` opens the integrated Windows window;
- `dist/codeatlas-server.cmd` runs foreground server mode;
- `dist/CodeAtlas.app` is the unsigned local macOS bundle;
- `dist/codeatlas-server` runs macOS foreground server mode;
- packaged first run may configure the provider through the Settings gear;
- Windows requires WebView2 Runtime plus a C/C++ compiler pair;
- macOS requires Xcode Command Line Tools and uses system WebKit;
- close-window shutdown, `-desktop=false`, no external browser, and no copied `.env`/credentials;
- installer, signing, notarization, DMG, updater, and custom icon are excluded.

Preserve the already-correct Node/npm version correction in the working README.

- [ ] **Step 2: Run focused automated verification**

```powershell
Set-Location I:\CodeAtlas
node --test frontend/tests/packaging-scripts.test.cjs
Set-Location backend
go test -tags fts5 ./internal/app ./internal/config ./internal/desktop ./cmd/codeatlas -count=1
go test -tags "fts5 desktop" ./internal/desktop ./cmd/codeatlas -count=1
go test -race ./internal/desktop ./internal/app -count=1
go build -tags "fts5 desktop" -trimpath -ldflags "-H=windowsgui" -o "$env:TEMP\codeatlas-final-desktop.exe" ./cmd/codeatlas
```

Expected: every test passes and the final Windows desktop compile succeeds. Delete only the exact temporary binary after recording the result.

- [ ] **Step 3: Run repository-wide regression checks**

```powershell
Set-Location I:\CodeAtlas\frontend
npm run check
Set-Location ..\backend
go test -tags fts5 ./... -count=1
go vet -tags fts5 ./...
Set-Location ..
git diff --check
```

Expected: frontend, all backend packages, vet, and whitespace checks pass.

- [ ] **Step 4: Perform the Windows native smoke flow**

Using the real packaging output:

1. Launch `dist/codeatlas.exe` by double-click and confirm exactly one CodeAtlas window, no console, and no external browser.
2. With an isolated empty settings profile and no provider environment, confirm the bootstrap Settings action opens.
3. Apply a valid local provider and confirm the same process reaches READY.
4. Close the window and prove the process exits and its listener port can bind immediately in a second process.
5. Run `dist/codeatlas-server.cmd -listen 127.0.0.1:0`, confirm foreground logs, and stop it with `Ctrl+C`.
6. Temporarily exercise the injected unavailable-runtime preflight in a test build; confirm the native fatal dialog is visible and contains no secret canary.

Record actual observations; automated compile/tests are not substitutes for the no-console/no-browser window assertions.

- [ ] **Step 5: Validate the macOS deliverable boundary**

On Windows, assert only the script/template contract and do not claim native execution. On a macOS host, run:

```bash
bash ./build_package_and_run.sh
open -W ./dist/CodeAtlas.app
./dist/codeatlas-server -listen 127.0.0.1:0
```

Repeat first-run Settings, same-process activation, close-window shutdown, and foreground `Ctrl+C` checks. Record whether the unsigned local bundle opens without quarantine because signing/notarization are outside this version.

- [ ] **Step 6: Refresh and verify canonical documentation metadata**

Add this plan plus any new documentation files to `docs/MANIFEST.txt`. Recompute `docs/SHA256SUMS.txt` from canonical LF/index content rather than CRLF working files. Verify the staged file list and hashes before commit:

```powershell
git diff --check
git status --short
```

Do not stage `.env`, `dist`, frontend build output, databases, temp settings, or credentials.

- [ ] **Step 7: Commit documentation and final metadata**

```powershell
git add README.md docs/MANIFEST.txt docs/SHA256SUMS.txt
git commit -m "docs: explain desktop package workflows"
```

---

## Final Acceptance Checklist

- [ ] Runtime-settings implementation is present, and missing provider enters the bootstrap Settings flow instead of terminating.
- [ ] Normal `fts5` builds exclude `github.com/webview/webview_go` from dependencies.
- [ ] Desktop-tagged Windows/macOS builds default to desktop; explicit `-desktop=false` remains headless.
- [ ] `store` commands attach/use a terminal and never create a window.
- [ ] Listener notification carries the actual port and precedes navigation.
- [ ] Wildcard bind addresses navigate through loopback without changing bind behavior.
- [ ] WebView runs on the main UI thread with title, initial size, minimum size, resize support, and developer tools disabled.
- [ ] Closing the final window cancels and waits for graceful HTTP/indexer/LSP shutdown.
- [ ] Runtime failure stays visibly rendered in escaped/redacted local HTML until the user closes the window.
- [ ] Missing WebView2 produces an actionable native dialog and never downloads an installer.
- [ ] `dist/codeatlas.exe` opens without companion console or external browser.
- [ ] `dist/codeatlas-server.cmd` forwards arguments and preserves foreground `Ctrl+C` behavior.
- [ ] `dist/CodeAtlas.app` has valid local bundle structure and embeds the frontend only in its binary.
- [ ] `dist/codeatlas-server` forwards arguments and preserves foreground server behavior.
- [ ] No package contains `.env`, settings JSON, credentials, frontend source, signature, installer, DMG, updater, or custom icon.
- [ ] `webview_go`, bundled native webview, and bundled WebView2-header licenses are documented at exact pinned versions.
- [ ] Controller/app tests pass under the race detector; packaging fixtures pass for Windows and macOS.
- [ ] Windows native smoke checks pass; native macOS checks are recorded from a macOS host before full cross-platform runtime completion is claimed.
- [ ] README and canonical documentation manifests/checksums match the committed tree.
