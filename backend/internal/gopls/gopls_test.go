package gopls

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"github.com/ThalesMMS/CodeAtlas/internal/domain"
	"github.com/ThalesMMS/CodeAtlas/internal/lspclient"
	"github.com/ThalesMMS/CodeAtlas/internal/lspfacts"
	"github.com/ThalesMMS/CodeAtlas/internal/semantic"
)

type fakeClient struct {
	mu              sync.Mutex
	state           lspclient.ClientState
	initResult      initializeResult
	notifications   map[string]int
	requestHandlers map[string]lspclient.ServerRequestHandler
	notifyHandlers  map[string]lspclient.NotificationHandler
	responses       map[string]json.RawMessage // method → canned result JSON
	requestParams   map[string]json.RawMessage
	onRequest       func(string)
}

func (f *fakeClient) respond(method string, result json.RawMessage) {
	f.mu.Lock()
	if f.responses == nil {
		f.responses = map[string]json.RawMessage{}
	}
	f.responses[method] = result
	f.mu.Unlock()
}

func newFakeClient(result initializeResult) *fakeClient {
	return &fakeClient{
		initResult:      result,
		notifications:   map[string]int{},
		requestHandlers: map[string]lspclient.ServerRequestHandler{},
		requestParams:   map[string]json.RawMessage{},
	}
}

func (f *fakeClient) Start(context.Context) error {
	f.mu.Lock()
	f.state = lspclient.StateRunning
	f.mu.Unlock()
	return nil
}

func (f *fakeClient) Request(_ context.Context, method string, params any, result any) error {
	data, _ := json.Marshal(params)
	f.mu.Lock()
	f.requestParams[method] = data
	f.mu.Unlock()
	if f.onRequest != nil {
		f.onRequest(method)
	}
	if method == "initialize" && result != nil {
		response, _ := json.Marshal(f.initResult)
		return json.Unmarshal(response, result)
	}
	f.mu.Lock()
	canned, ok := f.responses[method]
	f.mu.Unlock()
	if ok && result != nil {
		return json.Unmarshal(canned, result)
	}
	return nil
}

func (f *fakeClient) requested(method string) json.RawMessage {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append(json.RawMessage(nil), f.requestParams[method]...)
}

func (f *fakeClient) Notify(_ context.Context, method string, _ any) error {
	f.mu.Lock()
	f.notifications[method]++
	f.mu.Unlock()
	return nil
}

func (f *fakeClient) OnNotification(method string, handler lspclient.NotificationHandler) {
	f.mu.Lock()
	if f.notifyHandlers == nil {
		f.notifyHandlers = map[string]lspclient.NotificationHandler{}
	}
	f.notifyHandlers[method] = handler
	f.mu.Unlock()
}

// fire delivers a server→client notification to the registered handler.
func (f *fakeClient) fire(method string, params json.RawMessage) {
	f.mu.Lock()
	handler := f.notifyHandlers[method]
	f.mu.Unlock()
	if handler != nil {
		handler(params)
	}
}
func (f *fakeClient) OnRequest(method string, handler lspclient.ServerRequestHandler) {
	f.mu.Lock()
	f.requestHandlers[method] = handler
	f.mu.Unlock()
}
func (f *fakeClient) State() lspclient.ClientState {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.state
}
func (f *fakeClient) Close(context.Context) error {
	f.mu.Lock()
	f.state = lspclient.StateClosed
	f.mu.Unlock()
	return nil
}
func (f *fakeClient) notifyCount(method string) int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.notifications[method]
}
func (f *fakeClient) crash() {
	f.mu.Lock()
	f.state = lspclient.StateFailed
	f.mu.Unlock()
}

