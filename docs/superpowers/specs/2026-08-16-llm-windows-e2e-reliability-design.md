# LLM, DeepWiki, Windows, and E2E Reliability Design

**Date:** 2026-08-16

**Status:** Approved

## Objective

Resolve the four defects found during the complete CodeAtlas validation:

1. DeepWiki's LLM planner returns successful HTTP responses that fail local plan validation and force a deterministic fallback.
2. The OpenAI-compatible provider cannot send the production gateway's `reasoning_effort` control.
3. The complete Go test suite is not portable to Windows because several tests assume POSIX permissions, paths, line endings, MIME registration, or real tool availability.
4. Browser E2E tests pass Node `.mjs` fake language servers directly to Go's process launcher, which cannot execute them as native programs on Windows.

The result must keep CodeAtlas provider-compatible, preserve deterministic fallbacks, avoid executing workspace code, and pass the existing Linux/macOS behavior as well as Windows validation.

## Global Constraints

- Node.js remains `>=26.0.0` and npm remains `>=11.16.0 <12`.
- The application remains compatible with OpenAI-style `/v1/chat/completions` providers that only understand legacy `max_tokens`.
- `reasoning_effort` accepts only `none`, `minimal`, `low`, `medium`, `high`, `xhigh`, or `max`.
- API keys, endpoint URLs, prompts, raw model output, and workspace contents must not be added to logs, fixtures, reports, or versioned files.
- DeepWiki keeps its deterministic manifest as the final availability fallback.
- E2E fake language servers remain read-only protocol fixtures and must not execute code from the indexed workspace.
- No checked-in executable binaries are introduced.

## 1. OpenAI-Compatible Reasoning Control

### Configuration contract

Add an optional `CODEATLAS_LLM_REASONING_EFFORT` environment variable. An empty value preserves the current provider request shape. A non-empty value is normalized to lowercase and rejected at startup unless it is one of the seven values listed in the global constraints.

The loaded value is exposed as `Config.LLMReasoningEffort`, passed through the composition root, and stored in the OpenAI-compatible provider options. `.env.example`, `Makefile`, and the README configuration table document the setting. The local ignored `.env` used for live verification is configured as:

```dotenv
CODEATLAS_LLM_REASONING_EFFORT=medium
```

### Request contract

All business chat requests, including structured-output requests, use one shared token-and-reasoning body builder:

- When reasoning effort is empty, omit `reasoning_effort` and send the requested limit as `max_tokens`.
- When reasoning effort is configured, send `reasoning_effort` and send the requested limit as `max_completion_tokens`.
- Never send both token limit fields in one request.
- Preserve the existing temperature, messages, model, structured schema, fallback, timeout, and error-classification behavior.

Provider readiness probes remain minimal compatibility probes and do not enable reasoning. This prevents a configured medium/high reasoning budget from consuming the tiny probe completion allowance or making application startup unnecessarily expensive. Startup validation catches invalid local effort names; live business-call verification proves gateway support.

### Tests

Provider contract tests capture real HTTP request bodies and independently assert:

- legacy mode omits `reasoning_effort`, includes `max_tokens`, and omits `max_completion_tokens`;
- configured mode includes `reasoning_effort=medium`, includes `max_completion_tokens`, and omits `max_tokens`;
- the same behavior applies to structured output and its response-format compatibility retry;
- config loading accepts every supported value, normalizes case/whitespace, and rejects invalid values before startup.

## 2. DeepWiki Planner Reliability

### Schema grounding

The planner remains responsible for producing `WikiPlan` v1, but its response schema is derived from the current repository inventory instead of using only the generic static schema. The dynamic schema keeps the existing strict object shape and additionally:

- constrains every `scopePaths` item to the exact normalized paths present in the planning inventory;
- requires non-empty titles;
- constrains slugs and non-empty parent slugs to CodeAtlas' existing lowercase slug format;
- keeps the archetype enum and strict unknown-property rejection.

The deterministic manifest is included in the planner prompt as a valid structural baseline. The model may refine titles, grouping, and module/layer coverage, but the prompt explicitly requires the overview root, required archetypes, maximum depth, valid parent references, and exact inventory paths.

### Validation and retry

