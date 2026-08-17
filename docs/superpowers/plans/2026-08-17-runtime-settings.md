# Runtime Settings and OS Credential Storage Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Add an accessible Settings drawer that configures every documented CodeAtlas environment setting, persists per-user overrides, keeps API keys in the operating-system credential vault, and safely applies provider, embedding, and language-server changes at runtime.

**Architecture:** A versioned settings manager resolves defaults, environment values, and saved overrides into immutable snapshots. It prepares every fallible runtime replacement before atomically persisting a revision, then activates AI, embedding, and semantic-provider references with infallible swaps; startup-only values are retained as next-start configuration. Local-only revisioned HTTP endpoints expose sanitized snapshots to a drawer available from both the top bar and bootstrap overlay.

**Tech Stack:** Go 1.23+, `sync/atomic`, `net/http`, `github.com/zalando/go-keyring`, Windows Credential Manager, macOS Keychain, Vite 8, TypeScript 6, browser JavaScript, Node test runner, Playwright E2E.

**Spec:** `docs/superpowers/specs/2026-08-17-runtime-settings-design.md`

## Global Constraints

- The catalog contains exactly the 23 names in `.env.example`; internal variables such as watch/probe/database controls remain environment-only.
- Precedence is field-specific: compiled default, then `.env`/process environment, then an explicit saved override.
- Non-secret settings are global per OS user under `os.UserConfigDir()/CodeAtlas/settings.json`.
- `CODEATLAS_LLM_API_KEY` and `CODEATLAS_EMBEDDINGS_API_KEY` never enter settings JSON, API responses, browser state, DOM values, logs, error details, fixtures, or commits.
- Secrets use `preserve`, `replace`, and `inherit`; an empty string is never overloaded as a secret operation.
- Provider calls started before a swap finish on the old immutable provider. New calls use the new provider, including `ai.StructuredCompleter` and capability probes.
- An embedding fingerprint mismatch disables dense reads until one transactional rebuild publishes compatible vectors; lexical retrieval remains available.
- A changed LSP candidate starts and receives the current open-document set before the semantic router swaps. Failure preserves every previous manager and rejects the whole revision.
- Workspace, listen address, and maximum file size are persisted but never mutated in the running composition root.
- Settings administration requires a loopback peer, loopback `Host`, same-origin validation, and the per-process settings token. It remains available before `READY`.
- Node.js remains `>=26.0.0`; npm remains `>=11.16.0 <12`; backend builds retain `CGO_ENABLED=1` and `fts5` where currently required.
- Existing uncommitted packaging changes (`README.md`, both build scripts, and `frontend/tests/packaging-scripts.test.cjs`) are user work. Do not stage them accidentally.
- Tests inject fake credential stores and providers. Only explicit platform smoke tests may touch Windows Credential Manager or macOS Keychain.
- Do not import `.env` secrets into the vault automatically and do not stage `.env`.
- Existing explicit `-workspace`, `-listen`, and `-db` CLI flags remain process-local operator overrides above persisted settings; they are outside the UI catalog. Process tests must use isolated config roots and explicit flags so global user state cannot affect them.

---

### Task 1: Establish the field catalog and recoverable resolution model

**Files:**
- Create: `backend/internal/settings/catalog.go`
- Create: `backend/internal/settings/model.go`
- Create: `backend/internal/settings/resolve.go`
- Create: `backend/internal/settings/resolve_test.go`
- Modify: `backend/internal/config/config.go`
- Modify: `backend/internal/config/config_test.go`

**Interfaces:**
- Produces: `settings.FieldKey`, `Source`, `ApplyMode`, `Values`, `Overrides`, `Resolved`, and `FieldError`
- Produces: `settings.DocumentedFields() []FieldDefinition` in `.env.example` order
- Produces: `settings.Resolve(environment Environment, overrides Overrides, credentials SecretValues) Resolved`
- Produces: exported configuration validators for bootstrap, provider, embeddings, and LSP groups

- [ ] **Step 1: Write the failing catalog and precedence tests**

Parse `../../../.env.example` in the test and compare assignments to `DocumentedFields()`. Require exactly these keys in order:

```go
var documentedKeys = []FieldKey{
	"CODEATLAS_WORKSPACE", "CODEATLAS_LISTEN", "CODEATLAS_MAX_FILE_BYTES",
	"CODEATLAS_LLM_BASE_URL", "CODEATLAS_LLM_API_KEY", "CODEATLAS_LLM_MODEL",
	"CODEATLAS_LLM_REASONING_EFFORT", "CODEATLAS_LLM_TIMEOUT",
	"CODEATLAS_GOPLS", "CODEATLAS_GOPLS_PATH",
	"CODEATLAS_TYPESCRIPT_LSP", "CODEATLAS_TYPESCRIPT_LSP_PATH", "CODEATLAS_TYPESCRIPT_SDK_PATH",
	"CODEATLAS_SWIFT_LSP", "CODEATLAS_SWIFT_LSP_PATH",
	"CODEATLAS_PYTHON_LSP", "CODEATLAS_PYTHON_LSP_PATH",
	"CODEATLAS_RUST_LSP", "CODEATLAS_RUST_LSP_PATH",
	"CODEATLAS_ENABLE_EMBEDDINGS", "CODEATLAS_EMBEDDING_MODEL",
	"CODEATLAS_EMBEDDING_BASE_URL", "CODEATLAS_EMBEDDINGS_API_KEY",
}
```

