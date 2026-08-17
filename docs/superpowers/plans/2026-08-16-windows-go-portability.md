# Windows Go Portability Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Make the complete Go suite deterministic on Windows without weakening behavior that can be exercised portably.

**Architecture:** Replace POSIX-only failure simulation with structural filesystem errors or a narrow read dependency, use native temporary paths, normalize fixture line endings, gate machine-dependent integration tests explicitly, and make asset MIME selection independent of the Windows registry.

**Tech Stack:** Go, Windows and POSIX filesystem semantics, `net/http`, embedded frontend assets, language-server integration tests.

**Spec:** `docs/superpowers/specs/2026-08-16-llm-windows-e2e-reliability-design.md`

## Global Constraints

- Portable behavior remains tested on every operating system.
- Only POSIX permission-bit assertions and explicitly real LSP integration are conditionally skipped.
- Filesystem failures must be deterministic and must not depend on administrator/root behavior.
- Frontend asset MIME types are identical across host registries.
- No production code receives test-only cleanup methods.

---

### Task 1: Make filesystem failure tests portable

**Files:**
- Modify: `backend/internal/capabilities/local_probes_test.go`
- Modify: `backend/internal/indexer/indexer.go`
- Modify: `backend/internal/indexer/scan_test.go`

**Interfaces:**
- Produces: private `Indexer.readFile func(string) ([]byte, error)` initialized to `os.ReadFile`
- Consumes: real state-area probe and scan transaction logic

- [ ] **Step 1: Replace the chmod-based state-area test with a structural conflict**

Rewrite `TestStateAreaProbeReadOnlyDir` as `TestStateAreaProbeRejectsUnusableProbeDirectory`:

```go
func TestStateAreaProbeRejectsUnusableProbeDirectory(t *testing.T) {
	t.Parallel()
	stateDir := t.TempDir()
	if err := os.WriteFile(filepath.Join(stateDir, "probes"), []byte("not a directory"), 0o600); err != nil {
		t.Fatal(err)
	}
	probe := stateAreaProbe{probeBase{CapabilityStateArea, Required}, stateDir}
	result := runProbeDirect(t, probe)
	if result.State != CapabilityUnavailable || result.ErrorCode != ErrCodeStateAreaUnwritable {
		t.Fatalf("result = %#v, want unavailable/%s", result, ErrCodeStateAreaUnwritable)
	}
}
```

This exercises the real `os.MkdirAll` failure on Windows and POSIX.

- [ ] **Step 2: Write a failing controlled-read scan test**

Rewrite `TestScanReadErrorIsFatal` so it does not call `chmod`:

```go
func TestScanReadErrorIsFatal(t *testing.T) {
	t.Parallel()
	indexer, root, repository := newTestIndexer(t, codeparser.New(), nil, false)
	writeSource(t, root, "a.go", goSource)
	if err := indexer.Scan(context.Background()); err != nil {
		t.Fatalf("setup Scan() error = %v", err)
	}
	version := repository.Version()
	writeSource(t, root, "b.go", goSource)
	realReadFile := os.ReadFile
	indexer.readFile = func(name string) ([]byte, error) {
		if filepath.Base(name) == "b.go" {
			return nil, &fs.PathError{Op: "open", Path: name, Err: fs.ErrPermission}
		}
		return realReadFile(name)
	}
	if err := indexer.Scan(context.Background()); err == nil {
		t.Fatal("Scan succeeded despite a controlled read error")
	}
	assertUnchanged(t, indexer, repository, version, 1)
}
```

- [ ] **Step 3: Run both tests and prove the scanner test fails to compile**

```powershell
Set-Location backend
go test ./internal/capabilities ./internal/indexer -run 'TestStateAreaProbeRejectsUnusableProbeDirectory|TestScanReadErrorIsFatal' -count=1
```

Expected: the state-area test passes; the scanner test fails because `Indexer.readFile` does not exist.

- [ ] **Step 4: Add the narrow production read dependency**

Add the field to `Indexer`:

```go
readFile func(string) ([]byte, error)
```

Initialize it in `New`:

```go
readFile: os.ReadFile,
```

Change only `readObservedCandidate`:

```go
source, err := i.readFile(absolute)
```

No exported setter is added; tests in the package may replace the private boundary directly.

- [ ] **Step 5: Run both full packages**

```powershell
Set-Location backend
go test ./internal/capabilities ./internal/indexer -count=1
```

Expected: all tests pass on Windows with no UID/root branch.

- [ ] **Step 6: Commit portable filesystem tests**

```powershell
git add backend/internal/capabilities/local_probes_test.go backend/internal/indexer/indexer.go backend/internal/indexer/scan_test.go
git commit -m "test: make filesystem failures portable"
```

---