func capable() initializeResult {
	present := func() providerOption { return lspfacts.NewProviderOption(true) }
	semanticTokens := lspfacts.NewSemanticTokensProviderOption(lspfacts.SemanticTokensLegend{
		TokenTypes: []string{
			"namespace", "type", "typeParameter", "parameter", "property", "variable",
			"function", "method", "macro", "keyword", "comment", "string", "number", "operator", "label",
		},
		TokenModifiers: []string{"definition", "readonly", "defaultLibrary", "static"},
	}, true)
	return initializeResult{
		ServerInfo: serverInfo{Name: "gopls", Version: "v0.22.0"},
		Capabilities: serverCapabilities{
			PositionEncoding:       "utf-8",
			HoverProvider:          present(),
			DefinitionProvider:     present(),
			ReferencesProvider:     present(),
			ImplementationProvider: present(),
			CallHierarchyProvider:  present(),
			SemanticTokensProvider: semanticTokens,
		},
	}
}

func managerWithFake(t *testing.T, config Config, result initializeResult) (*Manager, *fakeClient) {
	t.Helper()
	fake := newFakeClient(result)
	manager := NewManager(config, t.TempDir(), func(lspclient.ProcessConfig) LSPClient { return fake })
	manager.versionProber = func(context.Context, string) (string, error) { return "v0.22.0", nil }
	return manager, fake
}

func TestStartInitializesAndNegotiatesEncoding(t *testing.T) {
	t.Parallel()
	manager, _ := managerWithFake(t, Config{Enable: EnableTrue}, capable())
	if err := manager.Start(context.Background(), true); err != nil {
		t.Fatalf("Start: %v", err)
	}
	if decision, _ := manager.Decision(); decision != DecisionEnabled {
		t.Fatalf("decision = %v, want enabled", decision)
	}
	if manager.Version() != "v0.22.0" {
		t.Fatalf("version = %q", manager.Version())
	}
	if manager.Encoding() != "utf-8" {
		t.Fatalf("encoding = %q, want negotiated utf-8", manager.Encoding())
	}
	caps := manager.Capabilities()
	if !caps.Hover || !caps.Definition || !caps.CallHierarchy || !caps.SemanticTokensFull || caps.PositionEncoding != "utf-8" {
		t.Fatalf("capabilities not reported: %+v", caps)
	}
	if manager.SessionID() == "" {
		t.Fatal("enabled manager must expose a provider session")
	}
}

func TestInitializeEnablesFullSemanticTokensWithoutMutatingCapabilities(t *testing.T) {
	t.Parallel()
	manager, fake := managerWithFake(t, Config{Enable: EnableTrue}, capable())
	if err := manager.Start(context.Background(), true); err != nil {
		t.Fatal(err)
	}
	var params initializeParams
	if err := json.Unmarshal(fake.requested("initialize"), &params); err != nil {
		t.Fatal(err)
	}
	semanticTokens := params.Capabilities.TextDocument.SemanticTokens
	if !params.InitializationOptions.SemanticTokens || !semanticTokens.Requests.Full || semanticTokens.Requests.Range {
		t.Fatalf("semantic token negotiation = %+v / %+v", params.InitializationOptions, semanticTokens.Requests)
	}
	if len(semanticTokens.TokenTypes) != len(semantic.CanonicalSemanticTokenTypes) || len(semanticTokens.TokenModifiers) != len(semantic.CanonicalSemanticTokenModifiers) {
		t.Fatalf("canonical legend not advertised: %+v", semanticTokens)
	}
	data := string(fake.requested("initialize"))
	for _, forbidden := range []string{"workspaceEdit", "codeAction", "rename", "documentFormatting"} {
		if strings.Contains(data, forbidden) {
			t.Fatalf("initialize announced forbidden mutating capability %q: %s", forbidden, data)
		}
	}
}

func TestStartUnavailableWhenRequiredCapabilityMissing(t *testing.T) {
	t.Parallel()
	result := capable()
	result.Capabilities.HoverProvider = lspfacts.NewProviderOption(false)
	manager, _ := managerWithFake(t, Config{Enable: EnableTrue}, result)
	if err := manager.Start(context.Background(), true); err == nil {
		t.Fatal("Start should fail when hover capability is missing")
	}
	if decision, reason := manager.Decision(); decision != DecisionUnavailable || reason != CodeCapabilityMissing {
		t.Fatalf("decision/reason = %v/%v, want unavailable/%s", decision, reason, CodeCapabilityMissing)
	}
}

