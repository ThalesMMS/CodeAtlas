# Runtime Settings and OS Credential Storage Design

**Date:** 2026-08-17

**Status:** Approved in chat; pending written-spec review

**Scope:** Every setting documented by `.env.example`, with per-user persistence and UI overrides that take precedence over environment values

## Context

CodeAtlas currently reads configuration once during startup. The OpenAI-compatible provider, embedding behavior, workspace limits, and language-server managers are then passed as fixed dependencies to long-lived services. The web UI has no configuration surface. Changing an endpoint, model, credential, or language-server path therefore requires editing `.env` and restarting the process.

The requested feature adds a small Settings button to the application and makes all `.env.example` values configurable. Settings are global to the operating-system user. Non-secret values are stored in the user's application-config directory, while API keys are stored in Windows Credential Manager or macOS Keychain. Saved UI values override `.env` on a field-by-field basis.

Provider and embedding changes apply without restarting CodeAtlas. Language-server changes replace the affected manager in-process. Workspace, listen address, and maximum file size are saved immediately but require a process restart.

## Goals

- Configure every variable in `.env.example` from the UI.
- Use deterministic precedence: `default < environment < saved UI override`.
- Persist API keys in the operating system's credential vault, never in the JSON settings file.
- Apply LLM, timeout, embedding, and LSP settings safely at runtime.
- Preserve in-flight requests while new requests use the newly activated provider.
- Keep the UI reachable when the LLM provider is missing or invalid so first-run configuration is possible.
- Never return, log, cache, or render a stored secret.
- Let users remove overrides and return to `.env` without manually editing files or the credential vault.

## Non-goals

- Building a general-purpose secrets manager.
- Synchronizing settings between users or machines.
- Supporting multiple named provider profiles in the first version.
- Automatically restarting the CodeAtlas operating-system process.
- Installing or updating language servers.
- Allowing remote clients to mutate credentials when CodeAtlas is accessed over a non-loopback interface.

## Configuration Inventory

| Group | Setting | Secret | Apply behavior |
|---|---|---:|---|
| General | `CODEATLAS_WORKSPACE` | No | Restart required |
| General | `CODEATLAS_LISTEN` | No | Restart required |
| General | `CODEATLAS_MAX_FILE_BYTES` | No | Restart required |
| LLM | `CODEATLAS_LLM_BASE_URL` | No | Validate, probe, hot apply |
| LLM | `CODEATLAS_LLM_API_KEY` | Yes | Validate, probe, hot apply |
| LLM | `CODEATLAS_LLM_MODEL` | No | Validate, probe, hot apply |
| LLM | `CODEATLAS_LLM_REASONING_EFFORT` | No | Validate, probe, hot apply |
| LLM | `CODEATLAS_LLM_TIMEOUT` | No | Validate and hot apply |
| Embeddings | `CODEATLAS_ENABLE_EMBEDDINGS` | No | Hot apply; may enqueue rebuild |
| Embeddings | `CODEATLAS_EMBEDDING_MODEL` | No | Validate, probe, hot apply; enqueue rebuild |
| Embeddings | `CODEATLAS_EMBEDDING_BASE_URL` | No | Validate, probe, hot apply; enqueue rebuild |
| Embeddings | `CODEATLAS_EMBEDDINGS_API_KEY` | Yes | Validate, probe, hot apply; enqueue rebuild |
| Go LSP | `CODEATLAS_GOPLS` | No | Staged manager replacement |
| Go LSP | `CODEATLAS_GOPLS_PATH` | No | Staged manager replacement |
| JS/TS LSP | `CODEATLAS_TYPESCRIPT_LSP` | No | Staged manager replacement |
| JS/TS LSP | `CODEATLAS_TYPESCRIPT_LSP_PATH` | No | Staged manager replacement |
| JS/TS LSP | `CODEATLAS_TYPESCRIPT_SDK_PATH` | No | Staged manager replacement |
| Swift LSP | `CODEATLAS_SWIFT_LSP` | No | Staged manager replacement |
| Swift LSP | `CODEATLAS_SWIFT_LSP_PATH` | No | Staged manager replacement |
| Python LSP | `CODEATLAS_PYTHON_LSP` | No | Staged manager replacement |
| Python LSP | `CODEATLAS_PYTHON_LSP_PATH` | No | Staged manager replacement |
| Rust LSP | `CODEATLAS_RUST_LSP` | No | Staged manager replacement |
| Rust LSP | `CODEATLAS_RUST_LSP_PATH` | No | Staged manager replacement |

