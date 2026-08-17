package httpapi_test

import (
	"context"
	"net/http"
	"testing"
	"time"

	"github.com/ThalesMMS/CodeAtlas/internal/contenthash"
	"github.com/ThalesMMS/CodeAtlas/internal/domain"
	"github.com/ThalesMMS/CodeAtlas/internal/semantic"
)

func TestSemanticTokensEndpointReportsExplicitDisabledProviderForExactVersion(t *testing.T) {
	t.Parallel()
	server, _ := buildDocServer(t)

	resp, opened := doJSON(t, http.MethodPost, server.URL+"/api/documents/open", nil, map[string]any{"path": "web/checkout.ts"})
	if resp.StatusCode != http.StatusCreated {
		t.Fatalf("open status=%d body=%v", resp.StatusCode, opened)
	}
	documentID := opened["documentId"].(string)
	resp, body := doJSON(t, http.MethodGet, server.URL+"/api/documents/"+documentID+"/semantic-tokens?version=1", nil, nil)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("semantic tokens status=%d body=%v", resp.StatusCode, body)
	}
	if body["legendVersion"] != semantic.SemanticTokenLegendVersion || body["documentId"] != documentID || body["documentVersion"].(float64) != 1 || body["contentHash"] == "" {
		t.Fatalf("semantic token metadata = %v", body)
	}
	if len(body["tokens"].([]any)) != 0 || body["semanticCoverage"].(map[string]any)["providerState"] != "disabled" {
		t.Fatalf("semantic token disabled result = %v", body)
	}
}

func TestDiagnosticsEndpointReturnsParserDiagnosticsForExactDocumentVersion(t *testing.T) {
	t.Parallel()
	server, _ := buildDocServer(t)

	resp, open := doJSON(t, http.MethodPost, server.URL+"/api/documents/open", nil, map[string]any{"path": "web/checkout.ts"})
	if resp.StatusCode != http.StatusCreated {
		t.Fatalf("open status=%d body=%v", resp.StatusCode, open)
	}
	documentID := open["documentId"].(string)
	leaseID := open["leaseId"].(string)

	resp, replaced := doJSON(t, http.MethodPut, server.URL+"/api/documents/"+documentID+"/content", nil, map[string]any{
		"leaseId": leaseID, "expectedVersion": 1, "newVersion": 2, "content": "export function broken( {\n",
	})
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("replace status=%d body=%v", resp.StatusCode, replaced)
	}

	resp, body := doJSON(t, http.MethodGet, server.URL+"/api/documents/"+documentID+"/diagnostics?version=2", nil, nil)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("diagnostics status=%d body=%v", resp.StatusCode, body)
	}
	if resp.Header.Get("Cache-Control") != "no-store" {
		t.Fatalf("diagnostics missing Cache-Control: no-store")
	}
	if body["documentId"] != documentID || body["documentVersion"].(float64) != 2 || body["viewHash"] == "" {
		t.Fatalf("diagnostic metadata = %v", body)
	}
	diagnostics := body["diagnostics"].([]any)
	if len(diagnostics) == 0 {
		t.Fatalf("diagnostics = %v, want parser diagnostic for invalid overlay content", diagnostics)
	}
	diagnostic := diagnostics[0].(map[string]any)
	if diagnostic["source"] != "tree-sitter" || diagnostic["severity"] != "error" || diagnostic["versionKnown"] != true {
		t.Fatalf("diagnostic = %v, want versioned tree-sitter error", diagnostic)
	}
	if diagnostic["diagnosticId"] == "" || diagnostic["message"] == "" {
		t.Fatalf("diagnostic missing stable id/message: %v", diagnostic)
	}
	coverage := body["semanticCoverage"].(map[string]any)
	if coverage["parser"] != "available" || coverage["lsp"] != "disabled" {
		t.Fatalf("diagnostic coverage = %v, want parser available and LSP disabled", coverage)
	}
}

