package mutation

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/ThalesMMS/CodeAtlas/internal/domain"
)

func TestMemoryRegistryMatchesPublishedSelfWriteByHashAndRevision(t *testing.T) {
	t.Parallel()
	clock := fixedClock(time.Unix(1700000000, 0).UTC())
	registry := NewMemoryRegistry(RegistryConfig{
		MaxTotalEntries:   16,
		MaxEntriesPerPath: 4,
		DefaultTTL:        time.Minute,
		Now:               clock.Now,
	})
	defer registry.Close()

	mutation := domain.InternalMutation{
		ID:                   "mut-1",
		TransactionID:        "tx-1",
		Path:                 "pkg/a.go",
		PreviousContentHash:  "sha256:old",
		PublishedContentHash: "sha256:new",
	}
	if err := registry.Stage(context.Background(), mutation); err != nil {
		t.Fatalf("Stage() error = %v", err)
	}
	if err := registry.MarkPublished(context.Background(), mutation.ID, MutationCommitResult{
		SnapshotID:  "sha256:snap",
		Revision:    3,
		CommittedAt: clock.Now(),
	}); err != nil {
		t.Fatalf("MarkPublished() error = %v", err)
	}

	match, err := registry.Match(context.Background(), domain.FileObservation{
		Path:             "pkg/a.go",
		Exists:           true,
		ContentHash:      "sha256:new",
		StoreContentHash: "sha256:new",
		StoreRevision:    3,
		HintSequenceMin:  10,
		HintSequenceMax:  12,
		ObservedAt:       clock.Now(),
	})
	if err != nil {
		t.Fatalf("Match() error = %v", err)
	}
	if !match.Matched || match.Classification != domain.ObservationSelfWriteConfirmed || match.MutationID != mutation.ID {
		t.Fatalf("match = %#v, want self_write_confirmed for %s", match, mutation.ID)
	}
	if err := registry.MarkObserved(context.Background(), mutation.ID, 12); err != nil {
		t.Fatalf("MarkObserved() error = %v", err)
	}

	snapshot := registry.Snapshot()
	if snapshot.PublishedCount != 0 || snapshot.ObservedCount != 1 || snapshot.SelfWriteConfirmedTotal != 1 {
		t.Fatalf("snapshot = %#v, want one observed self-write", snapshot)
	}
}

func TestMemoryRegistryDoesNotMatchDifferentHashOrNewerMutation(t *testing.T) {
	t.Parallel()
	clock := fixedClock(time.Unix(1700000000, 0).UTC())
	registry := NewMemoryRegistry(RegistryConfig{MaxTotalEntries: 16, MaxEntriesPerPath: 4, DefaultTTL: time.Minute, Now: clock.Now})
	defer registry.Close()

	first := domain.InternalMutation{ID: "mut-1", TransactionID: "tx-1", Path: "a.go", PreviousContentHash: "sha256:old", PublishedContentHash: "sha256:one"}
	second := domain.InternalMutation{ID: "mut-2", TransactionID: "tx-2", Path: "a.go", PreviousContentHash: "sha256:one", PublishedContentHash: "sha256:two"}
	for revision, mutation := range []domain.InternalMutation{first, second} {
		if err := registry.Stage(context.Background(), mutation); err != nil {
			t.Fatalf("Stage(%s) error = %v", mutation.ID, err)
		}
		if err := registry.MarkPublished(context.Background(), mutation.ID, MutationCommitResult{
			SnapshotID: "sha256:snap", Revision: domain.Revision(revision + 1), CommittedAt: clock.Now(),
		}); err != nil {
			t.Fatalf("MarkPublished(%s) error = %v", mutation.ID, err)
		}
	}

	match, err := registry.Match(context.Background(), domain.FileObservation{
		Path: "a.go", Exists: true, ContentHash: "sha256:one", StoreContentHash: "sha256:one", StoreRevision: 2, ObservedAt: clock.Now(),
	})
	if err != nil {
		t.Fatalf("Match(intermediate) error = %v", err)
	}
	if match.Matched || match.Classification != domain.ObservationExternalChange {
		t.Fatalf("intermediate match = %#v, want external change because newer mutation exists", match)
	}
	if err := registry.MarkExternal(context.Background(), match.MutationID); err != nil {
		t.Fatalf("MarkExternal() error = %v", err)
	}
	if registry.Snapshot().ExternalAfterInternalTotal != 1 {
		t.Fatalf("external-after-internal total = %d, want 1", registry.Snapshot().ExternalAfterInternalTotal)
	}

	match, err = registry.Match(context.Background(), domain.FileObservation{
		Path: "a.go", Exists: true, ContentHash: "sha256:one", StoreContentHash: "sha256:one", StoreRevision: 2, ObservedAt: clock.Now(),
	})
	if err != nil {
		t.Fatalf("Match(repeated) error = %v", err)
	}
	if err := registry.MarkExternal(context.Background(), match.MutationID); err != nil {
		t.Fatalf("MarkExternal(repeated) error = %v", err)
	}
	if match.Classification != domain.ObservationUnchanged || registry.Snapshot().ExternalAfterInternalTotal != 1 {
		t.Fatalf("repeated match = %#v snapshot = %#v, want retired mutation counted once", match, registry.Snapshot())
	}

	registry = NewMemoryRegistry(RegistryConfig{MaxTotalEntries: 16, MaxEntriesPerPath: 4, DefaultTTL: time.Minute, Now: clock.Now})
	defer registry.Close()
	if err := registry.Stage(context.Background(), second); err != nil {
		t.Fatalf("Stage(%s) error = %v", second.ID, err)
	}
	if err := registry.MarkPublished(context.Background(), second.ID, MutationCommitResult{
		SnapshotID: "sha256:snap", Revision: 2, CommittedAt: clock.Now(),
	}); err != nil {
		t.Fatalf("MarkPublished(%s) error = %v", second.ID, err)
	}

	match, err = registry.Match(context.Background(), domain.FileObservation{
		Path: "a.go", Exists: true, ContentHash: "sha256:external", StoreContentHash: "sha256:two", StoreRevision: 2, ObservedAt: clock.Now(),
	})
	if err != nil {
		t.Fatalf("Match(external) error = %v", err)
	}
	if match.Matched || match.Classification != domain.ObservationExternalChange {
		t.Fatalf("external match = %#v, want external change for different hash", match)
	}
	if err := registry.MarkExternal(context.Background(), match.MutationID); err != nil {
		t.Fatalf("MarkExternal(external) error = %v", err)
	}
	if registry.Snapshot().ExternalAfterInternalTotal == 0 {
		t.Fatal("external-after-internal metric was not recorded")
	}
}

