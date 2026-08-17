package semevidence

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/ThalesMMS/CodeAtlas/internal/contextpack"
	"github.com/ThalesMMS/CodeAtlas/internal/domain"
	"github.com/ThalesMMS/CodeAtlas/internal/semantic"
)

type fakeProvider struct {
	caps    semantic.SemanticCapabilities
	capsErr error
	hover   []semantic.SemanticFact
	defs    []semantic.SemanticFact
	defsErr error
}

func (f fakeProvider) ID() semantic.SemanticProviderID { return "gopls" }
func (f fakeProvider) Capabilities(context.Context) (semantic.SemanticCapabilities, error) {
	return f.caps, f.capsErr
}
func (f fakeProvider) Hover(context.Context, semantic.SemanticQuery) ([]semantic.SemanticFact, error) {
	return f.hover, nil
}
func (f fakeProvider) Definitions(context.Context, semantic.SemanticQuery) ([]semantic.SemanticFact, error) {
	return f.defs, f.defsErr
}
func (f fakeProvider) References(context.Context, semantic.SemanticQuery) ([]semantic.SemanticFact, error) {
	return nil, nil
}
func (f fakeProvider) Implementations(context.Context, semantic.SemanticQuery) ([]semantic.SemanticFact, error) {
	return nil, semantic.CapabilityUnsupported("implementation")
}
func (f fakeProvider) IncomingCalls(context.Context, semantic.SemanticQuery) ([]semantic.SemanticFact, error) {
	return nil, nil
}
func (f fakeProvider) OutgoingCalls(context.Context, semantic.SemanticQuery) ([]semantic.SemanticFact, error) {
	return nil, nil
}
func (f fakeProvider) Diagnostics(context.Context, semantic.SemanticQuery) ([]semantic.SemanticFact, error) {
	return nil, nil
}

func enabledCaps() semantic.SemanticCapabilities {
	return semantic.SemanticCapabilities{Hover: true, Definition: true, References: true, Implementation: true, CallHierarchy: true, Diagnostics: true}
}

func hoverFact() semantic.SemanticFact {
	return semantic.SemanticFact{
		Kind: semantic.KindHoverType, Subject: semantic.SymbolRef{Name: "Alpha", SymbolID: "a1"},
		Location: semantic.SourceLocation{Path: "a.go", Range: domain.Range{Start: domain.Position{Line: 1, Column: 1}}},
		Detail:   "func Alpha()", Confidence: semantic.ConfidenceLanguageServerResolved,
		Provenance: semantic.Provenance{Source: semantic.SourceGopls, ProviderID: "gopls"},
	}
}

func TestCollectConvertsFactsToEvidence(t *testing.T) {
	t.Parallel()
	collector := NewCollector(fakeProvider{caps: enabledCaps(), hover: []semantic.SemanticFact{hoverFact()}}, time.Second, 24)
	result, err := collector.Collect(context.Background(), contextpack.SemanticRequest{
		Methods:  []string{"hover", "definition"},
		Location: &contextpack.SourceLocation{Path: "a.go", Line: 1, Column: 1},
	})
	if err != nil {
		t.Fatalf("Collect: %v", err)
	}
	if len(result.Evidence) != 1 {
		t.Fatalf("expected one evidence, got %d", len(result.Evidence))
	}
	evidence := result.Evidence[0]
	if evidence.Kind != contextpack.KindLSPFact || evidence.Relation != "type" || evidence.Confidence != semantic.ConfidenceLanguageServerResolved {
		t.Fatalf("bad semantic evidence: %+v", evidence)
	}
	if len(evidence.Provenance) == 0 || evidence.Provenance[0].Source != semantic.SourceGopls {
		t.Fatalf("provenance lost: %+v", evidence.Provenance)
	}
}

