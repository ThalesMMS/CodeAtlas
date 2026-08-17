package semantic

import (
	"context"
	"errors"
	"strings"
	"testing"
)

type routedProvider struct {
	id    SemanticProviderID
	caps  SemanticCapabilities
	err   error
	calls []string
	sync  []string
}

func (p *routedProvider) ID() SemanticProviderID { return p.id }
func (p *routedProvider) Capabilities(context.Context) (SemanticCapabilities, error) {
	return p.caps, p.err
}
func (p *routedProvider) result(method string) ([]SemanticFact, error) {
	p.calls = append(p.calls, method)
	return []SemanticFact{{Detail: string(p.id)}}, nil
}
func (p *routedProvider) Hover(context.Context, SemanticQuery) ([]SemanticFact, error) {
	return p.result("hover")
}
func (p *routedProvider) Definitions(context.Context, SemanticQuery) ([]SemanticFact, error) {
	return p.result("definitions")
}
func (p *routedProvider) References(context.Context, SemanticQuery) ([]SemanticFact, error) {
	return p.result("references")
}
func (p *routedProvider) Implementations(context.Context, SemanticQuery) ([]SemanticFact, error) {
	return p.result("implementations")
}
func (p *routedProvider) IncomingCalls(context.Context, SemanticQuery) ([]SemanticFact, error) {
	return p.result("incoming")
}
func (p *routedProvider) OutgoingCalls(context.Context, SemanticQuery) ([]SemanticFact, error) {
	return p.result("outgoing")
}
func (p *routedProvider) Diagnostics(context.Context, SemanticQuery) ([]SemanticFact, error) {
	return p.result("diagnostics")
}
func (p *routedProvider) OpenDocument(_ context.Context, documentID DocumentID, version DocumentVersion, path, language string, _ []byte) error {
	p.sync = append(p.sync, "open:"+string(documentID)+":"+path+":"+language)
	return nil
}
func (p *routedProvider) ChangeDocument(_ context.Context, documentID DocumentID, _ DocumentVersion, path string, _ []byte) error {
	p.sync = append(p.sync, "change:"+string(documentID)+":"+path)
	return nil
}
func (p *routedProvider) CloseDocument(_ context.Context, documentID DocumentID, path string) error {
	p.sync = append(p.sync, "close:"+string(documentID)+":"+path)
	return nil
}

func TestPathRouterDispatchesByExtension(t *testing.T) {
	goProvider := &routedProvider{id: "go"}
	tsProvider := &routedProvider{id: "typescript"}
	router := NewPathRouter(map[string]SemanticProvider{
		".go": goProvider, "ts": tsProvider, ".tsx": tsProvider,
	})

	facts, err := router.Hover(context.Background(), SemanticQuery{Path: "internal/order.go"})
	if err != nil || len(facts) != 1 || facts[0].Detail != "go" {
		t.Fatalf("Go hover = %+v, %v", facts, err)
	}
	facts, err = router.Definitions(context.Background(), SemanticQuery{Path: "web/Order.TSX"})
	if err != nil || len(facts) != 1 || facts[0].Detail != "typescript" {
		t.Fatalf("TSX definition = %+v, %v", facts, err)
	}
	if len(goProvider.calls) != 1 || len(tsProvider.calls) != 1 {
		t.Fatalf("provider calls = go %v, ts %v", goProvider.calls, tsProvider.calls)
	}
}

func TestPathRouterRejectsUnregisteredExtension(t *testing.T) {
	router := NewPathRouter(nil)
	_, err := router.Hover(context.Background(), SemanticQuery{Path: "README.md"})
	if !errors.Is(err, ErrCapabilityUnsupported) {
		t.Fatalf("Hover error = %v", err)
	}
}

func TestPathRouterUnionsCapabilities(t *testing.T) {
	router := NewPathRouter(map[string]SemanticProvider{
		".go": &routedProvider{id: "go", caps: SemanticCapabilities{Hover: true, PositionEncoding: "utf-8", SemanticTokenTypes: []string{"function"}}},
		".ts": &routedProvider{id: "typescript", caps: SemanticCapabilities{Definition: true, PositionEncoding: "utf-16", SemanticTokenTypes: []string{"class", "function"}}},
	})
	caps, err := router.Capabilities(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if !caps.Hover || !caps.Definition || caps.PositionEncoding != "routed" {
		t.Fatalf("capabilities = %+v", caps)
	}
	if len(caps.SemanticTokenTypes) != 2 || caps.SemanticTokenTypes[0] != "class" || caps.SemanticTokenTypes[1] != "function" {
		t.Fatalf("token types = %v", caps.SemanticTokenTypes)
	}
}

func TestPathRouterCapabilitiesForPathDoesNotMaskSelectedProviderFailure(t *testing.T) {
	t.Parallel()
	goProvider := &routedProvider{id: "go", caps: SemanticCapabilities{Hover: true}}
	tsProvider := &routedProvider{id: "ts", err: errors.New("typescript unavailable")}
	router := NewPathRouter(map[string]SemanticProvider{".go": goProvider, ".ts": tsProvider})

	if _, err := router.CapabilitiesForPath(context.Background(), "web/app.ts"); err == nil || !strings.Contains(err.Error(), "typescript unavailable") {
		t.Fatalf("CapabilitiesForPath(.ts) error = %v", err)
	}
	if caps, err := router.CapabilitiesForPath(context.Background(), "main.go"); err != nil || !caps.Hover {
		t.Fatalf("CapabilitiesForPath(.go) = %+v, %v", caps, err)
	}
}

func TestPathRouterIdentifiesProviderAndRoutesDocumentSync(t *testing.T) {
	t.Parallel()
	goProvider := &routedProvider{id: "gopls"}
	tsProvider := &routedProvider{id: "typescript-lsp"}
	router := NewPathRouter(map[string]SemanticProvider{".go": goProvider, ".ts": tsProvider})

	if id, ok := router.ProviderIDForPath("internal/order.go"); !ok || id != "gopls" {
		t.Fatalf("ProviderIDForPath(.go) = %q, %v", id, ok)
	}
	if _, ok := router.ProviderIDForPath("README.md"); ok {
		t.Fatal("ProviderIDForPath accepted an unrouted extension")
	}
	ctx := context.Background()
	if err := router.OpenDocument(ctx, "doc-1", 1, "web/app.ts", "typescript", []byte("let x = 1")); err != nil {
		t.Fatal(err)
	}
	if err := router.ChangeDocument(ctx, "doc-1", 2, "web/app.ts", []byte("let x = 2")); err != nil {
		t.Fatal(err)
	}
	if err := router.CloseDocument(ctx, "doc-1", "web/app.ts"); err != nil {
		t.Fatal(err)
	}
	if len(goProvider.sync) != 0 || len(tsProvider.sync) != 3 {
		t.Fatalf("sync calls = go %v, ts %v", goProvider.sync, tsProvider.sync)
	}
	if err := router.OpenDocument(ctx, "doc-2", 1, "README.md", "markdown", nil); !errors.Is(err, ErrCapabilityUnsupported) {
		t.Fatalf("unrouted sync error = %v", err)
	}
}

func TestPathRouterRequiresReportedSessionStateForVersionBoundEditorFacts(t *testing.T) {
	t.Parallel()
	router := NewPathRouter(map[string]SemanticProvider{".go": &routedProvider{id: "gopls"}})
	state := router.ProviderStateForPath("main.go")
	if state.State != ProviderStateDisabled || state.Reason != "provider_state_not_reported" {
		t.Fatalf("unreported provider state = %+v, want conservative disabled state", state)
	}
}
