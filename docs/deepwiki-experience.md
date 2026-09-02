# DeepWiki-inspired knowledge workspace

## Decision

CodeAtlas keeps its Go backend, deterministic repository index, ContextPacks,
artifact lifecycle, and existing Vite/Monaco application. The new interface
adapts the information architecture and interaction patterns of
`AsyncFuncAI/deepwiki-open` into a static surface that can continue to be
embedded in the CodeAtlas binary.

This is intentionally not a Next.js transplant. A permanent Node server would
break the single-process desktop and server packaging model and duplicate
routing already owned by the Go API.

## User experience

The main toolbar now exposes **Explore**. It opens a full-screen knowledge
workspace with two modes:

### DeepWiki

- hierarchical page navigation derived from `parentSlug`;
- page filtering without another backend request;
- current snapshot and lifecycle status;
- rich Markdown, tables, code blocks, backend-produced Mermaid diagrams, and
  related-page links;
- source, page-outline, and artifact-provenance panels;
- one-click source preview with cited lines highlighted;
- refresh progress driven by the existing CodeAtlas job API.

### Codemap

- architecture/flow question composer and reusable example prompts;
- progress while retrieval, graph expansion, and grounded narration execute;
- deterministic graph visualization through the local `mermaid-lite` renderer;
- one or more guided execution flows;
- source chips for every anchored flow step;
- source, outline, and artifact-provenance panels.

The workspace supports narrow desktop and mobile layouts. `Command+Shift+E` on
macOS and `Ctrl+Shift+E` elsewhere toggle it. Escape closes the source drawer,
then responsive panels, then the workspace.

## API mapping

The UI consumes the existing CodeAtlas contracts directly:

| Experience | CodeAtlas API |
| --- | --- |
| Load current wiki | `GET /api/deepwiki` |
| Refresh wiki | `POST /api/deepwiki/refresh` |
| Generate Codemap | `POST /api/codemaps` |
| Poll work | `GET /api/jobs/{jobId}` |
| Read result | `GET /api/jobs/{jobId}/result` |
| Preview evidence | `GET /api/file?path=...` |

No DeepWiki-Open Python task registry, FAISS index, cache, provider model, or API
route is introduced. CodeAtlas remains the only source of truth.

## Security and grounding

`knowledge-model.ts` normalizes unknown JSON before presentation and permits only
three link classes:

1. validated `wiki:<slug>` links;
2. workspace-relative source paths with optional `#Lx-Ly` fragments;
3. external `https://` URLs.

Absolute paths, traversal, protocol-relative URLs, and active schemes such as
`javascript:` are rejected. Generated HTML is not admitted. Markdown is rendered
through a small local renderer, text is escaped exactly once, and source
navigation is reconstructed from validated path and line fields.

Mermaid output continues through `src/mermaid-lite.ts`, which accepts only the
backend-produced CodeAtlas subset and emits CSP-safe SVG without runtime style or
script injection. The new surface adds no remote assets and no production npm
dependencies.

## Files

- `frontend/src/knowledge-{types,links,markdown,model}.ts` — typed contracts, normalization, hierarchy, safe links, and local Markdown rendering.
- `frontend/src/knowledge-api.ts` — existing CodeAtlas job, wiki, codemap, and source API adapter.
- `frontend/src/knowledge-{view,workspace}.ts` — full-screen controller, DeepWiki/Codemap views, source preview, and persistence.
- `frontend/knowledge-{workspace,content}.css` — scoped visual system, documentation typography, diagrams, tours, and responsive rules.
- `frontend/tests/knowledge-model.test.ts` — hierarchy, wire-contract,
  sanitization, source-link, Mermaid, and Codemap regressions.

## Verification

Run the normal frontend gate:

```bash
make frontend-check
```

The added TypeScript tests are included in `frontend/tsconfig.test.json` and run
with the existing `npm test` command. The implementation does not alter backend
schemas or routes.

## Attribution

The visual and interaction direction is adapted from
`AsyncFuncAI/deepwiki-open`, licensed under the MIT License. Its license notice
is preserved in `frontend/DEEPWIKI_OPEN_LICENSE`. CodeAtlas-specific logic,
security constraints, data normalization, and API integration are implemented
for this repository and remain under the CodeAtlas project license.