Add table tests proving default `<` environment `<` saved override independently for strings, enums, booleans, durations, integers, and both secrets. Assert explicit empty overrides survive only for embedding base URL and TypeScript SDK path; `inherit` removes the override.

Add a recovery test: missing/invalid LLM endpoint, model, reasoning effort, or timeout yields provider field errors and an unconfigured candidate, but does not reject an otherwise valid workspace/listen bootstrap. Invalid workspace, listen address, or maximum file size remains fatal before bind.

- [ ] **Step 2: Run the focused tests and prove they fail**

```powershell
Set-Location backend
go test ./internal/settings ./internal/config -run 'TestDocumentedFieldCatalogMatchesEnvExample|TestResolvePrecedence|TestProviderConfigurationIsRecoverable' -count=1
```

Expected: `internal/settings` does not exist and the current loader rejects missing LLM values.

- [ ] **Step 3: Implement typed definitions and raw-source resolution**

Use one descriptor per field:

```go
type FieldDefinition struct {
	Key FieldKey
	Group Group
	Secret bool
	ApplyMode ApplyMode
	Default string
}

type Source string
const (
	SourceDefault Source = "default"
	SourceEnv Source = "env"
	SourceSettings Source = "settings"
	SourceNone Source = "none"
)
```

Use pointers in a concrete `Overrides` struct so missing and explicit empty values remain distinct. Keep secret material only in `SecretValues`, never in `Overrides` or public snapshots. Resolve raw strings first, normalize/parse second, and collect provider errors without failing bootstrap.

Split validation into:

```go
func ValidateBootstrap(Config) error
func ValidateProvider(Config) []ValidationIssue
func ValidateEmbeddings(Config) []ValidationIssue
func ValidateLanguageServers(Config) []ValidationIssue
```

Define `config.ValidationIssue{EnvironmentKey, Message}` in `config` and adapt it to `settings.FieldError` in the settings package, avoiding an import cycle. Retain environment-only parsing in `config`. Recompute the default database path after resolving workspace when `CODEATLAS_DB` was not explicit.

- [ ] **Step 4: Run all configuration/settings tests**

```powershell
Set-Location backend
go test ./internal/settings ./internal/config -count=1
```

Expected: all tests pass and the inventory test prevents drift.

- [ ] **Step 5: Commit the resolution contract**

```powershell
git add backend/internal/settings/catalog.go backend/internal/settings/model.go backend/internal/settings/resolve.go backend/internal/settings/resolve_test.go backend/internal/config/config.go backend/internal/config/config_test.go
git commit -m "feat: model runtime settings precedence"
```

---

### Task 2: Persist versioned non-secret overrides atomically

**Files:**
- Create: `backend/internal/settings/filestore.go`
- Create: `backend/internal/settings/filestore_test.go`
- Create: `backend/internal/settings/testdata/schema-v1.json`

**Interfaces:**
- Produces: `FileStore.Load(context.Context) (Document, error)` and `FileStore.Save(context.Context, Document) error`
- Produces: `DefaultPath() (string, error)` using `os.UserConfigDir()`
- Produces: `Document{SchemaVersion, Revision, Overrides, Credentials}`

- [ ] **Step 1: Write failing persistence tests**

Using a temporary config directory, require: absent file returns schema 1/revision 0; the exact v1 fixture round-trips; unknown root/nested fields and schema 2 fail without rewrite; a forced pre-rename failure preserves the old bytes; success uses `0600` on POSIX and leaves no temp file; `DefaultPath` ends in `CodeAtlas/settings.json`.

- [ ] **Step 2: Run and prove failure**

```powershell
Set-Location backend
go test ./internal/settings -run 'TestFileStore|TestDefaultSettingsPath' -count=1
```

Expected: persistence types are undefined.

- [ ] **Step 3: Implement strict atomic persistence**

```go
type Document struct {
	SchemaVersion int `json:"schemaVersion"`
	Revision uint64 `json:"revision"`
	Overrides Overrides `json:"overrides"`
	Credentials CredentialReferences `json:"credentials"`
}
```

Decode with `DisallowUnknownFields` and require exactly one JSON document. Write a randomized file in the same directory, chmod `0600`, encode, sync, close, rename over the target, and best-effort sync the directory. On failure, remove only the exact temporary file; never delete/truncate the target first.

- [ ] **Step 4: Run settings tests**

```powershell
Set-Location backend
go test ./internal/settings -count=1
```

Expected: tests pass and never touch the real user config directory.

- [ ] **Step 5: Commit atomic persistence**

```powershell
git add backend/internal/settings/filestore.go backend/internal/settings/filestore_test.go backend/internal/settings/testdata/schema-v1.json
git commit -m "feat: persist versioned user settings"
```

---

### Task 3: Add OS-vault credentials and transactional secret rotation

