package gopls

import (
	"context"
	"encoding/json"
	"errors"
	"path/filepath"
	"strings"
	"testing"
	"unicode/utf16"

	"github.com/ThalesMMS/CodeAtlas/internal/domain"
	"github.com/ThalesMMS/CodeAtlas/internal/lspadapter"
	"github.com/ThalesMMS/CodeAtlas/internal/lspconv"
	"github.com/ThalesMMS/CodeAtlas/internal/lspfacts"
	"github.com/ThalesMMS/CodeAtlas/internal/semantic"
)

func TestProviderCapabilitiesReportUnavailableManager(t *testing.T) {
	t.Parallel()
	manager, _ := managerWithFake(t, Config{Enable: EnableTrue}, capable())
	manager.setDecision(DecisionUnavailable, CodeCrashed)
	provider := NewProvider(manager, t.TempDir(), func(semantic.SemanticQuery, string) ([]byte, error) { return nil, nil })
	if _, err := provider.Capabilities(context.Background()); !errors.Is(err, ErrUnavailable) {
		t.Fatalf("Capabilities() error = %v, want ErrUnavailable", err)
	}
}

func providerWithFake(t *testing.T, files map[string][]byte) (*Provider, *fakeClient, string) {
	t.Helper()
	root := t.TempDir()
	manager, fake := managerWithFake(t, Config{Enable: EnableTrue}, capable())
	manager.workspaceRoot = root
	if err := manager.Start(context.Background(), true); err != nil {
		t.Fatal(err)
	}
	source := func(_ semantic.SemanticQuery, relative string) ([]byte, error) {
		if content, ok := files[relative]; ok {
			return content, nil
		}
		return nil, errors.New("no source for " + relative)
	}
	return NewProvider(manager, root, source), fake, root
}

// lspRangeJSON builds a canned LSP location result for a workspace file URI.
func lspLocationJSON(root, relative string, sl, sc, el, ec int) json.RawMessage {
	uri := lspconv.PathToURI(filepath.Join(root, relative))
	loc := lspLocation{URI: uri, Range: lspRange{Start: lspPosition{Line: sl, Character: sc}, End: lspPosition{Line: el, Character: ec}}}
	data, _ := json.Marshal([]lspLocation{loc})
	return data
}

func TestHoverProducesGroundedFact(t *testing.T) {
	t.Parallel()
	files := map[string][]byte{"pkg/a.go": []byte("package pkg\nfunc Alpha() {}\n")}
	provider, fake, _ := providerWithFake(t, files)
	fake.respond("textDocument/hover", json.RawMessage(`{"contents":{"kind":"markdown","value":"func Alpha()"}}`))

	query := semantic.SemanticQuery{SnapshotID: "s1", Path: "pkg/a.go", Position: domain.Position{Line: 2, Column: 6}}
	facts, err := provider.Hover(context.Background(), query)
	if err != nil {
		t.Fatalf("Hover: %v", err)
	}
	if len(facts) != 1 {
		t.Fatalf("expected one hover fact, got %d", len(facts))
	}
	fact := facts[0]
	if fact.Kind != semantic.KindHoverType || fact.Provenance.Source != semantic.SourceGopls {
		t.Fatalf("bad hover fact: %+v", fact)
	}
	if fact.Confidence != semantic.ConfidenceLanguageServerResolved {
		t.Fatalf("hover confidence = %v", fact.Confidence)
	}
	if err := semantic.ValidateFact(fact); err != nil {
		t.Fatalf("hover fact invalid: %v", err)
	}
	if fact.Detail != "func Alpha()" {
		t.Fatalf("hover detail = %q", fact.Detail)
	}
}

func TestHoverNullIsZeroFactsNotError(t *testing.T) {
	t.Parallel()
	provider, fake, _ := providerWithFake(t, map[string][]byte{"a.go": []byte("package a\n")})
	fake.respond("textDocument/hover", json.RawMessage(`null`))
	facts, err := provider.Hover(context.Background(), semantic.SemanticQuery{Path: "a.go", Position: domain.Position{Line: 1, Column: 1}})
	if err != nil || len(facts) != 0 {
		t.Fatalf("null hover should be zero facts: %d, %v", len(facts), err)
	}
}

