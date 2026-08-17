# DeepWiki Planner Reliability Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Make the DeepWiki LLM planner produce inventory-grounded plans that pass local validation and expose a safe diagnostic when its bounded retry still fails.

**Architecture:** Derive the planner JSON Schema from `knownPaths`, retain semantic graph validation locally, and introduce a content-free validation-summary contract so the repair prompt receives the violated rule. The deterministic manifest remains the final fallback.

**Tech Stack:** Go, JSON Schema, CodeAtlas `aiout` structured output, DeepWiki service tests, live OpenAI-compatible endpoint.

**Spec:** `docs/superpowers/specs/2026-08-16-llm-windows-e2e-reliability-design.md`

## Global Constraints

- Planner output uses `wiki-plan/v1` and no more than 25 pages.
- Every planned path must be one exact normalized inventory path.
- Model-authored values, raw output, prompts, keys, and endpoint URLs never appear in diagnostics.
- The retry count remains one initial call plus at most one repair call.
- No invalid plan is auto-normalized into acceptance.
- The deterministic manifest remains the availability fallback.

---

### Task 1: Constrain the planner schema to repository paths

**Files:**
- Modify: `backend/internal/aiout/schemas.go`
- Modify: `backend/internal/aiout/schemas_parse_test.go`

**Interfaces:**
- Produces: `func WikiPlanSchemaForPaths(allowedPaths []string) json.RawMessage`
- Consumes: `WikiPlanSchema`, `mustSchemaObject`, and `constrainStringArray`

- [ ] **Step 1: Write the failing schema-contract test**

Add this test to `backend/internal/aiout/schemas_parse_test.go`:

```go
func TestWikiPlanSchemaForPathsConstrainsPlannerAuthoredValues(t *testing.T) {
	allowed := []string{"cmd/api/main.go", "internal/order/service.go"}
	var schema map[string]any
	if err := json.Unmarshal(WikiPlanSchemaForPaths(allowed), &schema); err != nil {
		t.Fatal(err)
	}
	properties := schema["properties"].(map[string]any)
	pages := properties["pages"].(map[string]any)
	page := pages["items"].(map[string]any)
	pageProperties := page["properties"].(map[string]any)
	scope := pageProperties["scopePaths"].(map[string]any)
	items := scope["items"].(map[string]any)
	values := items["enum"].([]any)
	if len(values) != 2 || values[0] != allowed[0] || values[1] != allowed[1] {
		t.Fatalf("scope path enum = %#v, want %#v", values, allowed)
	}
	if pageProperties["title"].(map[string]any)["minLength"] != float64(1) {
		t.Fatalf("title schema = %#v, want minLength 1", pageProperties["title"])
	}
	if pageProperties["slug"].(map[string]any)["pattern"] != `^[a-z0-9]+(?:[.-][a-z0-9]+)*$` {
		t.Fatalf("slug schema = %#v", pageProperties["slug"])
	}
	if pageProperties["parentSlug"].(map[string]any)["pattern"] != `^(?:|[a-z0-9]+(?:[.-][a-z0-9]+)*)$` {
		t.Fatalf("parentSlug schema = %#v", pageProperties["parentSlug"])
	}
}
```

Add a second test asserting that an empty allowed list produces `scopePaths.maxItems == 0` and no empty enum.

- [ ] **Step 2: Run the schema tests and prove they fail**

```powershell
Set-Location backend
go test ./internal/aiout -run 'TestWikiPlanSchemaForPaths' -count=1
```

Expected: compilation fails because `WikiPlanSchemaForPaths` does not exist.

- [ ] **Step 3: Implement the dynamic schema**

Add this implementation after `WikiPlanSchema` in `backend/internal/aiout/schemas.go`:

```go
func WikiPlanSchemaForPaths(allowedPaths []string) json.RawMessage {
	var schema map[string]any
	if err := json.Unmarshal(WikiPlanSchema(), &schema); err != nil {
		panic("aiout: embedded wiki-plan schema is invalid: " + err.Error())
	}
	properties := mustSchemaObject(schema["properties"], "properties")
	pages := mustSchemaObject(properties["pages"], "properties.pages")
	page := mustSchemaObject(pages["items"], "properties.pages.items")
	pageProperties := mustSchemaObject(page["properties"], "properties.pages.items.properties")
	mustSchemaObject(pageProperties["slug"], "properties.pages.items.properties.slug")["pattern"] = `^[a-z0-9]+(?:[.-][a-z0-9]+)*$`
	mustSchemaObject(pageProperties["title"], "properties.pages.items.properties.title")["minLength"] = 1
	mustSchemaObject(pageProperties["parentSlug"], "properties.pages.items.properties.parentSlug")["pattern"] = `^(?:|[a-z0-9]+(?:[.-][a-z0-9]+)*)$`
	constrainStringArray(mustSchemaObject(pageProperties["scopePaths"], "properties.pages.items.properties.scopePaths"), allowedPaths)
	encoded, err := json.Marshal(schema)
	if err != nil {
		panic("aiout: failed to encode constrained wiki-plan schema: " + err.Error())
	}
	return encoded
}
```

- [ ] **Step 4: Run all schema tests**

```powershell
Set-Location backend
go test ./internal/aiout -count=1
```

Expected: all `aiout` tests pass and the generated schema still contains no `uniqueItems` keyword.

- [ ] **Step 5: Commit the schema boundary**

```powershell
git add backend/internal/aiout/schemas.go backend/internal/aiout/schemas_parse_test.go
git commit -m "feat: ground DeepWiki planner schema to repository paths"
```

---

### Task 2: Give the bounded retry a safe semantic reason

**Files:**
- Modify: `backend/internal/service/grounded.go`
- Modify: `backend/internal/service/grounded_test.go`
- Modify: `backend/internal/service/wikiplan.go`
- Modify: `backend/internal/service/wikiplan_test.go`

**Interfaces:**
- Produces: private interface `validationSummaryProvider { ValidationSummary() string }`
- Produces: private type `wikiPlanValidationError` with content-free categories
- Consumes: existing `generateGrounded`, `validationSummary`, and `validateWikiPlan`

- [ ] **Step 1: Write the failing generic retry-summary test**

In `grounded_test.go`, add a test-only error type and behavior test:

```go
type categorizedValidationError string

func (e categorizedValidationError) Error() string             { return "raw detail must not be reused" }
func (e categorizedValidationError) ValidationSummary() string { return string(e) }

func TestGenerateGroundedUsesContentFreeValidationCategory(t *testing.T) {
	provider := &scriptedProvider{responses: []string{`{}`, `{}`}}
	err := generateGrounded(context.Background(), provider, ai.GenerationRequest{
		Operation: "deepwiki-plan", UserPrompt: "inventory",
	}, func([]byte) error {
		return categorizedValidationError("unknown scope path")
	})
	if err == nil {
		t.Fatal("generateGrounded succeeded after two invalid plans")
	}
	if len(provider.prompts) != 2 || !strings.Contains(provider.prompts[1], "unknown scope path") {
		t.Fatalf("repair prompt = %#v, want content-free category", provider.prompts)
	}
	if strings.Contains(provider.prompts[1], "raw detail") {
		t.Fatalf("repair prompt leaked Error() detail: %q", provider.prompts[1])
	}
}
```

- [ ] **Step 2: Run the retry-summary test and prove it fails**

```powershell
Set-Location backend
go test ./internal/service -run TestGenerateGroundedUsesContentFreeValidationCategory -count=1
```

Expected: the second prompt contains only `invalid response`, not `unknown scope path`.

- [ ] **Step 3: Extend `validationSummary` with an explicit safe interface**

Add to `grounded.go`:

```go
type validationSummaryProvider interface {
	ValidationSummary() string
}
```

Update `validationSummary` between the existing `aiout.ValidationError` branch and the generic fallback:

```go
var provider validationSummaryProvider
if errors.As(err, &provider) {
	if summary := strings.TrimSpace(provider.ValidationSummary()); summary != "" {
		return summary
	}
}
```

The generic fallback remains `invalid response`; only explicitly safe error types may add repair detail.

