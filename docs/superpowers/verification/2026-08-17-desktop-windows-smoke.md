# Windows desktop package smoke — 2026-08-17

Host: Windows, repository `main`, commit under verification `64df055` plus the
packaging review fixes recorded in the following commit.

## Native desktop window

1. `build_package_and_run.cmd` completed `npm ci`, the embedded frontend build,
   and both native Go builds with `CGO_ENABLED=1` and tags `fts5 desktop`.
2. Launching `dist/codeatlas.exe` produced exactly one top-level window titled
   **CodeAtlas**. No companion console or new external-browser window appeared.
3. The bootstrap **Settings** action opened the in-process Settings drawer. The
   drawer exposed Workspace, listen address, LLM, embeddings, and language-server
   controls. This remained available when the arbitrary automation working
   directory caused the expected local workspace/state capability diagnostics.
4. Closing the native title-bar control removed the CodeAtlas window and left no
   `codeatlas.exe` process running.

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

## Platform boundary

The macOS bundle structure and packaging fixtures were verified on Windows. No
native macOS WebKit execution is claimed here; that remains a macOS-host check.