func TestDiagnosticsEndpointReportsMissingAnonymousSyntax(t *testing.T) {
	t.Parallel()
	server, _ := buildDocServer(t)

	resp, open := doJSON(t, http.MethodPost, server.URL+"/api/documents/open", nil, map[string]any{"path": "web/checkout.ts"})
	if resp.StatusCode != http.StatusCreated {
		t.Fatalf("open status=%d body=%v", resp.StatusCode, open)
	}
	documentID := open["documentId"].(string)
	leaseID := open["leaseId"].(string)

	resp, replaced := doJSON(t, http.MethodPut, server.URL+"/api/documents/"+documentID+"/content", nil, map[string]any{
		"leaseId": leaseID, "expectedVersion": 1, "newVersion": 2, "content": "export function go() { return 1\n",
	})
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("replace status=%d body=%v", resp.StatusCode, replaced)
	}

	resp, body := doJSON(t, http.MethodGet, server.URL+"/api/documents/"+documentID+"/diagnostics?version=2", nil, nil)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("diagnostics status=%d body=%v", resp.StatusCode, body)
	}
	diagnostics := body["diagnostics"].([]any)
	if len(diagnostics) == 0 {
		t.Fatalf("missing anonymous syntax produced no diagnostics: %v", body)
	}
}

func TestDiagnosticsEndpointRejectsStaleDocumentVersion(t *testing.T) {
	t.Parallel()
	server, _ := buildDocServer(t)

	resp, open := doJSON(t, http.MethodPost, server.URL+"/api/documents/open", nil, map[string]any{"path": "web/checkout.ts"})
	if resp.StatusCode != http.StatusCreated {
		t.Fatalf("open status=%d body=%v", resp.StatusCode, open)
	}
	documentID := open["documentId"].(string)
	leaseID := open["leaseId"].(string)

	resp, replaced := doJSON(t, http.MethodPut, server.URL+"/api/documents/"+documentID+"/content", nil, map[string]any{
		"leaseId": leaseID, "expectedVersion": 1, "newVersion": 2, "content": "export function go() { return 2 }\n",
	})
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("replace status=%d body=%v", resp.StatusCode, replaced)
	}

	resp, body := doJSON(t, http.MethodGet, server.URL+"/api/documents/"+documentID+"/diagnostics?version=1", nil, nil)
	if resp.StatusCode != http.StatusConflict {
		t.Fatalf("stale diagnostics status=%d body=%v", resp.StatusCode, body)
	}
}

func TestSemanticEndpointsApplyOnlyExactNormalizedProviderResults(t *testing.T) {
	provider := &semanticEditorProvider{sessionID: "typescript-lsp:test-1", tokensSupported: true}
	server, _ := buildDocServerWithProvider(t, true, provider)
	resp, opened := doJSON(t, http.MethodPost, server.URL+"/api/documents/open", nil, map[string]any{"path": "web/checkout.ts"})
	if resp.StatusCode != http.StatusCreated {
		t.Fatalf("open status=%d body=%v", resp.StatusCode, opened)
	}
	documentID := opened["documentId"].(string)
	contentHash := opened["contentHash"].(string)

	resp, tokens := doJSON(t, http.MethodGet, server.URL+"/api/documents/"+documentID+"/semantic-tokens?version=1", nil, nil)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("tokens status=%d body=%v", resp.StatusCode, tokens)
	}
	items := tokens["tokens"].([]any)
	if len(items) != 1 || items[0].(map[string]any)["tokenType"] != "function" || tokens["providerSession"] != provider.sessionID {
		t.Fatalf("normalized semantic tokens = %v", tokens)
	}
	if tokens["contentHash"] != contentHash || tokens["semanticCoverage"].(map[string]any)["providerState"] != "available" {
		t.Fatalf("token provenance/version = %v", tokens)
	}
	if tokens["legendVersion"] != semantic.SemanticTokenLegendVersion {
		t.Fatalf("semantic token legend version = %v", tokens["legendVersion"])
	}

	resp, diagnostics := doJSON(t, http.MethodGet, server.URL+"/api/documents/"+documentID+"/diagnostics?version=1", nil, nil)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("diagnostics status=%d body=%v", resp.StatusCode, diagnostics)
	}
	diagnosticItems := diagnostics["diagnostics"].([]any)
	if len(diagnosticItems) != 1 || diagnosticItems[0].(map[string]any)["source"] != "fake-ts" {
		t.Fatalf("normalized LSP diagnostics = %v", diagnostics)
	}
	if diagnostics["contentHash"] != contentHash || diagnostics["providerSession"] != provider.sessionID || diagnostics["semanticCoverage"].(map[string]any)["lsp"] != "available" {
		t.Fatalf("diagnostic provenance/version = %v", diagnostics)
	}

	provider.staleHash = true
	resp, stale := doJSON(t, http.MethodGet, server.URL+"/api/documents/"+documentID+"/semantic-tokens?version=1", nil, nil)
	if resp.StatusCode != http.StatusOK || len(stale["tokens"].([]any)) != 0 || stale["semanticCoverage"].(map[string]any)["providerState"] != "stale" {
		t.Fatalf("stale semantic result was not rejected: status=%d body=%v", resp.StatusCode, stale)
	}
}