func TestCollectKeepsExternalDefinitionsNonNavigable(t *testing.T) {
	t.Parallel()
	definition := semantic.SemanticFact{
		Kind: semantic.KindDefinition, Subject: semantic.SymbolRef{Name: "HandleFunc"},
		Location:   semantic.SourceLocation{Path: "file:///usr/local/go/src/net/http/server.go"},
		Confidence: semantic.ConfidenceLanguageServerResolved,
		Provenance: semantic.Provenance{Source: semantic.SourceGopls, ProviderID: "gopls"},
	}
	collector := NewCollector(fakeProvider{caps: enabledCaps(), defs: []semantic.SemanticFact{definition}}, time.Second, 24)
	result, err := collector.Collect(context.Background(), contextpack.SemanticRequest{
		Methods: []string{"definition"}, Location: &contextpack.SourceLocation{Path: "main.go", Line: 9, Column: 10},
	})
	if err != nil {
		t.Fatalf("Collect: %v", err)
	}
	if len(result.Evidence) != 1 || result.Evidence[0].Path != "" || !strings.Contains(result.Evidence[0].Title, "external boundary") {
		t.Fatalf("external definition evidence = %+v, want textual non-navigable boundary", result.Evidence)
	}
}

func TestDisabledProviderIsOmissionNotEmpty(t *testing.T) {
	t.Parallel()
	collector := NewCollector(fakeProvider{caps: semantic.SemanticCapabilities{}}, time.Second, 24)
	result, err := collector.Collect(context.Background(), contextpack.SemanticRequest{Methods: []string{"hover"}})
	if err != nil {
		t.Fatalf("disabled provider should not error (optional): %v", err)
	}
	if len(result.Evidence) != 0 || len(result.Omissions) == 0 {
		t.Fatalf("disabled provider must yield an omission, not silent empty: %+v", result)
	}
}

func TestMandatoryUnavailableIsError(t *testing.T) {
	t.Parallel()
	collector := NewCollector(fakeProvider{capsErr: errors.New("gopls crashed")}, time.Second, 24)
	if _, err := collector.Collect(context.Background(), contextpack.SemanticRequest{Methods: []string{"hover"}, Mandatory: true}); err == nil {
		t.Fatal("mandatory provider unavailable should error")
	}
}

func TestMandatoryRoutedProviderFailureIsNotMaskedByAnotherLanguage(t *testing.T) {
	t.Parallel()
	goProvider := fakeProvider{caps: enabledCaps()}
	tsProvider := fakeProvider{capsErr: errors.New("typescript crashed")}
	router := semantic.NewPathRouter(map[string]semantic.SemanticProvider{
		".go": goProvider,
		".ts": tsProvider,
	})
	collector := NewCollector(router, time.Second, 24)

	_, err := collector.Collect(context.Background(), contextpack.SemanticRequest{
		Methods: []string{"hover"}, Mandatory: true,
		Location: &contextpack.SourceLocation{Path: "web/app.ts", Line: 1, Column: 1},
	})
	if err == nil || !strings.Contains(err.Error(), "typescript crashed") {
		t.Fatalf("mandatory TypeScript provider error = %v", err)
	}
}

func TestMethodErrorBecomesOmission(t *testing.T) {
	t.Parallel()
	// implementation returns CapabilityUnsupported; hover succeeds.
	collector := NewCollector(fakeProvider{caps: enabledCaps(), hover: []semantic.SemanticFact{hoverFact()}}, time.Second, 24)
	result, err := collector.Collect(context.Background(), contextpack.SemanticRequest{Methods: []string{"hover", "implementation"}})
	if err != nil {
		t.Fatal(err)
	}
	if len(result.Evidence) != 1 {
		t.Fatalf("hover evidence should survive a failing sibling method: %d", len(result.Evidence))
	}
	foundOmission := false
	for _, omission := range result.Omissions {
		if omission.Ref == "semantic:implementation" {
			foundOmission = true
		}
	}
	if !foundOmission {
		t.Fatalf("the unsupported method should be an omission: %+v", result.Omissions)
	}
}
