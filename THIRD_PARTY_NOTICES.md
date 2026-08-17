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
| `webview/webview` (bundled by `webview_go`) | upstream snapshot in pinned module | MIT |
| Microsoft WebView2 headers (bundled by `webview_go`) | upstream snapshot in pinned module | BSD-3-Clause |
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
