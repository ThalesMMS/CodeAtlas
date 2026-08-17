package indexer

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/ThalesMMS/CodeAtlas/internal/domain"
	codeparser "github.com/ThalesMMS/CodeAtlas/internal/parser"
)

func TestBrokerMonotonicIDsAndObservableDrops(t *testing.T) {
	t.Parallel()
	broker := NewBroker()
	channel, cancel := broker.Subscribe()
	defer cancel()

	broker.Publish(domain.IndexEvent{Type: "a"})
	if first := <-channel; first.ID != "evt-1" {
		t.Fatalf("first id = %q, want evt-1", first.ID)
	}
	broker.Publish(domain.IndexEvent{Type: "b"})
	if second := <-channel; second.ID != "evt-2" {
		t.Fatalf("second id = %q, want evt-2", second.ID)
	}

	// Overflow the 16-slot buffer without draining; drops must be observable.
	for i := 0; i < 30; i++ {
		broker.Publish(domain.IndexEvent{Type: "x"})
	}
	if broker.Dropped() == 0 {
		t.Fatal("subscriber overflow was dropped silently (Dropped() == 0)")
	}
}

func drainEvents(t *testing.T, channel <-chan domain.IndexEvent) []domain.IndexEvent {
	t.Helper()
	events := make([]domain.IndexEvent, 0)
	for {
		select {
		case event := <-channel:
			events = append(events, event)
		default:
			return events
		}
	}
}

func TestScanEmitsSingleAggregatedCommitEvent(t *testing.T) {
	t.Parallel()
	indexer, root, _ := newTestIndexer(t, codeparser.New(), nil, false)
	writeSource(t, root, "a.go", goSource)
	writeSource(t, root, "b.go", goSource)
	channel, cancel := indexer.Broker().Subscribe()
	defer cancel()

	if err := indexer.Scan(context.Background()); err != nil {
		t.Fatalf("Scan() error = %v", err)
	}
	events := drainEvents(t, channel)

	var commit *domain.IndexEvent
	for i := range events {
		switch events[i].Type {
		case "workspace.files.changed":
			commit = &events[i]
		case "file-indexed", "file-removed":
			t.Fatalf("per-file event emitted: %s", events[i].Type)
		}
	}
	if commit == nil {
		t.Fatalf("no aggregated commit event among %d events", len(events))
	}
	if commit.ChangeSetID == "" || commit.StoreVersion == 0 {
		t.Fatalf("commit event missing changeSetId/storeVersion: %+v", commit)
	}
	if commit.Counts["added"] != 2 || commit.Counts["total"] != 2 {
		t.Fatalf("counts = %v, want added=2 total=2", commit.Counts)
	}
}

func TestScanPublishesWorkspaceChangeSinkAfterCommitWithHashes(t *testing.T) {
	t.Parallel()
	indexer, root, repository := newTestIndexer(t, codeparser.New(), nil, false)
	writeSource(t, root, "a.go", goSource)
	writeSource(t, root, "b.go", "package sample\n\nfunc Other() int { return 1 }\n")
	if err := indexer.Scan(context.Background()); err != nil {
		t.Fatalf("setup Scan() error = %v", err)
	}
	version := repository.Version()
	oldA, _ := repository.FileHash("a.go")
	oldB, _ := repository.FileHash("b.go")

	writeSource(t, root, "a.go", "package sample\n\nfunc Run() int { return 2 }\n")
	writeSource(t, root, "c.go", "package sample\n\nfunc Added() int { return 3 }\n")
	if err := os.Remove(filepath.Join(root, "b.go")); err != nil {
		t.Fatal(err)
	}

	changes := make(chan domain.PublishedWorkspaceChange, 1)
	indexer.SetWorkspaceChangeSink(func(_ context.Context, change domain.PublishedWorkspaceChange) {
		if repository.Version() != version+1 {
			t.Errorf("sink ran before commit: repository version = %d, want %d", repository.Version(), version+1)
		}
		changes <- change
	})

	if err := indexer.ReconcileFull(context.Background(), version); err != nil {
		t.Fatalf("ReconcileFull() error = %v", err)
	}

	var change domain.PublishedWorkspaceChange
	select {
	case change = <-changes:
	default:
		t.Fatal("workspace change sink was not called")
	}
	if change.OperationID == "" || change.SnapshotID != repository.SnapshotID() || change.Revision != repository.Revision() {
		t.Fatalf("change provenance = %#v, want current committed snapshot", change)
	}
	if len(change.Changes) != 3 {
		t.Fatalf("changes = %#v, want 3 file changes", change.Changes)
	}
	byPath := make(map[string]domain.PublishedFileChange)
	for _, file := range change.Changes {
		byPath[file.Path] = file
		if file.Origin != domain.FileChangeOriginExternal {
			t.Fatalf("%s origin = %q, want external", file.Path, file.Origin)
		}
	}
	if got := byPath["a.go"]; got.Kind != domain.FileChangeModified || got.OldHash != oldA || got.NewHash == "" || got.NewHash == oldA {
		t.Fatalf("a.go change = %#v, want modified with old/new hashes", got)
	}
	if got := byPath["b.go"]; got.Kind != domain.FileChangeRemoved || got.OldHash != oldB || got.NewHash != "" {
		t.Fatalf("b.go change = %#v, want removed with old hash", got)
	}
	if got := byPath["c.go"]; got.Kind != domain.FileChangeAdded || got.OldHash != "" || got.NewHash == "" {
		t.Fatalf("c.go change = %#v, want added with new hash", got)
	}
}

