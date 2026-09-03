# Codemap Generation Contract

These instructions belong in documentation and agent policy, not in the user-facing `codemap.json` payload.

## 1. Establish the analyzed state

Record:

- repository name;
- full Git commit SHA, or `working-tree` when uncommitted content is intentionally mapped;
- `clean` or `dirty` working-tree state;
- UTC creation timestamp;
- the exact question the codemap answers.

Never silently map a different revision from the one declared in metadata.

## 2. Design sections before assigning IDs

Use 3–8 sections for most feature maps. Typical closure is:

```text
trigger → boundary conversion → validation → orchestration
        → core behavior → durable/external effect → verification
```

IDs are `<section-number><lowercase-letter>`: `1a`, `1b`, `2a`. The numeric prefix must match the owning section. A node ID refers to one concrete source entity only.

## 3. Keep the canonical payload factual

A node source anchor contains only:

```json
{
  "path": "src/application/service.ts",
  "startLine": 40,
  "endLine": 52,
  "targetLine": 48,
  "symbol": "FeatureService.run",
  "snippet": "await repository.save(entity);"
}
```

Rules:

- use repository-relative `/` paths;
- copy the exact target source line into `snippet`;
- keep ranges contiguous and normally below 80 lines;
- do not put `sourceContext`, copied files, coordinates, HTML, or generation instructions in the canonical JSON;
- allow `verify_repository.py` to derive the line-numbered context from the checkout.

## 4. Prove relationships, not just endpoints

Classify each edge:

- `temporal`: invokes, awaits, dispatches, emits, commits, returns;
- `data`: passes, parses, transforms, loads, serializes, reads, writes;
- `structural`: imports, implements, registers, configures, contains, tests.

A `verified` edge requires one or more evidence ranges whose target line directly supports the relation:

```json
{
  "path": "src/application/service.ts",
  "startLine": 46,
  "endLine": 50,
  "targetLine": 48,
  "snippet": "await repository.save(entity);",
  "claim": "The use case hands the updated entity to persistence."
}
```

Proof that A exists and B exists does not prove A calls B.

Use `inferred` plus an explicit `reason` for:

- unresolved dependency injection;
- protocol/interface dispatch without construction proof;
- event publication without a verified consumer binding;
- cross-process delivery;
- reflection or string-based lookup;
- framework convention not represented in the checkout;
- generated bindings that are absent or unreproducible;
- imported legacy edges without relationship evidence.

Use conservative guide wording for inferred paths.

## 5. Build the hierarchy independently from the graph

Trace nesting explains stages and branches. It does not create edges.

- `group`: ownership or stage boundary;
- `note`: condition, transition, or branch label;
- `node`: source-backed entity.

Every node should appear exactly once in a trace. Map groups should represent architectural or flow boundaries, not one group per file.

## 6. Write reference-backed prose

Use clickable references such as `[1a]` or `[2b, 2c]` for claims whose source location matters.

- top summary: approximately 2–4 sentences;
- section summary: one sentence;
- **Motivation**: why the stage exists;
- **Details**: how the verified code participates;
- material uncertainty: explicit and local.

Do not narrate structural edges as runtime order. Tests verify behavior but are not the final production step unless the map specifically concerns test execution.

## 7. Do not lay out coordinates manually

Set group order, direction, and optional featured group only. The viewer derives node and group geometry. An agent must not invent `x`, `y`, `width`, or `height` values.

## 8. Final gates

Before delivery:

```bash
python3 scripts/validate.py codemap.json --strict
python3 scripts/verify_repository.py codemap.json --repository /path/to/repository --strict
python3 scripts/build.py --repository /path/to/repository --strict
python3 scripts/smoke_test.py --input dist/index.html --require-browser
python3 scripts/render_preview.py --input dist/index.html --output preview-guide.png --view guide
python3 scripts/render_preview.py --input dist/index.html --output preview-map.png --view map
python3 scripts/render_preview.py --input dist/index.html --output preview-mobile.png --view guide --mobile
```

Inspect the previews. Reject clipped metadata, horizontal mobile overflow, overlapping map nodes, unreadable edges, stale placeholder content, incorrect deep links, or source content that did not come from the declared checkout.