Local semantic validation stays authoritative because JSON Schema cannot prove graph connectivity or parent-reference integrity. The first invalid response produces the existing bounded repair attempt, but the repair prompt receives the sanitized, specific validation cause. No invented path, missing parent, or malformed slug is silently normalized.

If both attempts fail, DeepWiki still commits the deterministic manifest. Its omission records a bounded diagnostic category such as `unknown scope path`, `missing required archetype`, or `invalid parent reference` rather than discarding the reason behind the generic fallback message. Diagnostics never include model text or secrets.

### Acceptance

Tests cover dynamic path enums, malformed hierarchy rejection, retry diagnostics, and deterministic fallback. A live DeepWiki generation against the configured production endpoint must finish with an LLM-accepted plan: no planner-validation fallback omission, valid page hierarchy, successful page generation, and successful artifact retrieval.

## 3. Windows-Portable Go Suite

Each failing test is corrected at the boundary that caused the platform dependence:

- State-area unwritability is exercised with a cross-platform structural filesystem conflict instead of `chmod`.
- Scanner read failure uses a narrowly injected read boundary that returns a controlled error for one source file while the real scan/transaction logic remains active.
- Mermaid golden comparison normalizes CRLF in the fixture before comparing semantic text.
- URI round-trip tests derive absolute paths from `t.TempDir()` and `filepath.Join` rather than hardcoding Unix paths.
- Real language-server integration tests require an explicit opt-in environment variable and never run merely because an accidental executable is present on `PATH`.
- File and state-directory permission-bit assertions run only on operating systems that implement POSIX mode bits; content, location, schema, and durability assertions continue on Windows.
- Compressed frontend assets use CodeAtlas' explicit MIME mapping for known extensions before consulting the host registry, guaranteeing `.map` is `application/json` on every operating system.

Platform checks are kept as narrow as possible. Behavior that can be tested portably is not skipped.

## 4. Windows E2E Language-Server Launching

### Test-only native launcher

The production language-server command contract is not expanded for a test-fixture problem. Instead, the E2E harness builds a small native Go launcher before the browser suite on Windows. No binary is committed.

The launcher is copied under deterministic fake-tool names and performs only two operations:

1. For fake LSP executable names, start the repository-owned Node fixture with the current Node runtime, forward the original arguments and stdio unchanged, and propagate its exit code.
2. For the `pyright` and `swiftc` probe helpers, return their existing deterministic fixture version output.

The process manager resolves `.mjs` fixture paths to these generated `.exe` launchers only on Windows. POSIX systems keep invoking the executable shebang fixtures directly. Generated files live under an ignored E2E build directory and are cleaned/rebuilt by the harness.

The launcher resolves only a closed map of repository-owned fixture names. It does not accept an arbitrary workspace script path and does not weaken CodeAtlas' no-workspace-execution boundary.

### Tests

Harness tests verify fixture-name resolution, unknown-name rejection, argument forwarding, version-probe behavior, and native process startup. The complete 10-test browser suite then validates Go, TypeScript, Swift, Python, Rust, Monaco semantics, reindexing, smoke behavior, and scenario metadata on Windows.

## Verification Strategy

Implementation follows independent TDD cycles for the provider, DeepWiki, Go portability, and E2E launcher. Final verification is performed from fresh commands and includes:

1. focused Go and Node tests for every changed boundary;
2. `go test -tags fts5 ./...` from `backend`;
3. frontend check, build, and all frontend tests;
4. static Go checks and repository documentation verification;
5. the complete E2E browser suite;
6. npm audit;
7. live endpoint checks for the configured reasoning effort, CodeMap, and DeepWiki;
8. a browser walkthrough of all advanced UI surfaces with console errors and warnings inspected.

Any endpoint outage is reported separately from an application regression. Secrets and raw endpoint data are excluded from the final report.

## Non-Goals

- Per-feature reasoning policies or different reasoning levels for Hover, See More, CodeMap, and DeepWiki.
- Automatic rewriting of invalid planner facts.
- Changes to `/v1/responses`.
- Installing or updating real language servers.
- Adding public arbitrary command-line argument configuration for language servers.
- Replacing the deterministic DeepWiki manifest fallback.
