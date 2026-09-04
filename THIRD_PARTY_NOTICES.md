# Third-party notices

The project vendors source code required by the cgo Tree-sitter bridge so that
the backend can be built without downloading Go modules at build time.

## Vendored source

| Component | Vendored version | License file |
|---|---:|---|
| Tree-sitter runtime | 0.22.4 | `backend/internal/treesitter/LICENSE.tree-sitter` |
| tree-sitter-go grammar | 0.23.4 | `backend/internal/treesitter/LICENSE.tree-sitter-go` |
| tree-sitter-javascript grammar | 0.23.1 | `backend/internal/treesitter/LICENSE.tree-sitter-javascript` |
| tree-sitter-typescript/TSX grammars | 0.23.2 | `backend/internal/treesitter/LICENSE.tree-sitter-typescript` |
| tree-sitter-swift grammar | 0.7.3 | `backend/internal/treesitter/LICENSE.tree-sitter-swift` |
| tree-sitter-python grammar | 0.23.6 | `backend/internal/treesitter/LICENSE.tree-sitter-python` |
| tree-sitter-rust grammar | 0.23.3 | `backend/internal/treesitter/LICENSE.tree-sitter-rust` |

Generated parser sources remain subject to their corresponding upstream
licenses. The complete runtime headers/sources and grammar sources are under
`backend/internal/treesitter/vendor` and `backend/internal/treesitter/grammars`.

## Go module dependencies

| Component | Version | License |
|---|---:|---|
| `github.com/fsnotify/fsnotify` | 1.10.1 | BSD-3-Clause |
| `github.com/mattn/go-sqlite3` | 1.14.45 | MIT |
| `github.com/webview/webview_go` | `v0.0.0-20240831120633-6173450d4dd6` | MIT |
| `webview/webview` (bundled by `webview_go`) | commit `fb6b17d826041411e6346cd9a785a5ceba7987c4` | MIT |
| Microsoft WebView2 headers (bundled by `webview_go`) | 1.0.1150.38 | BSD-3-Clause |
| `github.com/zalando/go-keyring` | 0.2.6 | MIT |
| `golang.org/x/sys` | 0.26.0 | BSD-3-Clause |

## Frontend and E2E dependencies

| Component | Locked version | License | Usage |
|---|---:|---|---|
| `monaco-editor` | 0.53.0 | MIT | Lazy production editor and self-hosted workers |
| `playwright-core` | 1.61.1 | Apache-2.0 | Development-only Chrome E2E harness |

These packages are installed deterministically from their npm lockfiles. The
production Monaco dependency graph passes `npm audit` with zero findings at the
time of integration; Playwright is not embedded in the CodeAtlas binary.

## Bundled language servers

The macOS application bundle includes `golang.org/x/tools/gopls` v0.23.0 under
the Go project's BSD-3-Clause license. The required binary-distribution notice
is copied into the application as `Contents/Resources/gopls-LICENSE` from
`packaging/licenses/gopls-LICENSE`.

The bundle also ships a private Node.js runtime (copied from the build
machine's Node.js 26 installation, MIT and bundled third-party licenses in
`Contents/Resources/node-LICENSE`) together with the npm packages pinned in
`packaging/lsp/package-lock.json`:

| Package | Version | License | Notice in bundle |
|---|---:|---|---|
| `pyright` | 1.1.413 | MIT | `Contents/Resources/pyright-LICENSE` |
| `typescript-language-server` | 6.0.0 | Apache-2.0 | `Contents/Resources/typescript-language-server-LICENSE` |
| `typescript` | 5.9.3 | Apache-2.0 | `Contents/Resources/typescript-LICENSE` |

The packages are installed with `npm ci --ignore-scripts` and copied verbatim
into `Contents/Resources/lsp/node_modules/`. Source notices live under
`packaging/licenses/`.

## Adaptation sources

| Component | Source revision | License | Usage |
|---|---:|---|---|
| `AsyncFuncAI/deepwiki-open` | commit [`ff543868829b8e422fdc18817d7c7db15a68dfdd`](https://github.com/AsyncFuncAI/deepwiki-open/commit/ff543868829b8e422fdc18817d7c7db15a68dfdd) | MIT | Visual and interaction patterns adapted for the CodeAtlas DeepWiki/Codemap knowledge workspace; notice in `frontend/DEEPWIKI_OPEN_LICENSE` |

DeepWiki-Open is an adaptation source, not an installed npm package or runtime
dependency. The adaptation introduces no second application server.