- [ ] **Step 4: Convert planner validation failures to categories**

Add this private type to `wikiplan.go`:

```go
type wikiPlanValidationError struct {
	category string
}

func (e *wikiPlanValidationError) Error() string             { return e.category }
func (e *wikiPlanValidationError) ValidationSummary() string { return e.category }

func invalidWikiPlan(category string) error {
	return &wikiPlanValidationError{category: category}
}
```

Replace every `fmt.Errorf` return in `validateWikiPlan` with a stable category. Use exactly these literals so repair prompts contain no model values:

```text
invalid schema version
page count outside allowed range
invalid page slug
duplicate page slug
empty page title
unknown page archetype
empty page scope
unknown scope path
duplicate scope path
missing overview page
overview page has a parent
page disconnected from overview
page is its own parent
missing parent page
page depth exceeds two
parent cycle
page does not descend from overview
missing required archetype
missing testing page
missing frontend page
missing module or layer page
```

- [ ] **Step 5: Prove planner retry receives a category**

Add a `plannerSequenceProvider` in `wikiplan_test.go` that records `GenerationRequest` values and returns the same syntactically valid plan with `scopePaths:["invented.go"]` twice. Assert `Generate` makes two planner attempts, the second request contains `unknown scope path`, neither request contains a raw rejected JSON value in the diagnostic suffix, and the final manifest uses the deterministic fallback.

Run:

```powershell
Set-Location backend
go test ./internal/service -run 'TestGenerateGroundedUsesContentFreeValidationCategory|TestDeepWikiPlannerRetryUsesSafeSemanticCategory' -count=1
```

Expected: both tests pass.

- [ ] **Step 6: Commit safe retry diagnostics**

```powershell
git add backend/internal/service/grounded.go backend/internal/service/grounded_test.go backend/internal/service/wikiplan.go backend/internal/service/wikiplan_test.go
git commit -m "fix: preserve safe DeepWiki planner validation reasons"
```

---

### Task 3: Use the grounded schema and report bounded fallback diagnostics

**Files:**
- Modify: `backend/internal/service/wikiplan.go`
- Modify: `backend/internal/service/wikiplan_test.go`

**Interfaces:**
- Consumes: `aiout.WikiPlanSchemaForPaths(known []string)` from Task 1
- Consumes: safe `wikiPlanValidationError` categories from Task 2
- Produces: private `plannerFallbackOmission(error) string`

- [ ] **Step 1: Write failing planner-request and fallback tests**

Extend the existing `schemaCapturingWikiProvider` test to locate the `deepwiki-plan` request and assert its schema contains an enum whose values equal `knownWikiPaths(files, symbols)`.

Update `TestDeepWikiInvalidPlannerFallsBackToDeterministicManifest` so it still asserts fallback availability and also requires a bounded category:

```go
omissions := strings.Join(service.Collection().Manifest.Omissions, " ")
if !strings.Contains(omissions, "planner output failed validation") || !strings.Contains(omissions, "invalid response") {
	t.Fatalf("manifest omissions = %q, want bounded planner diagnostic", omissions)
}
```

Add a semantic-failure case that requires `unknown scope path` and asserts `invented.go` is absent from the omission.

- [ ] **Step 2: Run the planner tests and prove they fail**

```powershell
Set-Location backend
go test ./internal/service -run 'TestDeepWiki.*Planner|TestWikiPlannerRequestUsesKnownPathSchema' -count=1
```

Expected: the request still uses `WikiPlanSchema()` and the fallback omission is generic.

- [ ] **Step 3: Ground the request and baseline prompt**

In `planWikiManifest`, calculate `known` before building the request, pass the already-built fallback to the payload builder, and change the schema:

```go
payload := wikiPlannerPayload(symbols, modules, files, fallback)
OutputSchema: aiout.WikiPlanSchemaForPaths(known),
```

Change the payload signature:

```go
func wikiPlannerPayload(symbols []domain.Symbol, modules []domain.WikiModule, files []domain.File, fallback domain.WikiManifest) map[string]any {
```

Immediately before the existing return, build the baseline using only grounded fields:

```go
baseline := make([]map[string]any, 0, len(fallback.Pages))
for _, page := range fallback.Pages {
	baseline = append(baseline, map[string]any{
		"slug": page.Slug, "title": page.Title, "parentSlug": page.ParentSlug,
		"scopePaths": append([]string(nil), page.ScopePaths...), "archetype": page.Archetype,
	})
}
```

Replace the existing return with:

```go
return map[string]any{
	"modules": views, "entrypoints": entrypoints,
	"knownPaths": knownWikiPaths(files, symbols),
	"deterministicBaseline": baseline,
}
```

Extend `deepWikiPlannerSystemPrompt` with these requirements:

```text
Use deterministicBaseline as a valid structural starting point. Preserve all required archetypes and the overview-rooted depth-2 hierarchy. You may refine titles and grouping only when every scope path remains an exact knownPaths value.
```

- [ ] **Step 4: Add a bounded fallback-omission helper**

Add:

```go
func plannerFallbackOmission(err error) string {
	category := "invalid response"
	var appErr *apperror.AppError
	if errors.As(err, &appErr) && appErr.Cause != nil {
		for _, candidate := range []string{
			"invalid schema version", "page count outside allowed range", "invalid page slug",
			"duplicate page slug", "empty page title", "unknown page archetype", "empty page scope",
			"unknown scope path", "duplicate scope path", "missing overview page",
			"overview page has a parent", "page disconnected from overview", "page is its own parent",
			"missing parent page", "page depth exceeds two", "parent cycle",
			"page does not descend from overview", "missing required archetype",
			"missing testing page", "missing frontend page", "missing module or layer page",
		} {
			if strings.Contains(appErr.Cause.Error(), candidate) {
				category = candidate
				break
			}
		}
	}
	return "planner output failed validation (" + category + "); deterministic manifest used"
}
```

Use this helper in the validation-failure branch. Import `errors` and `apperror`. The closed category list prevents raw remote/model content from entering artifacts.

- [ ] **Step 5: Run DeepWiki and structured-output tests**

```powershell
Set-Location backend
go test ./internal/aiout ./internal/service -count=1
```

Expected: every package test passes, the fallback remains available, and diagnostic tests prove model values are absent.

- [ ] **Step 6: Commit planner reliability**

```powershell
git add backend/internal/service/wikiplan.go backend/internal/service/wikiplan_test.go
git commit -m "fix: ground DeepWiki planning and diagnose fallback"
```

---

### Task 4: Validate the planner against the live endpoint

**Files:**
- Generated and ignored runtime state only: `.codeatlas/`, E2E/live reports

**Interfaces:**
- Consumes: reasoning-aware provider plan and Tasks 1-3
- Produces: live evidence that the LLM plan, not the fallback, built the wiki

- [ ] **Step 1: Run focused tests from a fresh process**

```powershell
Set-Location backend
go test ./internal/aiout ./internal/service -count=1
Set-Location ..
make build
```

Expected: all tests and the build pass.

- [ ] **Step 2: Start CodeAtlas with the ignored `.env` and tinycommerce workspace**

Start `dist/codeatlas.exe` on a free loopback port with `examples/tinycommerce`, wait for `/api/health/ready`, and retain sanitized stdout/stderr in memory. Do not print environment values.

- [ ] **Step 3: Generate and inspect DeepWiki**

Trigger the existing DeepWiki generation API, follow its SSE/job status to completion, retrieve the manifest and pages, and assert:

```text
job state = completed
page count > 0
all parentSlug references resolve
tree depth <= 2
required archetypes are present
no omission contains "planner output failed validation"
every page retrieval returns HTTP 200
```

- [ ] **Step 4: Stop the process and report only sanitized outcomes**

Stop CodeAtlas cleanly and verify the loopback port closes. Record request counts, job/page counts, planner-fallback status, and HTTP outcomes without the endpoint URL, API key, prompts, or raw model text.

- [ ] **Step 5: Refresh documentation metadata after all plan documents are final**

```powershell
make docs-checksums
make verify-docs
```

Expected: every documentation checksum reports `OK`.
