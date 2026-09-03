# Hybrid Codemap Template v3

A clean-room, evidence-backed codemap template for repository mapping. It combines a hierarchical guide and grouped map with a factual source contract suitable for an LLM agent or an application such as CodeAtlas.

This package is a reference implementation, not code extracted from Devin or Windsurf.

## What changed in v3

The v3 contract separates facts from derived presentation data:

```text
codemap.json                         canonical claims and source anchors
        │
        ├── validate.py              schema and semantic checks
        ├── verify_repository.py     resolves every anchor against a checkout
        │       └── .build/source-snapshot.json
        │                            derived line-numbered source context
        └── build.py
                └── dist/index.html  self-contained interactive artifact
```

The canonical JSON intentionally contains **no source context, raw file contents, manual node coordinates, runtime instructions, or HTML fragments**. Source context is copied from the analyzed checkout after exact path/line/snippet verification. Map positions are derived deterministically in the viewer.

## Interface

The generated viewer includes:

- a metadata header with repository revision and working-tree status;
- native `<details>/<summary>` sections and optional **See more** guides;
- nested traces for groups, conditions, branches, and source nodes;
- stable references such as `[1a]` and deep links such as `#2b`;
- a grouped visual map with auto-layout, typed edges, zoom, pan, and reset;
- HTML `<button>` map nodes rather than opaque SVG interaction regions;
- a split source panel on wide screens and an accessible drawer on narrow screens;
- focus transfer, `Escape` close, and focus restoration for the drawer;
- system, light, and dark themes;
- English and Brazilian Portuguese interface labels;
- a hashed Content Security Policy for the self-contained export;
- no network dependency and no `innerHTML` rendering.

## Canonical model

`codemap.json` uses:

```text
schemaVersion: codeatlas-codemap-presentation/v1
meta
presentation
sections
  └── trace items: group | note | node
groups
nodes
  └── source: path + range + target line + symbol + exact line snippet
edges
  └── temporal | data | structural
      verified + direct relationship evidence
      inferred + explicit reason
```

A verified edge must prove the relationship itself. Finding both endpoint symbols is not enough.

## Run the template

From this directory:

```bash
python3 scripts/check_all.py \
  --require-browser \
  --render-previews
```

The placeholder uses `fixtures/example-repository` so the complete pipeline can be tested without another checkout.

Individual commands:

```bash
python3 scripts/validate.py codemap.json

python3 scripts/verify_repository.py \
  codemap.json \
  --repository fixtures/example-repository \
  --allow-non-git

python3 scripts/build.py

python3 scripts/smoke_test.py \
  --input dist/index.html \
  --require-browser

python3 scripts/render_preview.py \
  --input dist/index.html \
  --output preview-guide.png \
  --view guide \
  --node 2b
```

For a real codemap, replace the sample content, record the exact repository revision, set `meta.template` to `false`, and run strict gates:

```bash
python3 scripts/validate.py codemap.json --strict

python3 scripts/verify_repository.py \
  codemap.json \
  --repository /path/to/repository \
  --strict

python3 scripts/build.py \
  --repository /path/to/repository \
  --strict
```

Use `--allow-dirty` only when the codemap intentionally describes uncommitted content and `meta.repository.workingTree` records that state.

## Import an older CodeAtlas payload

The compatibility converter accepts `codeatlas-codemap/v3`:

```bash
python3 scripts/import_codeatlas.py \
  /path/to/legacy-codemap.json \
  /tmp/codemap.json
```

Legacy source ranges and prose are preserved where possible. Legacy edges are marked `inferred` because the old format does not contain direct relationship evidence. They must be checked and upgraded before finalization.

## Package deterministically

```bash
python3 scripts/package.py \
  --root . \
  --output ../codemap-template-hybrid-v3.zip \
  --folder-name codemap-template-hybrid-v3
```

The script writes `MANIFEST.sha256`, normalizes ZIP timestamps and permissions, excludes caches and build-only source snapshots, and sorts entries deterministically.

## Dependencies

Core validation and build use Python 3.11+ standard library. Optional quality gates use:

- `jsonschema` for Draft 2020-12 schema validation;
- Playwright and Chromium for interaction tests and preview rendering;
- Node.js only for `node --check` of the viewer script.

## Authoring rules

Read [GENERATION.md](GENERATION.md) before replacing the placeholder. Application integration guidance is in [INTEGRATION.md](INTEGRATION.md).