**Files:**
- Create: `backend/internal/settings/credentials.go`
- Create: `backend/internal/settings/keyring.go`
- Create: `backend/internal/settings/credentials_test.go`
- Modify: `backend/go.mod`
- Modify: `backend/go.sum`
- Modify: `THIRD_PARTY_NOTICES.md`

**Interfaces:**
- Produces: `CredentialStore { Get(context.Context, string) (string, error); Set(context.Context, string, string) error; Delete(context.Context, string) error }`
- Produces: `SecretOperation{Operation: preserve|replace|inherit, Value string}`
- Produces: `CredentialTransaction` prepare/rollback/cleanup operations
- Production service: `CodeAtlas`; accounts: `llm-api-key:<generation>` and `embeddings-api-key:<generation>`

- [ ] **Step 1: Write failing fake-vault tests**

Assert `preserve` makes no write; `replace` writes a random new generation without overwriting old; `inherit` clears the reference and uses environment fallback; partial writes roll back all new generations; file/runtime failure rolls back; old cleanup occurs only after commit; stale generations are retried during a later successful load/save; sanitized status is only configured plus source. A vault read failure at startup must leave the settings file intact, mark the provider unavailable with a safe field error, and allow bootstrap to await repair.

- [ ] **Step 2: Run and prove failure**

```powershell
Set-Location backend
go test ./internal/settings -run 'TestCredentialTransaction|TestSecretStatusNeverContainsValue' -count=1
```

Expected: credential contracts are undefined.

- [ ] **Step 3: Implement the production adapter**

Pin `github.com/zalando/go-keyring` with `go get`. Wrap only `keyring.Get`, `Set`, and `Delete`; translate `ErrNotFound` to a sentinel and sanitize all other errors. Generate opaque identifiers with `crypto/rand` and hex/base32, without another UUID dependency.

- [ ] **Step 4: Verify rollback and redaction**

Marshal all public DTOs and scan fake logs/errors for sentinel keys. Run:

```powershell
Set-Location backend
go test ./internal/settings -count=1
go mod tidy
go test ./internal/settings -count=1
```

Expected: tests pass and dependency locks are updated.

- [ ] **Step 5: Document and commit the dependency**

Add its pinned version and MIT license to `THIRD_PARTY_NOTICES.md`.

```powershell
git add backend/internal/settings/credentials.go backend/internal/settings/keyring.go backend/internal/settings/credentials_test.go backend/go.mod backend/go.sum THIRD_PARTY_NOTICES.md
git commit -m "feat: store settings secrets in the system vault"
```

---

### Task 4: Implement the revisioned settings transaction manager

**Files:**
- Create: `backend/internal/settings/manager.go`
- Create: `backend/internal/settings/manager_test.go`
- Create: `backend/internal/settings/snapshot.go`

**Interfaces:**
- Produces: `RuntimePreparer.Prepare(context.Context, Resolved, ChangeSet) (PreparedRuntime, error)`
- Produces: `PreparedRuntime.Activate() ActivationResult` and `PreparedRuntime.Abort(context.Context)`
- Produces: `Manager.Snapshot`, `Manager.Update`, and `Manager.Reset`
- Produces: `UpdateRequest{Revision, Overrides map[FieldKey]OverrideOperation, Secrets map[FieldKey]SecretOperation}`
- Produces: stable `SETTINGS_REVISION_CONFLICT` and field-keyed errors

- [ ] **Step 1: Write failing transaction tests**

Using fake file, credential, and runtime ports, require this exact success order:

```text
validate -> write candidate credentials -> prepare runtime -> save revision N+1 -> activate -> cleanup old credentials
```

Assert stale revision does no work and returns a fresh snapshot; unknown keys/ops/type mismatches fail; local validation precedes vault/network; prepare/vault/save failures preserve old state; activation occurs once after persistence; reset is a prepared all-inherit update; restart fields include running and saved values; no DTO contains secret sentinels.

- [ ] **Step 2: Run and prove failure**

```powershell
Set-Location backend
go test ./internal/settings -run 'TestManagerUpdate|TestManagerReset|TestManagerRejectsStaleRevision' -count=1
```

Expected: manager/update contracts are undefined.

- [ ] **Step 3: Implement serialized writes and immutable reads**

Guard writes with one mutex and publish deep-copied snapshots through `atomic.Pointer` only after activation. Use the catalog for operation/type validation and restart diffing.

```go
type SanitizedSnapshot struct {
	Revision uint64 `json:"revision"`
	Groups map[Group][]FieldSnapshot `json:"groups"`
	RestartRequired []FieldKey `json:"restartRequired"`
	Validation []FieldError `json:"validation,omitempty"`
}
```

Secret snapshots have `Configured *bool` and no `Value`. Restart fields add `RunningValue` alongside next-start `Value`.

- [ ] **Step 4: Run with the race detector**

```powershell
Set-Location backend
go test -race ./internal/settings -count=1
```

Expected: all settings tests pass without races or leakage.

- [ ] **Step 5: Commit the manager**

