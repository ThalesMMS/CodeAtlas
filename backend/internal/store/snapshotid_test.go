package store

import (
	"encoding/json"
	"math"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/ThalesMMS/CodeAtlas/internal/apperror"
	"github.com/ThalesMMS/CodeAtlas/internal/changeset"
	"github.com/ThalesMMS/CodeAtlas/internal/domain"
)

func openSnapshotStore(t *testing.T) (*Store, string) {
	t.Helper()
	path := filepath.Join(t.TempDir(), "index.json")
	repository, err := Open(path)
	if err != nil {
		t.Fatalf("Open() error = %v", err)
	}
	return repository, path
}

func TestSnapshotIDIsDeterministicAndOrderInvariant(t *testing.T) {
	t.Parallel()
	// Two independently built states with identical content (different map order)
	// must hash to the same id.
	if a, b := computeSnapshotID(validFinalizedState()), computeSnapshotID(validFinalizedState()); a != b {
		t.Fatalf("identical content produced different ids: %s vs %s", a, b)
	}
	if !strings.HasPrefix(string(computeSnapshotID(validFinalizedState())), "sha256:") {
		t.Fatal("SnapshotID is not in sha256:<hex> form")
	}
}

func TestSnapshotIDIgnoresVolatileFields(t *testing.T) {
	t.Parallel()
	state := validFinalizedState()
	id := computeSnapshotID(state)
	state.indexedAt = state.indexedAt.Add(time.Hour) // volatile
	state.version = 9999                             // revision is identity, not content
	if computeSnapshotID(state) != id {
		t.Fatal("a volatile field changed the SnapshotID")
	}
}

func TestSnapshotIDChangesOnStructuralChange(t *testing.T) {
	t.Parallel()
	state := validFinalizedState()
	id := computeSnapshotID(state)
	file := state.files["pkg/svc.go"]
	file.Hash = strings.Repeat("b", 64)
	state.files["pkg/svc.go"] = file
	if computeSnapshotID(state) == id {
		t.Fatal("a file hash change did not change the SnapshotID")
	}
}

func TestRevisionAndSnapshotSemantics(t *testing.T) {
	t.Parallel()
	repository, _ := openSnapshotStore(t)
	if repository.Revision() != 0 {
		t.Fatalf("empty store revision = %d, want 0", repository.Revision())
	}

	commit(t, repository, "a.go")
	if repository.Revision() != 1 {
		t.Fatalf("first commit revision = %d, want 1", repository.Revision())
	}
	idAfterFirst := repository.SnapshotID()

	// A genuine no-op (empty change set) advances neither revision nor id.
	empty, err := changeset.NewBuilder().AllowEmpty().Build(time.Date(2026, 6, 24, 0, 0, 0, 0, time.UTC))
	if err != nil {
		t.Fatal(err)
	}
	prepared, err := repository.Prepare(empty)
	if err != nil {
		t.Fatal(err)
	}
	if err := repository.CommitPrepared(prepared); err != nil {
		t.Fatal(err)
	}
	if repository.Revision() != 1 || repository.SnapshotID() != idAfterFirst {
		t.Fatalf("no-op changed revision/id: rev=%d", repository.Revision())
	}

	// A structural change bumps revision exactly once and changes the id.
	commit(t, repository, "b.go")
	if repository.Revision() != 2 {
		t.Fatalf("second structural commit revision = %d, want 2", repository.Revision())
	}
	if repository.SnapshotID() == idAfterFirst {
		t.Fatal("structural change did not change the SnapshotID")
	}
}

func TestSnapshotMetadataRecordsIdentityAlgorithm(t *testing.T) {
	t.Parallel()
	repository, _ := openSnapshotStore(t)
	commit(t, repository, "a.go")
	metadata := repository.SnapshotMetadata()
	if metadata.IdentityAlgorithm != "v1" {
		t.Fatalf("IdentityAlgorithm = %q, want v1 (algorithm must be versioned in the snapshot)", metadata.IdentityAlgorithm)
	}
	if metadata.Schema != snapshotSchema {
		t.Fatalf("Schema = %d, want %d", metadata.Schema, snapshotSchema)
	}
}

func TestRevisionOverflowIsFatal(t *testing.T) {
	t.Parallel()
	repository, _ := openSnapshotStore(t)
	repository.current.version = math.MaxUint64
	defer func() {
		if recovered := recover(); recovered != revisionOverflow {
			t.Fatalf("recover() = %v, want %s", recovered, revisionOverflow)
		}
	}()
	repository.ReplaceFile(parsed("a.go"))
	t.Fatal("expected a REVISION_OVERFLOW panic")
}

