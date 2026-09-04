# CodeAtlas

[![CI](https://github.com/ThalesMMS/CodeAtlas/actions/workflows/test.yml/badge.svg)](https://github.com/ThalesMMS/CodeAtlas/actions/workflows/test.yml)
[![License: AGPL v3](https://img.shields.io/badge/License-AGPL_v3-blue.svg)](LICENSE)

![CodeAtlas workspace showing code intelligence, Hover Explain, and DeepWiki](docs/assets/codeatlas-overview.png)

CodeAtlas is a local-first code intelligence workspace with a Go backend and an embedded web editor. It builds a shared repository index for four connected experiences:

- **Hover Explain** for concise, evidence-backed symbol explanations.
- **See More** for callers, callees, evidence, and change-impact notes.
- **Codemaps** for query-driven architecture and execution-flow maps.
- **DeepWiki** for repository documentation generated from indexed code.

CodeAtlas combines deterministic Tree-sitter parsing, symbol and relationship graphs, BM25 retrieval, optional embeddings, and optional language-server evidence. Generated explanations require an OpenAI-compatible chat endpoint; CodeAtlas returns explicit errors when that provider is unavailable instead of inventing fallback prose.

> [!NOTE]
> CodeAtlas is an experimental developer tool. Review generated explanations and proposed changes before relying on them.

## Features

- Local workspace scanning, incremental indexing, and file watching.
- Monaco-based editing with versioned document overlays and safe saves.
- Grounded AI output with citations, inferences, uncertainties, and confidence metadata.
- Search across files, symbols, documentation, and structural relationships.
- Optional semantic evidence from `gopls`, `typescript-language-server`, `sourcekit-lsp`, `pyright-langserver`, and `rust-analyzer`.
- SQLite FTS5 storage and optional dense retrieval through an embeddings endpoint.
- Offline evaluation fixtures and browser-based end-to-end tests.

## Requirements

- Go 1.23 or newer.
- Node.js 26 or newer and npm 11.16.0 or newer, below npm 12.
- GNU Make and a POSIX-compatible shell for the provided `Makefile`.
- A matching C and C++ compiler pair available to cgo, such as Clang/Clang++
  or GCC/G++.
- An OpenAI-compatible `/chat/completions` endpoint for generated explanations;
  it can be configured after CodeAtlas starts.

Language servers and embeddings are optional. Their absence reduces semantic precision but does not disable deterministic AST indexing.

## Quick start

```bash
git clone https://github.com/ThalesMMS/CodeAtlas.git
cd CodeAtlas
cp .env.example .env
```

On PowerShell, copy the environment file with:

```powershell
Copy-Item .env.example .env
```

Edit `.env` with your provider URL, model, and API key, or configure the provider
from the Settings button after startup. Then run:

```bash
make frontend-install
make run
```

Open [http://127.0.0.1:43127](http://127.0.0.1:43127). By default, CodeAtlas loads the included `examples/tinycommerce` workspace.

To analyze another repository:

```bash
make run WORKSPACE=/absolute/path/to/your/repository
```

The server listens on `127.0.0.1:43127` by default. Override it with `LISTEN=host:port` or `CODEATLAS_LISTEN`.

### Build a native executable

The packaging scripts build the frontend, embed it in the Go binary, assemble
the native desktop artifact for the current operating system, and open the
integrated CodeAtlas window. The application does not open an external browser.

On Windows, install GCC/G++ or Clang/Clang++ and the Microsoft WebView2 Runtime,
then run from Command Prompt or PowerShell:

```powershell
.\build_package_and_run.cmd
```

This creates:

- `dist/codeatlas.exe` — the Windows GUI executable; double-click it to open the
  integrated desktop window without a companion console;
- `dist/codeatlas-server.exe` — launcher-internal console-subsystem executable
  for reliable foreground signal handling (use the `.cmd` entry point below);
- `dist/codeatlas-server.cmd` — launcher for server mode, with console logs and
  `Ctrl+C` shutdown.

On macOS, install the Xcode Command Line Tools if necessary, then run:

```bash
xcode-select --install
bash ./build_package_and_run.sh
```

To create the same packaged application without launching it, run:

```bash
bash ./just-build.sh
```

This creates:

- `dist/CodeAtlas.app` — the unsigned local macOS application bundle using the
  system WebKit runtime. It ships everything the optional Go, Python and
  JavaScript/TypeScript semantic features need: a pinned `gopls`, a private
  Node.js runtime copied from the build machine, and the pinned `pyright` and
  `typescript-language-server` packages from `packaging/lsp/package-lock.json`,
  exposed as launchers in `Contents/Resources/bin/`;
- `dist/codeatlas-server` — foreground server mode with terminal logs and
  `Ctrl+C` shutdown.

Both scripts load unquoted `CODEATLAS_*=VALUE` assignments from `.env`, reject
other variable names so packaging controls cannot be overwritten, use
`examples/tinycommerce` when `CODEATLAS_WORKSPACE` is unset, and forward extra
arguments to CodeAtlas. Persisted in-app Settings override `.env` independently
per field. A packaged first run without a provider remains open and offers the
**Settings** action; applying a valid endpoint and model continues startup in
the same process.

When the macOS app is opened directly from Finder without a configured
workspace, it uses a private writable workspace under the user’s CodeAtlas
application data directory until a different workspace is selected in Settings.

To analyze any repository from the app itself, open **Settings → General →
Workspace**, use **Choose folder…** to pick the folder with a native dialog
(macOS and Windows desktop builds; the browser UI accepts a typed absolute
path), click **Test and apply**, and then **Restart CodeAtlas** in the restart
banner. The process tears the runtime down and starts it again in place with
the saved configuration, and the window reopens on the new workspace without
relaunching the application. The workspace can be chosen before the LLM
provider is configured: changes that do not touch the provider are saved even
while CodeAtlas is still waiting for a valid endpoint.

For example:

```bash
bash ./build_package_and_run.sh -workspace /absolute/path/to/project -listen 127.0.0.1:9090
```

```powershell
.\build_package_and_run.cmd -workspace "C:\Code\project" -listen 127.0.0.1:9090
```

Closing the final desktop window gracefully stops the HTTP server, indexer, and
language-server processes. For foreground mode, use the platform launcher below;
on Windows it selects the console-subsystem build and supplies `-desktop=false`:

```powershell
.\dist\codeatlas-server.cmd -listen 127.0.0.1:9090
```

```bash
./dist/codeatlas-server -listen 127.0.0.1:9090
```

Packaging stages a clean deliverable before replacing `dist` and never copies
`.env`, settings JSON, API keys, credentials, or frontend source; the production
frontend remains embedded in the binary.
This version intentionally excludes an installer, DMG, updater, code signing,
and notarization. The local macOS bundle is unsigned and includes the CodeAtlas
application icon.

## Configuration

The repository includes a documented [`.env.example`](.env.example), and every
variable in it is also available in the in-app Settings drawer. Use the small
gear beside **Check for changes**, or **Settings** on the startup screen when a
provider has not been configured yet. Provider configuration is recoverable:
CodeAtlas remains running in `AWAITING_CONFIGURATION`, and a valid apply
continues startup without restarting the process.

Saved settings are global for the current operating-system user and apply to
all CodeAtlas workspaces. Values are resolved independently per field in this
order:

1. a saved Settings override;
2. the corresponding environment or `.env` value;
3. the built-in default.

Explicit `-workspace` and `-listen` command-line flags still describe the
current process invocation. When they differ from a saved restart-only value,
Settings shows both the saved and running values.

| Group | Variable | Type / default | Apply | Purpose |
|---|---|---|---|---|
| General | `CODEATLAS_WORKSPACE` | string / `.` | Restart | Workspace to analyze. |
| General | `CODEATLAS_LISTEN` | address / `127.0.0.1:43127` | Restart | HTTP listen address. |
| General | `CODEATLAS_MAX_FILE_BYTES` | integer / `1500000` | Restart | Maximum source-file size accepted by the indexer. |
| LLM | `CODEATLAS_LLM_BASE_URL` | HTTP(S) URL | Live | OpenAI-compatible chat API base URL, commonly ending in `/v1`. |
| LLM | `CODEATLAS_LLM_API_KEY` | secret / empty | Live | Credential sent to the chat provider. |
| LLM | `CODEATLAS_LLM_MODEL` | string | Live | Chat model exposed by the provider. |
| LLM | `CODEATLAS_LLM_REASONING_EFFORT` | string / empty | Live | `none`, `minimal`, `low`, `medium`, `high`, `xhigh`, or `max`. |
| LLM | `CODEATLAS_LLM_TIMEOUT` | Go duration / `10m` | Live | Business-request timeout. |
| Embeddings | `CODEATLAS_ENABLE_EMBEDDINGS` | boolean / `false` | Live | Enables dense retrieval. |
| Embeddings | `CODEATLAS_EMBEDDING_MODEL` | string / empty | Live | Embedding model; required when embeddings are enabled. |
| Embeddings | `CODEATLAS_EMBEDDING_BASE_URL` | HTTP(S) URL / empty | Live | Separate endpoint; empty uses the LLM base URL. |
| Embeddings | `CODEATLAS_EMBEDDINGS_API_KEY` | secret / empty | Live | Separate credential; empty uses the LLM credential. |
| Language servers | `CODEATLAS_GOPLS` | `auto` / `true` / `false` | Live | Go language-server mode. |
| Language servers | `CODEATLAS_GOPLS_PATH` | path / `gopls` | Live | Go language-server executable. |
| Language servers | `CODEATLAS_TYPESCRIPT_LSP` | `auto` / `true` / `false` | Live | JavaScript/TypeScript language-server mode. |
| Language servers | `CODEATLAS_TYPESCRIPT_LSP_PATH` | path / `typescript-language-server` | Live | JavaScript/TypeScript language-server executable. |
| Language servers | `CODEATLAS_TYPESCRIPT_SDK_PATH` | path / empty | Live | Optional directory containing `tsserver.js`. |
| Language servers | `CODEATLAS_SWIFT_LSP` | `auto` / `true` / `false` | Live | Swift language-server mode. |
| Language servers | `CODEATLAS_SWIFT_LSP_PATH` | path / `sourcekit-lsp` | Live | Swift language-server executable. |
| Language servers | `CODEATLAS_PYTHON_LSP` | `auto` / `true` / `false` | Live | Python language-server mode. |
| Language servers | `CODEATLAS_PYTHON_LSP_PATH` | path / `pyright-langserver` | Live | Python language-server executable. |
| Language servers | `CODEATLAS_RUST_LSP` | `auto` / `true` / `false` | Live | Rust language-server mode. |
| Language servers | `CODEATLAS_RUST_LSP_PATH` | path / `rust-analyzer` | Live | Rust language-server executable. |

The drawer labels each value as **Settings**, **.env**, or **Default**, and as
**Live** or **Restart required**. LLM, embeddings, and language-server changes
are prepared and probed before an atomic runtime swap. Existing in-flight LLM
requests and working language-server sessions finish on their old runtime.
Changing embeddings schedules a linked rebuild while lexical retrieval remains
available. Workspace, listen address, and maximum file size are saved
immediately and become running values after a restart. The restart banner
offers **Restart CodeAtlas**, which calls `POST /api/settings/restart` to stop
the runtime and start it again in the same process with the saved values; the
desktop window and the browser page reconnect automatically once the new
listener is bound (a changed listen address in browser mode still requires
opening the new URL). Explicit `-workspace` and `-listen` flags keep winning
over saved values across in-process restarts. The **Workspace** field also
offers **Choose folder…** in the desktop builds, which opens the native folder
dialog and fills in the selected path.

### Persistence and API keys

Non-secret overrides are stored in a versioned JSON file:

| Platform | Per-user settings file |
|---|---|
| Windows | `%APPDATA%\CodeAtlas\settings.json` |
| macOS | `~/Library/Application Support/CodeAtlas/settings.json` |
| Linux | `$XDG_CONFIG_HOME/CodeAtlas/settings.json`, or `~/.config/CodeAtlas/settings.json` |

API keys are never written to that JSON file. Keys explicitly entered in
Settings are stored under the `CodeAtlas` service in Windows Credential Manager
or macOS Keychain. The browser and settings API receive only a configured flag
and source label, never a saved key. Existing `.env` secrets are not imported
automatically: they remain environment-sourced until you explicitly replace
them in Settings.

**Reset to .env** transactionally removes every saved override and saved key,
then returns to environment/default values. If the system vault is unavailable
or locked, a secret change is rejected and the previous file, credential, and
runtime remain active.

The settings administration API is intentionally narrower than the rest of the
application. It requires a per-process token injected only into uncached HTML,
a loopback TCP peer, a loopback/`localhost` Host, and exact same-origin mutation
requests. It is not a remote administration interface, even if the main server
is bound to a non-loopback address.

Hover explicitly sends `reasoning_effort: "none"` for low latency. When
reasoning is enabled for the other LLM features, CodeAtlas treats each
feature's output limit as the budget for the final validated answer. For
`minimal`, `low`, `medium`, `high`, and `xhigh`, it adds the gateway's default
reasoning reserve (256, 1,024, 4,096, 8,192, and 16,384 tokens respectively) to
`max_completion_tokens`. This prevents hidden reasoning from consuming the
answer budget. Gateways with custom `REASONING_EFFORT_BUDGETS` should keep those
values aligned; `max` has no separate bounded reserve.

CodeAtlas does not install, update, or discover language servers automatically.

## Language support

| Language family | Extensions | Tree-sitter index | Editable overlay | Optional semantic provider |
|---|---|---:|---:|---|
| Go | `.go` | Yes | Yes | `gopls` |
| JavaScript | `.js`, `.mjs`, `.cjs`, `.jsx` | Yes | Yes | `typescript-language-server` |
| TypeScript | `.ts`, `.mts`, `.cts`, `.tsx` | Yes | Yes | `typescript-language-server` |
| Swift | `.swift` | Yes | Yes | `sourcekit-lsp` |
| Python | `.py` | Yes | Yes | `pyright-langserver` |
| Rust | `.rs` | Yes | Yes | `rust-analyzer` |

Project files such as `go.mod`, `package.json`, `tsconfig.json`, and Markdown files may be collected as bounded context, but they are not treated as editable source-language families.

## Repository layout

```text
CodeAtlas/
├── backend/            Go server, index, retrieval, storage, and AI services
├── frontend/           Embedded Monaco web application
├── e2e/                Playwright-based end-to-end test harness
├── eval/               Offline retrieval and output-quality evaluation
├── examples/           Example repositories used by tests and demos
├── .github/workflows/  Continuous integration workflows
├── Makefile            Development, build, test, and evaluation commands
└── LICENSE             GNU Affero General Public License v3
```

The frontend build writes hashed assets and `codeatlas-manifest.json` to `backend/internal/webui/dist/`. The Go binary embeds that directory, so run `make frontend-build` before invoking direct Go build or test commands.

## Development

Install frontend dependencies once:

```bash
make frontend-install
```

Common commands:

| Command | Description |
|---|---|
| `make run` | Build the frontend and start CodeAtlas. |
| `make build` | Build `dist/codeatlas`. |
| `make test` | Run frontend and Go tests. |
| `make check` | Run type checks, syntax checks, tests, repository verification, and `go vet`. |
| `make e2e` | Build the application and run the browser scenario suite. |
| `make e2e-budget` | Enforce the frontend bundle budget. |
| `make eval` | Run offline retrieval and output-quality gates. |
| `make fmt` | Format Go source files. |

The e2e harness uses a loopback fake OpenAI-compatible provider and a temporary copy of the example workspace. Generated reports are written under `e2e/reports/` and are ignored by Git.

Real language-server integration is opt-in because installed binaries and toolchains are machine-dependent. Set `CODEATLAS_TEST_REAL_LSP=1` before running the five `TestReal*` packages; the default suite uses deterministic protocol fakes.

## Privacy and safety

CodeAtlas reads the workspace path you explicitly configure. Source excerpts included in generated explanations are sent to the LLM or embeddings endpoints you configure, so review the provider's data-handling policy before analyzing private code.

The server binds to loopback by default. If you expose it on another interface, place it behind appropriate authentication and network controls. CodeAtlas does not install dependencies, run workspace scripts, invoke build systems, accept language-server workspace edits, or apply language-server commands.

Use [`.codeatlasignore`](.codeatlasignore) to exclude sensitive or irrelevant paths from indexing.

## Contributing

Issues and pull requests are welcome. Keep changes focused, add or update tests for behavior changes, and run the relevant checks before submitting a pull request.

## License

CodeAtlas is licensed under the [GNU Affero General Public License version 3](LICENSE), using the `AGPL-3.0-only` SPDX identifier.

See [THIRD_PARTY_NOTICES.md](THIRD_PARTY_NOTICES.md) for third-party components and their licenses.