func TestDefinitionMapsWorkspaceLocation(t *testing.T) {
	t.Parallel()
	files := map[string][]byte{
		"pkg/a.go": []byte("package pkg\nfunc Caller() { Target() }\n"),
		"pkg/b.go": []byte("package pkg\nfunc Target() {}\n"),
	}
	provider, fake, root := providerWithFake(t, files)
	// gopls points the definition at pkg/b.go line 1 (0-based), chars 5..11 ("Target").
	fake.respond("textDocument/definition", lspLocationJSON(root, "pkg/b.go", 1, 5, 1, 11))

	facts, err := provider.Definitions(context.Background(), semantic.SemanticQuery{SnapshotID: "s1", Path: "pkg/a.go", Position: domain.Position{Line: 2, Column: 16}})
	if err != nil {
		t.Fatalf("Definitions: %v", err)
	}
	if len(facts) != 1 {
		t.Fatalf("expected one definition fact, got %d", len(facts))
	}
	loc := facts[0].Location
	if loc.Path != "pkg/b.go" {
		t.Fatalf("definition path = %q, want pkg/b.go", loc.Path)
	}
	// Range converted to internal (1-based line, 1-based byte column): line 2, col 6.
	if loc.Range.Start.Line != 2 || loc.Range.Start.Column != 6 {
		t.Fatalf("definition range = %+v, want start (2,6)", loc.Range)
	}
	if loc.Encoding != semantic.EncodingUTF8 {
		t.Fatalf("converted range should be utf-8 internal, got %q", loc.Encoding)
	}
}

func TestReferencesExternalLocationIsBoundary(t *testing.T) {
	t.Parallel()
	files := map[string][]byte{"pkg/a.go": []byte("package pkg\nfunc A() {}\n")}
	provider, fake, _ := providerWithFake(t, files)
	external := lspconv.PathToURI("/usr/local/go/src/fmt/print.go")
	data, _ := json.Marshal([]lspLocation{{URI: external, Range: lspRange{Start: lspPosition{Line: 10}, End: lspPosition{Line: 10, Character: 5}}}})
	fake.respond("textDocument/references", data)

	facts, err := provider.References(context.Background(), semantic.SemanticQuery{Path: "pkg/a.go", Position: domain.Position{Line: 2, Column: 6}})
	if err != nil {
		t.Fatalf("References: %v", err)
	}
	if len(facts) != 1 {
		t.Fatalf("expected one boundary fact, got %d", len(facts))
	}
	if !isExternalPath(facts[0].Location.Path) {
		t.Fatalf("external reference should stay an external boundary: %q", facts[0].Location.Path)
	}
}

func TestReferencesSendsIncludeDeclarationOnlyInsideContext(t *testing.T) {
	t.Parallel()
	provider, fake, _ := providerWithFake(t, map[string][]byte{"a.go": []byte("package a\n")})
	if _, err := provider.References(context.Background(), semantic.SemanticQuery{Path: "a.go", Position: domain.Position{Line: 1, Column: 1}}); err != nil {
		t.Fatal(err)
	}
	var params map[string]json.RawMessage
	if err := json.Unmarshal(fake.requested("textDocument/references"), &params); err != nil {
		t.Fatal(err)
	}
	if _, exists := params["includeDeclaration"]; exists {
		t.Fatalf("references has invalid top-level includeDeclaration: %s", fake.requested("textDocument/references"))
	}
	var referenceContext struct {
		IncludeDeclaration bool `json:"includeDeclaration"`
	}
	if err := json.Unmarshal(params["context"], &referenceContext); err != nil || referenceContext.IncludeDeclaration {
		t.Fatalf("references context = %s, %v", params["context"], err)
	}
}

