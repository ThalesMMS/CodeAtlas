package service

import (
	"strings"
	"testing"

	"github.com/ThalesMMS/CodeAtlas/internal/contextpack"
	"github.com/ThalesMMS/CodeAtlas/internal/domain"
)

func TestPackDiagramUsesCapturedSymbolMap(t *testing.T) {
	t.Parallel()
	pack := contextpack.ContextPack{Evidence: []contextpack.Evidence{{
		ID: "ev:submit", SymbolID: "sym:submit", Kind: contextpack.KindASTObservation,
		Title: "stale title", Path: "stale/path.go", Range: domain.Range{Start: domain.Position{Line: 1, Column: 1}}, Relevance: 1,
	}}}
	symbolRange := domain.Range{Start: domain.Position{Line: 12, Column: 3}, End: domain.Position{Line: 14, Column: 4}}
	diagram := packDiagram(map[string]domain.Symbol{
		"sym:submit": {ID: "sym:submit", Name: "SubmitOrder", Kind: "function", Path: "internal/order/service.go", Range: symbolRange},
	}, pack)
	if diagram == nil || !strings.Contains(diagram.Source, "SubmitOrder") {
		t.Fatalf("diagram = %+v", diagram)
	}
	if len(diagram.Sources) != 1 || diagram.Sources[0].Path != "internal/order/service.go" || diagram.Sources[0].Range != symbolRange {
		t.Fatalf("diagram sources = %+v", diagram.Sources)
	}
}

func TestPackDiagramFallsBackToSelfContainedEvidence(t *testing.T) {
	t.Parallel()
	evidenceRange := domain.Range{Start: domain.Position{Line: 4, Column: 1}, End: domain.Position{Line: 5, Column: 1}}
	pack := contextpack.ContextPack{Evidence: []contextpack.Evidence{{
		ID: "ev:config", Kind: contextpack.KindConfig, Title: "README", Path: "README.md", Range: evidenceRange, Relevance: 0.8,
	}}}
	diagram := packDiagram(nil, pack)
	if diagram == nil || !strings.Contains(diagram.Source, "README") {
		t.Fatalf("diagram = %+v", diagram)
	}
	if len(diagram.Sources) != 1 || diagram.Sources[0].Path != "README.md" || diagram.Sources[0].Range != evidenceRange {
		t.Fatalf("diagram sources = %+v", diagram.Sources)
	}
}

func TestSourceGroupIsSharedByCodemapAndPackDiagrams(t *testing.T) {
	t.Parallel()
	tests := []struct {
		path string
		kind string
		want string
	}{
		{path: "internal/order/service.go", kind: "function", want: "internal"},
		{path: `internal\order\service.go`, kind: "function", want: "internal"},
		{path: "service_test.go", kind: "function", want: "Tests"},
		{path: "web/checkout.spec.ts", kind: "function", want: "Tests"},
		{path: "internal/tests/helper.go", kind: "function", want: "Tests"},
		{path: "internal/contest/service.go", kind: "function", want: "internal"},
		{path: "attestation.go", kind: "function", want: "Root"},
		{path: "latest.go", kind: "function", want: "Root"},
		{path: "README.md", kind: "file", want: "Files"},
		{path: "main.go", kind: "function", want: "Root"},
	}
	for _, test := range tests {
		if got := sourceGroup(test.path, test.kind); got != test.want {
			t.Errorf("sourceGroup(%q, %q) = %q, want %q", test.path, test.kind, got, test.want)
		}
	}
}
