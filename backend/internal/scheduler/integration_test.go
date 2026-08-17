package scheduler

import (
	"context"
	"os"
	"path/filepath"
	"runtime"
	"testing"
	"time"

	"github.com/ThalesMMS/CodeAtlas/internal/ai"
	"github.com/ThalesMMS/CodeAtlas/internal/indexer"
	codeparser "github.com/ThalesMMS/CodeAtlas/internal/parser"
	"github.com/ThalesMMS/CodeAtlas/internal/repository"
	"github.com/ThalesMMS/CodeAtlas/internal/retrieval"
	"github.com/ThalesMMS/CodeAtlas/internal/watcher"
)

func TestIntegrationWatcherHintsCommitFinalStateOnce(t *testing.T) {
	root := t.TempDir()
	repo, err := repository.OpenJSON(filepath.Join(t.TempDir(), "index.json"))
	if err != nil {
		t.Fatal(err)
	}
	retriever := retrieval.NewHybrid(repo, ai.Disabled{}, false)
	idx := indexer.New(root, 1_500_000, codeparser.New(), repo, retriever)
	fakeWatcher := newFakeWorkspaceWatcher()
	clock := NewManualClock(time.Unix(1700000000, 0).UTC())
	controller, err := NewController(Options{
		Mode:              ModeNative,
		Debounce:          250 * time.Millisecond,
		MaxBatchDelay:     2 * time.Second,
		ReconcileInterval: time.Hour,
		Clock:             clock,
		Executor:          idx,
		WatcherFactory: func() (watcher.WorkspaceWatcher, error) {
			return fakeWatcher, nil
		},
	})
	if err != nil {
		t.Fatalf("NewController() error = %v", err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan struct{})
	go func() {
		controller.Run(ctx)
		close(done)
	}()
	<-controller.Started()

	writeIntegrationFile(t, root, "a.go", "package main\nfunc A() int { return 1 }\n")
	fakeWatcher.events <- watcher.WorkspaceEvent{Sequence: 1, Path: "a.go", Operations: watcher.WatchCreate, Kind: watcher.KindFile}
	fakeWatcher.events <- watcher.WorkspaceEvent{Sequence: 2, Path: "a.go", Operations: watcher.WatchWrite, Kind: watcher.KindFile}
	waitSchedulerState(t, controller, "debouncing")
	clock.Advance(250 * time.Millisecond)
	waitForHash(t, repo, "a.go")

	if repo.Version() != 1 {
		t.Fatalf("repository version = %d, want one commit", repo.Version())
	}
	if _, ok := repo.FileHash("a.go"); !ok {
		t.Fatal("a.go was not indexed")
	}

	cancel()
	<-done
}

func writeIntegrationFile(t *testing.T, root, relative, content string) {
	t.Helper()
	path := filepath.Join(root, filepath.FromSlash(relative))
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
}

func waitForHash(t *testing.T, repo repository.Store, path string) {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if _, ok := repo.FileHash(path); ok {
			return
		}
		runtime.Gosched()
	}
	t.Fatalf("%s was not indexed before deadline", path)
}
