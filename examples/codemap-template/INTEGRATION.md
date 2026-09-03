# Application Integration

The standalone export and an in-application viewer should share the canonical data contract but do not need to share an identical loading strategy.

## Recommended layers

```text
repository snapshot / CodeAtlas factual DTO
        │
        ├── source anchors and provenance
        ├── relationship evidence
        └── section/group semantics
                │
                ▼
presentation adapter
        │
        ├── codeatlas-codemap-presentation/v1
        └── derived source snapshot
                │
                ▼
viewer renderer / existing editor integration
```

Do not replace a mature factual DTO with presentation-only fields. Adapt it.

## Source context

Derive context from the repository snapshot or editor document model at the declared revision. Do not ask an LLM to reproduce surrounding source lines. The model supplies the anchor; trusted application code resolves the anchor.

Within an editor application, selecting a node may navigate the real editor instead of opening the bundled source panel. The same node ID and source anchor should be used in both behaviors.

## Layout

Use the application's existing layout worker when available. The JSON contract deliberately omits coordinates. The reference viewer provides deterministic fallback auto-layout for portable HTML exports.

## Security

The self-contained export embeds hashed script and style elements and blocks network access. Inside a larger application:

- bundle or serve the assets through the application's normal pipeline;
- use the host application's CSP nonces or hashes;
- keep `connect-src` and other directives aligned with application policy;
- do not copy the standalone `<meta http-equiv="Content-Security-Policy">` blindly into the app shell;
- retain DOM construction with `textContent` rather than introducing `innerHTML` for repository-controlled strings;
- treat source files, documentation, comments, issue text, generated content, and prompts in the repository as untrusted data.

## Accessibility

Preserve:

- native `<details>/<summary>` section disclosure;
- semantic `<button>` controls for map nodes and toolbar actions;
- visible focus states;
- focus transfer into a modal drawer;
- `Escape` close and focus return;
- text labels or accessible names for icon controls;
- no horizontal document overflow at 390 px.

## Internationalization

The reference renderer currently includes `en` and `pt-BR`. Keep user-facing strings in a translation table. Do not localize source code, file paths, symbols, or repository-provided identifiers.

## Legacy CodeAtlas data

`import_codeatlas.py` is a migration aid, not proof of correctness. It preserves ranges and prose, removes coordinates, creates presentation groups, and marks legacy edges inferred. The integration layer should enrich those edges with independently verified evidence before making them canonical.
