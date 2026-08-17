package swiftlsp

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"os/exec"
	"sync"
	"testing"

	"github.com/ThalesMMS/CodeAtlas/internal/lspclient"
	"github.com/ThalesMMS/CodeAtlas/internal/lspfacts"
)

type fakeClient struct {
	mu              sync.Mutex
	state           lspclient.ClientState
	initResult      initializeResult
	notifications   map[string]json.RawMessage
	requestHandlers map[string]lspclient.ServerRequestHandler
	responses       map[string]any // canned results by method
	onRequest       func(string)
}

func newFake(result initializeResult) *fakeClient {
	return &fakeClient{initResult: result, notifications: map[string]json.RawMessage{}, requestHandlers: map[string]lspclient.ServerRequestHandler{}, responses: map[string]any{}}
}
func (f *fakeClient) Start(context.Context) error {
	f.mu.Lock()
	f.state = lspclient.StateRunning
	f.mu.Unlock()
	return nil
}
func (f *fakeClient) Request(_ context.Context, method string, _ any, result any) error {
	if f.onRequest != nil {
		f.onRequest(method)
	}
	if method == "initialize" && result != nil {
		data, _ := json.Marshal(f.initResult)
		return json.Unmarshal(data, result)
	}
	f.mu.Lock()
	canned, ok := f.responses[method]
	f.mu.Unlock()
	if ok && result != nil {
		data, _ := json.Marshal(canned)
		return json.Unmarshal(data, result)
	}
	return nil
}
func (f *fakeClient) Notify(_ context.Context, method string, params any) error {
	data, _ := json.Marshal(params)
	f.mu.Lock()
	f.notifications[method] = data
	f.mu.Unlock()
	return nil
}
func (f *fakeClient) OnNotification(string, lspclient.NotificationHandler) {}
func (f *fakeClient) OnRequest(method string, handler lspclient.ServerRequestHandler) {
	f.mu.Lock()
	f.requestHandlers[method] = handler
	f.mu.Unlock()
}
func (f *fakeClient) State() lspclient.ClientState { f.mu.Lock(); defer f.mu.Unlock(); return f.state }
func (f *fakeClient) Close(context.Context) error {
	f.mu.Lock()
	f.state = lspclient.StateClosed
	f.mu.Unlock()
	return nil
}
func (f *fakeClient) notified(method string) (json.RawMessage, bool) {
	f.mu.Lock()
	defer f.mu.Unlock()
	v, ok := f.notifications[method]
	return v, ok
}

func capable() initializeResult {
	present := lspfacts.NewProviderOption(true)
	semanticTokens := lspfacts.NewSemanticTokensProviderOption(lspfacts.SemanticTokensLegend{
		TokenTypes:     []string{"class", "enum", "interface", "namespace", "typeParameter", "type", "parameter", "variable", "enumMember", "property", "function", "member"},
		TokenModifiers: []string{"declaration", "static", "async", "readonly", "defaultLibrary", "local"},
	}, true)
	return initializeResult{ServerInfo: lspfacts.ServerInfo{Name: "SourceKit-LSP", Version: "6.3.3"}, Capabilities: serverCapabilities{
		PositionEncoding: "utf-16", HoverProvider: present, DefinitionProvider: present,
		ReferencesProvider: present, ImplementationProvider: present, CallHierarchyProvider: present,
		SemanticTokensProvider: semanticTokens,
	}}
}

func managerWithFake(t *testing.T, config Config) (*Manager, *fakeClient) {
	t.Helper()
	fake := newFake(capable())
	manager := NewManager(config, t.TempDir(), func(lspclient.ProcessConfig) LSPClient { return fake })
	manager.versionProber = func(context.Context, string) (string, error) { return "", nil }
	t.Cleanup(func() { _ = manager.Shutdown(context.Background()) })
	return manager, fake
}

func TestLanguageIDMapping(t *testing.T) {
	t.Parallel()
	cases := map[string]string{
		"a.swift": "swift", "A.SWIFT": "swift", "README.md": "plaintext",
	}
	for path, want := range cases {
		if got := LanguageID(path); got != want {
			t.Errorf("LanguageID(%s) = %q, want %q", path, got, want)
		}
	}
}

func TestStartAndCapabilities(t *testing.T) {
	t.Parallel()
	manager, _ := managerWithFake(t, Config{Enable: EnableTrue})
	if err := manager.Start(context.Background(), true); err != nil {
		t.Fatalf("Start: %v", err)
	}
	if decision, _ := manager.Decision(); decision != DecisionEnabled {
		t.Fatalf("decision = %v", decision)
	}
	if manager.Encoding() != "utf-16" || manager.Version() != "6.3.3" {
		t.Fatalf("encoding/version = %q/%q", manager.Encoding(), manager.Version())
	}
	caps := manager.Capabilities()
	if !caps.Hover || !caps.Definition || !caps.SemanticTokensFull {
		t.Fatalf("capabilities: %+v", caps)
	}
}

func TestStartClassifiesExecutableNotFoundByErrorIdentity(t *testing.T) {
	t.Parallel()
	manager, _ := managerWithFake(t, Config{Enable: EnableTrue})
	manager.versionProber = func(context.Context, string) (string, error) { return "", exec.ErrNotFound }
	err := manager.Start(context.Background(), true)
	if !errors.Is(err, exec.ErrNotFound) {
		t.Fatalf("Start() error = %v, want wrapped exec.ErrNotFound", err)
	}
	if decision, reason := manager.Decision(); decision != DecisionUnavailable || reason != CodeNotFound {
		t.Fatalf("decision = %v (%s), want unavailable/%s", decision, reason, CodeNotFound)
	}
}

