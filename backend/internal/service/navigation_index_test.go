package service

import (
	"testing"

	"github.com/ThalesMMS/CodeAtlas/internal/domain"
	"github.com/ThalesMMS/CodeAtlas/internal/repository"
)

type countingNavigationView struct {
	repository.ReadView
	allSymbolsCalls int
	allEdgesCalls   int
}

func (v *countingNavigationView) AllSymbols() []domain.Symbol {
	v.allSymbolsCalls++
	return v.ReadView.AllSymbols()
}

func (v *countingNavigationView) AllEdges() []domain.Edge {
	v.allEdgesCalls++
	return v.ReadView.AllEdges()
}

func TestNavigationAndExplainHotPathsUseIndexedLookups(t *testing.T) {
	t.Parallel()
	_, repo := navigationFixture(t)
	view := &countingNavigationView{ReadView: repo.Snapshot()}
	t.Cleanup(func() { _ = view.Close() })

	persistID := symbolIDByName(t, repo, "persist")
	persist, ok := view.GetSymbol(persistID)
	if !ok {
		t.Fatalf("GetSymbol(%q) failed", persistID)
	}

	candidates := matchingNavigationSymbols(view, "persist", persist.Path)
	if len(candidates) == 0 || candidates[0].ID != persist.ID {
		t.Fatalf("matchingNavigationSymbols = %+v, want persist first", candidates)
	}
	if resolved, found := navigationSymbolForOccurrence(view, persist.OccurrenceID); !found || resolved.ID != persist.ID {
		t.Fatalf("navigationSymbolForOccurrence = %+v/%v, want %q", resolved, found, persist.ID)
	}
	if resolved, found := symbolForOccurrence(view, persist.OccurrenceID); !found || resolved.ID != persist.ID {
		t.Fatalf("symbolForOccurrence = %+v/%v, want %q", resolved, found, persist.ID)
	}

	targets, err := queryReferenceTargets(view, exactNavigationSubject(persist))
	if err != nil {
		t.Fatalf("queryReferenceTargets: %v", err)
	}
	if len(targets) == 0 {
		t.Fatal("queryReferenceTargets returned no references")
	}
	if view.allSymbolsCalls != 0 || view.allEdgesCalls != 0 {
		t.Fatalf("hot path called AllSymbols %d times and AllEdges %d times", view.allSymbolsCalls, view.allEdgesCalls)
	}
}

func TestEdgeSnippetUsesSymbolsAlreadyLoadedForCodemap(t *testing.T) {
	t.Parallel()
	symbols := map[string]domain.Symbol{
		"caller": {
			ID:    "caller",
			Code:  "func caller() {\n\tcallee()\n}",
			Range: domain.Range{Start: domain.Position{Line: 10, Column: 1}},
		},
	}
	edge := domain.Edge{FromSymbolID: "caller", ToSymbolID: "callee", Type: "calls", Line: 11}
	if got := edgeSnippet(edge, symbols); got != "callee()" {
		t.Fatalf("edgeSnippet = %q, want callee()", got)
	}
}