func TestSnapshotMetadataPersistsAcrossReopen(t *testing.T) {
	t.Parallel()
	repository, path := openSnapshotStore(t)
	commit(t, repository, "a.go")
	if err := repository.Persist(); err != nil {
		t.Fatalf("Persist() error = %v", err)
	}
	wantID, wantRevision := repository.SnapshotID(), repository.Revision()

	reopened, err := Open(path)
	if err != nil {
		t.Fatalf("Open(reopen) error = %v", err)
	}
	if reopened.SnapshotID() != wantID {
		t.Fatalf("SnapshotID after reopen = %s, want %s", reopened.SnapshotID(), wantID)
	}
	if reopened.Revision() != wantRevision {
		t.Fatalf("Revision after reopen = %d, want %d", reopened.Revision(), wantRevision)
	}
}

func TestTamperedSnapshotIDIsRejected(t *testing.T) {
	t.Parallel()
	repository, path := openSnapshotStore(t)
	commit(t, repository, "a.go")
	if err := repository.Persist(); err != nil {
		t.Fatal(err)
	}

	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var raw map[string]any
	if err := json.Unmarshal(data, &raw); err != nil {
		t.Fatal(err)
	}
	raw["snapshotId"] = "sha256:deadbeefdeadbeef" // tamper without changing content
	tampered, _ := json.Marshal(raw)
	if err := os.WriteFile(path, tampered, 0o600); err != nil {
		t.Fatal(err)
	}

	_, err = Open(path)
	if err == nil {
		t.Fatal("Open accepted a snapshot whose stored id does not match its content")
	}
	if appErr, ok := apperror.As(err); !ok || appErr.Code != apperror.CodeStoreCorrupted {
		t.Fatalf("error = %v, want STORE_CORRUPTED", err)
	}
}

func TestLegacySnapshotWithoutIDMigrates(t *testing.T) {
	t.Parallel()
	path := filepath.Join(t.TempDir(), "index.json")
	legacy := diskSnapshot{
		Version:      snapshotVersion,
		StoreVersion: 1,
		Files:        []domain.File{{Path: "a.go", Language: "go", Hash: "a.go"}},
		Symbols:      []domain.Symbol{{ID: "a.go", Path: "a.go", Name: "a.go", Kind: "file", Range: domain.Range{Start: domain.Position{Line: 1, Column: 1}, End: domain.Position{Line: 2, Column: 1}}}},
		// No SnapshotID / SnapshotSchema: a legacy snapshot.
	}
	data, _ := json.MarshalIndent(legacy, "", "  ")
	if err := os.WriteFile(path, data, 0o600); err != nil {
		t.Fatal(err)
	}

	repository, err := Open(path)
	if err != nil {
		t.Fatalf("Open(legacy) error = %v, want successful migration", err)
	}
	if repository.SnapshotID() == "" {
		t.Fatal("legacy snapshot did not receive a computed SnapshotID")
	}
}

func TestReadViewIsCoherentAcrossMutation(t *testing.T) {
	t.Parallel()
	repository, _ := openSnapshotStore(t)
	commit(t, repository, "a.go")

	view := repository.Snapshot()
	pinnedID := view.Metadata().ID
	pinnedCount := len(view.AllSymbols())

	// Mutate the store after pinning the view.
	repository.ReplaceFile(parsed("b.go"))

	if view.Metadata().ID != pinnedID {
		t.Fatal("ReadView metadata changed after a store mutation")
	}
	if len(view.AllSymbols()) != pinnedCount {
		t.Fatal("ReadView reads changed after a store mutation")
	}
	if repository.SnapshotID() == pinnedID {
		t.Fatal("store SnapshotID did not advance after the mutation")
	}
}

func TestSnapshotConcurrentReadsAndCommits(t *testing.T) {
	t.Parallel()
	repository, _ := openSnapshotStore(t)
	var wg sync.WaitGroup
	for w := 0; w < 4; w++ {
		wg.Add(1)
		go func(worker int) {
			defer wg.Done()
			for i := 0; i < 25; i++ {
				repository.ReplaceFile(parsed("pkg/f" + string(rune('a'+worker)) + ".go"))
				view := repository.Snapshot()
				_ = view.Metadata().ID
				_ = view.AllSymbols()
				_ = repository.SnapshotID()
			}
		}(w)
	}
	wg.Wait()
}
