# LLM Reasoning Control Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Add opt-in, validated `reasoning_effort` support to all OpenAI-compatible business chat requests without breaking legacy providers.

**Architecture:** Configuration owns normalization and enum validation. The provider owns one shared request-body helper that chooses either legacy `max_tokens` or reasoning-aware `reasoning_effort` plus `max_completion_tokens`; probes remain reasoning-free.

**Tech Stack:** Go 1.25+, `net/http`, JSON, environment configuration, Go tests.

**Spec:** `docs/superpowers/specs/2026-08-16-llm-windows-e2e-reliability-design.md`

## Global Constraints

- Node.js remains `>=26.0.0` and npm remains `>=11.16.0 <12`.
- Empty reasoning configuration preserves the legacy request body.
- Supported values are exactly `none`, `minimal`, `low`, `medium`, `high`, `xhigh`, and `max`.
- A reasoning request sends `max_completion_tokens`; a legacy request sends `max_tokens`; no request sends both.
- Readiness probes remain reasoning-free.
- No endpoint, key, prompt, or raw completion may be logged or committed.

---

### Task 1: Validate and expose reasoning configuration

**Files:**
- Modify: `backend/internal/config/config.go`
- Modify: `backend/internal/config/config_test.go`
- Modify: `.env.example`
- Modify: `Makefile`
- Modify: `README.md`
- Modify locally, never stage: `.env`

**Interfaces:**
- Produces: `Config.LLMReasoningEffort string`
- Produces: environment variable `CODEATLAS_LLM_REASONING_EFFORT`
- Consumes: existing `envOr`, `strings.TrimSpace`, and `Config.validate`

- [ ] **Step 1: Write the failing configuration test**

Add this table-driven test to `backend/internal/config/config_test.go` and clear the variable in `llmEnv` so the host environment cannot affect unrelated tests:

```go
func TestLoadLLMReasoningEffort(t *testing.T) {
	tests := []struct {
		name    string
		value   string
		want    string
		wantErr bool
	}{
		{name: "unset", value: "", want: ""},
		{name: "normalizes", value: " Medium ", want: "medium"},
		{name: "none", value: "none", want: "none"},
		{name: "minimal", value: "minimal", want: "minimal"},
		{name: "low", value: "low", want: "low"},
		{name: "high", value: "high", want: "high"},
		{name: "xhigh", value: "xhigh", want: "xhigh"},
		{name: "max", value: "max", want: "max"},
		{name: "invalid", value: "extreme", wantErr: true},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			resetFlags(t)
			llmEnv(t)
			t.Setenv("CODEATLAS_LLM_REASONING_EFFORT", test.value)
			cfg, err := Load()
			if test.wantErr {
				if err == nil || !strings.Contains(err.Error(), "CODEATLAS_LLM_REASONING_EFFORT") {
					t.Fatalf("Load() error = %v, want reasoning-effort validation error", err)
				}
				return
			}
			if err != nil {
				t.Fatalf("Load() error = %v", err)
			}
			if cfg.LLMReasoningEffort != test.want {
				t.Fatalf("LLMReasoningEffort = %q, want %q", cfg.LLMReasoningEffort, test.want)
			}
		})
	}
}
```

- [ ] **Step 2: Run the test and prove it fails**

Run:

```powershell
Set-Location backend
go test ./internal/config -run TestLoadLLMReasoningEffort -count=1
```

Expected: compilation fails because `Config.LLMReasoningEffort` does not exist.

- [ ] **Step 3: Implement normalization and enum validation**

Add `LLMReasoningEffort string` beside the other LLM fields. Load it with:

```go
LLMReasoningEffort: strings.ToLower(strings.TrimSpace(os.Getenv("CODEATLAS_LLM_REASONING_EFFORT"))),
```

Add validation before the watch/LSP switches:

```go
switch c.LLMReasoningEffort {
case "", "none", "minimal", "low", "medium", "high", "xhigh", "max":
default:
	return fmt.Errorf("CODEATLAS_LLM_REASONING_EFFORT must be none, minimal, low, medium, high, xhigh, or max")
}
```

Add this line to `llmEnv`:

```go
t.Setenv("CODEATLAS_LLM_REASONING_EFFORT", "")
```

- [ ] **Step 4: Run configuration tests**

Run:

```powershell
Set-Location backend
go test ./internal/config -count=1
```

Expected: all `internal/config` tests pass.

- [ ] **Step 5: Document and enable the setting locally**

Add the variable after `CODEATLAS_LLM_MODEL` in `.env.example`:

```dotenv
# Optional: none|minimal|low|medium|high|xhigh|max. Omit for legacy providers.
CODEATLAS_LLM_REASONING_EFFORT=medium
```

Export it beside the other LLM variables in `Makefile`:

```make
export CODEATLAS_LLM_REASONING_EFFORT
```

Add this row to the README configuration table:

```markdown
| `CODEATLAS_LLM_REASONING_EFFORT` | No | Optional reasoning budget: `none`, `minimal`, `low`, `medium`, `high`, `xhigh`, or `max`; omitted for legacy providers. |
```

Prepend the following non-secret line to the ignored local `.env` without displaying or rewriting its existing values:

```dotenv
CODEATLAS_LLM_REASONING_EFFORT=medium
```

- [ ] **Step 6: Commit the configuration contract**

```powershell
git add backend/internal/config/config.go backend/internal/config/config_test.go .env.example Makefile README.md
git commit -m "feat: configure LLM reasoning effort"
```

Do not stage `.env`.

---

### Task 2: Send reasoning-aware completion budgets

**Files:**
- Modify: `backend/internal/ai/openai_compatible.go`
- Modify: `backend/internal/ai/openai_compatible_test.go`
- Modify: `backend/cmd/codeatlas/main.go`

