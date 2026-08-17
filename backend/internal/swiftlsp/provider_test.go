package swiftlsp

import (
	"bytes"
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

// providerWithFake builds an enabled manager + provider whose workspace contains a
// single TS file, plus a fake LSP client to script responses.
func providerWithFake(t *testing.T, file string, content string) (*Provider, *fakeClient) {
	t.Helper()
	root := t.TempDir()
	manager, fake := managerWithFake(t, Config{Enable: EnableTrue})
	manager.workspaceRoot = root
	if err := manager.Start(context.Background(), true); err != nil {
		t.Fatalf("Start: %v", err)
	}
	source := func(_ semantic.SemanticQuery, relative string) ([]byte, error) {
		if relative == file || filepath.Base(relative) == file {
			return []byte(content), nil
		}
		return []byte(content), nil
	}
	return NewProvider(manager, root, source), fake
}

func swiftQuery(file string) semantic.SemanticQuery {
	return semantic.SemanticQuery{Path: file, Position: domain.Position{Line: 1, Column: 1}}
}

func TestProviderHoverSanitizes(t *testing.T) {
	t.Parallel()
	provider, fake := providerWithFake(t, "svc.swift", "export function pay() {}\n")
	fake.responses["textDocument/hover"] = map[string]any{
		"contents": map[string]any{"kind": "markdown", "value": "```ts\nfunction pay(): void\n```"},
	}
	facts, err := provider.Hover(context.Background(), swiftQuery("svc.swift"))
	if err != nil {
		t.Fatalf("Hover: %v", err)
	}
	if len(facts) != 1 || facts[0].Kind != semantic.KindHoverType {
		t.Fatalf("expected one hover-type fact: %+v", facts)
	}
	if facts[0].Provenance.Source != semantic.SourceSwiftLSP {
		t.Fatalf("provenance source = %v", facts[0].Provenance.Source)
	}
	if facts[0].Detail == "" {
		t.Fatal("hover detail should be populated")
	}
}

func TestProviderHoverNullIsZeroFacts(t *testing.T) {
	t.Parallel()
	provider, _ := providerWithFake(t, "svc.swift", "export const x = 1\n")
	facts, err := provider.Hover(context.Background(), swiftQuery("svc.swift"))
	if err != nil {
		t.Fatalf("Hover: %v", err)
	}
	if len(facts) != 0 {
		t.Fatalf("null hover must be zero facts, got %d", len(facts))
	}
}

func TestProviderDefinitionExternalIsBoundary(t *testing.T) {
	t.Parallel()
	provider, fake := providerWithFake(t, "svc.swift", "import {x} from 'lib'\n")
	external := "file:///elsewhere/node_modules/lib/index.d.swift"
	fake.responses["textDocument/definition"] = []lspfacts.Location{{
		URI: external, Range: lspfacts.Range{Start: lspfacts.Position{Line: 2, Character: 0}, End: lspfacts.Position{Line: 2, Character: 5}}},
	}
	facts, err := provider.Definitions(context.Background(), swiftQuery("svc.swift"))
	if err != nil {
		t.Fatalf("Definitions: %v", err)
	}
	if len(facts) != 1 {
		t.Fatalf("expected one definition, got %d", len(facts))
	}
	if facts[0].Location.Path != external {
		t.Fatalf("external definition should keep its URI as a boundary: %q", facts[0].Location.Path)
	}
}

func TestProviderReferencesDedup(t *testing.T) {
	t.Parallel()
	provider, fake := providerWithFake(t, "svc.swift", "const a = 1\nconst b = a\nconst c = a\n")
	uri := lspconv.PathToURI(filepath.Join(provider.workspaceRoot, "svc.swift"))
	dup := lspfacts.Location{URI: uri, Range: lspfacts.Range{Start: lspfacts.Position{Line: 1, Character: 10}, End: lspfacts.Position{Line: 1, Character: 11}}}
	fake.responses["textDocument/references"] = []lspfacts.Location{dup, dup}
	facts, err := provider.References(context.Background(), swiftQuery("svc.swift"))
	if err != nil {
		t.Fatalf("References: %v", err)
	}
	if len(facts) != 1 {
		t.Fatalf("duplicate references must dedup to one, got %d", len(facts))
	}
	if facts[0].Kind != semantic.KindReference {
		t.Fatalf("kind = %v", facts[0].Kind)
	}
}

func TestProviderReferencesKeepsDistinctEndRanges(t *testing.T) {
	t.Parallel()
	provider, fake := providerWithFake(t, "svc.swift", "const a = 1\nconst b = a\n")
	uri := lspconv.PathToURI(filepath.Join(provider.workspaceRoot, "svc.swift"))
	fake.responses["textDocument/references"] = []lspfacts.Location{
		{URI: uri, Range: lspfacts.Range{Start: lspfacts.Position{Line: 1, Character: 10}, End: lspfacts.Position{Line: 1, Character: 11}}},
		{URI: uri, Range: lspfacts.Range{Start: lspfacts.Position{Line: 1, Character: 10}, End: lspfacts.Position{Line: 1, Character: 12}}},
	}
	facts, err := provider.References(context.Background(), swiftQuery("svc.swift"))
	if err != nil {
		t.Fatal(err)
	}
	if len(facts) != 2 {
		t.Fatalf("references with distinct end ranges = %d, want 2", len(facts))
	}
}

func TestProviderOutgoingCallRangesUseCallerURI(t *testing.T) {
	t.Parallel()
	provider, fake := providerWithFake(t, "caller.swift", "export function caller() { callee() }\n")
	callerURI := lspconv.PathToURI(filepath.Join(provider.workspaceRoot, "caller.swift"))
	calleeURI := lspconv.PathToURI(filepath.Join(provider.workspaceRoot, "callee.swift"))
	fake.responses["textDocument/prepareCallHierarchy"] = []lspfacts.CallHierarchyItem{{
		Name: "caller", URI: callerURI,
		Range:          lspfacts.Range{Start: lspfacts.Position{}, End: lspfacts.Position{Character: 37}},
		SelectionRange: lspfacts.Range{Start: lspfacts.Position{Character: 16}, End: lspfacts.Position{Character: 22}},
	}}
	fake.responses["callHierarchy/outgoingCalls"] = []lspfacts.OutgoingCall{{
		To: lspfacts.CallHierarchyItem{
			Name: "callee", URI: calleeURI,
			Range:          lspfacts.Range{Start: lspfacts.Position{}, End: lspfacts.Position{Character: 37}},
			SelectionRange: lspfacts.Range{Start: lspfacts.Position{Character: 27}, End: lspfacts.Position{Character: 33}},
		},
		FromRanges: []lspfacts.Range{{Start: lspfacts.Position{Character: 27}, End: lspfacts.Position{Character: 33}}},
	}}

	facts, err := provider.OutgoingCalls(context.Background(), swiftQuery("caller.swift"))
	if err != nil {
		t.Fatalf("OutgoingCalls: %v", err)
	}
	if len(facts) != 1 || facts[0].Location.Path != "callee.swift" {
		t.Fatalf("outgoing call = %+v, want callee primary location", facts)
	}
	if len(facts[0].Related) != 1 || facts[0].Related[0].Path != "caller.swift" {
		t.Fatalf("outgoing related locations = %+v, want caller.swift", facts[0].Related)
	}
}

func TestProviderDiagnosticsRespectVersion(t *testing.T) {
	t.Parallel()
	provider, _ := providerWithFake(t, "svc.swift", "const a: number = 'x'\n")
	uri := lspconv.PathToURI(filepath.Join(provider.workspaceRoot, "svc.swift"))
	provider.manager.handlePublishDiagnostics(mustJSON(t, lspfacts.PublishDiagnosticsParams{
		URI: uri, Diagnostics: []lspfacts.Diagnostic{{
			Range:    lspfacts.Range{Start: lspfacts.Position{Line: 0, Character: 6}, End: lspfacts.Position{Line: 0, Character: 7}},
			Severity: 1, Message: "Type 'string' is not assignable to type 'number'.",
		}},
	}))
	facts, err := provider.Diagnostics(context.Background(), swiftQuery("svc.swift"))
	if err != nil {
		t.Fatalf("Diagnostics: %v", err)
	}
	if len(facts) != 1 || facts[0].Kind != semantic.KindDiagnostic {
		t.Fatalf("expected one diagnostic fact: %+v", facts)
	}
	if facts[0].Confidence != semantic.ConfidenceDiagnostic {
		t.Fatalf("diagnostic confidence = %v", facts[0].Confidence)
	}
}

func TestSemanticTokensNormalizeNegotiatedLegendAndUTF16Ranges(t *testing.T) {
	t.Parallel()
	content := "const emoji = \"🚀\"; const value = emoji;\r\nclass Box {}\r\n"
	provider, fake := providerWithFake(t, "svc.swift", content)
	valueByte := bytes.Index([]byte(content), []byte("value"))
	valueUTF16 := len(utf16.Encode([]rune(content[:valueByte])))
	fake.responses["textDocument/semanticTokens/full"] = lspfacts.SemanticTokens{
		ResultID: "result-1",
		Data: []uint32{
			0, uint32(valueUTF16), 5, 7, 1, // variable + declaration
			1, 6, 3, 0, 0, // class Box on the CRLF-following line
		},
	}
	query := swiftQuery("svc.swift")
	query.DocumentID = "doc-1"
	query.DocumentVersion = 1
	query.Content = []byte(content)
	if err := provider.OpenDocument(context.Background(), query.DocumentID, query.DocumentVersion, query.Path, "sourcekit", query.Content); err != nil {
		t.Fatal(err)
	}
	set, err := provider.SemanticTokens(context.Background(), query)
	if err != nil {
		t.Fatalf("SemanticTokens: %v", err)
	}
	if set.DocumentID != query.DocumentID || set.DocumentVersion != query.DocumentVersion || set.ContentHash == "" || set.ProviderSession == "" {
		t.Fatalf("token binding = %+v", set)
	}
	if len(set.Tokens) != 2 || set.Tokens[0].TokenType != "variable" || set.Tokens[1].TokenType != "class" {
		t.Fatalf("canonical tokens = %+v", set.Tokens)
	}
	if got, want := set.Tokens[0].Range.Start.Column, valueByte+1; got != want {
		t.Fatalf("non-BMP token byte column = %d, want %d", got, want)
	}
	if len(set.Tokens[0].Modifiers) != 1 || set.Tokens[0].Modifiers[0] != "declaration" {
		t.Fatalf("canonical modifiers = %+v", set.Tokens[0].Modifiers)
	}
}

func TestSemanticTokensRejectMalformedStaleAndRestartedResponses(t *testing.T) {
	t.Parallel()
	provider, fake := providerWithFake(t, "svc.swift", "export const View = () => <div />\n")
	query := swiftQuery("svc.swift")
	query.DocumentID = "doc-1"
	query.DocumentVersion = 1
	query.Content = []byte("export const View = () => <div />\n")
	if err := provider.OpenDocument(context.Background(), query.DocumentID, query.DocumentVersion, query.Path, "swift", query.Content); err != nil {
		t.Fatal(err)
	}

	stale := query
	stale.DocumentVersion = 2
	if _, err := provider.SemanticTokens(context.Background(), stale); !errors.Is(err, ErrStaleVersion) {
		t.Fatalf("stale tokens error = %v", err)
	}

	fake.responses["textDocument/semanticTokens/full"] = lspfacts.SemanticTokens{Data: []uint32{0, 1, 2}}
	if _, err := provider.SemanticTokens(context.Background(), query); !errors.Is(err, ErrMalformedSemanticTokens) {
		t.Fatalf("malformed tokens error = %v", err)
	}

	fake.responses["textDocument/semanticTokens/full"] = lspfacts.SemanticTokens{Data: []uint32{
		0, 13, 4, 7, 0,
		0, 1, 4, 7, 0,
	}}
	if _, err := provider.SemanticTokens(context.Background(), query); !errors.Is(err, ErrMalformedSemanticTokens) {
		t.Fatalf("overlapping tokens error = %v", err)
	}

	fake.responses["textDocument/semanticTokens/full"] = lspfacts.SemanticTokens{Data: []uint32{0, 13, 4, 7, 0}}
	fake.onRequest = func(method string) {
		if method == "textDocument/semanticTokens/full" {
			provider.manager.mu.Lock()
			provider.manager.sessionID = "sourcekit-lsp:restarted"
			provider.manager.mu.Unlock()
		}
	}
	if _, err := provider.SemanticTokens(context.Background(), query); !errors.Is(err, ErrProviderRestarted) {
		t.Fatalf("restarted tokens error = %v", err)
	}
}

func TestSemanticTokensBoundProviderPayload(t *testing.T) {
	content := "const " + strings.Repeat("x", lspadapter.MaxSemanticTokens+2) + " = 1\n"
	provider, fake := providerWithFake(t, "large.swift", content)
	data := make([]uint32, 0, (lspadapter.MaxSemanticTokens+1)*5)
	for index := 0; index < lspadapter.MaxSemanticTokens+1; index++ {
		data = append(data, 0, 1, 1, 7, 0)
	}
	fake.responses["textDocument/semanticTokens/full"] = lspfacts.SemanticTokens{Data: data}
	query := swiftQuery("large.swift")
	query.DocumentID = "doc-large"
	query.DocumentVersion = 1
	query.Content = []byte(content)
	if err := provider.OpenDocument(context.Background(), query.DocumentID, query.DocumentVersion, query.Path, "sourcekit", query.Content); err != nil {
		t.Fatal(err)
	}

	set, err := provider.SemanticTokens(context.Background(), query)
	if err != nil {
		t.Fatalf("SemanticTokens: %v", err)
	}
	if len(set.Tokens) != lspadapter.MaxSemanticTokens || !set.Truncated || set.OmittedCount != 1 {
		t.Fatalf("tokens=%d truncated=%v omitted=%d, want bounded %d-token result", len(set.Tokens), set.Truncated, set.OmittedCount, lspadapter.MaxSemanticTokens)
	}
}

func TestProviderUnavailableErrors(t *testing.T) {
	t.Parallel()
	manager, _ := managerWithFake(t, Config{Enable: EnableFalse})
	_ = manager.Start(context.Background(), true)
	provider := NewProvider(manager, t.TempDir(), func(semantic.SemanticQuery, string) ([]byte, error) { return nil, nil })
	if _, err := provider.Hover(context.Background(), swiftQuery("svc.swift")); err == nil {
		t.Fatal("hover on a disabled provider should error")
	}
}

func TestProviderCapabilitiesReportsUnavailableManager(t *testing.T) {
	t.Parallel()
	manager, _ := managerWithFake(t, Config{Enable: EnableTrue})
	manager.setDecision(DecisionUnavailable, CodeCrashed)
	provider := NewProvider(manager, t.TempDir(), func(semantic.SemanticQuery, string) ([]byte, error) { return nil, nil })
	if _, err := provider.Capabilities(context.Background()); !errors.Is(err, ErrUnavailable) {
		t.Fatalf("Capabilities() error = %v, want ErrUnavailable", err)
	}
}

func TestChangeInvalidatesVersionUnknownDiagnostics(t *testing.T) {
	t.Parallel()
	provider, _ := providerWithFake(t, "svc.swift", "const value = 1\n")
	doc := Document{DocumentID: "doc-1", Path: "svc.swift", Version: 1, Content: []byte("const value = 1\n")}
	if err := provider.manager.Open(context.Background(), doc); err != nil {
		t.Fatal(err)
	}
	uri := lspconv.PathToURI(filepath.Join(provider.workspaceRoot, doc.Path))
	provider.manager.handlePublishDiagnostics(mustJSON(t, lspfacts.PublishDiagnosticsParams{
		URI: uri, Diagnostics: []lspfacts.Diagnostic{{
			Range:   lspfacts.Range{Start: lspfacts.Position{Line: 0}, End: lspfacts.Position{Line: 0, Character: 5}},
			Message: "unknown version",
		}},
	}))
	if _, exists := provider.manager.diagnostics.Get(uri); !exists {
		t.Fatal("diagnostic setup failed")
	}
	doc.Version = 2
	doc.Content = []byte("const value = 2\n")
	if err := provider.manager.Change(context.Background(), doc); err != nil {
		t.Fatal(err)
	}
	if _, exists := provider.manager.diagnostics.Get(uri); exists {
		t.Fatal("version-unknown diagnostics survived didChange")
	}
}

func mustJSON(t *testing.T, value any) json.RawMessage {
	t.Helper()
	data, err := json.Marshal(value)
	if err != nil {
		t.Fatal(err)
	}
	return data
}
