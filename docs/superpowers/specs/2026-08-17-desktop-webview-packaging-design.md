# Desktop WebView Packaging Design

**Date:** 2026-08-17

**Status:** Approved in chat; pending written-spec review

**Platforms:** Windows and macOS

**Desktop runtime:** [`github.com/webview/webview_go`](https://github.com/webview/webview_go)

## Context

CodeAtlas currently builds the frontend, embeds it in the Go HTTP server, and
produces a native server executable. The packaging scripts run that server in
the foreground, but the executable is not a desktop application: it does not
create a native window, and opening it directly can terminate immediately when
startup configuration is unavailable. The intended packaged experience is a
native CodeAtlas window on Windows and macOS while preserving the existing
headless server mode for development and automation.

A separate, already-approved runtime-settings design introduces a settings
drawer, per-user persistence, OS credential storage, and recoverable startup
when the LLM provider is missing or invalid. The desktop layer must consume
that behavior instead of creating a second configuration or credential path.

## Goals

- Open CodeAtlas in an integrated native desktop window on Windows and macOS.
- Reuse the existing embedded frontend and loopback HTTP API unchanged.
- Keep the WebView on the operating system's main UI thread and the server in a
  background goroutine.
- Close the server gracefully when the last CodeAtlas window closes.
- Preserve a headless server mode through `-desktop=false` and convenient
  platform launchers.
- Let the packaged application reach the Settings UI without `.env` when the
  provider has not been configured yet.
- Show fatal startup failures visibly instead of flashing and disappearing.
- Produce `dist/codeatlas.exe` on Windows and `dist/CodeAtlas.app` on macOS.
- Keep normal builds, tests, and non-desktop commands free of WebView runtime
  requirements.

## Non-goals

- Replacing the existing HTTP API with WebView RPC bindings.
- Migrating the frontend or backend to Wails, Electron, or another framework.
- Shipping a Windows installer, DMG, auto-updater, code signing, or macOS
  notarization in the first version.
- Adding a custom application icon in the first version.
- Supporting Linux desktop packaging in this change.
- Implementing settings persistence, keyring access, or provider hot swapping;
  those belong to the runtime-settings implementation.
- Enforcing a single application instance.
- Automatically installing or downloading the WebView2 Runtime.

## Dependency and Integration Boundary

Desktop implementation follows the runtime-settings work described in
`docs/superpowers/specs/2026-08-17-runtime-settings-design.md`.

The settings implementation owns:

- configuration precedence (`defaults < environment/.env < saved settings`);
- recoverable startup when the provider is absent or invalid;
- the bootstrap Settings action and settings drawer;
- settings persistence and Windows Credential Manager/macOS Keychain access;
- runtime activation of provider, embeddings, and language-server changes.

The desktop implementation owns only:

- selecting desktop versus headless process behavior;
- the native window and its lifecycle;
- navigation to the already-running local frontend;
- visible presentation of failures that occur before the HTTP UI is usable;
- platform packaging and launchers.

The desktop layer will not search for, parse, copy, or persist `.env` itself.
Repository build scripts may continue loading `.env` for developer convenience.
A directly launched packaged application can instead start in the recoverable
bootstrap state and be configured through Settings.

## Build Isolation

Desktop support is opt-in at compile time:

```text
normal build:  -tags fts5
desktop build: -tags "fts5 desktop"
```

The `internal/desktop` package has build-tagged implementations:

- `desktop && windows` and `desktop && darwin` import `webview_go` and expose
  the native shell;
- non-desktop builds expose a small stub whose default is headless and which
  never imports or links WebView code;
- unsupported desktop targets fail with a clear build-time or startup error.

This boundary keeps ordinary `go test -tags fts5 ./...`, server builds, and
Linux CI independent of GTK/WebKit dependencies. The desktop build adds the
`webview_go` module and its MIT license to the third-party documentation.

## Process Modes and Command Semantics

The main application accepts a boolean `-desktop` flag. Its build-dependent
default is:

- `true` in Windows/macOS builds carrying the `desktop` tag;
- `false` in normal server builds.

`-desktop=false` always selects the existing foreground server lifecycle.
The `store` subcommand and any future non-server subcommands bypass desktop
startup entirely and retain their current CLI behavior.

The desktop decision must be available even when full configuration loading
fails. A narrow argument inspection determines whether a fatal error should be
presented through the desktop fatal-error adapter; canonical flag parsing and
validation remain in the configuration layer.

## Runtime Architecture

### Desktop shell boundary

`internal/desktop` exposes a small interface around the external library so
lifecycle behavior can be tested without creating native windows. Its
responsibilities are limited to:

- create and destroy a window;
- set title and initial/minimum size;
- navigate or set local HTML;
- run and terminate the UI loop;
- dispatch work safely onto the UI thread;
- present an OS-native fatal dialog when a WebView cannot be created.

The production adapter uses `webview_go`; tests use a deterministic fake.
The window title is `CodeAtlas`, the release build disables developer tools,
and the window is resizable. The initial dimensions are large enough for the
existing application layout without imposing fixed bounds.

### Listener notification

`app.RuntimeDeps` gains an optional listener notification callback. `app.Run`
invokes it after `Listen` succeeds and before serving requests. The callback
receives the actual bound address, which is important when port `0` is used.
Headless callers can leave the callback nil and retain current behavior.

The callback is notification-only: it cannot replace the listener, block
startup indefinitely, or assume the readiness bootstrap has completed.

### Desktop startup sequence

1. Determine the requested process mode and handle CLI-only subcommands.
2. Resolve runtime settings and construct the existing composition root.
3. Create a cancelable process context.
4. In desktop mode, create the native shell on the main operating-system
   thread. `webview_go` locks that thread as required by its runtime.
5. Start `app.Run` in a background goroutine.
6. Wait for either listener notification or an early runtime error.
7. Convert the bound address to the local navigation URL and navigate the
   WebView to the embedded frontend.
8. Run the native UI event loop on the main thread.
9. When the window closes, cancel the process context, wait for `app.Run` to
   shut down the HTTP server/indexer, destroy the WebView, and return its final
   exit status.

The desktop window opens as soon as the HTTP listener exists. Readiness and
provider setup remain visible through the frontend's existing bootstrap flow;
the desktop shell does not wait for indexing or provider probes before showing
the UI.

### Navigation address

The WebView uses the actual listener port. A wildcard bind host such as
`0.0.0.0` or `::` is converted only for navigation to a loopback host while
the configured bind behavior remains unchanged. IPv6 literals are formatted
with brackets. No external browser is opened.

## Lifecycle and Failure Handling

### User closes the window

Returning from the WebView loop is treated as the user closing the final
window. The desktop controller cancels the runtime context and waits for the
existing graceful HTTP shutdown, bootstrap/indexer termination, LSP shutdown,
and persistence hooks. The process exits only after this bounded shutdown.

### Runtime fails after the window opens

If `app.Run` returns unexpectedly, the controller dispatches a sanitized local
HTML error page into the existing window. The page reports the actionable error
without credentials, request bodies, or sensitive endpoint details. The window
remains open until the user closes it, and the final process exit code is
nonzero.

### Failure before the frontend is reachable

Configuration-file corruption, listener bind failure, invalid workspace, or
another fatal composition error is shown through a local error page when a
WebView can be created. If WebView creation itself is impossible, a narrow
platform adapter uses a Windows message box or macOS alert and exits nonzero.

A missing or invalid LLM provider is not fatal after the runtime-settings work:
the normal bootstrap UI opens and exposes Settings. The desktop layer must not
render a competing provider-configuration form.

### WebView2 availability

Windows desktop startup performs a non-mutating WebView2 Runtime preflight
before constructing the WebView. When it is unavailable, CodeAtlas displays an
actionable native error and links the requirement in documentation. It does
not download or execute an installer automatically. macOS uses the system
WebKit framework.

## Windows Packaging

`build_package_and_run.cmd` performs the existing locked frontend build, then
builds the Go command with:

- build tags `fts5 desktop`;
- cgo enabled;
- a validated C and C++ compiler pair;
- the Windows GUI subsystem linker flag so double-clicking the executable does
  not create a console window;
- output `dist\codeatlas.exe`.

The script also writes `dist\codeatlas-server.cmd`. This launcher invokes the
same executable with `-desktop=false`, attaches standard streams to its console,
forwards all arguments, and waits for termination. It is the documented Windows
entry point for foreground server use. Direct GUI invocation with
`-desktop=false` remains functional, but the launcher provides predictable
terminal waiting and output behavior.

After a successful build, the packaging script starts the desktop executable
and waits for it. Extra arguments are forwarded unchanged.

## macOS Packaging

`build_package_and_run.sh` performs the existing locked frontend build, then
creates:

```text
dist/
  CodeAtlas.app/
    Contents/
      Info.plist
      MacOS/
        codeatlas
      Resources/
  codeatlas-server
```

The binary uses build tags `fts5 desktop`, cgo, and the system WebKit framework.
`Info.plist` declares the CodeAtlas name, bundle identifier, executable, bundle
type, and version metadata required for a locally runnable application bundle.
No credentials or separate frontend asset directory are copied into the
bundle; the frontend remains embedded in the Go binary.

`dist/codeatlas-server` executes the bundle's binary with `-desktop=false` and
forwards its arguments. After building, the script launches the bundle with
`open -W --args` so arguments reach the app and the script waits for exit.

The first version is an unsigned local-development bundle. Signing,
notarization, DMG creation, and a custom icon are documented follow-up work.

## Security and Privacy

- Production WebViews run with debug/developer tools disabled.
- The WebView navigates only to the local CodeAtlas listener; application data
  and secrets are not injected through WebView RPC bindings.
- The existing HTTP same-origin, CSP, readiness, and settings-administration
  boundaries remain authoritative.
- Fatal error pages are constructed from escaped, sanitized text and contain no
  remote resources or executable script.
- Settings and credentials remain in the user config directory and OS keychain;
  they are never copied into `dist`, the Windows executable, or the macOS
  bundle.
- The packaging process does not download or execute WebView runtimes.

## Testing Strategy

### Unit tests

- build-dependent desktop default and explicit `-desktop=false` selection;
- CLI subcommands bypassing desktop initialization;
- listener notification before navigation;
- actual listener address and wildcard-to-loopback URL conversion;
- window close canceling the runtime and waiting for shutdown;
- runtime failure replacing the window contents with a sanitized error page;
- early fatal failure selecting the correct visible error path;
- fake-window assertions that headless mode never creates a WebView;
- error HTML escaping and secret-redaction cases.

### Runtime integration tests

- extend `app.Run` tests for the optional listener callback without changing
  existing headless behavior;
- run a desktop controller against a fake WebView and real ephemeral HTTP
  listener;
- verify the bootstrap UI is navigated to when provider configuration is
  absent, relying on the runtime-settings recoverable-startup contract;
- verify window closure produces no leaked server/indexer goroutines.

### Packaging-script tests

The existing Node fixture tests use fake build tools to verify:

- `fts5 desktop`, cgo, compiler, linker, and output arguments;
- Windows GUI output plus `codeatlas-server.cmd` behavior;
- the macOS `.app` directory tree and required `Info.plist` keys;
- argument forwarding for desktop and server launchers;
- frontend output is embedded before the native build;
- invalid tool versions or missing C++ compiler fail before packaging.

### Native smoke verification

On Windows:

- build with the actual local toolchain;
- double-click `codeatlas.exe` and verify one CodeAtlas window, no console, and
  no external browser;
- open Settings from first-run bootstrap, apply provider configuration in the
  same process, and confirm normal UI operation;
- close the window and confirm the HTTP port and process are released;
- run `codeatlas-server.cmd` and verify foreground logs and `Ctrl+C` shutdown.

On macOS:

- build and open `CodeAtlas.app` from Finder;
- repeat first-run Settings and window-close lifecycle checks;
- run `codeatlas-server` and verify foreground shutdown.

The current workspace is Windows, so macOS runtime smoke verification requires
a macOS host. Windows development can still verify the bundle structure and
script contract; completion reporting must explicitly distinguish those
structural tests from native macOS execution.

## Acceptance Criteria

- Double-clicking `dist/codeatlas.exe` opens CodeAtlas in one native Windows
  window without a companion console or external browser.
- Opening `dist/CodeAtlas.app` on macOS opens the same embedded CodeAtlas UI.
- With no configured provider, the packaged UI remains open and exposes the
  runtime Settings action.
- Applying valid provider settings makes the running app usable without a
  process restart.
- Closing the final window gracefully terminates the local server and process.
- Fatal pre-UI failures remain visible and return a nonzero exit status.
- `-desktop=false` preserves headless behavior, with documented convenience
  launchers on both platforms.
- Normal non-desktop builds and tests do not require WebView system libraries.
- `store` and other CLI-only commands never open a window.
- The Windows artifact contains no copied `.env`; the macOS bundle contains no
  credentials or separate web build.
- Automated lifecycle and packaging tests pass on Windows; macOS bundle
  structure passes cross-platform tests and native behavior is verified on a
  macOS host before claiming full macOS runtime validation.

## Documentation Changes

- Update README packaging instructions with desktop and server artifacts,
  WebView2 and C++ compiler requirements, macOS bundle limitations, and the
  Settings-first packaged startup flow.
- Add `webview_go` and its transitive native webview components to third-party
  notices and repository documentation manifests.
- Document `-desktop=false`, `codeatlas-server.cmd`, and `codeatlas-server`.
- State clearly that Windows installer, macOS signing/notarization, DMG, and
  custom icons are not included yet.

## Rejected Alternatives

### Wails

Wails provides richer desktop tooling, but adopting it would restructure the
existing server/frontend lifecycle. Stable Wails v2 also complicates a single
codebase that preserves headless behavior, while Wails v3 is not the selected
stable foundation. CodeAtlas needs only a window around its existing local UI.

### Platform APIs implemented directly

Direct WebView2 and WKWebView adapters would avoid the Go binding but require
substantial duplicated C++/Objective-C lifecycle code. `webview_go` already
provides the minimal common surface needed here.

### Opening the default browser

This keeps packaging simple but does not provide the approved integrated
desktop window and leaves browser/session lifecycle separate from the server.

### Console-subsystem Windows executable

A console executable can preserve terminal semantics automatically, but it
creates or flashes a console when launched as a desktop app. A GUI-subsystem
executable plus an explicit server launcher provides the intended default
experience while retaining headless operation.
