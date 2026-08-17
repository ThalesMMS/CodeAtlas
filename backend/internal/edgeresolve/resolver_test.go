package edgeresolve

import (
	"fmt"
	"testing"

	"github.com/ThalesMMS/CodeAtlas/internal/domain"
)

func TestResolveIndexesImportSuffixesAndCachesNamedCandidates(t *testing.T) {
	t.Parallel()
	symbols := map[string]domain.Symbol{
		"file-a": {ID: "file-a", Kind: domain.KindFile, Name: "bar.go", Path: "a/pkg/foo/bar.go"},
		"file-z": {ID: "file-z", Kind: domain.KindFile, Name: "bar.go", Path: "z/pkg/foo/bar.go"},
		"run-a":  {ID: "run-a", Kind: "function", Name: "Run", Path: "a/caller.go"},
		"run-b":  {ID: "run-b", Kind: "function", Name: "Run", Path: "b/caller.go"},
		"run-c":  {ID: "run-c", Kind: "function", Name: "Run", Path: "c/caller.go"},
	}
	edges := make([]domain.Edge, 0, 200)
	for i := 0; i < 100; i++ {
		edges = append(edges, domain.Edge{Type: "imports", ToName: "foo/bar", Path: fmt.Sprintf("src/%d.go", i)})
		edges = append(edges, domain.Edge{Type: "calls", ToName: "pkg.Run", Path: "b/caller.go"})
	}

	stats := resolve(symbols, edges)
	if stats.importCandidates != 2 || stats.importLookups != 100 {
		t.Fatalf("import stats = %+v, want two indexed candidates and 100 lookups", stats)
	}
	if stats.namedCandidateScan != 3 {
		t.Fatalf("named candidate scans = %d, want one scan of three candidates", stats.namedCandidateScan)
	}
	for i, edge := range edges {
		if edge.Type == "imports" && edge.ToSymbolID != "file-a" {
			t.Fatalf("edge[%d] import target = %q, want deterministic file-a", i, edge.ToSymbolID)
		}
		if edge.Type == "calls" && edge.ToSymbolID != "run-b" {
			t.Fatalf("edge[%d] named target = %q, want same-path run-b", i, edge.ToSymbolID)
		}
	}
}

func TestResolveSkipsEmptyImportsAndSubstringSuffixes(t *testing.T) {
	t.Parallel()
	symbols := map[string]domain.Symbol{
		"file": {ID: "file", Kind: domain.KindFile, Name: "bar.go", Path: "pkg/foo/bar.go"},
	}
	edges := []domain.Edge{
		{Type: "imports", ToName: "./", Confidence: 0.4},
		{Type: "imports", ToName: "o/bar", Confidence: 0.5},
		{Type: "imports", ToName: "foo/bar", Confidence: 0.6},
	}

	stats := resolve(symbols, edges)
	if stats.importLookups != 2 {
		t.Fatalf("import lookups = %d, want two non-empty targets", stats.importLookups)
	}
	if edges[0].ToSymbolID != "" || edges[0].Confidence != 0.4 {
		t.Fatalf("empty import target changed: %+v", edges[0])
	}
	if edges[1].ToSymbolID != "" || edges[1].Confidence != 0.5 {
		t.Fatalf("substring import target resolved: %+v", edges[1])
	}
	if edges[2].ToSymbolID != "file" || edges[2].Confidence != 0.9 {
		t.Fatalf("segment-aligned import target = %+v, want file at 0.9", edges[2])
	}
}

func TestResolvePreservesValidTargetsAndClearsUnresolvableStaleTargets(t *testing.T) {
	t.Parallel()
	symbols := map[string]domain.Symbol{
		"target": {ID: "target", Kind: "function", Name: "Target", Path: "target.go"},
	}
	edges := []domain.Edge{
		{Type: "calls", ToName: "Missing", ToSymbolID: "target", Confidence: 1},
		{Type: "calls", ToName: "Missing", ToSymbolID: "stale", Confidence: 0.5},
	}

	Resolve(symbols, edges)
	if edges[0].ToSymbolID != "target" || edges[0].Confidence != 1 {
		t.Fatalf("valid target changed: %+v", edges[0])
	}
	if edges[1].ToSymbolID != "" || edges[1].Confidence != 0.5 {
		t.Fatalf("stale unresolvable target not cleared cleanly: %+v", edges[1])
	}
}
