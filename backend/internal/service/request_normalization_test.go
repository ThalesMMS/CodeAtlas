package service

import (
	"testing"

	"github.com/ThalesMMS/CodeAtlas/internal/domain"
)

func TestNormalizeCodemapRequestCanonicalizesLimits(t *testing.T) {
	tests := []struct {
		name     string
		maxNodes int
		want     int
	}{
		{name: "default", maxNodes: 0, want: DefaultCodemapMaxNodes},
		{name: "floor", maxNodes: 1, want: MinCodemapMaxNodes},
		{name: "unchanged", maxNodes: 24, want: 24},
		{name: "cap", maxNodes: 99, want: MaxCodemapMaxNodes},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			got, err := NormalizeCodemapRequest(domain.CodemapRequest{Query: "  checkout flow  ", MaxNodes: test.maxNodes})
			if err != nil {
				t.Fatalf("NormalizeCodemapRequest() error = %v", err)
			}
			if got.Query != "checkout flow" || got.MaxNodes != test.want {
				t.Fatalf("normalized = %#v, want query checkout flow and maxNodes %d", got, test.want)
			}
		})
	}
	for _, request := range []domain.CodemapRequest{{Query: " "}, {Query: "valid", MaxNodes: -1}} {
		if _, err := NormalizeCodemapRequest(request); err == nil {
			t.Fatalf("NormalizeCodemapRequest(%#v) succeeded, want validation error", request)
		}
	}
}

func TestNormalizeExplainRequestCanonicalizesAndValidates(t *testing.T) {
	position := &domain.Position{Line: 3, Column: 5}
	got, err := NormalizeExplainRequest(domain.ExplainRequest{Path: "main.go", Position: position})
	if err != nil {
		t.Fatalf("NormalizeExplainRequest() error = %v", err)
	}
	if got.Feature != domain.ExplainFeatureHover || got.Depth != "hover" || got.Line != 3 || got.Column != 5 || got.Position.Encoding != "utf-16" {
		t.Fatalf("normalized hover = %#v", got)
	}
	if position.Encoding != "" {
		t.Fatalf("caller position mutated to encoding %q", position.Encoding)
	}

	seeMore, err := NormalizeExplainRequest(domain.ExplainRequest{
		Depth: "more", Target: &domain.ExplainTarget{SymbolID: "symbol-1"},
	})
	if err != nil || seeMore.Feature != domain.ExplainFeatureSeeMore || seeMore.Depth != "more" {
		t.Fatalf("normalized see more = %#v, error = %v", seeMore, err)
	}

	invalid := []domain.ExplainRequest{
		{Path: "main.go", Position: &domain.Position{Line: 1, Column: 1, Encoding: "utf-8"}},
		{Feature: domain.ExplainFeature("unknown"), Path: "main.go", Line: 1, Column: 1},
		{Feature: domain.ExplainFeatureHover, Path: "main.go"},
		{Feature: domain.ExplainFeatureSeeMore, Path: "main.go"},
		{Feature: domain.ExplainFeatureHover, Path: "main.go", Line: 1, Column: 1, DocumentID: "doc"},
	}
	for _, request := range invalid {
		if _, err := NormalizeExplainRequest(request); err == nil {
			t.Fatalf("NormalizeExplainRequest(%#v) succeeded, want validation error", request)
		}
	}
}
