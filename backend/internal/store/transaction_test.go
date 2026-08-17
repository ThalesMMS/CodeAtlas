package store_test

import (
	"errors"
	"fmt"
	"path/filepath"
	"testing"
	"time"

	"github.com/ThalesMMS/CodeAtlas/internal/changeset"
	"github.com/ThalesMMS/CodeAtlas/internal/domain"
	"github.com/ThalesMMS/CodeAtlas/internal/store"
)

var buildTime = time.Date(2026, 6, 24, 0, 0, 0, 0, time.UTC)

func openStore(t *testing.T) *store.Store {
	t.Helper()
	repository, err := store.Open(filepath.Join(t.TempDir(), "index.json"))
	if err != nil {
		t.Fatalf("Open() error = %v", err)
	}
	return repository
}

// parsedFile builds a file with two symbols (a file symbol and a function), so
// every committed state satisfies the invariant symbols == 2*files, edges == 0.
func parsedFile(path string) domain.ParsedFile {
	return domain.ParsedFile{
		File: domain.File{Path: path, Language: "go", Hash: path},
		Symbols: []domain.Symbol{
			{ID: path + ":file", Path: path, Name: path, Kind: "file", Range: domain.Range{Start: domain.Position{Line: 1, Column: 1}, End: domain.Position{Line: 3, Column: 1}}},
			{ID: path + ":fn", Path: path, Name: "fn", Kind: "function", Range: domain.Range{Start: domain.Position{Line: 2, Column: 1}, End: domain.Position{Line: 2, Column: 5}}},
		},
	}
}

func upsertChange(t *testing.T, path string) *changeset.ChangeSet {
	t.Helper()
	cs, err := changeset.NewBuilder().Upsert(parsedFile(path)).Build(buildTime)
	if err != nil {
		t.Fatalf("Build() error = %v", err)
	}
	return cs
}

func TestPrepareDoesNotMutateActiveState(t *testing.T) {
	t.Parallel()
	repository := openStore(t)
	repository.ReplaceFile(parsedFile("a.go"))
	baseVersion := repository.Version()
	baseFiles, baseSymbols, _, _, _ := repository.Counts()

	prepared, err := repository.Prepare(upsertChange(t, "b.go"))
	if err != nil {
		t.Fatalf("Prepare() error = %v", err)
	}

	// Active store is untouched until commit.
	files, symbols, _, _, _ := repository.Counts()
	if files != baseFiles || symbols != baseSymbols || repository.Version() != baseVersion {
		t.Fatalf("Prepare mutated active state: files=%d symbols=%d version=%d", files, symbols, repository.Version())
	}
	if hits := repository.Search("fn", 5); len(hits) != 1 {
		t.Fatalf("active search changed after Prepare: %d hits", len(hits))
	}

	if err := repository.CommitPrepared(prepared); err != nil {
		t.Fatalf("CommitPrepared() error = %v", err)
	}
	files, symbols, _, _, _ = repository.Counts()
	if files != baseFiles+1 || symbols != baseSymbols+2 {
		t.Fatalf("after commit files=%d symbols=%d, want %d/%d", files, symbols, baseFiles+1, baseSymbols+2)
	}
	if repository.Version() != prepared.NextVersion {
		t.Fatalf("version = %d, want %d", repository.Version(), prepared.NextVersion)
	}
}

func TestVersionConflictLeavesStateUnchanged(t *testing.T) {
	t.Parallel()
	repository := openStore(t)
	repository.ReplaceFile(parsedFile("a.go"))

	first, err := repository.Prepare(upsertChange(t, "b.go"))
	if err != nil {
		t.Fatalf("Prepare(first) error = %v", err)
	}
	second, err := repository.Prepare(upsertChange(t, "c.go"))
	if err != nil {
		t.Fatalf("Prepare(second) error = %v", err)
	}
	if first.ExpectedVersion != second.ExpectedVersion {
		t.Fatalf("expected versions differ: %d vs %d", first.ExpectedVersion, second.ExpectedVersion)
	}

	if err := repository.CommitPrepared(first); err != nil {
		t.Fatalf("CommitPrepared(first) error = %v", err)
	}
	if err := repository.CommitPrepared(second); !errors.Is(err, store.ErrVersionConflict) {
		t.Fatalf("CommitPrepared(second) error = %v, want ErrVersionConflict", err)
	}

	// Only the first commit's file (b.go) is present; c.go was rejected.
	if _, ok := repository.FileHash("b.go"); !ok {
		t.Fatal("first commit was not applied")
	}
	if _, ok := repository.FileHash("c.go"); ok {
		t.Fatal("rejected commit leaked into the store")
	}
}

func TestNoopCommitDoesNotIncrementVersion(t *testing.T) {
	t.Parallel()
	repository := openStore(t)
	repository.ReplaceFile(parsedFile("a.go"))
	version := repository.Version()

	noop, err := changeset.NewBuilder().AllowEmpty().Build(buildTime)
	if err != nil {
		t.Fatalf("Build(noop) error = %v", err)
	}
	prepared, err := repository.Prepare(noop)
	if err != nil {
		t.Fatalf("Prepare(noop) error = %v", err)
	}
	if !prepared.IsNoop() {
		t.Fatal("expected a no-op prepared commit")
	}
	if err := repository.CommitPrepared(prepared); err != nil {
		t.Fatalf("CommitPrepared(noop) error = %v", err)
	}
	if repository.Version() != version {
		t.Fatalf("no-op changed version to %d, want %d", repository.Version(), version)
	}
}

func TestConcurrentReadersNeverSeeIntermediateCounts(t *testing.T) {
	t.Parallel()
	repository := openStore(t)

	stop := make(chan struct{})
	readerDone := make(chan struct{})
	go func() {
		defer close(readerDone)
		for {
			select {
			case <-stop:
				return
			default:
			}
			files, symbols, edges, _, _ := repository.Counts()
			if symbols != files*2 || edges != 0 {
				t.Errorf("torn read: files=%d symbols=%d edges=%d", files, symbols, edges)
				return
			}
		}
	}()

	for i := 0; i < 300; i++ {
		prepared, err := repository.Prepare(upsertChange(t, fmt.Sprintf("pkg/f%d.go", i)))
		if err != nil {
			t.Fatalf("Prepare() error = %v", err)
		}
		if err := repository.CommitPrepared(prepared); err != nil {
			t.Fatalf("CommitPrepared() error = %v", err)
		}
	}
	close(stop)
	<-readerDone
}