```powershell
git add backend/internal/settings/manager.go backend/internal/settings/manager_test.go backend/internal/settings/snapshot.go
git commit -m "feat: coordinate revisioned settings updates"
```

---

### Task 5: Add an immutable hot-swappable AI provider

**Files:**
- Create: `backend/internal/ai/runtime.go`
- Create: `backend/internal/ai/runtime_test.go`
- Modify: `backend/internal/observability/provider.go`
- Modify: `backend/internal/observability/provider_test.go`

**Interfaces:**
- Produces: `ai.Runtime` implementing `ai.Provider`, `ai.StructuredCompleter`, and `ai.CapabilityProbe`
- Produces: `ai.RuntimeCandidate{Provider ai.Provider, Probe ai.CapabilityProbe}` and `Runtime.Swap(RuntimeCandidate)`
- Preserves: one concrete provider snapshot for the entire delegated operation

- [ ] **Step 1: Write failing concurrent delegation tests**

Create blocking old/new providers. Start `Complete`, `CompleteStructured`, `Embed`, `ProbeChat`, and `ProbeEmbeddings` on old, swap, then release. Every in-flight result must be old and every subsequent result new. Run 100 concurrent readers/swaps under `-race` and prove `Name`/`Available` always come from a complete snapshot.

Add an observability test proving each concrete candidate is wrapped before activation and retains native structured completion. Do not wrap the runtime reference in a way that hides `StructuredCompleter` or `CapabilityProbe`.

- [ ] **Step 2: Run and prove failure**

```powershell
Set-Location backend
go test ./internal/ai ./internal/observability -run 'TestRuntimeProvider|TestObservedRuntimeCandidatePreservesStructuredCompletion' -count=1
```

Expected: `ai.Runtime` is undefined.

- [ ] **Step 3: Implement one-load-per-call delegation**

```go
type runtimeSnapshot struct {
	provider Provider
	probe CapabilityProbe
}
```

Store this immutable object in `atomic.Pointer`. Each method loads once. `CompleteStructured` delegates to the loaded `StructuredCompleter`, or calls `ai.Generate` against that same loaded provider. Initialize both roles with `Disabled{}`. Candidate preparation/probing happens elsewhere; `Swap` only stores a pointer.

- [ ] **Step 4: Run package and race tests**

```powershell
Set-Location backend
go test ./internal/ai ./internal/observability -count=1
go test -race ./internal/ai ./internal/observability -count=1
```

Expected: native JSON Schema behavior survives swaps.

- [ ] **Step 5: Commit the AI runtime**

```powershell
git add backend/internal/ai/runtime.go backend/internal/ai/runtime_test.go backend/internal/observability/provider.go backend/internal/observability/provider_test.go
git commit -m "feat: hot swap AI provider snapshots"
```

---

### Task 6: Stage and swap semantic language-server runtimes

**Files:**
- Create: `backend/internal/semantic/runtime.go`
- Create: `backend/internal/semantic/runtime_test.go`
- Create: `backend/internal/lspruntime/coordinator.go`
- Create: `backend/internal/lspruntime/coordinator_test.go`
- Modify: `backend/cmd/codeatlas/main.go`

**Interfaces:**
- Produces: `semantic.Runtime` implementing `SemanticProvider`, `SemanticTokenProvider`, `DocumentSemanticSync`, `CapabilitiesForPath`, `ProviderIDForPath`, and `ProviderStateForPath`
- Produces: `semantic.Runtime.Prepare(candidate *PathRouter) (activate func(), abort func(context.Context))`
- Produces: `lspruntime.Coordinator.Prepare(context.Context, settings.Values, settings.ChangeSet) (Prepared, []settings.FieldError)`
- Owns: five manager/provider slots and bounded shutdown functions

- [ ] **Step 1: Write failing semantic replay/swap tests**

Open two documents through `semantic.Runtime`, change one, and prepare a router. The candidate must receive the latest version/content of both before activation. During preparation, queries use old; after activation, path reporting, navigation, diagnostics, tokens, and changes use new.

Force replay failure on the second document. Assert no swap, close the first candidate document, and prove the old router still receives later changes.

- [ ] **Step 2: Write failing five-family coordinator tests**

Inject factories rather than real processes. Require only changed families to construct; all candidates start before activation; one failure aborts all and preserves the full old router; mode `false` installs AST-only then shuts down old; shutdown happens only after activation with bounded contexts; capability entries update without exposing executable paths/output.

- [ ] **Step 3: Run and prove failure**

```powershell
Set-Location backend
go test ./internal/semantic ./internal/lspruntime -run 'TestRuntimeReplaysOpenDocumentsBeforeSwap|TestCoordinatorStagesAllChangedFamilies' -count=1
```

Expected: semantic runtime and LSP coordinator are undefined.

- [ ] **Step 4: Implement replay and staged replacement**

Track open documents in `semantic.Runtime` under a mutex while routing snapshots use an atomic pointer. `Prepare` snapshots documents, opens them on the candidate in deterministic ID order, and returns an infallible activation closure only after every replay succeeds.