The UI mirrors `.env.example`; internal environment variables that are not documented there remain environment-only.

## Precedence and Override Semantics

Each field is resolved independently:

```text
compiled default < .env/process environment < saved UI override
```

The settings file stores only explicit overrides. A missing override inherits the environment or compiled default. An empty string is a valid explicit value only for fields whose existing configuration contract allows it, such as the optional embedding base URL and TypeScript SDK path.

Secrets use explicit operations rather than overloaded empty strings:

- `preserve`: do not modify the saved credential or its inheritance state;
- `replace`: store the supplied value as a new credential generation;
- `inherit`: delete the saved override and use the environment fallback.

The UI never receives a secret value. It receives only whether a secret is configured and whether its effective source is `settings`, `env`, or `none`.

## Persistent Storage

### Non-secret settings

Use `os.UserConfigDir()` and store a versioned JSON document at:

- Windows: `%AppData%\CodeAtlas\settings.json`
- macOS: `~/Library/Application Support/CodeAtlas/settings.json`

The file is written with a temporary file, file sync, atomic replace, and best-effort directory sync. POSIX permissions are `0600`. The document contains a monotonically increasing revision, explicit non-secret overrides, and opaque credential-generation identifiers; it never contains credential material.

Example shape:

```json
{
  "schemaVersion": 1,
  "revision": 12,
  "overrides": {
    "llmBaseUrl": "http://127.0.0.1:8000/v1",
    "llmModel": "default",
    "enableEmbeddings": true,
    "embeddingModel": "text-embedding"
  },
  "credentials": {
    "llmApiKeyGeneration": "01K...",
    "embeddingsApiKeyGeneration": "01K..."
  }
}
```

Unknown schema versions and unknown fields are rejected without overwriting the file. This prevents an older binary from silently discarding settings written by a newer binary.

### Secrets

