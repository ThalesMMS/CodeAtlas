# Integrated Live Verification Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Prove the four fixes work together in the complete repository, production endpoint, and browser UI without leaking credentials or model content.

**Architecture:** Run fresh static/unit/eval/E2E gates first, then start the built application with the ignored `.env`, exercise every advanced LLM surface through public APIs and the browser, inspect sanitized observability, and shut down cleanly.

**Tech Stack:** Node.js 26, npm 11.16, Go, Make, browser E2E, OpenAI-compatible chat endpoint.

**Spec:** `docs/superpowers/specs/2026-08-16-llm-windows-e2e-reliability-design.md`

## Global Constraints

- Verification commands are fresh and uncached where supported.
- The ignored `.env` is loaded without printing its endpoint or credentials.
- Reports contain counts, durations, states, and sanitized error codes only.
- Any endpoint outage is distinguished from a CodeAtlas regression.
- All started processes are stopped and loopback ports are verified closed.

---

### Task 1: Run complete offline repository gates

**Files:**
- Generated and ignored: frontend build output, `dist/`, eval reports, E2E reports
- Modify generated documentation metadata: `docs/MANIFEST.txt`, `docs/SHA256SUMS.txt`

**Interfaces:**
- Consumes: all four implementation plans
- Produces: fresh build, static, unit, eval, audit, and E2E evidence

- [ ] **Step 1: Confirm required runtimes**

```powershell
node --version
npm --version
go version
```

Expected: Node reports a `v26` release and npm reports `11.16.0`; record the exact Go version without changing it.

- [ ] **Step 2: Refresh and verify versioned documentation**

```powershell
make docs-checksums
make verify-docs
```

Expected: every file reports `OK`.

- [ ] **Step 3: Run static checks and production build**

```powershell
make check
make build
```

Expected: frontend checks, Tree-sitter verification, documentation verification, Go vet, frontend build, and backend build all exit zero.

- [ ] **Step 4: Run all frontend and Go tests**

```powershell
make test
```

Expected: all frontend tests and `go test -tags fts5 ./...` pass. Record exact pass/fail/skip totals.

- [ ] **Step 5: Run the offline quality evaluation**

```powershell
make eval
```

Expected: all retrieval and four-surface quality gates pass against the versioned baseline.

- [ ] **Step 6: Run the complete browser E2E suite**

```powershell
make e2e
```

Expected: every smoke, reindex, scenario, Monaco, Go, TypeScript, Swift, Python, Rust, and launcher test passes.

- [ ] **Step 7: Audit both npm workspaces**

```powershell
Set-Location frontend
npm audit --audit-level=high
Set-Location ../e2e
npm audit --audit-level=high
Set-Location ..
```

Expected: both audits exit zero with no high or critical vulnerabilities.

---

### Task 2: Exercise every advanced surface against the live endpoint

**Files:**
- Generated and ignored: `.codeatlas/` runtime state and sanitized local test reports

**Interfaces:**
- Consumes: built `dist/codeatlas.exe`, ignored `.env`, `examples/tinycommerce`
- Produces: live request/job/artifact acceptance results

- [ ] **Step 1: Start the production binary safely**

Load `.env` into the child process without echoing values. Start `dist/codeatlas.exe` with `examples/tinycommerce`, a free `127.0.0.1` port, and a workspace-local temporary database. Capture stdout/stderr into a bounded buffer and poll `/api/health/ready` until ready or the configured timeout expires.

Expected readiness assertions:

```text
HTTP status = 200
chat provider = available
structured output = available
required local capabilities = available
```

- [ ] **Step 2: Exercise synchronous LLM and retrieval surfaces**

Use public API routes to request Hover and See More for a known tinycommerce symbol. Assert HTTP 200, valid schema versions, grounded evidence identifiers, non-empty summaries, and no provider-unavailable error.

- [ ] **Step 3: Exercise CodeMap as a complete job**

Trigger CodeMap generation, follow SSE/job state to terminal completion, retrieve the artifact, and assert:

```text
job state = completed
schemaVersion = codemap-narrative/v2
nodes and flows are non-empty
all referenced node/evidence IDs resolve
artifact retrieval = HTTP 200
```

- [ ] **Step 4: Exercise DeepWiki as a complete job**

Trigger DeepWiki generation, follow progress to completion, retrieve the manifest and every page, and assert:

```text
job state = completed
page count > 0
all required archetypes are present
all parent links resolve with depth <= 2
all page retrievals = HTTP 200
no omission contains "planner output failed validation"
```

This is the live acceptance criterion for the dynamic schema, safe repair diagnostic, `reasoning_effort=medium`, and `max_completion_tokens` request path.

- [ ] **Step 5: Exercise reindexing**

Trigger reindexing, follow its job/SSE state, then repeat one LLM surface and one artifact retrieval to prove the provider and generated state remain usable after index version changes.

- [ ] **Step 6: Inspect sanitized runtime evidence**

Count LLM calls, completed jobs, failed jobs, dropped SSE events, warnings, and errors. Assert no log line contains the configured API key, Authorization header, complete prompt, raw completion, or endpoint URL.

---

### Task 3: Walk the advanced browser UI and shut down cleanly

**Files:**
- No source changes expected

**Interfaces:**
- Consumes: running live application from Task 2
- Produces: user-visible UI acceptance and clean process shutdown

- [ ] **Step 1: Open the application in the in-app browser**

Navigate to the loopback application and wait for the repository tree and editor to render. Inspect the browser console from this point onward.

- [ ] **Step 2: Exercise repository navigation and Monaco semantics**

Open a tinycommerce source file, select a known symbol, and exercise hover/semantic navigation. Assert the editor renders, the selected source range is correct, and no unavailable-provider banner appears unexpectedly.

- [ ] **Step 3: Exercise Hover, See More, CodeMap, and DeepWiki UI flows**

Open each advanced surface, wait for its final state, and verify the visible result is non-empty and linked to source evidence. For CodeMap, verify the diagram and narrative render. For DeepWiki, verify the manifest navigation opens multiple pages and parent/child links work.

- [ ] **Step 4: Inspect browser diagnostics**

Assert zero uncaught console errors, zero failed same-origin API requests, and no new warning tied to provider parsing, Monaco semantic providers, source-map MIME, CodeMap, or DeepWiki.

- [ ] **Step 5: Stop all processes and verify cleanup**

Terminate the application gracefully, wait for exit, and verify the chosen loopback port is closed. Confirm no E2E fake LSP or child CodeAtlas process remains. Generated `.codeatlas`, `dist`, `e2e/.generated`, and report files remain ignored and unstaged.

- [ ] **Step 6: Produce the final evidence report**

Report:

```text
runtime versions
static/build/test/eval/E2E pass totals
npm audit outcomes
live LLM call counts
CodeMap and DeepWiki job/page counts
planner fallback used: yes/no
browser console errors/warnings
cleanup/port status
```

List any remaining limitation separately. Do not include secrets, raw model output, prompts, or private endpoint coordinates.