Move duplicated manager construction/capability mapping from `main.go` behind injected `lspruntime.Factories`, preserving each adapter's safe initialization policy. Assemble one candidate `PathRouter` from unchanged old slots and changed candidates, then prepare it through `semantic.Runtime`.

- [ ] **Step 5: Run semantic/LSP/main tests**

```powershell
Set-Location backend
go test ./internal/semantic ./internal/lspruntime ./cmd/codeatlas -count=1
go test -race ./internal/semantic ./internal/lspruntime -count=1
```

Expected: tests pass without real LSP binaries.

- [ ] **Step 6: Commit staged LSP replacement**

```powershell
git add backend/internal/semantic/runtime.go backend/internal/semantic/runtime_test.go backend/internal/lspruntime/coordinator.go backend/internal/lspruntime/coordinator_test.go backend/cmd/codeatlas/main.go
git commit -m "feat: replace language servers in process"
```

---

### Task 7: Make embeddings and dense availability dynamic

**Files:**
- Create: `backend/internal/retrieval/runtime.go`
- Create: `backend/internal/retrieval/runtime_test.go`
- Modify: `backend/internal/retrieval/hybrid.go`
- Modify: `backend/internal/retrieval/embedding.go`
- Modify: `backend/internal/retrieval/embedding_test.go`
- Modify: `backend/internal/httpapi/server.go`
- Modify: `backend/internal/httpapi/scheduler_test.go`

**Interfaces:**
- Produces: `retrieval.EmbeddingRuntime` states `disabled`, `available`, `rebuilding`, `unavailable`
- Produces: `EmbeddingFingerprint{Provider, Model, ConfigurationHash, Dimension, TemplateVersion, Distance}`
- Produces: prepare/activate/mark-available/mark-failed operations
- Consumes: shared `ai.Runtime` and the existing `embeddings.rebuild` job path

- [ ] **Step 1: Write failing state/fingerprint tests**

Require this matrix:

| State | Query embedding | Dense read | Incremental embedding | Lexical |
|---|---:|---:|---:|---:|
| disabled | no | no | no | yes |
| rebuilding | no | no | no | yes |
| unavailable | no | no | no | yes |
| available + compatible | yes | yes | yes | yes |

Changing enabled/model/base URL/credential generation must probe a dimension, compare metadata, enter rebuilding on mismatch, and become available only after compatible metadata commits. A failed job leaves lexical search working and state unavailable.

- [ ] **Step 2: Run and prove failure**

```powershell
Set-Location backend
go test ./internal/retrieval ./internal/httpapi -run 'TestEmbeddingRuntime|TestHybridUsesDenseOnlyWhenRuntimeAvailable' -count=1
```

Expected: `Hybrid` still captures fixed provider/enabled fields.

- [ ] **Step 3: Implement immutable embedding snapshots**

Keep `NewHybrid(store, provider, enabled)` for existing tests but make it construct a runtime; add a production constructor accepting the shared runtime. Load one embedding snapshot per Search/Generate/Reconcile operation.

Activate incompatible changes as rebuilding and submit/reuse the existing `embeddings:repository` deduplication key. Derive `ConfigurationHash` from the normalized embedding endpoint and saved credential generation, store only its SHA-256-derived opaque provider identity in existing embedding metadata, and never persist the endpoint or key. The job captures its fingerprint, publishes vectors transactionally, and calls `MarkAvailable` only if still current; a superseded job cannot reactivate an old fingerprint.

- [ ] **Step 4: Run retrieval/job race tests**

```powershell
Set-Location backend
go test ./internal/retrieval ./internal/httpapi -count=1
go test -race ./internal/retrieval -count=1
```

Expected: stale jobs cannot enable incompatible dense indexes.

- [ ] **Step 5: Commit dynamic embeddings**

```powershell
git add backend/internal/retrieval/runtime.go backend/internal/retrieval/runtime_test.go backend/internal/retrieval/hybrid.go backend/internal/retrieval/embedding.go backend/internal/retrieval/embedding_test.go backend/internal/httpapi/server.go backend/internal/httpapi/scheduler_test.go
git commit -m "feat: apply embedding settings without restart"
```

---

### Task 8: Compose concrete runtime preparation and startup settings

**Files:**
- Create: `backend/internal/app/settings_runtime.go`
- Create: `backend/internal/app/settings_runtime_test.go`
- Modify: `backend/cmd/codeatlas/main.go`
- Modify: `backend/internal/app/runtime.go`
- Modify: `backend/internal/app/runtime_test.go`

**Interfaces:**
- Produces: concrete `settings.RuntimePreparer` combining AI, embedding, and LSP candidates
- Produces: startup settings manager loaded before workspace/store/listener construction
- Preserves: fixed running bootstrap values while exposing saved next-start values

- [ ] **Step 1: Write failing all-or-nothing tests**

With fake AI probes, embedding runtime, and LSP coordinator, change all live groups plus workspace. Require every probe/candidate prepare before persistence; one LSP failure aborts AI/embedding and leaves workspace unsaved. Success performs one file commit before swaps and returns workspace in `restartRequired`.

- [ ] **Step 2: Run and prove failure**

