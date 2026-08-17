# CodeAtlas frontend

This package owns the production frontend build. It wraps the app in a
Vite/TypeScript entry point and writes generated assets directly to
`backend/internal/webui/dist/` for Go embedding.

## Commands

- `npm ci`: deterministic dependency install from `package-lock.json`.
- `npm run dev`: loopback Vite dev server with `/api` proxied to the Go backend.
- `npm run build`: production build with hashed JS/CSS assets and manifest.
- `npm run check`: TypeScript entrypoint, production JS syntax and Node tests.
- `npm run audit:high`: supply-chain visibility gate for high/critical findings.

Root Makefile wrappers:

- `make frontend-install`
- `make frontend-dev`
- `make frontend-build`
- `make frontend-check`
- `make frontend-test`

## Editor dependency

ADR-010 selects Monaco and production ships exact `monaco-editor@0.53.0`.
`app.js` remains the single application controller loaded by `src/main.ts`; it
lazy-imports `src/monaco-editor-adapter.ts` only after mandatory readiness data
has loaded. The adapter owns one `codeatlas://workspace/...` model per
DocumentID, Go/JavaScript/TypeScript/Swift/Python/Rust lexical tokenizers, view state, commands,
decorations and self-hosted editor/TypeScript workers. Failure to load Monaco is
an explicit `EDITOR_RUNTIME_UNAVAILABLE` bootstrap failure; there is no textarea
fallback or parallel controller.

For Go, JavaScript/TypeScript, Swift, Python and Rust overlays, the application requests
backend-normalized semantic tokens and diagnostics only after an acknowledged
version. Responses must match legend `codeatlas-semantic-tokens/v1`, DocumentID,
DocumentVersion and content hash; available semantic responses also carry a
validated provider session. The adapter renders canonical token classes as
owned decorations and never executes LSP commands or markup.

The chosen version has a clean `npm audit` result in the locked production
dependency graph. Monaco stays out of the initial JS/CSS entrypoints; its lazy
assets and worker baseline are gated by `p10-monaco-v1`.
