# Third-party notices

This directory vendors the Tree-sitter runtime and grammars. The authoritative
provenance (version, commit, upstream URL, archive SHA-256, license) is recorded
in [`deps.lock.json`](./deps.lock.json) and verified by `make verify-treesitter`.

All components are licensed under the MIT License. Full texts:

| Component | SPDX | License file |
|---|---|---|
| tree-sitter (C runtime) | MIT | [`LICENSE.tree-sitter`](./LICENSE.tree-sitter) |
| tree-sitter-go | MIT | [`LICENSE.tree-sitter-go`](./LICENSE.tree-sitter-go) |
| tree-sitter-javascript | MIT | [`LICENSE.tree-sitter-javascript`](./LICENSE.tree-sitter-javascript) |
| tree-sitter-typescript / TSX | MIT | [`LICENSE.tree-sitter-typescript`](./LICENSE.tree-sitter-typescript) |
| tree-sitter-swift | MIT | [`LICENSE.tree-sitter-swift`](./LICENSE.tree-sitter-swift) |
| tree-sitter-python | MIT | [`LICENSE.tree-sitter-python`](./LICENSE.tree-sitter-python) |
| tree-sitter-rust | MIT | [`LICENSE.tree-sitter-rust`](./LICENSE.tree-sitter-rust) |

Upstream: <https://github.com/tree-sitter> and
<https://github.com/alex-pinkus/tree-sitter-swift>. No modifications are made to the
vendored sources; the cgo adapter (`treesitter.go`) and the `*.c` build shims are
original to this repository.