func TestEnablePolicies(t *testing.T) {
	t.Parallel()
	// false: disabled, no process.
	disabled, fakeDisabled := managerWithFake(t, Config{Enable: EnableFalse}, capable())
	_ = disabled.Start(context.Background(), true)
	if decision, _ := disabled.Decision(); decision != DecisionDisabled {
		t.Fatalf("false → %v, want disabled", decision)
	}
	if fakeDisabled.notifyCount("initialized") != 0 {
		t.Fatal("disabled mode must not touch a process")
	}
	// auto without Go: disabled.
	autoNoGo, _ := managerWithFake(t, Config{Enable: EnableAuto}, capable())
	_ = autoNoGo.Start(context.Background(), false)
	if decision, _ := autoNoGo.Decision(); decision != DecisionDisabled {
		t.Fatalf("auto/no-go → %v, want disabled", decision)
	}
	// auto with Go: enabled.
	autoGo, _ := managerWithFake(t, Config{Enable: EnableAuto}, capable())
	if err := autoGo.Start(context.Background(), true); err != nil {
		t.Fatal(err)
	}
	if decision, _ := autoGo.Decision(); decision != DecisionEnabled {
		t.Fatalf("auto/go → %v, want enabled", decision)
	}
}

func TestDocumentSyncTracksVersions(t *testing.T) {
	t.Parallel()
	manager, fake := managerWithFake(t, Config{Enable: EnableTrue}, capable())
	if err := manager.Start(context.Background(), true); err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	doc := Document{DocumentID: "doc1", Path: "pkg/a.go", LanguageID: "go", Version: 1, Content: []byte("package pkg\n")}
	if err := manager.Open(ctx, doc); err != nil {
		t.Fatalf("Open: %v", err)
	}
	if v, ok := manager.SyncedVersion("doc1"); !ok || v != 1 {
		t.Fatalf("synced after open = %d/%v", v, ok)
	}
	doc.Version = 2
	doc.Content = []byte("package pkg\nfunc A(){}\n")
	if err := manager.Change(ctx, doc); err != nil {
		t.Fatalf("Change: %v", err)
	}
	if v, _ := manager.SyncedVersion("doc1"); v != 2 {
		t.Fatalf("synced after change = %d", v)
	}
	// A non-advancing version is rejected.
	if err := manager.Change(ctx, doc); !errors.Is(err, ErrStaleVersion) {
		t.Fatalf("stale change = %v, want ErrStaleVersion", err)
	}
	if err := manager.Save(ctx, doc); err != nil {
		t.Fatalf("Save: %v", err)
	}
	if err := manager.Close(ctx, doc); err != nil {
		t.Fatalf("Close: %v", err)
	}
	if _, ok := manager.SyncedVersion("doc1"); ok {
		t.Fatal("synced version should be gone after close")
	}
	for _, method := range []string{"textDocument/didOpen", "textDocument/didChange", "textDocument/didSave", "textDocument/didClose"} {
		if fake.notifyCount(method) == 0 {
			t.Fatalf("expected a %s notification", method)
		}
	}
}

