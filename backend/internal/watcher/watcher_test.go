package watcher

import (
	"context"
	"errors"
	"math"
	"os"
	"path/filepath"
	"sort"
	"sync"
	"testing"
	"time"
)

type fakeNativeWatcher struct {
	events chan NativeEvent
	errs   chan error

	mu      sync.Mutex
	added   []string
	removed []string
	closed  bool
	addErr  map[string]error
}

func newFakeNativeWatcher() *fakeNativeWatcher {
	return &fakeNativeWatcher{
		events: make(chan NativeEvent, 32),
		errs:   make(chan error, 32),
		addErr: map[string]error{},
	}
}

func (f *fakeNativeWatcher) Add(path string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if err := f.addErr[filepath.Clean(path)]; err != nil {
		return err
	}
	f.added = append(f.added, filepath.Clean(path))
	return nil
}

func (f *fakeNativeWatcher) Remove(path string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.removed = append(f.removed, filepath.Clean(path))
	return nil
}

func (f *fakeNativeWatcher) Close() error {
	f.mu.Lock()
	f.closed = true
	f.mu.Unlock()
	return nil
}

func (f *fakeNativeWatcher) Events() <-chan NativeEvent { return f.events }
func (f *fakeNativeWatcher) Errors() <-chan error       { return f.errs }

func (f *fakeNativeWatcher) send(event NativeEvent) { f.events <- event }

func (f *fakeNativeWatcher) closeEvents() { close(f.events) }

func (f *fakeNativeWatcher) addedRel(root string) []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	out := make([]string, 0, len(f.added))
	for _, path := range f.added {
		rel, err := filepath.Rel(root, path)
		if err != nil {
			out = append(out, path)
			continue
		}
		out = append(out, filepath.ToSlash(rel))
	}
	sort.Strings(out)
	return out
}

func TestStartAddsRecursiveWatchesAndSkipsIgnoredDirs(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	mkdirAll(t, root,
		"pkg/sub",
		".git/objects",
		".codeatlas",
		"node_modules/mod",
		"backend/internal/treesitter/grammars/go",
	)
	writeFile(t, root, ".codeatlasignore", "backend/internal/treesitter/grammars/\n")
	fake := newFakeNativeWatcher()
	w, err := New(root, WithNativeWatcher(fake))
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	defer w.Close()

	if err := w.Start(context.Background()); err != nil {
		t.Fatalf("Start: %v", err)
	}

	got := fake.addedRel(w.root)
	want := []string{".", "backend", "backend/internal", "backend/internal/treesitter", "pkg", "pkg/sub"}
	if !sameStrings(got, want) {
		t.Fatalf("watched dirs = %v, want %v", got, want)
	}
	if snapshot := w.Snapshot(); snapshot.State != StateRunning || snapshot.WatchedDirectoryCount != len(want) {
		t.Fatalf("snapshot = %+v, want running with %d dirs", snapshot, len(want))
	}
}

func TestCreateDirectoryAddsSubtreeAndEmitsExistingFiles(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	fake := newFakeNativeWatcher()
	w, err := New(root, WithNativeWatcher(fake))
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	defer w.Close()
	if err := w.Start(context.Background()); err != nil {
		t.Fatalf("Start: %v", err)
	}

	mkdirAll(t, w.root, "newdir/nested")
	writeFile(t, w.root, "newdir/nested/file.go", "package nested\n")
	fake.send(NativeEvent{Name: filepath.Join(w.root, "newdir"), Op: NativeCreate})

	waitForEvent(t, w.Events(), func(event WorkspaceEvent) bool {
		return event.Path == "newdir/nested/file.go" && event.Operations.Has(WatchCreate) && event.Source == SourceWatcherInternal
	})
	if got := fake.addedRel(w.root); !containsAll(got, "newdir", "newdir/nested") {
		t.Fatalf("watched dirs after create = %v, want new subtree", got)
	}
}

func TestNormalizesCombinedFlagsMetadataAndSequenceOverflow(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	writeFile(t, root, "a.go", "package main\n")
	fake := newFakeNativeWatcher()
	w, err := New(root, WithNativeWatcher(fake))
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	defer w.Close()
	w.sequence = math.MaxUint64
	if err := w.Start(context.Background()); err != nil {
		t.Fatalf("Start: %v", err)
	}

	fake.send(NativeEvent{Name: filepath.Join(w.root, "a.go"), Op: NativeCreate | NativeWrite | NativeChmod})
	event := waitForEvent(t, w.Events(), func(event WorkspaceEvent) bool { return event.Path == "a.go" })
	if event.Sequence != 1 {
		t.Fatalf("sequence = %d, want wrap to 1", event.Sequence)
	}
	for _, op := range []WatchOperation{WatchCreate, WatchWrite, WatchMetadata} {
		if !event.Operations.Has(op) {
			t.Fatalf("event operations = %s, missing %s", event.Operations, op)
		}
	}
	if event.Kind != KindFile || event.Source != SourceFSNotify || event.ObservedAt.Location() != time.UTC {
		t.Fatalf("event = %+v, want file/fsnotify/UTC", event)
	}
}