**Interfaces:**
- Consumes: `Config.LLMReasoningEffort string` from Task 1
- Produces: `Options.ReasoningEffort string`
- Produces: `func (p *OpenAICompatible) applyCompletionControls(body map[string]any, maxTokens int)`
- Preserves: `Complete`, `CompleteStructured`, schema fallback, and provider probe public interfaces

- [ ] **Step 1: Write failing request-contract tests**

Add a helper that decodes one captured request body without using provider implementation helpers:

```go
func completionBodyServer(t *testing.T, captured chan<- map[string]any) *httptest.Server {
	t.Helper()
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var body map[string]any
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Errorf("decode request: %v", err)
			w.WriteHeader(http.StatusBadRequest)
			return
		}
		captured <- body
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"model":"test-model","choices":[{"message":{"content":"ok"},"finish_reason":"stop"}]}`))
	}))
}
```

Add a table test for plain completions:

```go
func TestCompleteSelectsLegacyOrReasoningTokenField(t *testing.T) {
	for _, test := range []struct {
		name      string
		effort    string
		wantField string
		omitField string
	}{
		{name: "legacy", wantField: "max_tokens", omitField: "max_completion_tokens"},
		{name: "reasoning", effort: "medium", wantField: "max_completion_tokens", omitField: "max_tokens"},
	} {
		t.Run(test.name, func(t *testing.T) {
			captured := make(chan map[string]any, 1)
			server := completionBodyServer(t, captured)
			defer server.Close()
			provider := NewOpenAICompatible(Options{BaseURL: server.URL, Model: "test-model", ReasoningEffort: test.effort}).(*OpenAICompatible)
			if _, err := provider.Complete(context.Background(), "system", "user", 6144); err != nil {
				t.Fatal(err)
			}
			body := <-captured
			if body[test.wantField] != float64(6144) {
				t.Fatalf("%s = %#v, want 6144", test.wantField, body[test.wantField])
			}
			if _, exists := body[test.omitField]; exists {
				t.Fatalf("request unexpectedly contains %s: %#v", test.omitField, body)
			}
			if got, exists := body["reasoning_effort"]; exists != (test.effort != "") || (exists && got != test.effort) {
				t.Fatalf("reasoning_effort = %#v/%v, want %q", got, exists, test.effort)
			}
		})
	}
}
```

Add a structured-output test that makes the first response a typed `response_format` rejection, captures both attempts, and asserts both contain `reasoning_effort=medium`, `max_completion_tokens=4096`, no `max_tokens`, while only the first contains `response_format`.

- [ ] **Step 2: Run the provider tests and prove they fail**

```powershell
Set-Location backend
go test ./internal/ai -run 'TestCompleteSelectsLegacyOrReasoningTokenField|TestCompleteStructuredPreservesReasoningControlsOnSchemaFallback' -count=1
```

Expected: compilation fails because `Options.ReasoningEffort` does not exist.

- [ ] **Step 3: Implement one shared body-control helper**

Add `reasoningEffort string` to `OpenAICompatible`, `ReasoningEffort string` to `Options`, and copy the trimmed lowercase option in `NewOpenAICompatible`. Add:

```go
func (p *OpenAICompatible) applyCompletionControls(body map[string]any, maxTokens int) {
	if p.reasoningEffort == "" {
		body["max_tokens"] = maxTokens
		return
	}
	body["reasoning_effort"] = p.reasoningEffort
	body["max_completion_tokens"] = maxTokens
}
```

Remove the literal `max_tokens` entry from both business request builders and call `applyCompletionControls` after constructing each body. Do not modify `ProbeChat` in `probe.go`.

- [ ] **Step 4: Wire the composition root**

Add this option in `backend/cmd/codeatlas/main.go`:

```go
ReasoningEffort: cfg.LLMReasoningEffort,
```

- [ ] **Step 5: Run focused and package tests**

```powershell
Set-Location backend
go test ./internal/ai ./internal/config ./cmd/codeatlas -count=1
```

Expected: all selected packages pass, including schema-fallback behavior.

- [ ] **Step 6: Commit provider support**

```powershell
git add backend/internal/ai/openai_compatible.go backend/internal/ai/openai_compatible_test.go backend/cmd/codeatlas/main.go
git commit -m "feat: send reasoning-aware completion budgets"
```

---

### Task 3: Verify compatibility and repository documentation

**Files:**
- Modify generated metadata: `docs/MANIFEST.txt`
- Modify generated metadata: `docs/SHA256SUMS.txt`

**Interfaces:**
- Consumes: provider/config changes from Tasks 1-2
- Produces: verified documentation metadata and a buildable application

- [ ] **Step 1: Format and run the LLM-related suite**

```powershell
Set-Location backend
go fmt ./internal/config ./internal/ai ./cmd/codeatlas
go test ./internal/config ./internal/ai ./cmd/codeatlas -count=1
go vet -tags fts5 ./internal/config ./internal/ai ./cmd/codeatlas
```

Expected: every command exits zero.

- [ ] **Step 2: Refresh and verify documentation checksums**

```powershell
Set-Location ..
make docs-checksums
make verify-docs
```

Expected: every manifest entry reports `OK`.

- [ ] **Step 3: Build with the ignored live configuration**

```powershell
make build
```

Expected: `dist/codeatlas.exe` is rebuilt successfully with no secret output.

- [ ] **Step 4: Commit generated documentation metadata**

```powershell
git add docs/MANIFEST.txt docs/SHA256SUMS.txt
git commit -m "docs: document LLM reasoning configuration"
```

The live endpoint behavior is exercised in the final integrated verification after the DeepWiki plan is implemented.
