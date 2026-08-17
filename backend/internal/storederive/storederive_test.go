package storederive

import (
	"reflect"
	"strings"
	"testing"
	"unicode/utf8"

	"github.com/ThalesMMS/CodeAtlas/internal/domain"
)

func TestResolveSymbolUsesSharedQualificationAndLocationPolicy(t *testing.T) {
	t.Parallel()
	symbols := []domain.Symbol{
		{ID: "other", Name: "Run", QualifiedName: "other.Worker.Run", Path: "other/worker.go", Kind: domain.KindMethod},
		{ID: "near", Name: "Run", QualifiedName: "pkg.Worker.Run", Path: "pkg/worker.go", Kind: domain.KindMethod},
		{ID: "exact", Name: "Run", QualifiedName: "pkg.Service.Run", Path: "pkg/current.go", Kind: domain.KindMethod},
	}

	got, ok := ResolveSymbol(symbols, "receiver.Run()", "pkg/current.go")
	if !ok || got.ID != "exact" {
		t.Fatalf("ResolveSymbol = %+v/%v, want exact", got, ok)
	}
}

func TestSnippetKeepsUTF8Boundaries(t *testing.T) {
	t.Parallel()
	content := strings.Repeat("a", 89) + "é" + strings.Repeat("b", 400)
	got := Snippet(domain.Symbol{DocComment: content}, []string{"é"})
	if !utf8.ValidString(got) {
		t.Fatalf("Snippet returned invalid UTF-8: %q", got)
	}
}

func TestSymbolLookupNamesIncludesQualifiedTail(t *testing.T) {
	t.Parallel()
	got := SymbolLookupNames(domain.Symbol{Name: "Run", QualifiedName: "pkg/Worker::Execute"})
	want := []string{"Run", "Execute"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("SymbolLookupNames = %v, want %v", got, want)
	}
}

func TestFileTreeIsDeterministic(t *testing.T) {
	t.Parallel()
	got := FileTree([]domain.File{
		{Path: "z.go", Language: "go"},
		{Path: "pkg/b.go", Language: "go"},
		{Path: "pkg/a.go", Language: "go"},
	})
	if len(got.Children) != 2 || got.Children[0].Name != "pkg" || got.Children[1].Name != "z.go" {
		t.Fatalf("root children = %+v, want pkg then z.go", got.Children)
	}
	if children := got.Children[0].Children; len(children) != 2 || children[0].Name != "a.go" || children[1].Name != "b.go" {
		t.Fatalf("pkg children = %+v, want a.go then b.go", children)
	}
}

func TestTraverseGraphHonorsDepthLimitAndReturnsOnlyInducedEdges(t *testing.T) {
	t.Parallel()
	edges := []domain.Edge{
		{ID: 1, FromSymbolID: "a", ToSymbolID: "b", Type: "calls"},
		{ID: 2, FromSymbolID: "b", ToSymbolID: "c", Type: "calls"},
		{ID: 3, FromSymbolID: "b", ToName: "external", Type: "calls"},
	}
	adjacency := map[string][]int{"a": {0}, "b": {0, 1, 2}, "c": {1}}
	exists := func(id string) bool { return id == "a" || id == "b" || id == "c" }

	visited, _ := TraverseGraph([]string{"a"}, 1, 10, exists, adjacency, edges)
	if _, ok := visited["b"]; !ok {
		t.Fatalf("depth 1 did not reach b: %v", visited)
	}
	if _, ok := visited["c"]; ok {
		t.Fatalf("depth 1 unexpectedly reached c: %v", visited)
	}
	got := GraphEdges(edges, adjacency, visited)
	if len(got) != 2 || got[0].ID != 1 || got[1].ID != 3 {
		t.Fatalf("GraphEdges = %+v, want internal a->b plus unresolved external relation", got)
	}

	limited, _ := TraverseGraph([]string{"a"}, 2, 1, exists, adjacency, edges)
	if len(limited) != 1 {
		t.Fatalf("maxNodes=1 visited %v", limited)
	}
	if got := GraphEdges(edges, adjacency, limited); len(got) != 0 {
		t.Fatalf("limited graph contains dangling edges: %+v", got)
	}
}
