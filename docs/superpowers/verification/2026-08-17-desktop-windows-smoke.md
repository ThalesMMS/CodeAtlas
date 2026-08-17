# Windows desktop package smoke — 2026-08-17

Host: Windows, repository `main`, commits through the desktop packaging review
fixes recorded on 2026-08-17.

## Native desktop window

1. `build_package_and_run.cmd` completed `npm ci`, the embedded frontend build,
   and both native Go builds with `CGO_ENABLED=1` and tags `fts5 desktop`.
2. Launching `dist/codeatlas.exe` produced exactly one top-level window titled
   **CodeAtlas**. No companion console or new external-browser window appeared.
3. With an isolated `%APPDATA%`, valid workspace, fixed listener at
   `127.0.0.1:19092`, and no provider environment, the same window reached
   **Configuration required** / `AWAITING_CONFIGURATION` and opened Settings.
4. Entering the isolated local provider URL and model through the visible drawer,
   then selecting **Test and apply**, moved the same PID to the full application.
   The UI reported `IA: openai-compatible:fake-codeatlas`, `index updated`, and
   `GET /api/health/ready` returned `state=READY`.
5. Before close, process inspection found the expected WebView2 and `gopls`
   children. Closing the native title bar stopped `codeatlas.exe`, WebView2, and
   `gopls`, removed the top-level window, and allowed port 19092 to bind
   immediately in a new process.

## Windows executable contracts

GNU `objdump -x` reported:

```text
dist/codeatlas.exe        Subsystem 00000002 (Windows GUI)
dist/codeatlas-server.exe Subsystem 00000003 (Windows CUI)
```

The clean staged `dist` contained only:

```text
codeatlas.exe
codeatlas-server.exe
codeatlas-server.cmd
```

## Foreground server shutdown

1. `dist/codeatlas-server.cmd -workspace ... -listen 127.0.0.1:19091` opened the
   listener on port 19091.
2. `Ctrl+C` stopped `codeatlas-server.exe`; process inspection found no remaining
   server process and a new TCP connection confirmed the port was released.
3. Running the same launcher again reopened port 19091, proving immediate port
   reuse, and a second `Ctrl+C` stopped it.

## Missing WebView2 native dialog

The opt-in `TestManualUnavailableWebView2Dialog` test build injected registry
`not found` results through the WebView2 preflight and opened the production
Windows fatal dialog. The visible dialog was titled **CodeAtlas could not start**,
explained that the Microsoft WebView2 Evergreen Runtime is required, and rendered
the injected `sk-WEBVIEW-SECRET` canary only as `[REDACTED]`. Selecting **OK**
allowed the interactive test to pass.

## Platform boundary

The macOS bundle structure and packaging fixtures were verified on Windows. No
native macOS WebKit execution is claimed here; that remains a macOS-host check.
