package store

import (
	"path/filepath"
	"testing"
	"time"

	"github.com/ThalesMMS/CodeAtlas/internal/domain"
)

func TestStoreReplaceSearchGraphAndPersistence(t *testing.T) {
	t.Parallel()
	path := filepath.Join(t.TempDir(), "index.json")
	repository, err := Open(path)
	if err != nil {
		t.Fatalf("Open() error = %v", err)
	}

	// Parser-supplied (legacy) ids are remapped to v1 SymbolIDs at ingestion; the
	// test captures the real ids from the API rather than hardcoding them.
	fileID := "file"
	functionID := "submit"
	repository.ReplaceFile(domain.ParsedFile{
		File: domain.File{Path: "checkout/service.go", Language: "go", Hash: "abc", IndexedAt: time.Now()},
		Symbols: []domain.Symbol{
			{ID: fileID, Path: "checkout/service.go", Name: "service.go", QualifiedName: "checkout/service.go", Kind: "file", Range: domain.Range{Start: domain.Position{Line: 1, Column: 1}, End: domain.Position{Line: 20, Column: 1}}, Summary: "checkout file"},
			{ID: functionID, Path: "checkout/service.go", Name: "SubmitOrder", QualifiedName: "checkout/service.go::SubmitOrder", Kind: "function", Language: "go", Range: domain.Range{Start: domain.Position{Line: 4, Column: 1}, End: domain.Position{Line: 10, Column: 2}}, Signature: "func SubmitOrder(id string) error", Summary: "submits an order"},
		},
		Edges: []domain.Edge{{FromSymbolID: fileID, ToSymbolID: functionID, ToName: "SubmitOrder", Type: "contains", Path: "checkout/service.go", Line: 4, Confidence: 1}},
	})

	hits := repository.Search("submit order", 5)
	if len(hits) == 0 || hits[0].Symbol.Name != "SubmitOrder" {
		t.Fatalf("Search() = %#v, want SubmitOrder first", hits)
	}
	submitID := hits[0].Symbol.ID
	at, ok := repository.SymbolAt("checkout/service.go", 6, 3)
	if !ok || at.Identity.Name != "SubmitOrder" || string(at.Occurrence.SymbolID) != submitID {
		t.Fatalf("SymbolAt() = %#v, %v", at, ok)
	}
	if at.Occurrence.ID == "" {
		t.Fatal("SymbolAt() occurrence has no OccurrenceID")
	}
	nodes, edges := repository.Graph([]string{submitID}, 1, 10)
	if len(nodes) != 2 || len(edges) != 1 {
		t.Fatalf("Graph() nodes=%d edges=%d, want 2/1", len(nodes), len(edges))
	}
	if err := repository.Persist(); err != nil {
		t.Fatalf("Persist() error = %v", err)
	}

	reopened, err := Open(path)
	if err != nil {
		t.Fatalf("Open(reopen) error = %v", err)
	}
	if got, ok := reopened.GetSymbol(submitID); !ok || got.Name != "SubmitOrder" {
		t.Fatalf("reopened symbol = %#v, %v", got, ok)
	}
}
