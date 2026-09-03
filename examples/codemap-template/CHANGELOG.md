# Changelog

## 3.0.0 — 2026-08-21

- Replaced generic schema versioning with `codeatlas-codemap-presentation/v1`.
- Split canonical claims from repository-derived source context.
- Removed `_instructions`, `sourceContext`, `contextStartLine`, and manual map coordinates from `codemap.json`.
- Added deterministic grouped auto-layout.
- Rebuilt map nodes as semantic HTML buttons with an SVG edge overlay.
- Replaced custom disclosure controls with native `<details>/<summary>`.
- Added responsive split-panel/drawer behavior, keyboard focus management, `Escape` close, and mobile overflow tests.
- Added system, light, and dark themes plus `en`/`pt-BR` interface strings.
- Added strict JSON Schema closure with `additionalProperties: false`.
- Added repository-derived source snapshots and exact range/snippet/revision checks.
- Added direct evidence requirements for verified edges and reasons for inferred edges.
- Added a legacy `codeatlas-codemap/v3` converter that conservatively marks imported edges inferred.
- Added hashed CSP, offline build, browser interaction tests, preview rendering, and deterministic packaging.
