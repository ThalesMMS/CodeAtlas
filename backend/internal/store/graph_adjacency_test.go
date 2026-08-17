package store

import (
	"fmt"
	"testing"

	"github.com/ThalesMMS/CodeAtlas/internal/domain"
)

func TestGraphTraversalVisitsOnlyAdjacentEdges(t *testing.T) {
	t.Parallel()
	st := newState()
	st.symbols = map[string]domain.Symbol{
		"seed":     {ID: "seed", Name: "Seed", Path: "seed.go"},
		"neighbor": {ID: "neighbor", Name: "Neighbor", Path: "neighbor.go"},
	}
	st.edges = append(st.edges, domain.Edge{ID: 1, FromSymbolID: "seed", ToSymbolID: "neighbor", Type: "calls"})
	for index := 0; index < 10_000; index++ {
		st.edges = append(st.edges, domain.Edge{
			ID: int64(index + 2), FromSymbolID: fmt.Sprintf("unrelated-from-%d", index),
			ToSymbolID: fmt.Sprintf("unrelated-to-%d", index), Type: "calls",
		})
	}
	st.rebuildAdjacency()

	nodes, edges, stats := st.graphWithStats([]string{"seed"}, 1, 10)
	if len(nodes) != 2 || len(edges) != 1 {
		t.Fatalf("graph nodes/edges = %d/%d, want 2/1", len(nodes), len(edges))
	}
	if stats.edgeVisits != 1 {
		t.Fatalf("edge visits = %d, want 1 expansion visit independent of 10,000 unrelated edges", stats.edgeVisits)
	}
	if got := st.adjacency["seed"]; len(got) != 1 || got[0] != 0 {
		t.Fatalf("seed adjacency = %v, want edge index 0", got)
	}
}