Introduce a narrow `CredentialStore` interface with `Get`, `Set`, and `Delete`. The production adapter uses [`github.com/zalando/go-keyring`](https://github.com/zalando/go-keyring), which targets Windows Credential Manager and macOS Keychain. Tests use an in-memory fake.

Use service name `CodeAtlas` and versioned account identifiers:

```text
llm-api-key:<generation>
embeddings-api-key:<generation>
```

Credential rotation is transactional across the keyring and settings file:

1. Validate the candidate settings locally.
2. Write replacement secrets under new generation identifiers.
3. Construct and remotely probe the candidate provider.
4. Atomically write settings JSON that references the new generations.
5. Atomically activate the runtime candidate.
6. Best-effort delete superseded credential generations.

Failure before step 4 deletes the candidate generations and leaves the old settings active. Failure after step 4 but before activation is recovered on the next settings reload; activation itself is designed as an infallible pointer swap after all fallible work has completed.

## Backend Architecture

### `RuntimeConfigManager`

Add a manager responsible for:

- reading environment values and saved overrides;
- resolving the effective configuration and source of every field;
- validating candidate patches;
- coordinating credential rotation and atomic persistence;
- preparing runtime replacements;
- publishing a sanitized settings snapshot and revision;
- reporting which changes were applied and which require restart.

The manager serializes writes with a mutex. Reads use immutable snapshots. A `PUT` based on a stale revision returns `409 SETTINGS_REVISION_CONFLICT`, preventing two tabs from silently overwriting each other.

### Hot-swappable AI provider

Add a provider reference that implements the existing `ai.Provider` and `ai.CapabilityProbe` contracts while holding an immutable concrete provider snapshot. Every method loads one snapshot and delegates the whole operation to it. A swap changes the pointer only after the candidate has passed validation and probes.

Consequences:

- an in-flight request keeps its original provider for the full call;
- a request beginning after activation uses the new provider;
- Explainer, Codemap, DeepWiki, Retrieval, HTTP diagnostics, and capability probes continue depending on one stable interface;
- observability wraps each concrete provider before activation so metrics and secret redaction remain intact.

The initial configuration may resolve to `ai.Disabled`. Missing or invalid LLM configuration no longer prevents the HTTP server and embedded UI from starting. The readiness UI exposes Settings from the bootstrap overlay. Applying a valid provider reruns the mandatory provider probe and resumes the readiness transition without restarting the process.

### Embedding runtime

`retrieval.Hybrid` currently captures a fixed provider and enabled flag. Replace these with a runtime embedding snapshot containing provider, enabled state, model fingerprint, and state (`disabled`, `available`, or `rebuilding`).

When an embedding-affecting field changes:

1. Probe the candidate embedding endpoint when embeddings are enabled.
2. Compare the desired fingerprint with stored embedding metadata.
3. Disable dense reads for incompatible metadata.
4. Activate the provider snapshot.
5. Enqueue or reuse the existing embedding rebuild job.
6. Re-enable dense reads only after compatible vectors are published.

Lexical and graph retrieval remain available while dense retrieval rebuilds.

### Language-server replacements

Create an LSP runtime coordinator around the five existing managers. For a changed language family, build and start a candidate manager first. Only after a successful candidate start does the coordinator swap the routed semantic provider and shut down the previous manager.

If candidate startup fails, the old manager remains active and the entire revisioned `PUT` is rejected without persisting any group. The API returns a field-level error. Selecting `false` is an intentional successful transition that swaps to the existing unavailable/AST-only provider and then shuts down the old process. CodeAtlas still never installs or updates a language server.

### Restart-required settings

Workspace, listen address, and maximum file size are validated and persisted but do not mutate the running composition root. The response reports these fields in `restartRequired`. The UI keeps showing the running effective value separately from the saved next-start value.

At the next startup, CodeAtlas resolves environment/default values first and overlays saved settings afterward. A listen-bind failure or a workspace that disappeared remains a fatal startup error; these cannot be repaired through the UI because no safe HTTP composition root exists. Provider absence alone is recoverable through Settings.

## HTTP API

### `GET /api/settings`

Returns a sanitized, no-store snapshot:

```json
{
  "revision": 12,
  "groups": {
    "llm": {
      "baseUrl": {"value": "http://127.0.0.1:8000/v1", "source": "settings", "applyMode": "live"},
      "apiKey": {"configured": true, "source": "settings", "applyMode": "live"}
    }
  },
  "restartRequired": []
}
```

### `PUT /api/settings`

Accepts `application/json` only, with a bounded request body and expected revision. The request contains non-secret override operations and explicit secret operations. It validates, probes, persists, applies, and returns:

- the new sanitized snapshot;
- live-applied groups;
- restart-required fields;
- an embedding rebuild job identifier when applicable;
- structured field errors without reflecting submitted secrets.

### `DELETE /api/settings/overrides`

Removes all saved overrides and saved keyring credentials, resolves environment/default values, validates runtime candidates, and applies the same live/restart rules. This powers **Reset to .env**. It is not a blind file delete: runtime replacements must be prepared before committing the reset.

### Security boundary

Settings endpoints are local administration endpoints:

- require the request's remote address to be loopback;
- require same-origin `Origin`/`Host` validation;
- require `application/json` for mutations;
- require an unpredictable per-process `X-CodeAtlas-Settings-Token` injected into the embedded page;
- send `Cache-Control: no-store`;
- never include request bodies, credentials, or credential-store error details in logs;
- redact endpoint errors through the existing application-error envelope;
- reject oversized bodies before JSON decoding.

When CodeAtlas is bound to a non-loopback interface, remote users may use normal read/editor features subject to existing policy, but they cannot read or mutate local settings. The local browser must use the loopback URL to administer settings.

## Frontend Design

Add a compact gear icon at the far right of `.topbar-actions`, after **Check for changes**. Add the same action to the bootstrap card so an unconfigured provider can be repaired on first use.

The action opens an accessible right-side drawer. On narrow viewports the drawer fills the available width. It contains four sections:

1. **General** — workspace, listen address, and maximum file size;
2. **LLM** — base URL, model, API key replacement, reasoning effort, and timeout;
3. **Embeddings** — enabled state, model, optional base URL, and optional separate API key;
4. **Language servers** — mode and executable path for Go, JS/TS, Swift, Python, and Rust, plus TypeScript SDK path.

Each field displays its source (`Settings`, `.env`, or `Default`) and apply mode (`Live` or `Restart required`). Secret fields display only **Saved in system keychain**, **Using .env**, or **Not configured**. The UI never inserts a stored secret into a DOM value or application state.

Drawer actions:

- **Test and apply** — submit a revisioned patch and keep the drawer open until success;
- **Reset to .env** — confirmation followed by the reset endpoint;
- **Cancel** — discard unsaved browser state.

Validation errors render next to the associated field. Remote probe errors identify the affected endpoint without echoing URLs containing credentials or secret values. Successful live fields show **Applied**. Saved bootstrap fields show a persistent **Restart required** banner.

The drawer traps focus, supports `Escape`, restores focus to the gear button, uses semantic labels/descriptions, and follows the current dark theme and responsive layout.

## Failure Handling

- Invalid local values: reject before keyring or network work.
- Keyring unavailable or locked: reject secret changes; keep previous runtime and file.
- Provider probe failure: delete candidate credentials and keep previous provider.
- Settings-file write failure: delete candidate credentials and keep previous runtime.
- Provider activation: pointer swap only; no fallible work remains.
- Embedding rebuild failure: keep lexical retrieval available, dense state unavailable, and surface the existing job error.
- LSP candidate failure: keep every current runtime component and reject the whole settings update without persistence.
- Stale browser revision: return `409` with a fresh sanitized snapshot.
- Cleanup failure for an old credential generation: log only the generation identifier and retry cleanup during a later successful save/startup.

## Testing Strategy

### Backend unit tests

- per-field precedence across default, environment, and saved override;
- explicit empty value versus inherited value;
- settings schema validation and future-version rejection;
- credential `preserve`, `replace`, and `inherit` semantics;
- versioned credential rotation and rollback at every fallible step;
- no secret values in snapshots, errors, logs, or JSON;
- provider snapshot behavior across concurrent in-flight calls;
- embedding state transitions and fingerprint changes;
- LSP staged replacement and old-manager preservation;
- restart-required diff calculation;
- optimistic revision conflicts.

### HTTP integration tests

- sanitized GET response;
- successful live PUT with fake provider and fake keyring;
- invalid/probe-failed PUT leaves runtime and persistence unchanged;
- reset to environment;
- loopback, same-origin, token, content-type, and body-size enforcement;
- first-run provider configuration from the bootstrap state;
- embedding rebuild job linkage.

### Frontend tests

- gear-button placement and accessible name;
- drawer focus management and keyboard behavior;
- all `.env.example` fields represented exactly once;
- source/apply-mode rendering;
- secret fields never populated with stored values;
- patch construction for preserve/replace/inherit;
- inline validation and restart banner;
- stale-revision refresh behavior.

### End-to-end tests

- start without an LLM provider, open Settings from bootstrap, configure, and reach READY;
- switch between two fake chat endpoints without restart;
- change embeddings and observe rebuild before dense availability;
- reject a broken LSP candidate while the previous one remains active;
- persist settings, restart CodeAtlas, and confirm UI overrides environment;
- reset and confirm environment values become effective;
- verify settings mutations are rejected from a non-loopback client.

Windows Credential Manager and macOS Keychain adapters receive platform-specific smoke coverage. Normal tests use a fake and never touch a developer's real credential vault.

## Documentation and Migration

- Update README configuration documentation with precedence, storage locations, live versus restart behavior, and reset instructions.
- Add third-party notice information for the keyring dependency and its license.
- Existing `.env` users require no migration. With no settings file, behavior is unchanged except that a missing provider starts the UI in recoverable configuration mode instead of terminating before bind.
- The settings schema begins at version 1 and contains no automatic import of `.env` secrets into the keyring. Environment credentials stay environment-sourced until the user explicitly replaces them in Settings.

## Rejected Alternatives

### Persist everything and restart

This avoids runtime references but fails the approved immediate-apply experience for providers, embeddings, and LSPs.

### Send credentials with every browser request

This spreads secrets into frontend state and request payloads, couples every AI endpoint to UI configuration, and expands the exposure surface.

### Store API keys in settings JSON

File permissions alone are weaker and less consistent than Windows Credential Manager and macOS Keychain. The user explicitly selected the OS credential vault.

### Automatically restart the process

CodeAtlas may be launched from a terminal, IDE, service manager, or packaging script. A child process cannot reliably recreate the original supervision environment. The first version reports restart requirements instead.

## Acceptance Criteria

- Every `.env.example` variable is editable in Settings.
- Saved overrides win over environment values independently per field.
- API keys persist only in the OS credential vault and never appear in JSON, API responses, DOM state, errors, or logs.
- Valid provider changes affect new requests without interrupting existing ones.
- Embedding changes never query an incompatible dense index.
- Invalid LSP changes preserve the prior working manager.
- Startup-only changes are clearly marked and used on the next process start.
- The Settings UI is reachable when provider configuration is absent.
- Reset removes overrides and restores `.env`/default behavior.
- Settings administration is restricted to same-origin loopback clients.
