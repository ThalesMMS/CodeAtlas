# Evaluation harness

Offline, network-free evaluation of Hover, See More, Codemap and DeepWiki. The
harness retains the ContextPack retrieval/grounding gates and also exercises the
real four-surface services with a contract-aware fake provider, producing a
deterministic output-quality scorecard.

## Running

```sh
make eval            # run + gate against eval/baseline.json (exits non-zero on violation)
make eval-update     # regenerate the baseline (intentional policy/schema change only)
make eval-judge      # optional live comparison through the configured LLM endpoint
```

`go test ./internal/eval/...` runs the same gate in CI (`TestEvalGatesPassAndAreDeterministic`).
Reports are written to `eval/report.json` and `eval/report.md` (gitignored).
The default and CI modes never contact a model. If `eval-judge` is requested
without `CODEATLAS_LLM_BASE_URL` and `CODEATLAS_LLM_MODEL`, the judge is reported
as skipped rather than failing the deterministic run.

## Corpus

- `examples/tinycommerce` — Go backend (`internal/order`: service/handler/repository/model + test), TS frontend (`web/src`: checkout → api → service), and root `go.mod` config evidence, giving callers/callees, imports cross-file, frontend→API→service flow, and line-anchored project metadata.
- `examples/swiftcommerce` — Swift protocols, structs, actors, extensions,
  overloads, enum cases, `async throws` methods and XCTest declarations without
  a package manifest or any executable build hook.
- `examples/pythoncommerce` — Python packages, absolute and relative imports,
  protocols, inheritance, decorators, annotations, async methods, properties,
  pytest and unittest shapes without an environment or executable build hook.
- `examples/rustcommerce` — Rust modules, use aliases, structs, traits and
  impls, associated functions, async methods, declarative macros and cfg/test
  declarations without Cargo metadata, build scripts or executable hooks.
- `eval/corpus/ambiguous` — homonym methods (`Fast.Process` vs `Slow.Process`), an interface with implementations, and a `ping`/`pong` call cycle.
- `eval/corpus/adversarial` — comments/strings containing model instructions, fake citations (`ev:FAKE-…`), fake paths (`/etc/passwd`, `../../secret/…`) and `<script>`; all must be treated as data.

Fixtures are small, original and live in this repository — no third-party code.

## Cases

`eval/cases.json` is the versioned case list. Each case names a feature, a target
(path/line/column) or query/scope, and the expected ground truth: resolved symbol,
relevant symbol keys (for Recall@K), required evidence-content substrings (such
as extracted doc comments), required exact evidence paths, forbidden paths, and
minimum graph nodes.

## Deterministic retrieval metrics

Per case and aggregated:

- **target resolution** — the pack's target resolves to the expected symbol.
- **Recall@K** — fraction of annotated relevant symbols present in the evidence.
- **required evidence content** — annotated substrings must occur in at least one
  evidence item; absence is a case error.
- **evidence count**, **duplicate rate**, **no-provenance rate**.
- **forbidden-path hits** — evidence under an annotated forbidden prefix.
- **budget violations** — pack used bytes over the requested budget.
- **grounding invalid** — the built pack fails `contextpack.ValidatePack`.
- **hash stability** — a second independent build reproduces the exact `ContextPackHash` and ordering.

## Gates (`eval/baseline.json`)

The baseline is **versioned per policy/schema**. CI fails when, relative to it:

| gate | threshold |
|---|---|
| target resolution | `< 100%` of exact-target cases |
| grounding invalid | `> 0` |
| budget violations | `> 0` |
| forbidden-path hits | `> 0` |
| determinism | any non-reproducible hash |
| mean Recall@K | drops more than `0.05` below baseline |
| policy/schema versions | differ from the baseline (must `make eval-update` + note in PR) |

A case error (e.g. `minNodes` not met) is always a violation.

## Output-quality scorecard

The offline provider returns schema-valid IDs selected from the actual prompt
contracts. The real services then resolve and render the output. Per surface the
report records:

- citation density;
- code-block presence for See More and DeepWiki;
- speculation phrases such as `probably` and `there is no evidence`;
- expected structure (See More sections, Codemap flows/anchored steps, DeepWiki
  archetypes/pages/links/diagrams);
- Codemap external-node ratio;
- DeepWiki statistics-only detection and page count.

Current surface scores and speculation/noise ceilings are committed in
`eval/baseline.json` and gated in CI.

## Recorded comparisons

`eval/fixtures/manifest.json` records the query or exact target plus split
markers for four author-owned comparison files. Each file preserves the
historical CodeAtlas output and the reference output. Rubrics compare their
structure and features, never exact prose. The See More fixture intentionally
keeps the known `probably` case as a speculation-detector regression test.

## LLM evaluation modes

1. **offline (required, default)** — contract-aware fake provider plus real
   services/renderers; fully deterministic and CI-gated.
2. **recorded (required)** — structural rubrics over the four comparison
   fixtures.
3. **live judge (optional)** — fixed 1–5 groundedness/completeness/usefulness
   prompt against the references; manual only and never a CI requirement.

The required offline gate never compares exact prose. Output validation checks
schema, EvidenceIDs, field coverage and the absence of unknown references; only
the manually requested live judge evaluates prose against the recorded reference.
