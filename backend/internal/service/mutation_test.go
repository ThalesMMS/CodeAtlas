package service

import (
	"context"
	"testing"
	"time"

	"github.com/ThalesMMS/CodeAtlas/internal/domain"
	"github.com/ThalesMMS/CodeAtlas/internal/mutation"
)

func TestCommitStagesAndPublishesInternalMutation(t *testing.T) {
	t.Parallel()
	f := setupCommit(t)
	registry := mutation.NewMemoryRegistry(mutation.RegistryConfig{DefaultTTL: time.Minute})
	defer registry.Close()
	f.coord.SetMutationRegistry(registry)

	prepared := f.prepare(t)
	version, err := f.coord.Commit(prepared, nil)
	if err != nil {
		t.Fatalf("Commit() error = %v", err)
	}
	if version != 1 {
		t.Fatalf("version = %d, want 1", version)
	}

	match, err := registry.Match(context.Background(), domain.FileObservation{
		Path:             "a.go",
		Exists:           true,
		ContentHash:      prepared.NewContentHash(),
		StoreContentHash: prepared.NewContentHash(),
		StoreRevision:    domain.Revision(version),
		ObservedAt:       time.Now().UTC(),
	})
	if err != nil {
		t.Fatalf("Match() error = %v", err)
	}
	if !match.Matched || match.Classification != domain.ObservationSelfWriteConfirmed {
		t.Fatalf("match = %#v, want published self-write", match)
	}
}

func TestCommitCancelsStagedMutationOnRollback(t *testing.T) {
	t.Parallel()
	f := setupCommit(t)
	registry := mutation.NewMemoryRegistry(mutation.RegistryConfig{DefaultTTL: time.Minute})
	defer registry.Close()
	f.coord.SetMutationRegistry(registry)

	prepared := f.prepare(t)
	f.store.ReplaceFile(domain.ParsedFile{
		File:    domain.File{Path: "other.go", Hash: "x"},
		Symbols: []domain.Symbol{{ID: "other", Path: "other.go", Kind: "file", Range: domain.Range{Start: domain.Position{Line: 1, Column: 1}, End: domain.Position{Line: 2, Column: 1}}}},
	})
	if _, err := f.coord.Commit(prepared, nil); err == nil {
		t.Fatal("Commit() succeeded despite stale prepared index")
	}
	if snapshot := registry.Snapshot(); snapshot.StagedCount != 0 || snapshot.PublishedCount != 0 {
		t.Fatalf("snapshot = %#v, want staged mutation cancelled after rollback", snapshot)
	}
}

func TestCommitDoesNotFailWhenMutationRegistryIsFull(t *testing.T) {
	t.Parallel()
	f := setupCommit(t)
	registry := mutation.NewMemoryRegistry(mutation.RegistryConfig{MaxTotalEntries: 1, DefaultTTL: time.Minute})
	defer registry.Close()
	if err := registry.Stage(context.Background(), domain.InternalMutation{
		ID:                   "existing",
		Path:                 "other.go",
		PublishedContentHash: "sha256:other",
	}); err != nil {
		t.Fatalf("Stage(existing) error = %v", err)
	}
	f.coord.SetMutationRegistry(registry)

	version, err := f.coord.Commit(f.prepare(t), nil)
	if err != nil {
		t.Fatalf("Commit() error = %v, want save to ignore registry capacity: %v", err, registry.Snapshot())
	}
	if version != 1 {
		t.Fatalf("version = %d, want 1", version)
	}
	source, storeVersion := f.reopenState(t)
	if source != newSource || storeVersion != 1 {
		t.Fatalf("after commit source/version = %q/%d, want new/1", source, storeVersion)
	}
}
