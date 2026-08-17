# Editor spikes

This nested package isolates the Monaco and CodeMirror 6 comparison for
ADR-010. Nothing under `frontend/spikes/` is imported by the production bundle.

## Commands

- `npm install`
- `npm run fixtures`
- `npm run build`
- `npm run test`
- `npm run benchmark`

Root Makefile wrappers:

- `make spike-editor-monaco`
- `make spike-editor-codemirror`
- `make spike-test`
- `make benchmark-editors`

`fixtures/generated/` and `dist/` are generated locally. Compact benchmark
summaries under `results/` are versioned.