func TestCrashFlipsToUnavailable(t *testing.T) {
	t.Parallel()
	manager, fake := managerWithFake(t, Config{Enable: EnableTrue}, capable())
	if err := manager.Start(context.Background(), true); err != nil {
		t.Fatal(err)
	}
	manager.Open(context.Background(), Document{DocumentID: "doc1", Path: "a.go", Version: 1, Content: []byte("package a\n")})
	fake.fire("textDocument/publishDiagnostics", json.RawMessage(`{"uri":"`+manager.documentURI("a.go")+`","version":1,"diagnostics":[{"range":{"start":{"line":0,"character":0},"end":{"line":0,"character":1}},"message":"old session"}]}`))
	fake.crash()
	if err := manager.Change(context.Background(), Document{DocumentID: "doc1", Path: "a.go", Version: 2, Content: []byte("x")}); !errors.Is(err, ErrUnavailable) {
		t.Fatalf("change after crash = %v, want ErrUnavailable", err)
	}
	if decision, reason := manager.Decision(); decision != DecisionUnavailable || reason != CodeCrashed {
		t.Fatalf("decision/reason = %v/%v, want unavailable/%s", decision, reason, CodeCrashed)
	}
	if _, ok := manager.diagnostics.Get(manager.documentURI("a.go")); ok {
		t.Fatal("diagnostics from a crashed provider session must be discarded")
	}
}

func TestApplyEditIsDenied(t *testing.T) {
	t.Parallel()
	manager, fake := managerWithFake(t, Config{Enable: EnableTrue}, capable())
	if err := manager.Start(context.Background(), true); err != nil {
		t.Fatal(err)
	}
	handler := fake.requestHandlers["workspace/applyEdit"]
	if handler == nil {
		t.Fatal("workspace/applyEdit handler not registered")
	}
	result, _ := handler(context.Background(), json.RawMessage(`{}`))
	response, ok := result.(map[string]any)
	if !ok || response["applied"] != false {
		t.Fatalf("applyEdit must be denied, got %#v", result)
	}
}

// TestRealGopls is an opt-in integration test against the real binary, skipped
// when gopls is not installed so a developer without it is never blocked.
func TestRealGopls(t *testing.T) {
	if os.Getenv("CODEATLAS_TEST_REAL_LSP") != "1" {
		t.Skip("set CODEATLAS_TEST_REAL_LSP=1 to run real language-server integration")
	}
	path, err := exec.LookPath("gopls")
	if err != nil {
		t.Skip("gopls not installed; skipping integration test")
	}
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "go.mod"), []byte("module example.com/codeatlas-gopls-test\n\ngo 1.23\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	content := []byte("package main\n\nfunc main() { value := 1; _ = value }\n")
	if err := os.WriteFile(filepath.Join(root, "main.go"), content, 0o600); err != nil {
		t.Fatal(err)
	}
	manager := NewManager(Config{Enable: EnableTrue, Path: path}, root, nil)
	if err := manager.Start(context.Background(), true); err != nil {
		t.Fatalf("real gopls Start: %v", err)
	}
	defer manager.Shutdown(context.Background())
	if decision, reason := manager.Decision(); decision != DecisionEnabled {
		t.Fatalf("real gopls decision = %v (%s)", decision, reason)
	}
	caps := manager.Capabilities()
	if !caps.Hover || !caps.Definition || !caps.SemanticTokensFull {
		t.Fatalf("real gopls did not advertise required read-only semantics: %+v", caps)
	}
	provider := NewProvider(manager, root, func(_ semantic.SemanticQuery, relative string) ([]byte, error) {
		return os.ReadFile(filepath.Join(root, relative))
	})
	query := semantic.SemanticQuery{
		Path: "main.go", DocumentID: "real-gopls-doc", DocumentVersion: 1,
		Content: content, Position: domain.Position{Line: 3, Column: 6},
	}
	if err := provider.OpenDocument(context.Background(), query.DocumentID, query.DocumentVersion, query.Path, "go", content); err != nil {
		t.Fatalf("real gopls didOpen: %v", err)
	}
	tokens, err := provider.SemanticTokens(context.Background(), query)
	if err != nil {
		t.Fatalf("real gopls semantic tokens: %v", err)
	}
	if len(tokens.Tokens) == 0 || tokens.ProviderSession == "" || tokens.ContentHash == "" {
		t.Fatalf("real gopls returned unbound/empty semantic tokens: %+v", tokens)
	}
	t.Logf("real gopls %s, encoding=%s", manager.Version(), manager.Encoding())
}
