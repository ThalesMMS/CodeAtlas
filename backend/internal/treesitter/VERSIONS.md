# Vendored Tree-sitter components

The authoritative source of truth is [`deps.lock.json`](./deps.lock.json). This
file is a human-readable summary; `make verify-treesitter` checks the vendored
tree against the lock offline (and runs in CI via `make check`).

| Component | Type | Version | Commit |
|---|---|---|---|
| tree-sitter (C runtime) | runtime | 0.22.4 | `51ec7527` |
| tree-sitter-go | grammar | 0.23.4 | `3c3775fa` |
| tree-sitter-javascript | grammar | 0.23.1 | `3a837b6f` |
| tree-sitter-typescript / TSX | grammar | 0.23.2 | `f975a621` |
| tree-sitter-swift | grammar | 0.7.3 | `31d17fe7` |
| tree-sitter-python | grammar | 0.23.6 | `bffb65a8` |
| tree-sitter-rust | grammar | 0.23.3 | `3d087c3d` |

The Go package is a deliberately small local cgo adapter rather than the full
upstream Go binding. It owns parser/tree cleanup and exposes only operations used
by `internal/parser`.

## Bootstrap & verify

- `make bootstrap-treesitter` — downloads runtime/grammars from the lock's
  allowlisted URLs (github.com), verifying each archive's SHA-256 before
  extracting. Requires network; `go build` itself never accesses the network.
- `make verify-treesitter` — offline; recomputes the vendored content hash
  (`vendorTreeSha256`) and checks presence/licenses against the lock. An
  intentional change must update `deps.lock.json` and be noted in the PR.

## Toolchain

`CGO_ENABLED=1` is required to build (the cgo wrapper compiles the vendored C
runtime and generated parsers). A C11 toolchain (clang/gcc) is needed; macOS
(arm64/amd64) and Linux (amd64/arm64) are the supported platforms.