func TestImplementationCapabilityGated(t *testing.T) {
	t.Parallel()
	result := capable()
	result.Capabilities.ImplementationProvider = lspfacts.NewProviderOption(false)
	root := t.TempDir()
	// A manager whose gopls did not advertise implementation capability.
	manager, _ := managerWithFake(t, Config{Enable: EnableTrue}, result)
	manager.workspaceRoot = root
	_ = manager.Start(context.Background(), true)
	provider := NewProvider(manager, root, func(semantic.SemanticQuery, string) ([]byte, error) { return []byte("package a\n"), nil })
	_, err := provider.Implementations(context.Background(), semantic.SemanticQuery{Path: "a.go", Position: domain.Position{Line: 1, Column: 1}})
	if !errors.Is(err, semantic.ErrCapabilityUnsupported) {
		t.Fatalf("implementation without capability = %v, want ErrCapabilityUnsupported", err)
	}
}

func TestOverlayQueryRequiresSyncedVersion(t *testing.T) {
	t.Parallel()
	files := map[string][]byte{"a.go": []byte("package a\n")}
	provider, _, _ := providerWithFake(t, files)
	// No document synced → an overlay query is stale.
	query := semantic.SemanticQuery{Path: "a.go", DocumentID: "doc1", DocumentVersion: 5, Position: domain.Position{Line: 1, Column: 1}}
	if _, err := provider.Hover(context.Background(), query); !errors.Is(err, ErrStaleVersion) {
		t.Fatalf("overlay query without synced version = %v, want ErrStaleVersion", err)
	}
}

func TestSemanticTokensAreCanonicalAndBoundToOverlaySession(t *testing.T) {
	t.Parallel()
	line := `var _ = "😀"; var Value = 1`
	content := "package main\r\n" + line + "\r\n"
	provider, fake, _ := providerWithFake(t, map[string][]byte{"main.go": []byte(content)})
	provider.manager.mu.Lock()
	provider.manager.encoding = "utf-16"
	provider.manager.mu.Unlock()
	valueByte := strings.Index(line, "Value")
	valueUTF16 := len(utf16.Encode([]rune(line[:valueByte])))
	data, _ := json.Marshal(lspfacts.SemanticTokens{
		ResultID: "go-result-1",
		Data:     []uint32{1, uint32(valueUTF16), 5, 5, 1<<0 | 1<<1},
	})
	fake.respond("textDocument/semanticTokens/full", data)

	query := semantic.SemanticQuery{
		SnapshotID: "s1", Path: "main.go", DocumentID: "doc-go", DocumentVersion: 1,
		Content: []byte(content), Position: domain.Position{Line: 2, Column: valueByte + 1},
	}
	if err := provider.OpenDocument(context.Background(), query.DocumentID, query.DocumentVersion, query.Path, "go", query.Content); err != nil {
		t.Fatal(err)
	}
	set, err := provider.SemanticTokens(context.Background(), query)
	if err != nil {
		t.Fatalf("SemanticTokens: %v", err)
	}
	if set.DocumentID != query.DocumentID || set.DocumentVersion != query.DocumentVersion || set.ContentHash == "" || set.ProviderSession == "" {
		t.Fatalf("token binding = %+v", set)
	}
	if len(set.Tokens) != 1 || set.Tokens[0].TokenType != "variable" || set.Tokens[0].Range.Start.Column != valueByte+1 {
		t.Fatalf("canonical non-BMP token = %+v", set.Tokens)
	}
	if got := strings.Join(set.Tokens[0].Modifiers, ","); got != "definition,readonly" {
		t.Fatalf("canonical modifiers = %q", got)
	}
	if set.Provenance.Source != semantic.SourceGopls || set.Provenance.Method != "textDocument/semanticTokens/full" {
		t.Fatalf("provenance = %+v", set.Provenance)
	}
}