func TestSemanticTokensEndpointDistinguishesUnsupportedZeroAndRestartedProviders(t *testing.T) {
	tests := []struct {
		name           string
		provider       *semanticEditorProvider
		wantState      string
		wantOmission   string
		wantTokenCount int
	}{
		{
			name: "unsupported", provider: &semanticEditorProvider{sessionID: "typescript-lsp:unsupported"},
			wantState: "unsupported", wantOmission: "provider_unsupported",
		},
		{
			name: "legitimate zero", provider: &semanticEditorProvider{sessionID: "typescript-lsp:zero", tokensSupported: true, emptyTokens: true},
			wantState: "available", wantTokenCount: 0,
		},
		{
			name: "restarted", provider: &semanticEditorProvider{sessionID: "typescript-lsp:before", tokensSupported: true, restartOnTokens: true},
			wantState: "restarted", wantOmission: "provider_restarted",
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			server, _ := buildDocServerWithProvider(t, true, tc.provider)
			resp, opened := doJSON(t, http.MethodPost, server.URL+"/api/documents/open", nil, map[string]any{"path": "web/checkout.ts"})
			if resp.StatusCode != http.StatusCreated {
				t.Fatalf("open status=%d body=%v", resp.StatusCode, opened)
			}
			resp, body := doJSON(t, http.MethodGet, server.URL+"/api/documents/"+opened["documentId"].(string)+"/semantic-tokens?version=1", nil, nil)
			if resp.StatusCode != http.StatusOK {
				t.Fatalf("tokens status=%d body=%v", resp.StatusCode, body)
			}
			if got := body["semanticCoverage"].(map[string]any)["providerState"]; got != tc.wantState {
				t.Fatalf("providerState=%v want %q: %v", got, tc.wantState, body)
			}
			if got := len(body["tokens"].([]any)); got != tc.wantTokenCount {
				t.Fatalf("token count=%d want %d: %v", got, tc.wantTokenCount, body)
			}
			omissions, _ := body["omissions"].([]any)
			if tc.wantOmission == "" {
				if len(omissions) != 0 {
					t.Fatalf("legitimate zero result has omissions: %v", omissions)
				}
			} else if len(omissions) != 1 || omissions[0].(map[string]any)["reason"] != tc.wantOmission {
				t.Fatalf("omissions=%v want reason %q", omissions, tc.wantOmission)
			}
		})
	}
}

func TestDiagnosticsEndpointClassifiesProviderSessionChangeAsRestarted(t *testing.T) {
	provider := &semanticEditorProvider{sessionID: "typescript-lsp:before", tokensSupported: true, restartOnDiagnostics: true}
	server, _ := buildDocServerWithProvider(t, true, provider)
	resp, opened := doJSON(t, http.MethodPost, server.URL+"/api/documents/open", nil, map[string]any{"path": "web/checkout.ts"})
	if resp.StatusCode != http.StatusCreated {
		t.Fatalf("open status=%d body=%v", resp.StatusCode, opened)
	}
	resp, body := doJSON(t, http.MethodGet, server.URL+"/api/documents/"+opened["documentId"].(string)+"/diagnostics?version=1", nil, nil)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("diagnostics status=%d body=%v", resp.StatusCode, body)
	}
	if got := body["semanticCoverage"].(map[string]any)["lsp"]; got != "restarted" {
		t.Fatalf("lsp state=%v want restarted: %v", got, body)
	}
	if len(body["diagnostics"].([]any)) != 0 {
		t.Fatalf("diagnostics from prior provider session were applied: %v", body)
	}
	omissions := body["omissions"].([]any)
	if len(omissions) != 1 || omissions[0].(map[string]any)["reason"] != "provider_restarted" {
		t.Fatalf("restart omission = %v", omissions)
	}
}