func TestWorkspaceChangeSinkCompletesBeforeCommitEvent(t *testing.T) {
	t.Parallel()
	indexer, root, repository := newTestIndexer(t, codeparser.New(), nil, false)
	writeSource(t, root, "a.go", goSource)
	if err := indexer.Scan(context.Background()); err != nil {
		t.Fatalf("setup Scan() error = %v", err)
	}
	version := repository.Version()
	writeSource(t, root, "a.go", "package sample\n\nfunc Run() int { return 2 }\n")

	events, cancel := indexer.Broker().Subscribe()
	defer cancel()
	sinkStarted := make(chan struct{})
	releaseSink := make(chan struct{})
	indexer.SetWorkspaceChangeSink(func(_ context.Context, _ domain.PublishedWorkspaceChange) {
		close(sinkStarted)
		<-releaseSink
	})
	done := make(chan error, 1)
	go func() {
		done <- indexer.ReconcileFull(context.Background(), version)
	}()

	select {
	case <-sinkStarted:
	case err := <-done:
		t.Fatalf("ReconcileFull() returned before sink: %v", err)
	case <-time.After(5 * time.Second):
		t.Fatal("workspace change sink did not start")
	}
	commitPublishedEarly := false
	draining := true
	for draining {
		select {
		case event := <-events:
			commitPublishedEarly = commitPublishedEarly || event.Type == "workspace.files.changed"
		default:
			draining = false
		}
	}
	close(releaseSink)
	if err := <-done; err != nil {
		t.Fatalf("ReconcileFull() error = %v", err)
	}
	if commitPublishedEarly {
		t.Fatal("workspace.files.changed was visible before the workspace change sink completed")
	}
	waitIndexEventType(t, events, "workspace.files.changed")
}

func TestScanPublishesBecameIgnoredWorkspaceChangeKind(t *testing.T) {
	t.Parallel()
	indexer, root, repository := newTestIndexer(t, codeparser.New(), nil, false)
	writeSource(t, root, "a.go", goSource)
	if err := indexer.Scan(context.Background()); err != nil {
		t.Fatalf("setup Scan() error = %v", err)
	}
	version := repository.Version()

	writeSource(t, root, ".gitignore", "a.go\n")
	changes := make(chan domain.PublishedWorkspaceChange, 1)
	indexer.SetWorkspaceChangeSink(func(_ context.Context, change domain.PublishedWorkspaceChange) {
		changes <- change
	})
	if err := indexer.ReconcileFull(context.Background(), version); err != nil {
		t.Fatalf("ReconcileFull() error = %v", err)
	}

	var change domain.PublishedWorkspaceChange
	select {
	case change = <-changes:
	default:
		t.Fatal("workspace change sink was not called")
	}
	if len(change.Changes) != 1 || change.Changes[0].Path != "a.go" || change.Changes[0].Kind != domain.FileChangeBecameIgnored {
		t.Fatalf("changes = %#v, want a.go became_ignored", change.Changes)
	}
}

func TestScanParseFailureEmitsQuarantineNotPrepareFailure(t *testing.T) {
	t.Parallel()
	indexer, root, _ := newTestIndexer(t, failingParser{}, nil, false)
	writeSource(t, root, "a.go", goSource)
	channel, cancel := indexer.Broker().Subscribe()
	defer cancel()

	_ = indexer.Scan(context.Background())
	events := drainEvents(t, channel)

	var sawFailed, sawCommit, sawQuarantine bool
	for _, event := range events {
		if event.Type == "index.prepare.failed" {
			sawFailed = true
		}
		if event.Type == "index.file.quarantined" {
			sawQuarantine = true
			if event.Path != "a.go" || event.Error == nil || event.Error.Code != "SOURCE_PARSE_FAILED" {
				t.Fatalf("quarantine event = %#v", event)
			}
		}
		if event.Type == "workspace.files.changed" {
			sawCommit = true
		}
	}
	if sawFailed {
		t.Fatal("a per-file parse failure emitted index.prepare.failed")
	}
	if !sawQuarantine {
		t.Fatal("a parse failure did not emit index.file.quarantined")
	}
	if sawCommit {
		t.Fatal("a commit event was emitted for an all-quarantined no-op scan")
	}
}
