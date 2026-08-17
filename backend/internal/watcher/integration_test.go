package watcher

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestIntegrationFileLifecycleAndIgnoredDirectories(t *testing.T) {
	root := t.TempDir()
	w, err := New(root)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	defer w.Close()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	if err := w.Start(ctx); err != nil {
		t.Fatalf("Start: %v", err)
	}

	writeFile(t, root, "a.go", "package main\n")
	waitForOperation(t, w.Events(), "a.go", WatchCreate|WatchWrite)

	writeFile(t, root, "a.go", "package main\nvar A = 1\n")
	waitForOperation(t, w.Events(), "a.go", WatchWrite)

	if err := os.Rename(filepath.Join(root, "a.go"), filepath.Join(root, "b.go")); err != nil {
		t.Fatal(err)
	}
	waitForOperation(t, w.Events(), "a.go", WatchRename|WatchRemove)

	if err := os.Remove(filepath.Join(root, "b.go")); err != nil {
		t.Fatal(err)
	}
	waitForOperation(t, w.Events(), "b.go", WatchRemove|WatchRename)

	if err := os.MkdirAll(filepath.Join(root, "node_modules", "pkg"), 0o700); err != nil {
		t.Fatal(err)
	}
	writeFile(t, root, "node_modules/pkg/ignored.go", "package ignored\n")
	if event, ok := eventWithin(t, w.Events(), 250*time.Millisecond, func(event WorkspaceEvent) bool {
		return event.Path == "node_modules/pkg/ignored.go"
	}); ok {
		t.Fatalf("received ignored node_modules event: %+v", event)
	}
}

func waitForOperation(t *testing.T, events <-chan WorkspaceEvent, path string, acceptable WatchOperation) WorkspaceEvent {
	t.Helper()
	return waitForEvent(t, events, func(event WorkspaceEvent) bool {
		return event.Path == path && event.Operations&acceptable != 0
	})
}

func eventWithin(t *testing.T, events <-chan WorkspaceEvent, duration time.Duration, match func(WorkspaceEvent) bool) (WorkspaceEvent, bool) {
	t.Helper()
	timer := time.NewTimer(duration)
	defer timer.Stop()
	for {
		select {
		case event := <-events:
			if match(event) {
				return event, true
			}
		case <-timer.C:
			return WorkspaceEvent{}, false
		}
	}
}