func TestSemanticTokensRejectUnsupportedStaleMalformedAndRestartedResponses(t *testing.T) {
	t.Parallel()
	content := []byte("package main\nvar Value = 1\n")
	provider, fake, _ := providerWithFake(t, map[string][]byte{"main.go": content})
	query := semantic.SemanticQuery{Path: "main.go", DocumentID: "doc-go", DocumentVersion: 1, Content: content, Position: domain.Position{Line: 1, Column: 1}}
	if err := provider.OpenDocument(context.Background(), query.DocumentID, query.DocumentVersion, query.Path, "go", content); err != nil {
		t.Fatal(err)
	}

	stale := query
	stale.DocumentVersion = 2
	if _, err := provider.SemanticTokens(context.Background(), stale); !errors.Is(err, ErrStaleVersion) {
		t.Fatalf("stale tokens error = %v", err)
	}

	malformed, _ := json.Marshal(lspfacts.SemanticTokens{Data: []uint32{0, 1, 2}})
	fake.respond("textDocument/semanticTokens/full", malformed)
	if _, err := provider.SemanticTokens(context.Background(), query); !errors.Is(err, ErrMalformedSemanticTokens) {
		t.Fatalf("malformed tokens error = %v", err)
	}

	valid, _ := json.Marshal(lspfacts.SemanticTokens{Data: []uint32{1, 4, 5, 5, 0}})
	fake.respond("textDocument/semanticTokens/full", valid)
	fake.onRequest = func(method string) {
		if method == "textDocument/semanticTokens/full" {
			provider.manager.mu.Lock()
			provider.manager.sessionID = "gopls:restarted"
			provider.manager.mu.Unlock()
		}
	}
	if _, err := provider.SemanticTokens(context.Background(), query); !errors.Is(err, ErrProviderRestarted) {
		t.Fatalf("restarted tokens error = %v", err)
	}

	result := capable()
	result.Capabilities.SemanticTokensProvider = lspfacts.SemanticTokensProviderOption{}
	manager, _ := managerWithFake(t, Config{Enable: EnableTrue}, result)
	manager.workspaceRoot = t.TempDir()
	if err := manager.Start(context.Background(), true); err != nil {
		t.Fatal(err)
	}
	unsupported := NewProvider(manager, manager.workspaceRoot, func(semantic.SemanticQuery, string) ([]byte, error) { return content, nil })
	if _, err := unsupported.SemanticTokens(context.Background(), semantic.SemanticQuery{Path: "main.go", Position: domain.Position{Line: 1, Column: 1}}); !errors.Is(err, semantic.ErrCapabilityUnsupported) {
		t.Fatalf("unsupported tokens error = %v", err)
	}
}

func TestSemanticTokensBoundProviderPayload(t *testing.T) {
	content := []byte("package main\nvar " + strings.Repeat("x", lspadapter.MaxSemanticTokens+2) + "\n")
	provider, fake, _ := providerWithFake(t, map[string][]byte{"main.go": content})
	data := make([]uint32, 0, (lspadapter.MaxSemanticTokens+1)*5)
	for index := 0; index < lspadapter.MaxSemanticTokens+1; index++ {
		if index == 0 {
			data = append(data, 1, 4, 1, 5, 0)
		} else {
			data = append(data, 0, 1, 1, 5, 0)
		}
	}
	payload, _ := json.Marshal(lspfacts.SemanticTokens{Data: data})
	fake.respond("textDocument/semanticTokens/full", payload)
	query := semantic.SemanticQuery{
		Path: "main.go", DocumentID: "doc-large", DocumentVersion: 1,
		Content: content, Position: domain.Position{Line: 1, Column: 1},
	}
	if err := provider.OpenDocument(context.Background(), query.DocumentID, query.DocumentVersion, query.Path, "go", content); err != nil {
		t.Fatal(err)
	}
	set, err := provider.SemanticTokens(context.Background(), query)
	if err != nil {
		t.Fatalf("SemanticTokens: %v", err)
	}
	if len(set.Tokens) != lspadapter.MaxSemanticTokens || !set.Truncated || set.OmittedCount != 1 {
		t.Fatalf("tokens=%d truncated=%v omitted=%d", len(set.Tokens), set.Truncated, set.OmittedCount)
	}
}