func TestNativeEventChannelCloseDesynchronizes(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	fake := newFakeNativeWatcher()
	w, err := New(root, WithNativeWatcher(fake))
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	defer w.Close()
	if err := w.Start(context.Background()); err != nil {
		t.Fatalf("Start: %v", err)
	}

	fake.closeEvents()
	event := waitForEvent(t, w.Events(), func(event WorkspaceEvent) bool {
		return event.Operations.Has(WatchRescanRequired) && event.ReasonCode == ErrCodeEventChannelClosed
	})
	if event.Path != "" {
		t.Fatalf("desync event path = %q, want no path", event.Path)
	}
	errEvent := waitForError(t, w.Errors())
	var watcherErr *WatcherError
	if !errors.As(errEvent, &watcherErr) || watcherErr.Code != ErrCodeEventChannelClosed {
		t.Fatalf("error = %v, want %s", errEvent, ErrCodeEventChannelClosed)
	}
	waitForSnapshot(t, w, StateDesynchronized)
}

func TestBackpressureOverflowDesynchronizes(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	writeFile(t, root, "a.go", "package main\n")
	writeFile(t, root, "b.go", "package main\n")
	fake := newFakeNativeWatcher()
	w, err := New(root, WithNativeWatcher(fake), WithEventBufferSize(1))
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	defer w.Close()
	if err := w.Start(context.Background()); err != nil {
		t.Fatalf("Start: %v", err)
	}

	fake.send(NativeEvent{Name: filepath.Join(w.root, "a.go"), Op: NativeWrite})
	fake.send(NativeEvent{Name: filepath.Join(w.root, "b.go"), Op: NativeWrite})

	waitForSnapshot(t, w, StateDesynchronized)
	snapshot := w.Snapshot()
	if snapshot.OverflowCount == 0 || snapshot.DesyncCount == 0 {
		t.Fatalf("snapshot = %+v, want overflow/desync counts", snapshot)
	}

	first := waitForEvent(t, w.Events(), func(event WorkspaceEvent) bool {
		return event.Path == "a.go" && event.Operations.Has(WatchWrite)
	})
	if first.Operations.Has(WatchRescanRequired) {
		t.Fatalf("first event = %+v, want the accepted file event", first)
	}
	rescan := waitForEvent(t, w.Events(), func(event WorkspaceEvent) bool {
		return event.Operations.Has(WatchRescanRequired)
	})
	if rescan.ReasonCode != ErrCodeBackpressureOverflow {
		t.Fatalf("rescan reason = %q, want %q", rescan.ReasonCode, ErrCodeBackpressureOverflow)
	}
	w.AcknowledgeResync()
	if snapshot := w.Snapshot(); snapshot.State != StateRunning || snapshot.LastError != "" {
		t.Fatalf("snapshot after acknowledged rescan = %+v, want recovered running state", snapshot)
	}
}

func TestOutsideWorkspaceEventIsRejected(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	outside := filepath.Join(t.TempDir(), "outside.go")
	writePath(t, outside, "package outside\n")
	fake := newFakeNativeWatcher()
	w, err := New(root, WithNativeWatcher(fake))
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	defer w.Close()
	if err := w.Start(context.Background()); err != nil {
		t.Fatalf("Start: %v", err)
	}

	fake.send(NativeEvent{Name: outside, Op: NativeWrite})
	errEvent := waitForError(t, w.Errors())
	var watcherErr *WatcherError
	if !errors.As(errEvent, &watcherErr) || watcherErr.Code != ErrCodePathOutsideWorkspace {
		t.Fatalf("error = %v, want %s", errEvent, ErrCodePathOutsideWorkspace)
	}
}

func TestCloseIsConcurrentAndIdempotent(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	fake := newFakeNativeWatcher()
	w, err := New(root, WithNativeWatcher(fake))
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	if err := w.Start(context.Background()); err != nil {
		t.Fatalf("Start: %v", err)
	}

	var wg sync.WaitGroup
	for i := 0; i < 8; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if err := w.Close(); err != nil {
				t.Errorf("Close: %v", err)
			}
		}()
	}
	wg.Wait()
	if snapshot := w.Snapshot(); snapshot.State != StateClosed {
		t.Fatalf("state = %s, want closed", snapshot.State)
	}
}

func mkdirAll(t *testing.T, root string, rels ...string) {
	t.Helper()
	for _, rel := range rels {
		if err := os.MkdirAll(filepath.Join(root, filepath.FromSlash(rel)), 0o700); err != nil {
			t.Fatal(err)
		}
	}
}

func writeFile(t *testing.T, root, rel, content string) {
	t.Helper()
	writePath(t, filepath.Join(root, filepath.FromSlash(rel)), content)
}

func writePath(t *testing.T, path, content string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
}

func waitForEvent(t *testing.T, events <-chan WorkspaceEvent, match func(WorkspaceEvent) bool) WorkspaceEvent {
	t.Helper()
	deadline := time.After(2 * time.Second)
	for {
		select {
		case event := <-events:
			if match(event) {
				return event
			}
		case <-deadline:
			t.Fatal("timed out waiting for watcher event")
		}
	}
}

func waitForError(t *testing.T, errors <-chan error) error {
	t.Helper()
	select {
	case err := <-errors:
		return err
	case <-time.After(2 * time.Second):
		t.Fatal("timed out waiting for watcher error")
		return nil
	}
}

func waitForSnapshot(t *testing.T, w WorkspaceWatcher, state WatcherState) {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if snapshot := w.Snapshot(); snapshot.State == state {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("timed out waiting for state %s; last snapshot=%+v", state, w.Snapshot())
}

func sameStrings(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

func containsAll(values []string, required ...string) bool {
	seen := map[string]struct{}{}
	for _, value := range values {
		seen[value] = struct{}{}
	}
	for _, value := range required {
		if _, ok := seen[value]; !ok {
			return false
		}
	}
	return true
}
