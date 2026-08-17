package factfusion

import (
	"testing"

	"github.com/ThalesMMS/CodeAtlas/internal/domain"
	"github.com/ThalesMMS/CodeAtlas/internal/semantic"
)

func astCall(caller domain.SymbolID, calleeID domain.SymbolID, calleeName string, confidence float64) semantic.SemanticFact {
	return semantic.SemanticFact{
		ID: "ast-" + string(caller) + "-" + calleeName, Kind: semantic.KindSyntacticCall,
		Subject:    semantic.SymbolRef{SymbolID: caller, Resolved: true},
		Object:     &semantic.SymbolRef{SymbolID: calleeID, Name: calleeName, Resolved: calleeID != ""},
		Confidence: confidence,
		Provenance: semantic.Provenance{Source: semantic.SourceTreeSitter, ProviderID: "tree-sitter"},
	}
}

func goplsOutgoing(caller, calleeID domain.SymbolID, calleeName string) semantic.SemanticFact {
	return semantic.SemanticFact{
		ID: "gopls-" + string(caller) + "-" + calleeName, Kind: semantic.KindCallOutgoing,
		Subject:    semantic.SymbolRef{SymbolID: caller, Resolved: true},
		Object:     &semantic.SymbolRef{SymbolID: calleeID, Name: calleeName, Resolved: true},
		Confidence: semantic.ConfidenceLanguageServerResolved,
		Provenance: semantic.Provenance{Source: semantic.SourceGopls, ProviderID: "gopls", Method: "callHierarchy/outgoingCalls"},
	}
}

func TestMergeSameRelationKeepsBothProvenancesAndMaxConfidence(t *testing.T) {
	t.Parallel()
	result, err := Merge(FusionInput{
		ASTFacts:      []semantic.SemanticFact{astCall("A", "T", "Target", 0.6)},
		ProviderFacts: []semantic.SemanticFact{goplsOutgoing("A", "T", "Target")},
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(result.Relations) != 1 {
		t.Fatalf("expected one fused relation, got %d", len(result.Relations))
	}
	relation := result.Relations[0]
	if relation.SupportCount != 2 {
		t.Fatalf("both sources should be evidences: %d", relation.SupportCount)
	}
	sources := map[string]bool{}
	for _, evidence := range relation.Evidences {
		sources[evidence.Provenance.Source] = true
	}
	if !sources[semantic.SourceTreeSitter] || !sources[semantic.SourceGopls] {
		t.Fatalf("fusion erased a provenance: %v", sources)
	}
	// Confidence is the MAX, never summed/inflated.
	if relation.RankingConfidence != semantic.ConfidenceLanguageServerResolved {
		t.Fatalf("ranking confidence = %v, want max %v (no inflation)", relation.RankingConfidence, semantic.ConfidenceLanguageServerResolved)
	}
}

func TestUnresolvedASTCorrelatesWithResolvedProvider(t *testing.T) {
	t.Parallel()
	result, err := Merge(FusionInput{
		ASTFacts:      []semantic.SemanticFact{astCall("A", "", "Target", 0.6)}, // name-only, unresolved
		ProviderFacts: []semantic.SemanticFact{goplsOutgoing("A", "T", "Target")},
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(result.Relations) != 1 {
		t.Fatalf("unresolved AST should correlate into one relation, got %d", len(result.Relations))
	}
	relation := result.Relations[0]
	if relation.Resolution != resolutionBySemantic {
		t.Fatalf("resolution = %q, want %q", relation.Resolution, resolutionBySemantic)
	}
	if relation.Key.ObjectID != "T" || relation.SupportCount != 2 {
		t.Fatalf("correlated relation should use resolved object T with both evidences: %+v", relation)
	}
}

func TestNonRelationalAndOutOfScope(t *testing.T) {
	t.Parallel()
	diagnostic := semantic.SemanticFact{Kind: semantic.KindDiagnostic, Confidence: 1.0, Provenance: semantic.Provenance{Source: semantic.SourceGopls}}
	stale := astCall("A", "T", "Target", 0.6)
	stale.SnapshotID = "other-snapshot"

	result, err := Merge(FusionInput{
		SnapshotID:    "snap1",
		ASTFacts:      []semantic.SemanticFact{stale},
		ProviderFacts: []semantic.SemanticFact{diagnostic},
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(result.NonRelational) != 1 || result.NonRelational[0].Kind != semantic.KindDiagnostic {
		t.Fatalf("diagnostic should be non-relational: %+v", result.NonRelational)
	}
	if len(result.Relations) != 0 {
		t.Fatalf("stale-snapshot fact must be dropped, got %d relations", len(result.Relations))
	}
	if len(result.Omissions) == 0 {
		t.Fatal("a dropped out-of-scope fact should be recorded as an omission")
	}
}

func TestDeterministicOutput(t *testing.T) {
	t.Parallel()
	input := FusionInput{
		ASTFacts:      []semantic.SemanticFact{astCall("A", "T", "Target", 0.6), astCall("B", "U", "Other", 0.6)},
		ProviderFacts: []semantic.SemanticFact{goplsOutgoing("A", "T", "Target")},
	}
	first, _ := Merge(input)
	second, _ := Merge(input)
	if len(first.Relations) != len(second.Relations) {
		t.Fatal("non-deterministic relation count")
	}
	for i := range first.Relations {
		if first.Relations[i].Key != second.Relations[i].Key {
			t.Fatalf("relation order differs at %d", i)
		}
	}
	if first.PolicyVersion != PolicyVersion {
		t.Fatalf("policy version = %q", first.PolicyVersion)
	}
}