### Task 2: Normalize paths, golden text, mode assertions, and MIME

**Files:**
- Modify: `backend/internal/diagram/mermaid_test.go`
- Modify: `backend/internal/lspconv/lspconv_test.go`
- Modify: `backend/internal/service/commit_test.go`
- Modify: `backend/internal/store/sqlite/sqlite_test.go`
- Modify: `backend/internal/webui/embed.go`
- Modify: `backend/internal/webui/embed_test.go`

**Interfaces:**
- Produces: host-independent `assetContentType`
- Preserves: source map MIME `application/json`
- Consumes: `runtime.GOOS`, `filepath.Join`, and `t.TempDir`

- [ ] **Step 1: Add direct cross-platform MIME assertions**

Add this table test in `embed_test.go` within package `webui_test` through the existing compressed-asset handler boundary:

```go
func TestHandlerUsesDeterministicMIMEForCompressedKnownAssets(t *testing.T) {
	assets := viteAssets()
	assets["assets/main-Abc12345.js.map"] = &fstest.MapFile{Data: []byte(`{"version":3}`)}
	assets["assets/main-Abc12345.js.map.br"] = &fstest.MapFile{Data: []byte("map-br")}
	handler := webui.HandlerWithAssets(assets)
	request := httptest.NewRequest(http.MethodGet, "/assets/main-Abc12345.js.map", nil)
	request.Header.Set("Accept-Encoding", "br")
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, request)
	if got := response.Header().Get("Content-Type"); got != "application/json" {
		t.Fatalf("Content-Type = %q, want application/json", got)
	}
}
```

The exact equality catches Windows returning `text/plain; charset=utf-8` from its registry.

- [ ] **Step 2: Run the MIME test and prove it fails on Windows**

```powershell
Set-Location backend
go test ./internal/webui -run TestHandlerUsesDeterministicMIMEForCompressedKnownAssets -count=1
```

Expected on Windows before the fix: MIME is not `application/json`.

- [ ] **Step 3: Prefer CodeAtlas' known MIME map**

Rewrite `assetContentType` so the stable map is consulted first:

```go
func assetContentType(clean string) string {
	extension := strings.ToLower(path.Ext(clean))
	known := map[string]string{
		".css":   "text/css; charset=utf-8",
		".js":    "text/javascript; charset=utf-8",
		".map":   "application/json",
		".woff":  "font/woff",
		".woff2": "font/woff2",
	}
	if contentType := known[extension]; contentType != "" {
		return contentType
	}
	return mime.TypeByExtension(extension)
}
```

- [ ] **Step 4: Normalize Mermaid fixture line endings**

Change `assertGolden` in `mermaid_test.go` to derive the expected semantic text from the fixture while ignoring checkout line endings:

```go
normalized := strings.ReplaceAll(string(want), "\r\n", "\n")
normalized = strings.TrimSuffix(normalized, "\n")
if got != normalized {
	t.Fatalf("%s mismatch\nwant:\n%s\ngot:\n%s", name, normalized, got)
}
```

- [ ] **Step 5: Use native temporary paths in the URI test**

Rewrite `TestURIRoundTripAndWorkspaceScope`:

```go
func TestURIRoundTripAndWorkspaceScope(t *testing.T) {
	t.Parallel()
	workspace := filepath.Join(t.TempDir(), "ws")
	inside := filepath.Join(workspace, "pkg", "a.go")
	outside := filepath.Join(t.TempDir(), "outside.go")
	uri := PathToURI(inside)
	decoded, err := URIToPath(uri)
	if err != nil || filepath.Clean(decoded) != filepath.Clean(inside) {
		t.Fatalf("uri round-trip = %q (%v), want %q", decoded, err, inside)
	}
	if rel, ok := WorkspaceRelative(workspace, uri); !ok || rel != "pkg/a.go" {
		t.Fatalf("workspace relative = %q/%v, want pkg/a.go", rel, ok)
	}
	if _, ok := WorkspaceRelative(workspace, PathToURI(outside)); ok {
		t.Fatal("a path outside the workspace must not be workspace-relative")
	}
	if _, err := URIToPath("https://evil.example/x"); err == nil {
		t.Fatal("a non-file URI scheme must be rejected")
	}
}
```

Import `path/filepath`.

- [ ] **Step 6: Limit mode-bit assertions to POSIX**

Import `runtime` in `commit_test.go` and `sqlite_test.go`. Guard only the existing permission assertions:

```go
if runtime.GOOS != "windows" && info.Mode().Perm() != 0o600 {
	t.Fatalf("destination mode = %o, want 600", info.Mode().Perm())
}
```

and:

```go
if runtime.GOOS != "windows" {
	if perm := info.Mode().Perm(); perm != 0o700 {
		t.Fatalf("state dir perm = %o, want 700", perm)
	}
}
```

