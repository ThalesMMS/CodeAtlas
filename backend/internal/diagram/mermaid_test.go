package diagram

import (
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/ThalesMMS/CodeAtlas/internal/domain"
)

func TestFlowchartMatchesGoldenAndIgnoresInputOrder(t *testing.T) {
	t.Parallel()
	nodes := []Node{
		testNode("external", "Stripe", "external", "", 0.1, true, 0),
		testNode("service", "NewService", "internal", "internal/order/service.go", 0.7, false, 12),
		testNode("main", "main", "cmd", "cmd/api/main.go", 0.9, false, 10),
		testNode("create", "Create", "internal", "internal/order/http.go", 0.8, false, 15),
	}
	edges := []Edge{
		{ID: "e-import", Source: "main", Target: "external", Type: "imports"},
		{ID: "e-create", Source: "create", Target: "service", Type: "calls"},
		{ID: "e-main", Source: "main", Target: "service", Type: "calls"},
	}

	first := Flowchart(nodes, edges, 20)
	slices.Reverse(nodes)
	slices.Reverse(edges)
	second := Flowchart(nodes, edges, 20)
	if first.Source != second.Source {
		t.Fatalf("input order changed diagram:\nfirst:\n%s\nsecond:\n%s", first.Source, second.Source)
	}
	assertGolden(t, "flowchart.golden", first.Source)
	if first.Version != Version || first.Kind != "flowchart" {
		t.Fatalf("diagram metadata = %+v", first)
	}
	if len(first.Sources) != 3 || first.Sources[0].Path != "cmd/api/main.go" {
		t.Fatalf("sources = %+v", first.Sources)
	}
	if !strings.Contains(first.Source, "-. imports .->") {
		t.Fatalf("external dependency is not dashed:\n%s", first.Source)
	}
}

func TestFlowchartCapsHighestValueNodes(t *testing.T) {
	t.Parallel()
	nodes := []Node{
		testNode("low", "Low", "pkg", "pkg/low.go", 0.1, false, 1),
		testNode("main", "main", "cmd", "cmd/main.go", 0.2, false, 1),
		testNode("called", "Called", "pkg", "pkg/called.go", 0.3, false, 1),
	}
	edges := []Edge{{Source: "main", Target: "called", Type: "calls"}}
	result := Flowchart(nodes, edges, 2)
	if strings.Contains(result.Source, "Low") || !strings.Contains(result.Source, "main") || !strings.Contains(result.Source, "Called") {
		t.Fatalf("cap did not retain entrypoint/call nodes:\n%s", result.Source)
	}
}

func TestSequenceMatchesGoldenAndBoundsCycles(t *testing.T) {
	t.Parallel()
	nodes := []Node{
		testNode("save", "Save", "internal", "internal/order/repository.go", 0.4, false, 30),
		testNode("create", "Create", "internal", "internal/order/http.go", 0.6, false, 20),
		testNode("main", "main", "cmd", "cmd/api/main.go", 0.9, false, 10),
		testNode("service", "NewService", "internal", "internal/order/service.go", 0.7, false, 12),
	}
	edges := []Edge{
		{ID: "e4", Source: "save", Target: "service", Type: "calls"},
		{ID: "e2", Source: "service", Target: "create", Type: "calls"},
		{ID: "e1", Source: "main", Target: "service", Type: "calls"},
		{ID: "e3", Source: "create", Target: "save", Type: "calls"},
		{ID: "ignored", Source: "main", Target: "save", Type: "imports"},
	}
	result := Sequence(nodes, edges, "", 5)
	assertGolden(t, "sequence.golden", result.Source)
	if strings.Count(result.Source, "Save") != 2 {
		t.Fatalf("cycle was not bounded or arrow missing:\n%s", result.Source)
	}
}

func TestSequenceReturnsEmptyWithoutCalls(t *testing.T) {
	t.Parallel()
	result := Sequence([]Node{testNode("main", "main", "cmd", "main.go", 1, false, 1)}, []Edge{{Source: "main", Target: "x", Type: "imports"}}, "", 5)
	if result.Source != "" {
		t.Fatalf("sequence without calls = %+v", result)
	}
}

func testNode(id, label, group, path string, relevance float64, external bool, line int) Node {
	return Node{
		ID: id, Label: label, Kind: "function", Group: group, Path: path, Relevance: relevance, External: external,
		Range: domain.Range{Start: domain.Position{Line: line, Column: 1}, End: domain.Position{Line: line + 1, Column: 1}},
	}
}

func assertGolden(t *testing.T, name, got string) {
	t.Helper()
	want, err := os.ReadFile(filepath.Join("testdata", name))
	if err != nil {
		t.Fatal(err)
	}
	normalized := strings.ReplaceAll(string(want), "\r\n", "\n")
	normalized = strings.TrimSuffix(normalized, "\n")
	if got != normalized {
		t.Fatalf("%s mismatch\nwant:\n%s\ngot:\n%s", name, normalized, got)
	}
}
