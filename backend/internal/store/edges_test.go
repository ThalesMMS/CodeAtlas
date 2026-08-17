package store

import (
	"encoding/json"
	"fmt"
	"math"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/ThalesMMS/CodeAtlas/internal/changeset"
	"github.com/ThalesMMS/CodeAtlas/internal/domain"
)

var edgeTestTime = time.Date(2026, 6, 24, 0, 0, 0, 0, time.UTC)

func openEdgeStore(t *testing.T) *Store {
	t.Helper()
	repository, err := Open(filepath.Join(t.TempDir(), "index.json"))
	if err != nil {
		t.Fatalf("Open() error = %v", err)
	}
	return repository
}

func fileWithEdges(path string, count int) domain.ParsedFile {
	symbols := []domain.Symbol{
		{ID: path + ":file", Path: path, Name: path, Kind: "file", Range: domain.Range{Start: domain.Position{Line: 1, Column: 1}, End: domain.Position{Line: 10, Column: 1}}},
		{ID: path + ":fn", Path: path, Name: "fn", Kind: "function", Range: domain.Range{Start: domain.Position{Line: 2, Column: 1}, End: domain.Position{Line: 3, Column: 1}}},
	}
	edges := make([]domain.Edge, count)
	for i := range edges {
		edges[i] = domain.Edge{FromSymbolID: path + ":file", ToSymbolID: path + ":fn", ToName: "fn", Type: "contains", Path: path, Line: i + 1}
	}
	return domain.ParsedFile{File: domain.File{Path: path, Hash: path}, Symbols: symbols, Edges: edges}
}

func edgeIDs(edges []domain.Edge) []int64 {
	ids := make([]int64, len(edges))
	for i, edge := range edges {
		ids[i] = edge.ID
	}
	return ids
}

func TestEdgeIDsAreNotReusedAfterDeleteAndUpsert(t *testing.T) {
	t.Parallel()
	repository := openEdgeStore(t)
	repository.ReplaceFile(fileWithEdges("a.go", 2))
	before := edgeIDs(repository.AllEdges())
	repository.DeleteFile("a.go")
	repository.ReplaceFile(fileWithEdges("a.go", 2))
	after := edgeIDs(repository.AllEdges())

	seen := map[int64]struct{}{}
	for _, id := range before {
		seen[id] = struct{}{}
	}
	for _, id := range after {
		if _, reused := seen[id]; reused {
			t.Fatalf("edge id %d reused after delete+upsert (before=%v after=%v)", id, before, after)
		}
	}
}

func TestAbortedPrepareDoesNotAdvanceActiveCounter(t *testing.T) {
	t.Parallel()
	repository := openEdgeStore(t)
	repository.ReplaceFile(fileWithEdges("a.go", 2))
	base := repository.current.nextEdgeID

	change, err := changeset.NewBuilder().WithExpectedVersion(repository.Version()).Upsert(fileWithEdges("b.go", 3)).Build(edgeTestTime)
	if err != nil {
		t.Fatal(err)
	}
	// Prepare twice without committing.
	if _, err := repository.Prepare(change); err != nil {
		t.Fatal(err)
	}
	if _, err := repository.Prepare(change); err != nil {
		t.Fatal(err)
	}
	if repository.current.nextEdgeID != base {
		t.Fatalf("aborted prepares advanced the active counter to %d, want %d", repository.current.nextEdgeID, base)
	}
}

func TestOldSnapshotDerivesNextEdgeID(t *testing.T) {
	t.Parallel()
	path := filepath.Join(t.TempDir(), "index.json")
	// Old format: no nextEdgeId field (omitempty omits 0), unique positive ids.
	data, _ := json.MarshalIndent(diskSnapshot{
		Version: snapshotVersion, SnapshotSchema: snapshotSchema, StoreVersion: 1,
		Edges: []domain.Edge{{ID: 1, Type: "contains", Path: "a.go"}, {ID: 2, Type: "calls", Path: "a.go"}, {ID: 5, Type: "imports", Path: "a.go"}},
	}, "", "  ")
	if err := os.WriteFile(path, data, 0o600); err != nil {
		t.Fatal(err)
	}
	repository, err := Open(path)
	if err != nil {
		t.Fatalf("Open() error = %v", err)
	}
	if repository.current.nextEdgeID != 6 {
		t.Fatalf("nextEdgeID = %d, want 6 (max+1)", repository.current.nextEdgeID)
	}
}

func TestSnapshotWithDuplicateIDsIsRepaired(t *testing.T) {
	t.Parallel()
	path := filepath.Join(t.TempDir(), "index.json")
	data, _ := json.MarshalIndent(diskSnapshot{
		Version: snapshotVersion, SnapshotSchema: snapshotSchema, StoreVersion: 1,
		Edges: []domain.Edge{{ID: 1, Type: "a", Path: "x"}, {ID: 1, Type: "b", Path: "x"}, {ID: 0, Type: "c", Path: "x"}},
	}, "", "  ")
	if err := os.WriteFile(path, data, 0o600); err != nil {
		t.Fatal(err)
	}
	repository, err := Open(path)
	if err != nil {
		t.Fatalf("Open() error = %v", err)
	}
	if err := validateEdgeIDs(repository.AllEdges(), repository.current.nextEdgeID); err != nil {
		t.Fatalf("repaired snapshot still invalid: %v", err)
	}
	if repository.current.nextEdgeID != 4 {
		t.Fatalf("nextEdgeID = %d, want 4 after repair", repository.current.nextEdgeID)
	}
}

func TestNewFormatSnapshotWithBadCounterIsRejected(t *testing.T) {
	t.Parallel()
	path := filepath.Join(t.TempDir(), "index.json")
	// New format: nextEdgeId present but <= max id -> corrupt.
	data, _ := json.MarshalIndent(diskSnapshot{
		Version: snapshotVersion, SnapshotSchema: snapshotSchema, StoreVersion: 1, NextEdgeID: 2,
		Edges: []domain.Edge{{ID: 1, Type: "a", Path: "x"}, {ID: 5, Type: "b", Path: "x"}},
	}, "", "  ")
	if err := os.WriteFile(path, data, 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := Open(path); err == nil {
		t.Fatal("Open accepted a snapshot with an inconsistent edge counter")
	}
}

func TestThousandsOfCyclesProduceNoDuplicateEdgeIDs(t *testing.T) {
	t.Parallel()
	repository := openEdgeStore(t)
	for i := 0; i < 2000; i++ {
		repository.ReplaceFile(fileWithEdges(fmt.Sprintf("pkg/f%d.go", i%5), 2))
	}
	seen := map[int64]struct{}{}
	for _, edge := range repository.AllEdges() {
		if edge.ID <= 0 {
			t.Fatalf("non-positive edge id %d", edge.ID)
		}
		if _, dup := seen[edge.ID]; dup {
			t.Fatalf("duplicate edge id %d after add/delete cycles", edge.ID)
		}
		seen[edge.ID] = struct{}{}
	}
}

func TestEdgeIDOverflowIsFatal(t *testing.T) {
	t.Parallel()
	repository := openEdgeStore(t)
	repository.current.nextEdgeID = math.MaxInt64
	defer func() {
		if recovered := recover(); recovered != edgeIDExhausted {
			t.Fatalf("recover() = %v, want %s", recovered, edgeIDExhausted)
		}
	}()
	repository.ReplaceFile(fileWithEdges("a.go", 1))
	t.Fatal("expected EDGE_ID_EXHAUSTED panic")
}