All content, database, path, and schema assertions still run on Windows.

- [ ] **Step 7: Run affected packages**

```powershell
Set-Location backend
go test ./internal/diagram ./internal/lspconv ./internal/service ./internal/store/sqlite ./internal/webui -count=1 -tags fts5
```

Expected: all selected packages pass.

- [ ] **Step 8: Commit host-independent behavior**

```powershell
git add backend/internal/diagram/mermaid_test.go backend/internal/lspconv/lspconv_test.go backend/internal/service/commit_test.go backend/internal/store/sqlite/sqlite_test.go backend/internal/webui/embed.go backend/internal/webui/embed_test.go
git commit -m "fix: make Go behavior host-independent"
```

---

### Task 3: Make real language-server tests explicitly opt-in

**Files:**
- Modify: `backend/internal/gopls/gopls_test.go`
- Modify: `backend/internal/typescriptlsp/typescriptlsp_test.go`
- Modify: `backend/internal/swiftlsp/swiftlsp_test.go`
- Modify: `backend/internal/pythonlsp/pythonlsp_test.go`
- Modify: `backend/internal/rustlsp/rustlsp_test.go`
- Modify: `README.md`

**Interfaces:**
- Produces: test-only environment contract `CODEATLAS_TEST_REAL_LSP=1`
- Preserves: existing real server assertions when explicitly enabled

- [ ] **Step 1: Add the opt-in gate to all five tests**

At the first line of `TestRealGopls`, `TestRealTypeScriptLSP`, `TestRealSwiftLSP`, `TestRealPythonLSP`, and `TestRealRustLSP`, add:

```go
if os.Getenv("CODEATLAS_TEST_REAL_LSP") != "1" {
	t.Skip("set CODEATLAS_TEST_REAL_LSP=1 to run real language-server integration")
}
```

Add `os` to packages that do not already import it. Keep the subsequent `exec.LookPath` skip for explicitly enabled machines missing one server.

- [ ] **Step 2: Prove the tests skip before touching PATH executables**

```powershell
Set-Location backend
$env:CODEATLAS_TEST_REAL_LSP = ''
go test ./internal/gopls ./internal/typescriptlsp ./internal/swiftlsp ./internal/pythonlsp ./internal/rustlsp -run '^TestReal' -count=1 -v
```

Expected: all five tests report `SKIP` with the opt-in message; no real server starts.

- [ ] **Step 3: Document the integration-test command**

Add a short README testing note:

```markdown
Real language-server integration is opt-in because installed binaries and toolchains are machine-dependent. Set `CODEATLAS_TEST_REAL_LSP=1` before running the five `TestReal*` packages; the default suite uses deterministic protocol fakes.
```

- [ ] **Step 4: Run all five packages normally**

```powershell
Set-Location backend
Remove-Item Env:CODEATLAS_TEST_REAL_LSP -ErrorAction SilentlyContinue
go test ./internal/gopls ./internal/typescriptlsp ./internal/swiftlsp ./internal/pythonlsp ./internal/rustlsp -count=1
```

Expected: all packages pass independently of accidental `PATH` entries.

- [ ] **Step 5: Commit deterministic integration policy**

```powershell
git add backend/internal/gopls/gopls_test.go backend/internal/typescriptlsp/typescriptlsp_test.go backend/internal/swiftlsp/swiftlsp_test.go backend/internal/pythonlsp/pythonlsp_test.go backend/internal/rustlsp/rustlsp_test.go README.md
git commit -m "test: gate real language servers explicitly"
```

---

### Task 4: Run the full Windows Go verification

**Files:**
- No source changes expected

**Interfaces:**
- Consumes: Tasks 1-3
- Produces: fresh full-suite evidence on Windows

- [ ] **Step 1: Format changed Go packages**

```powershell
Set-Location backend
go fmt ./internal/capabilities ./internal/indexer ./internal/diagram ./internal/lspconv ./internal/service ./internal/store/sqlite ./internal/webui ./internal/gopls ./internal/typescriptlsp ./internal/swiftlsp ./internal/pythonlsp ./internal/rustlsp
```

- [ ] **Step 2: Run the complete tagged Go suite uncached**

```powershell
$env:CODEATLAS_TEST_REAL_LSP = ''
go test -tags fts5 ./... -count=1
```

Expected: every package passes; no permission, CRLF, Unix-path, accidental rust-analyzer, mode-bit, or MIME failure remains.

- [ ] **Step 3: Run static checks**

```powershell
go vet -tags fts5 ./...
```

Expected: exit code zero.

- [ ] **Step 4: Record the exact package/test totals**

Capture only package counts, pass/fail/skip totals, command versions, and duration. Do not include local absolute paths beyond repository file links in the final report.