func TestStartDoesNotClassifyMessageOnlyAsExecutableNotFound(t *testing.T) {
	t.Parallel()
	manager, _ := managerWithFake(t, Config{Enable: EnableTrue})
	manager.versionProber = func(context.Context, string) (string, error) {
		return "", errors.New("executable file not found")
	}
	if err := manager.Start(context.Background(), true); err == nil {
		t.Fatal("Start() succeeded after version probe failure")
	}
	if decision, reason := manager.Decision(); decision != DecisionUnavailable || reason != CodeVersionProbeFailed {
		t.Fatalf("decision = %v (%s), want unavailable/%s", decision, reason, CodeVersionProbeFailed)
	}
}

func TestEnablePolicies(t *testing.T) {
	t.Parallel()
	disabled, _ := managerWithFake(t, Config{Enable: EnableFalse})
	_ = disabled.Start(context.Background(), true)
	if d, _ := disabled.Decision(); d != DecisionDisabled {
		t.Fatalf("false → %v", d)
	}
	autoNo, _ := managerWithFake(t, Config{Enable: EnableAuto})
	_ = autoNo.Start(context.Background(), false)
	if d, _ := autoNo.Decision(); d != DecisionDisabled {
		t.Fatalf("auto/no-js → %v", d)
	}
}

func TestDocumentSyncWithLanguageID(t *testing.T) {
	t.Parallel()
	manager, fake := managerWithFake(t, Config{Enable: EnableTrue})
	if err := manager.Start(context.Background(), true); err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	doc := Document{DocumentID: "doc1", Path: "Sources/Checkout.swift", Version: 1, Content: []byte("let value = 1\n")}
	if err := manager.Open(ctx, doc); err != nil {
		t.Fatalf("Open: %v", err)
	}
	open, _ := fake.notified("textDocument/didOpen")
	if want := `"languageId":"swift"`; !contains(string(open), want) {
		t.Fatalf("didOpen missing %s: %s", want, open)
	}
	if v, _ := manager.SyncedVersion("doc1"); v != 1 {
		t.Fatalf("synced = %d", v)
	}
	doc.Version = 2
	if err := manager.Change(ctx, doc); err != nil {
		t.Fatalf("Change: %v", err)
	}
	if err := manager.Change(ctx, doc); !errors.Is(err, ErrStaleVersion) {
		t.Fatalf("stale change = %v", err)
	}
	if err := manager.Close(ctx, doc); err != nil {
		t.Fatalf("Close: %v", err)
	}
	if _, ok := manager.SyncedVersion("doc1"); ok {
		t.Fatal("synced should be gone after close")
	}
}

func TestDocumentSyncLockWaitRespectsCancellation(t *testing.T) {
	t.Parallel()
	manager, _ := managerWithFake(t, Config{Enable: EnableTrue})
	if err := manager.Start(context.Background(), true); err != nil {
		t.Fatal(err)
	}
	unlock, err := manager.documents.Lock(context.Background(), "doc1")
	if err != nil {
		t.Fatal(err)
	}
	defer unlock()
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	err = manager.Change(ctx, Document{DocumentID: "doc1", Path: "a.swift", Version: 2})
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("Change() error = %v, want context.Canceled", err)
	}
}

func TestDynamicRegistrationAllowlist(t *testing.T) {
	t.Parallel()
	manager, fake := managerWithFake(t, Config{Enable: EnableTrue})
	if err := manager.Start(context.Background(), true); err != nil {
		t.Fatal(err)
	}
	handler := fake.requestHandlers["client/registerCapability"]
	if handler == nil {
		t.Fatal("registerCapability handler not registered")
	}
	_, _ = handler(context.Background(), json.RawMessage(`{"registrations":[{"id":"r1","method":"textDocument/hover"},{"id":"r2","method":"workspace/executeCommand"}]}`))
	manager.mu.Lock()
	_, hoverOK := manager.registered["r1"]
	_, execOK := manager.registered["r2"]
	manager.mu.Unlock()
	if !hoverOK {
		t.Fatal("allowlisted hover registration should be accepted")
	}
	if execOK {
		t.Fatal("non-allowlisted executeCommand registration must be ignored")
	}
}

func TestRealSwiftLSP(t *testing.T) {
	if os.Getenv("CODEATLAS_TEST_REAL_LSP") != "1" {
		t.Skip("set CODEATLAS_TEST_REAL_LSP=1 to run real language-server integration")
	}
	path, err := exec.LookPath("sourcekit-lsp")
	if err != nil {
		t.Skip("sourcekit-lsp not installed; skipping integration test")
	}
	manager := NewManager(Config{Enable: EnableTrue, Path: path}, t.TempDir(), nil)
	if err := manager.Start(context.Background(), true); err != nil {
		t.Fatalf("real SourceKit-LSP Start: %v", err)
	}
	defer manager.Shutdown(context.Background())
	if decision, reason := manager.Decision(); decision != DecisionEnabled {
		t.Fatalf("real SourceKit-LSP decision = %v (%s)", decision, reason)
	}
	caps := manager.Capabilities()
	if !caps.Hover || !caps.Definition {
		t.Fatalf("real SourceKit-LSP capabilities: %+v", caps)
	}
	t.Logf("real sourcekit-lsp %s, encoding=%s", manager.Version(), manager.Encoding())
}

func contains(haystack, needle string) bool {
	return len(haystack) >= len(needle) && (haystack == needle || indexOf(haystack, needle) >= 0)
}
func indexOf(s, sub string) int {
	for i := 0; i+len(sub) <= len(s); i++ {
		if s[i:i+len(sub)] == sub {
			return i
		}
	}
	return -1
}