type semanticEditorProvider struct {
	sessionID            string
	tokensSupported      bool
	staleHash            bool
	emptyTokens          bool
	restartOnTokens      bool
	restartOnDiagnostics bool
}

func (p *semanticEditorProvider) ID() semantic.SemanticProviderID { return "typescript-lsp" }
func (p *semanticEditorProvider) ProviderState() semantic.ProviderState {
	return semantic.ProviderState{State: semantic.ProviderStateAvailable, SessionID: p.sessionID}
}
func (p *semanticEditorProvider) Capabilities(context.Context) (semantic.SemanticCapabilities, error) {
	return semantic.SemanticCapabilities{Diagnostics: true, SemanticTokensFull: p.tokensSupported}, nil
}
func (p *semanticEditorProvider) SemanticTokens(_ context.Context, query semantic.SemanticQuery) (semantic.SemanticTokenSet, error) {
	hash := contenthash.HashContent(query.Content)
	if p.staleHash {
		hash = "sha256:stale"
	}
	tokens := []semantic.SemanticToken{{
		Range:     domain.Range{Start: domain.Position{Line: 1, Column: 17}, End: domain.Position{Line: 1, Column: 19}},
		TokenType: "function", Modifiers: []string{"declaration"},
	}}
	if p.emptyTokens {
		tokens = []semantic.SemanticToken{}
	}
	set := semantic.SemanticTokenSet{
		DocumentID: query.DocumentID, DocumentVersion: query.DocumentVersion, ContentHash: hash,
		ProviderSession: p.sessionID,
		Tokens:          tokens,
		Provenance:      semantic.Provenance{Source: semantic.SourceTypeScriptLSP, ProviderID: p.ID(), Method: "textDocument/semanticTokens/full", ObservedAt: time.Now().UTC()},
	}
	if p.restartOnTokens {
		p.sessionID = "typescript-lsp:after"
	}
	return set, nil
}
func (p *semanticEditorProvider) Diagnostics(_ context.Context, query semantic.SemanticQuery) ([]semantic.SemanticFact, error) {
	facts := []semantic.SemanticFact{{
		Kind: semantic.KindDiagnostic,
		Location: semantic.SourceLocation{
			Path: query.Path, Range: domain.Range{Start: domain.Position{Line: 1, Column: 8}, End: domain.Position{Line: 1, Column: 16}}, Encoding: semantic.EncodingUTF8,
		},
		Provenance: semantic.Provenance{Source: semantic.SourceTypeScriptLSP, ProviderID: p.ID(), ToolVersion: "test", Method: "textDocument/publishDiagnostics", ObservedAt: time.Now().UTC()},
		SnapshotID: query.SnapshotID, DocumentID: query.DocumentID, DocumentVersion: query.DocumentVersion,
		ContentHash: contenthash.HashContent(query.Content), ProviderSession: p.sessionID, VersionKnown: true,
		Diagnostic: &semantic.DiagnosticFact{Severity: "warning", Code: "TS9000", Source: "fake-ts", Message: "synthetic warning"},
	}}
	if p.restartOnDiagnostics {
		p.sessionID = "typescript-lsp:after"
	}
	return facts, nil
}
func (p *semanticEditorProvider) Hover(context.Context, semantic.SemanticQuery) ([]semantic.SemanticFact, error) {
	return nil, semantic.CapabilityUnsupported("hover")
}
func (p *semanticEditorProvider) Definitions(context.Context, semantic.SemanticQuery) ([]semantic.SemanticFact, error) {
	return nil, semantic.CapabilityUnsupported("definition")
}
func (p *semanticEditorProvider) References(context.Context, semantic.SemanticQuery) ([]semantic.SemanticFact, error) {
	return nil, semantic.CapabilityUnsupported("references")
}
func (p *semanticEditorProvider) Implementations(context.Context, semantic.SemanticQuery) ([]semantic.SemanticFact, error) {
	return nil, semantic.CapabilityUnsupported("implementation")
}
func (p *semanticEditorProvider) IncomingCalls(context.Context, semantic.SemanticQuery) ([]semantic.SemanticFact, error) {
	return nil, semantic.CapabilityUnsupported("incomingCalls")
}
func (p *semanticEditorProvider) OutgoingCalls(context.Context, semantic.SemanticQuery) ([]semantic.SemanticFact, error) {
	return nil, semantic.CapabilityUnsupported("outgoingCalls")
}