```powershell
Set-Location backend
go test ./internal/app ./cmd/codeatlas -run 'TestSettingsRuntimePreparesEveryLiveGroupBeforeCommit|TestStartupAppliesSavedOverridesBeforeComposition' -count=1
```

Expected: concrete runtime preparation/wiring is absent.

- [ ] **Step 3: Build candidates without mutating live state**

For AI changes, construct raw `OpenAICompatible`, keep its `CapabilityProbe`, wrap the business interface with `observability.ObserveProvider`, and prepare one `ai.RuntimeCandidate`. Probe chat when chat fields change or the runtime is unconfigured; probe embeddings when enabled and embedding fields change.

Combine AI, `EmbeddingRuntime.Prepare`, and `lspruntime.Coordinator.Prepare`. Return one prepared object whose `Activate` only swaps pointers/publishes state and schedules bounded old-resource cleanup.

- [ ] **Step 4: Load settings before fixed composition**

In `run()`: load environment/defaults and the document; resolve vault generations; validate workspace/listen/max; capture running values; construct dynamic AI/embedding/semantic references; construct services against them; attach the concrete preparer. Isolate user-config roots in process tests so developer globals cannot affect workspaces/flags.

- [ ] **Step 5: Run app/main tests**

```powershell
Set-Location backend
go test ./internal/app ./cmd/codeatlas ./internal/settings -count=1
```

Expected: saved overrides apply before composition and invalid provider fields yield `ai.Disabled`.

- [ ] **Step 6: Commit composition**

```powershell
git add backend/internal/app/settings_runtime.go backend/internal/app/settings_runtime_test.go backend/cmd/codeatlas/main.go backend/internal/app/runtime.go backend/internal/app/runtime_test.go
git commit -m "feat: compose persisted runtime settings"
```

---

### Task 9: Make first-run provider configuration recoverable

**Files:**
- Modify: `backend/internal/readiness/state.go`
- Modify: `backend/internal/readiness/coordinator.go`
- Modify: `backend/internal/readiness/coordinator_test.go`
- Modify: `backend/internal/app/bootstrap.go`
- Create: `backend/internal/app/bootstrap_test.go`
- Modify: `backend/internal/httpapi/readiness_middleware.go`
- Modify: `backend/internal/httpapi/readiness_middleware_test.go`

**Interfaces:**
- Produces: `readiness.StateAwaitingConfiguration = "AWAITING_CONFIGURATION"`
- Produces: coalescing retry signal from successful provider activation to bootstrap
- Preserves: fatal local probes, store migration/indexing, bind failure, and invalid workspace

- [ ] **Step 1: Write failing lifecycle tests**

Require:

```text
BOOTING -> PROBING_CAPABILITIES -> AWAITING_CONFIGURATION
AWAITING_CONFIGURATION -> PROBING_CAPABILITIES -> MIGRATING_STORE/INDEXING -> READY
```

Provider absence or sanitized provider probe failure enters awaiting state, keeps HTTP alive, and waits without polling. A valid settings activation signals one retry. Local mandatory failure remains terminal `FAILED`; cancellation exits. Settings routes are allowed before ready while functional routes remain gated.

- [ ] **Step 2: Run and prove failure**

```powershell
Set-Location backend
go test ./internal/readiness ./internal/app ./internal/httpapi -run 'Test.*AwaitingConfiguration|TestSettingsRoutesAllowedBeforeReady' -count=1
```

Expected: current bootstrap enters `FAILED`.

- [ ] **Step 3: Implement the auditable retry cycle**

Separate local mandatory probes from provider probes. Only provider failures recover. Record the capability result, transition to awaiting, and select on a coalescing manager retry channel or cancellation. On valid apply, transition back to probing and continue bootstrap. Never permit arbitrary `FAILED -> READY`. After `READY`, valid hot changes update capabilities without leaving ready.

- [ ] **Step 4: Run affected suites**

```powershell
Set-Location backend
go test ./internal/readiness ./internal/app ./internal/httpapi -count=1
```

Expected: first-run repair works without restart.

- [ ] **Step 5: Commit recoverable startup**

```powershell
git add backend/internal/readiness/state.go backend/internal/readiness/coordinator.go backend/internal/readiness/coordinator_test.go backend/internal/app/bootstrap.go backend/internal/app/bootstrap_test.go backend/internal/httpapi/readiness_middleware.go backend/internal/httpapi/readiness_middleware_test.go
git commit -m "feat: await provider configuration at startup"
```

---

### Task 10: Expose a local-only sanitized settings API

**Files:**
- Create: `backend/internal/httpapi/settings.go`
- Create: `backend/internal/httpapi/settings_test.go`
- Modify: `backend/internal/httpapi/server.go`
- Modify: `backend/internal/httpapi/error_test.go`
- Modify: `backend/internal/webui/embed.go`
- Modify: `backend/internal/webui/embed_test.go`
- Modify: `frontend/index.html`

**Interfaces:**
- Adds: `GET /api/settings`, `PUT /api/settings`, `DELETE /api/settings/overrides`
- Adds: `X-CodeAtlas-Settings-Token` sourced from `<meta name="codeatlas-settings-token">`
- Produces: `Server.SetSettingsManager(*settings.Manager, token string)`