func TestMemoryRegistryExpiresAndEnforcesCapacity(t *testing.T) {
	t.Parallel()
	clock := fixedClock(time.Unix(1700000000, 0).UTC())
	registry := NewMemoryRegistry(RegistryConfig{MaxTotalEntries: 1, MaxEntriesPerPath: 1, DefaultTTL: time.Second, Now: clock.Now})
	defer registry.Close()

	if err := registry.Stage(context.Background(), domain.InternalMutation{ID: "mut-1", Path: "a.go", PublishedContentHash: "sha256:new"}); err != nil {
		t.Fatalf("Stage() error = %v", err)
	}
	if err := registry.Stage(context.Background(), domain.InternalMutation{ID: "mut-2", Path: "b.go", PublishedContentHash: "sha256:new"}); !errors.Is(err, ErrRegistryFull) {
		t.Fatalf("Stage(over capacity) error = %v, want ErrRegistryFull", err)
	}
	clock.Advance(2 * time.Second)
	expired := registry.Expire(clock.Now())
	if len(expired) != 1 || expired[0].ID != "mut-1" {
		t.Fatalf("expired = %#v, want mut-1", expired)
	}
	if snapshot := registry.Snapshot(); snapshot.ExpiredCount != 1 || snapshot.StagedCount != 0 {
		t.Fatalf("snapshot = %#v, want expired mutation swept", snapshot)
	}
}

func TestMemoryRegistryRejectsUnsafeWorkspacePath(t *testing.T) {
	t.Parallel()
	registry := NewMemoryRegistry(RegistryConfig{})
	err := registry.Stage(context.Background(), domain.InternalMutation{
		ID: "mut-unsafe", Path: `..\secret.go`, PublishedContentHash: "sha256:new",
	})
	if !errors.Is(err, ErrMutationInvalid) {
		t.Fatalf("Stage(unsafe path) error = %v, want ErrMutationInvalid", err)
	}
}

func TestMemoryRegistryBoundsTerminalDiagnostics(t *testing.T) {
	t.Parallel()
	clock := fixedClock(time.Unix(1700000000, 0).UTC())
	registry := NewMemoryRegistry(RegistryConfig{MaxTotalEntries: 4, MaxTerminalEntries: 1, DefaultTTL: time.Minute, Now: clock.Now})
	defer registry.Close()

	for _, mutation := range []domain.InternalMutation{
		{ID: "mut-1", Path: "a.go", PublishedContentHash: "sha256:one"},
		{ID: "mut-2", Path: "b.go", PublishedContentHash: "sha256:two"},
	} {
		if err := registry.Stage(context.Background(), mutation); err != nil {
			t.Fatalf("Stage(%s) error = %v", mutation.ID, err)
		}
		if err := registry.MarkPublished(context.Background(), mutation.ID, MutationCommitResult{
			SnapshotID: "sha256:snap", Revision: 1, CommittedAt: clock.Now(),
		}); err != nil {
			t.Fatalf("MarkPublished(%s) error = %v", mutation.ID, err)
		}
		if err := registry.MarkObserved(context.Background(), mutation.ID, 0); err != nil {
			t.Fatalf("MarkObserved(%s) error = %v", mutation.ID, err)
		}
	}

	snapshot := registry.Snapshot()
	if snapshot.SelfWriteConfirmedTotal != 2 || len(snapshot.Entries) != 1 || snapshot.ObservedCount != 1 {
		t.Fatalf("snapshot = %#v, want capped terminal entry and total count retained", snapshot)
	}
}

type fixedClock time.Time

func (c *fixedClock) Now() time.Time { return time.Time(*c) }

func (c *fixedClock) Advance(d time.Duration) { *c = fixedClock(time.Time(*c).Add(d)) }