- [ ] **Step 1: Write failing API tests**

Use a real loopback test server where peer addresses matter. Cover sanitized/no-store GET with 23 fields; valid PUT with applied/restart/job result; `409 SETTINGS_REVISION_CONFLICT`; prepared reset; `403` for non-loopback peer/Host, bad token, cross-origin Origin; `415` for mutation without JSON; `413` before decode over 64 KiB; unknown fields; and absence of request body/vault errors from logs/details.

- [ ] **Step 2: Write failing token-injection tests**

Require exactly one nonce placeholder and one settings-token placeholder in built HTML. Each HTML response receives the process token and stays no-store; hashed assets contain neither token. CSP nonce remains fresh per response.

- [ ] **Step 3: Run and prove failure**

```powershell
Set-Location backend
go test ./internal/httpapi ./internal/webui -run 'TestSettings|TestHandlerInjectsSettingsToken' -count=1
```

Expected: routes and token injection are absent.

- [ ] **Step 4: Implement security and handlers**

Generate 32 random bytes once in the composition root and compare in constant time. Parse `RemoteAddr` and require `IP.IsLoopback`; accept only loopback IP/`localhost` Host. Mutations require Origin exactly `http(s)://` plus Host; GET may omit Origin but rejects mismatch. Every endpoint requires token.

Decode through `MaxBytesReader`, `DisallowUnknownFields`, and an EOF check. Map manager conflicts/field errors into existing error envelopes without reflecting userinfo URLs or submitted values.

- [ ] **Step 5: Run HTTP/web UI suites**

```powershell
Set-Location backend
go test ./internal/httpapi ./internal/webui -count=1
```

Expected: security/redaction tests pass.

- [ ] **Step 6: Commit the API**

```powershell
git add backend/internal/httpapi/settings.go backend/internal/httpapi/settings_test.go backend/internal/httpapi/server.go backend/internal/httpapi/error_test.go backend/internal/webui/embed.go backend/internal/webui/embed_test.go frontend/index.html
git commit -m "feat: expose local settings administration API"
```

---

### Task 11: Build the discreet accessible Settings drawer

**Files:**
- Create: `frontend/settings.js`
- Create: `frontend/tests/settings.test.cjs`
- Modify: `frontend/index.html`
- Modify: `frontend/app.js`
- Modify: `frontend/src/main.ts`
- Modify: `frontend/styles.css`
- Modify: `frontend/app.test.cjs`

**Interfaces:**
- Produces: `createSettingsController({api, token, focusManager, announce})`
- Produces: pure `buildUpdateRequest`, `renderFieldStatus`, and `settingsFieldInventory` helpers
- Consumes: sanitized settings DTOs

- [ ] **Step 1: Write failing structure/inventory tests**

Require `#settings-button` with accessible name Settings after `#reindex-button`, and `#bootstrap-settings-button` inside bootstrap actions. Parse `.env.example`; require all 23 keys exactly once across General, LLM, Embeddings, Language servers. Secret descriptors must never accept a prefilled value.

- [ ] **Step 2: Write failing pure controller tests**

Cover unchanged omission/preserve; typed normal `set` and `inherit`; secret `replace` only for non-empty local input, explicit Use `.env` as `inherit`, otherwise `preserve`; stale revision refresh with cleared secrets; keyed inline errors; running/saved restart banner; exact source labels; exact secret status labels.

- [ ] **Step 3: Run and prove failure**

```powershell
Set-Location frontend
npm test -- --test-name-pattern="settings|Settings"
```

Expected: buttons/controller/drawer are absent.

- [ ] **Step 4: Add drawer markup and styling**

Add a hidden right drawer with `role="dialog"`, `aria-modal`, labelled heading, close button, four sections, status region, and **Reset to .env**, **Cancel**, **Test and apply**. Reuse theme tokens. Keep the gear subordinate to reindex; use `min(100vw, 34rem)` full height on narrow screens. Render server values only via nodes/`textContent`, never server-derived `innerHTML`.

- [ ] **Step 5: Bind during `boot()` before readiness**

Import `settings.js` before `app.js` from `src/main.ts` and expose only the controller factory/pure helpers through `globalThis.CodeAtlasSettings` (plus `module.exports` for Node tests). Read the token only from meta into the controller closure. Bind both buttons before polling. Reuse `FocusManager` save/trap/release/restore, support Escape/backdrop, and restore to the opener. Fetch fresh on each open; never cache secret text; clear secret inputs after every success/failure/close; send token on every call; keep open on validation failure; map `AWAITING_CONFIGURATION` to a continuously polled configuration phase, and restart readiness polling after first-run success.

- [ ] **Step 6: Run frontend check/build**

```powershell
Set-Location frontend
npm run check
npm run build
```

Expected: typecheck, syntax, tests, and build pass with one token placeholder.

- [ ] **Step 7: Commit the drawer**

```powershell
git add frontend/settings.js frontend/tests/settings.test.cjs frontend/index.html frontend/app.js frontend/src/main.ts frontend/styles.css frontend/app.test.cjs
git commit -m "feat: add runtime settings drawer"
```

---

### Task 12: Verify E2E behavior, packaging, and documentation

**Files:**
- Create: `e2e/tests/settings.test.mjs`
- Modify: `e2e/harness/process-manager.mjs`
- Modify: `e2e/harness/fake-provider.mjs`
- Modify: `e2e/package.json`
- Create: `backend/internal/settings/keyring_smoke_test.go`
- Modify: `README.md`
- Modify: `docs/MANIFEST.txt`
- Modify: `docs/SHA256SUMS.txt`
- Verify only: `build_package_and_run.cmd`
- Verify only: `build_package_and_run.sh`

**Interfaces:**
- Produces: isolated E2E config roots and token discovery from served HTML
- Verifies: opt-in Windows Credential Manager/macOS Keychain smoke tests
- Documents: precedence, storage, live/restart, reset, loopback boundary, vault errors

- [ ] **Step 1: Isolate the E2E user profile**

Create a temp config root per process. Set `APPDATA` on Windows, `HOME` on macOS, and `XDG_CONFIG_HOME` on Linux. Add wait mode for `AWAITING_CONFIGURATION` and a token-meta reader that never prints the token.

- [ ] **Step 2: Add E2E scenarios**

1. Start without LLM base/model, configure from bootstrap, reach ready.
2. Block an old chat request, switch fake endpoint, prove old finishes on A and next uses B.
3. Change embedding fingerprint, observe linked rebuild, lexical during rebuild, then dense compatible.
4. Apply broken LSP path; PUT fails and old session still answers.
5. Save workspace/listen/max, see running/saved, restart, confirm overrides beat conflicting env.
6. Reset and confirm environment becomes effective.
7. Synthetic non-loopback settings request is rejected.
8. HTML/JSON/stdout/stderr/reports contain zero sentinel API-key matches.

- [ ] **Step 3: Run focused cross-layer suites**

```powershell
Set-Location backend
go test -race ./internal/settings ./internal/ai ./internal/semantic ./internal/lspruntime ./internal/retrieval ./internal/app ./internal/httpapi -count=1
Set-Location ..\frontend
npm run check
Set-Location ..\e2e
npm test -- --test-name-pattern="settings|runtime provider|embedding settings|LSP settings"
```

Expected: every command passes and reports are secret-free.

- [ ] **Step 4: Add opt-in platform vault smoke tests**

Environment-gated tests create one randomized `CodeAtlas-test` account, read it, and delete that exact account. Run Windows against Credential Manager and macOS against Keychain. Default `go test ./...` skips them.

- [ ] **Step 5: Update README**

Document entry points, all 23 fields/source badges, both settings paths, vault storage, precedence, live versus restart groups, reset, loopback-only administration, and no automatic secret import. Preserve and review the existing uncommitted packaging edits before staging README.

- [ ] **Step 6: Run complete verification**

```powershell
Set-Location I:\CodeAtlas
make check
make build
cmd /c build_package_and_run.cmd --help
bash ./build_package_and_run.sh --help
```

Expected: check/build pass and both scripts verify non-destructively. If no `--help` exists, use their documented non-long-lived verification mode.

- [ ] **Step 7: Refresh canonical docs metadata**

With Windows `core.autocrlf=true`, calculate `docs/SHA256SUMS.txt` from canonical LF/index content, not CRLF working files. Update manifest and verify staged entries against `git show :docs/<path>`.

```powershell
make verify-docs
git diff --check
git status --short
```

Expected: docs/whitespace pass and unrelated packaging files remain deliberately scoped.

- [ ] **Step 8: Commit E2E/docs**

```powershell
git add e2e/tests/settings.test.mjs e2e/harness/process-manager.mjs e2e/harness/fake-provider.mjs e2e/package.json backend/internal/settings/keyring_smoke_test.go README.md docs/MANIFEST.txt docs/SHA256SUMS.txt
git commit -m "test: verify persisted runtime settings"
```

Do not stage `.env`, temp config roots, generated databases, vault values, or unrelated user changes.

---

## Final Acceptance Checklist

- [ ] Inventory matches `.env.example` exactly and covers all 23 variables.
- [ ] Saved non-secrets survive restart and win independently over environment.
- [ ] API keys survive restart only in the OS vault and never appear in JSON/API/DOM/logs.
- [ ] Provider swaps preserve structured output, probes, observability, and in-flight isolation.
- [ ] Embedding changes keep lexical available and never read incompatible dense vectors.
- [ ] Failed LSP candidates preserve prior manager and open-document behavior.
- [ ] Startup-only fields show running versus next-start and apply after restart.
- [ ] First-run Settings advances the same process from awaiting configuration to ready.
- [ ] Settings rejects remote peers, non-loopback hosts, cross-origin, bad token, wrong content type, and oversize bodies.
- [ ] Drawer is accessible, responsive, discreet, and never prefills secrets.
- [ ] Windows/macOS packages include keyring integration and both build scripts remain functional.
- [ ] Backend, frontend, E2E, docs, race, build, and secret-scan verification passes.
