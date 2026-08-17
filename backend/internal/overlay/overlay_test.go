package overlay

import (
	"context"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/ThalesMMS/CodeAtlas/internal/apperror"
	"github.com/ThalesMMS/CodeAtlas/internal/contenthash"
	"github.com/ThalesMMS/CodeAtlas/internal/domain"
)

type fakeWorkspace struct{ files map[string][]byte }

type manualLeaseScheduler struct {
	mu     sync.Mutex
	now    time.Duration
	timers []*manualLeaseTimer
}

type manualLeaseTimer struct {
	scheduler *manualLeaseScheduler
	due       time.Duration
	callback  func()
	stopped   bool
}

func (s *manualLeaseScheduler) afterFunc(delay time.Duration, callback func()) leaseTimer {
	s.mu.Lock()
	defer s.mu.Unlock()
	timer := &manualLeaseTimer{scheduler: s, due: s.now + delay, callback: callback}
	s.timers = append(s.timers, timer)
	return timer
}

func (t *manualLeaseTimer) Stop() bool {
	t.scheduler.mu.Lock()
	defer t.scheduler.mu.Unlock()
	wasActive := !t.stopped
	t.stopped = true
	return wasActive
}

func (s *manualLeaseScheduler) advance(delta time.Duration) {
	s.mu.Lock()
	s.now += delta
	var callbacks []func()
	for _, timer := range s.timers {
		if !timer.stopped && timer.due <= s.now {
			timer.stopped = true
			callbacks = append(callbacks, timer.callback)
		}
	}
	s.mu.Unlock()
	for _, callback := range callbacks {
		callback()
	}
}

func (w fakeWorkspace) Resolve(p string) (string, error) {
	if _, ok := w.files[p]; !ok {
		return "", apperror.FileNotFound(p, nil)
	}
	return "/abs/" + p, nil
}

func (w fakeWorkspace) Read(p string) ([]byte, error) {
	content, ok := w.files[p]
	if !ok {
		return nil, apperror.FileNotFound(p, nil)
	}
	return append([]byte(nil), content...), nil
}

func newTestStore(t *testing.T, config Config, files map[string][]byte) *Store {
	t.Helper()
	ws := fakeWorkspace{files: files}
	detect := func(path string) (string, bool) { return "go", strings.HasSuffix(path, ".go") }
	snapshotID := func() domain.SnapshotID { return "sha256:base" }
	return NewStore(ws, detect, snapshotID, nil, config)
}

func codeOf(t *testing.T, err error) apperror.Code {
	t.Helper()
	appErr, ok := apperror.As(err)
	if !ok {
		t.Fatalf("error %v is not an app error", err)
	}
	return appErr.Code
}

func mustOpen(t *testing.T, store *Store, path string) OverlaySnapshot {
	t.Helper()
	snap, err := store.Open(context.Background(), OpenRequest{Path: path})
	if err != nil {
		t.Fatalf("Open(%s): %v", path, err)
	}
	return snap
}

func TestOpenAndReplaceVersioning(t *testing.T) {
	t.Parallel()
	store := newTestStore(t, Config{}, map[string][]byte{"a.go": []byte("package a\n")})
	open := mustOpen(t, store, "a.go")
	if open.Version != 1 || open.Dirty || open.BaseContentHash != open.ContentHash || string(open.BaseContent) != "package a\n" {
		t.Fatalf("unexpected open snapshot: %+v", open)
	}
	if open.BaseSnapshotID != "sha256:base" {
		t.Fatalf("base snapshot not pinned: %q", open.BaseSnapshotID)
	}

	updated, err := store.Replace(context.Background(), ReplaceRequest{
		DocumentID: open.DocumentID, LeaseID: open.LeaseID,
		ExpectedVersion: 1, NewVersion: 2, Content: []byte("package a\nfunc x() {}\n"),
	})
	if err != nil {
		t.Fatalf("Replace: %v", err)
	}
	if updated.Version != 2 || !updated.Dirty {
		t.Fatalf("expected dirty v2, got %+v", updated)
	}
	if string(updated.BaseContent) != "package a\n" {
		t.Fatalf("dirty overlay lost its exact base content: %q", updated.BaseContent)
	}

	latest, err := store.Get(open.DocumentID, nil)
	if err != nil || latest.Version != 2 {
		t.Fatalf("Get latest = %+v, %v", latest, err)
	}
	stale := DocumentVersion(1)
	if _, err := store.Get(open.DocumentID, &stale); codeOf(t, err) != apperror.CodeDocumentVersionConflict {
		t.Fatalf("Get(v1) after v2 should conflict, got %v", err)
	}
}

func TestOpenRejectsSecondWritableLease(t *testing.T) {
	t.Parallel()
	store := newTestStore(t, Config{}, map[string][]byte{"a.go": []byte("package a\n")})
	mustOpen(t, store, "a.go")
	if _, err := store.Open(context.Background(), OpenRequest{Path: "a.go"}); codeOf(t, err) != apperror.CodeDocumentAlreadyOpen {
		t.Fatalf("second open should be DOCUMENT_ALREADY_OPEN, got %v", err)
	}
}

func TestReclaimRotatesLeaseAndInvalidatesDelayedClose(t *testing.T) {
	t.Parallel()
	store := newTestStore(t, Config{}, map[string][]byte{"a.go": []byte("package a\n")})
	open := mustOpen(t, store, "a.go")

	reclaimed, err := store.Reclaim(open.DocumentID, open.LeaseID)
	if err != nil {
		t.Fatalf("Reclaim: %v", err)
	}
	if reclaimed.LeaseID == "" || reclaimed.LeaseID == open.LeaseID {
		t.Fatalf("Reclaim lease = %q, want a fresh token", reclaimed.LeaseID)
	}
	if err := store.Close(open.DocumentID, open.LeaseID, false); codeOf(t, err) != apperror.CodeDocumentNotFound {
		t.Fatalf("delayed close with transferred lease should be rejected, got %v", err)
	}
	if err := store.Close(open.DocumentID, reclaimed.LeaseID, false); err != nil {
		t.Fatalf("close with reclaimed lease: %v", err)
	}
	if _, err := store.Open(context.Background(), OpenRequest{Path: "a.go"}); err != nil {
		t.Fatalf("path remained locked after reclaimed close: %v", err)
	}
}

func TestOpenRejectsUnsupported(t *testing.T) {
	t.Parallel()
	store := newTestStore(t, Config{}, map[string][]byte{"notes.txt": []byte("hi")})
	if _, err := store.Open(context.Background(), OpenRequest{Path: "notes.txt"}); codeOf(t, err) != apperror.CodeUnsupportedLanguage {
		t.Fatalf("unsupported file should be rejected, got %v", err)
	}
}

func TestReplaceRejectsStaleVersionAndLease(t *testing.T) {
	t.Parallel()
	store := newTestStore(t, Config{}, map[string][]byte{"a.go": []byte("package a\n")})
	open := mustOpen(t, store, "a.go")

	wrongVersion := ReplaceRequest{DocumentID: open.DocumentID, LeaseID: open.LeaseID, ExpectedVersion: 99, NewVersion: 100, Content: []byte("x")}
	if _, err := store.Replace(context.Background(), wrongVersion); codeOf(t, err) != apperror.CodeDocumentVersionConflict {
		t.Fatalf("stale expectedVersion should conflict, got %v", err)
	}
	nonSequential := ReplaceRequest{DocumentID: open.DocumentID, LeaseID: open.LeaseID, ExpectedVersion: 1, NewVersion: 5, Content: []byte("package a\n//x\n")}
	if _, err := store.Replace(context.Background(), nonSequential); codeOf(t, err) != apperror.CodeDocumentVersionConflict {
		t.Fatalf("non-sequential newVersion should conflict, got %v", err)
	}
	wrongLease := ReplaceRequest{DocumentID: open.DocumentID, LeaseID: "stale-lease", ExpectedVersion: 1, NewVersion: 2, Content: []byte("x")}
	if _, err := store.Replace(context.Background(), wrongLease); codeOf(t, err) != apperror.CodeDocumentNotFound {
		t.Fatalf("wrong lease should be rejected, got %v", err)
	}
}

func TestReplaceRejectsNonUTF8(t *testing.T) {
	t.Parallel()
	store := newTestStore(t, Config{}, map[string][]byte{"a.go": []byte("package a\n")})
	open := mustOpen(t, store, "a.go")
	bad := ReplaceRequest{DocumentID: open.DocumentID, LeaseID: open.LeaseID, ExpectedVersion: 1, NewVersion: 2, Content: []byte{0xff, 0xfe}}
	if _, err := store.Replace(context.Background(), bad); codeOf(t, err) != apperror.CodeInvalidArgument {
		t.Fatalf("invalid UTF-8 should be rejected, got %v", err)
	}
}

func TestCloseDirtyRequiresDiscard(t *testing.T) {
	t.Parallel()
	store := newTestStore(t, Config{}, map[string][]byte{"a.go": []byte("package a\n")})
	open := mustOpen(t, store, "a.go")
	dirty, _ := store.Replace(context.Background(), ReplaceRequest{DocumentID: open.DocumentID, LeaseID: open.LeaseID, ExpectedVersion: 1, NewVersion: 2, Content: []byte("package a\nfunc x() {}\n")})
	if !dirty.Dirty {
		t.Fatal("expected dirty after replace")
	}
	if err := store.Close(open.DocumentID, open.LeaseID, false); codeOf(t, err) != apperror.CodeDocumentDirty {
		t.Fatalf("closing dirty without discard should be DOCUMENT_DIRTY, got %v", err)
	}
	if err := store.Close(open.DocumentID, open.LeaseID, true); err != nil {
		t.Fatalf("discard close should succeed: %v", err)
	}
	// After close, the path is openable again.
	if _, err := store.Open(context.Background(), OpenRequest{Path: "a.go"}); err != nil {
		t.Fatalf("reopen after discard close: %v", err)
	}
}

func TestMarkSavedClearsDirty(t *testing.T) {
	t.Parallel()
	store := newTestStore(t, Config{}, map[string][]byte{"a.go": []byte("package a\n")})
	open := mustOpen(t, store, "a.go")
	dirty, _ := store.Replace(context.Background(), ReplaceRequest{DocumentID: open.DocumentID, LeaseID: open.LeaseID, ExpectedVersion: 1, NewVersion: 2, Content: []byte("package a\nfunc x() {}\n")})

	saved, err := store.MarkSaved(open.DocumentID, open.LeaseID, dirty.Version, dirty.ContentHash, "sha256:new")
	if err != nil {
		t.Fatalf("MarkSaved: %v", err)
	}
	if saved.Dirty || saved.BaseContentHash != dirty.ContentHash || saved.BaseSnapshotID != "sha256:new" || saved.Version != 2 {
		t.Fatalf("MarkSaved did not clear dirty / advance base: %+v", saved)
	}
	if string(saved.BaseContent) != string(saved.Content) {
		t.Fatalf("saved base content = %q, want current content %q", saved.BaseContent, saved.Content)
	}
}

func TestLeaseRenewalExtendsTTLAndAbandonedDirtyOverlayExpires(t *testing.T) {
	store := newTestStore(t, Config{LeaseTTL: 50 * time.Millisecond}, map[string][]byte{"a.go": []byte("package a\n")})
	scheduler := &manualLeaseScheduler{}
	store.afterFunc = scheduler.afterFunc
	expired := make(chan OverlaySnapshot, 1)
	store.SetExpireHandler(func(snapshot OverlaySnapshot) { expired <- snapshot })
	open := mustOpen(t, store, "a.go")
	dirty, err := store.Replace(context.Background(), ReplaceRequest{
		DocumentID: open.DocumentID, LeaseID: open.LeaseID,
		ExpectedVersion: 1, NewVersion: 2, Content: []byte("package a\n// dirty\n"),
	})
	if err != nil {
		t.Fatal(err)
	}

	scheduler.advance(30 * time.Millisecond)
	if _, err := store.Renew(dirty.DocumentID, dirty.LeaseID, nil); err != nil {
		t.Fatalf("Renew() error = %v", err)
	}
	scheduler.advance(49 * time.Millisecond)
	if _, err := store.Get(dirty.DocumentID, nil); err != nil {
		t.Fatalf("lease expired before renewed TTL elapsed: %v", err)
	}

	scheduler.advance(time.Millisecond)
	select {
	case snapshot := <-expired:
		if !snapshot.Dirty || snapshot.DocumentID != dirty.DocumentID {
			t.Fatalf("expired snapshot = %+v, want dirty %s", snapshot, dirty.DocumentID)
		}
	default:
		t.Fatal("abandoned lease did not expire")
	}
	if _, err := store.Get(dirty.DocumentID, nil); codeOf(t, err) != apperror.CodeDocumentNotFound {
		t.Fatalf("expired document remained in store: %v", err)
	}
	if _, err := store.Open(context.Background(), OpenRequest{Path: "a.go"}); err != nil {
		t.Fatalf("expired path was not released: %v", err)
	}
}

func TestApplyExternalChangeReloadsCleanDocument(t *testing.T) {
	t.Parallel()
	files := map[string][]byte{"a.go": []byte("package a\n")}
	store := newTestStore(t, Config{}, files)
	open := mustOpen(t, store, "a.go")
	files["a.go"] = []byte("package a\nfunc External() {}\n")
	newHash := contenthash.HashContent(files["a.go"])

	transitions, err := store.ApplyExternalChange(context.Background(), ExternalUpdate{
		OperationID: "op-1", Path: "a.go", NewHash: newHash, SnapshotID: "sha256:next",
		Revision: 2, Kind: domain.FileChangeModified, Origin: domain.FileChangeOriginExternal,
	})
	if err != nil {
		t.Fatalf("ApplyExternalChange() error = %v", err)
	}
	if len(transitions) != 1 || transitions[0].From != StateClean || transitions[0].To != StateExternalChangedClean {
		t.Fatalf("transitions = %#v, want clean external reload", transitions)
	}
	latest, err := store.Get(open.DocumentID, nil)
	if err != nil {
		t.Fatal(err)
	}
	if latest.Version != open.Version+1 || latest.Dirty || latest.ContentHash != newHash || latest.BaseSnapshotID != "sha256:next" {
		t.Fatalf("latest = %#v, want externally reloaded clean document", latest)
	}
	if latest.Conflict == nil || latest.Conflict.ReasonCode != ReasonExternalModification {
		t.Fatalf("conflict metadata = %#v, want external change metadata", latest.Conflict)
	}
}

func TestReplaceAfterExternalCleanReloadClearsConflictMetadata(t *testing.T) {
	t.Parallel()
	files := map[string][]byte{"a.go": []byte("package a\n")}
	store := newTestStore(t, Config{}, files)
	open := mustOpen(t, store, "a.go")
	files["a.go"] = []byte("package a\nfunc External() {}\n")
	externalHash := contenthash.HashContent(files["a.go"])
	if _, err := store.ApplyExternalChange(context.Background(), ExternalUpdate{
		OperationID: "op-1", Path: "a.go", NewHash: externalHash, SnapshotID: "sha256:next",
		Revision: 2, Kind: domain.FileChangeModified, Origin: domain.FileChangeOriginExternal,
	}); err != nil {
		t.Fatalf("ApplyExternalChange() error = %v", err)
	}
	reloaded, err := store.Get(open.DocumentID, nil)
	if err != nil {
		t.Fatal(err)
	}
	if reloaded.State != StateExternalChangedClean || reloaded.Conflict == nil {
		t.Fatalf("reloaded = %#v, want external_changed_clean with metadata", reloaded)
	}

	edited, err := store.Replace(context.Background(), ReplaceRequest{
		DocumentID: reloaded.DocumentID, LeaseID: reloaded.LeaseID,
		ExpectedVersion: reloaded.Version, NewVersion: reloaded.Version + 1,
		Content: []byte("package a\nfunc Local() {}\n"),
	})
	if err != nil {
		t.Fatalf("Replace() error = %v", err)
	}
	if edited.State != StateLocalDirty || edited.Conflict != nil {
		t.Fatalf("edited = %#v, want local_dirty without stale conflict metadata", edited)
	}
}

func TestApplyExternalChangeMarksDirtyDocumentConflicted(t *testing.T) {
	t.Parallel()
	files := map[string][]byte{"a.go": []byte("package a\n")}
	store := newTestStore(t, Config{}, files)
	open := mustOpen(t, store, "a.go")
	dirty, err := store.Replace(context.Background(), ReplaceRequest{
		DocumentID: open.DocumentID, LeaseID: open.LeaseID,
		ExpectedVersion: 1, NewVersion: 2, Content: []byte("package a\nfunc Local() {}\n"),
	})
	if err != nil {
		t.Fatal(err)
	}
	files["a.go"] = []byte("package a\nfunc External() {}\n")
	externalHash := contenthash.HashContent(files["a.go"])

	transitions, err := store.ApplyExternalChange(context.Background(), ExternalUpdate{
		OperationID: "op-1", Path: "a.go", NewHash: externalHash, SnapshotID: "sha256:next",
		Revision: 2, Kind: domain.FileChangeModified, Origin: domain.FileChangeOriginExternal,
	})
	if err != nil {
		t.Fatalf("ApplyExternalChange() error = %v", err)
	}
	if len(transitions) != 1 || transitions[0].To != StateConflictedDirty {
		t.Fatalf("transitions = %#v, want conflicted dirty", transitions)
	}
	latest, _ := store.Get(open.DocumentID, nil)
	if latest.Version != dirty.Version || string(latest.Content) != string(dirty.Content) || latest.State != StateConflictedDirty {
		t.Fatalf("latest = %#v, want local content preserved and conflicted", latest)
	}
	if latest.Conflict == nil || latest.Conflict.ExternalContentHash != externalHash {
		t.Fatalf("conflict = %#v, want external hash", latest.Conflict)
	}
}

func TestApplyExternalRemovalResolvesWithoutReadingMissingFile(t *testing.T) {
	t.Parallel()
	for _, strategy := range []ResolveStrategy{ResolveKeepLocalRebase, ResolveReloadExternal} {
		strategy := strategy
		t.Run(string(strategy), func(t *testing.T) {
			files := map[string][]byte{"a.go": []byte("package a\n")}
			store := newTestStore(t, Config{}, files)
			open := mustOpen(t, store, "a.go")
			transitions, err := store.ApplyExternalChange(context.Background(), ExternalUpdate{
				OperationID: "op-1", Path: "a.go", SnapshotID: "sha256:next", Revision: 2,
				Kind: domain.FileChangeRemoved, Origin: domain.FileChangeOriginExternal,
			})
			if err != nil || len(transitions) != 1 || transitions[0].To != StateExternalDeleted {
				t.Fatalf("ApplyExternalChange() = %#v, %v", transitions, err)
			}
			delete(files, "a.go")
			latest, _ := store.Get(open.DocumentID, nil)
			resolved, err := store.ResolveConflict(context.Background(), ResolveConflictRequest{
				DocumentID: open.DocumentID, LeaseID: open.LeaseID, DocumentVersion: latest.Version,
				ExternalContentHash: "", Strategy: strategy,
			})
			if err != nil {
				t.Fatalf("ResolveConflict(%s) read the deleted file: %v", strategy, err)
			}
			if strategy == ResolveKeepLocalRebase && (!resolved.Snapshot.Dirty || string(resolved.Snapshot.Content) != string(open.Content)) {
				t.Fatalf("keep-local result = %#v", resolved)
			}
			if strategy == ResolveReloadExternal && (resolved.Snapshot.Dirty || len(resolved.Snapshot.Content) != 0) {
				t.Fatalf("reload-external result = %#v", resolved)
			}
		})
	}
}

func TestOverlayLimitsAndRemovalAccountForContentAndBaseContent(t *testing.T) {
	t.Parallel()
	files := map[string][]byte{"a.go": []byte("1234")}
	rejected := newTestStore(t, Config{MaxTotalBytes: 7}, files)
	if _, err := rejected.Open(context.Background(), OpenRequest{Path: "a.go"}); codeOf(t, err) != apperror.CodeOverlayLimitExceeded {
		t.Fatalf("Open should count both buffers, got %v", err)
	}

	store := newTestStore(t, Config{MaxTotalBytes: 8}, files)
	open := mustOpen(t, store, "a.go")
	if store.totalBytes != 8 {
		t.Fatalf("totalBytes after open = %d, want 8", store.totalBytes)
	}
	if _, err := store.Replace(context.Background(), ReplaceRequest{
		DocumentID: open.DocumentID, LeaseID: open.LeaseID,
		ExpectedVersion: 1, NewVersion: 2, Content: []byte("12345"),
	}); codeOf(t, err) != apperror.CodeOverlayLimitExceeded {
		t.Fatalf("Replace should include base buffer, got %v", err)
	}
	if err := store.Close(open.DocumentID, open.LeaseID, true); err != nil {
		t.Fatal(err)
	}
	if store.totalBytes != 0 {
		t.Fatalf("totalBytes after close = %d, want 0", store.totalBytes)
	}

	saveBounded := newTestStore(t, Config{MaxTotalBytes: 10}, files)
	saveOpen := mustOpen(t, saveBounded, "a.go")
	if _, err := saveBounded.Replace(context.Background(), ReplaceRequest{
		DocumentID: saveOpen.DocumentID, LeaseID: saveOpen.LeaseID,
		ExpectedVersion: 1, NewVersion: 2, Content: []byte("123456"),
	}); codeOf(t, err) != apperror.CodeOverlayLimitExceeded {
		t.Fatalf("Replace should reserve the eventual saved base buffer, got %v", err)
	}
}

func TestApplyWorkspaceChangeSkipsInternalAndMapsIgnoredUnsupportedReasons(t *testing.T) {
	t.Parallel()
	store := newTestStore(t, Config{}, map[string][]byte{
		"a.go": []byte("package a\n"),
		"b.go": []byte("package b\n"),
		"c.go": []byte("package c\n"),
	})
	internal := mustOpen(t, store, "a.go")
	ignored := mustOpen(t, store, "b.go")
	unsupported := mustOpen(t, store, "c.go")

	transitions, err := store.ApplyWorkspaceChange(context.Background(), domain.PublishedWorkspaceChange{
		OperationID: "op-1", SnapshotID: "sha256:next", Revision: 2,
		Changes: []domain.PublishedFileChange{
			{Path: "a.go", Kind: domain.FileChangeModified, NewHash: "sha256:internal", Origin: domain.FileChangeOriginInternal},
			{Path: "b.go", Kind: domain.FileChangeBecameIgnored, Origin: domain.FileChangeOriginExternal},
			{Path: "c.go", Kind: domain.FileChangeBecameUnsupported, Origin: domain.FileChangeOriginExternal},
		},
	})
	if err != nil {
		t.Fatalf("ApplyWorkspaceChange() error = %v", err)
	}
	if len(transitions) != 2 {
		t.Fatalf("transitions = %#v, want ignored and unsupported only", transitions)
	}
	latestInternal, _ := store.Get(internal.DocumentID, nil)
	if latestInternal.State != StateClean || latestInternal.Conflict != nil {
		t.Fatalf("internal-origin change affected overlay: %#v", latestInternal)
	}
	latestIgnored, _ := store.Get(ignored.DocumentID, nil)
	if latestIgnored.Conflict == nil || latestIgnored.Conflict.ReasonCode != ReasonBecameIgnored {
		t.Fatalf("ignored conflict = %#v, want BECAME_IGNORED", latestIgnored.Conflict)
	}
	latestUnsupported, _ := store.Get(unsupported.DocumentID, nil)
	if latestUnsupported.Conflict == nil || latestUnsupported.Conflict.ReasonCode != ReasonLanguageUnsupported {
		t.Fatalf("unsupported conflict = %#v, want LANGUAGE_UNSUPPORTED", latestUnsupported.Conflict)
	}
}

func TestResolveConflictReloadExternalAndKeepLocalRebase(t *testing.T) {
	t.Parallel()
	files := map[string][]byte{"a.go": []byte("package a\n")}
	store := newTestStore(t, Config{}, files)
	clean := mustOpen(t, store, "a.go")
	files["a.go"] = []byte("package a\nfunc External() {}\n")
	externalHash := contenthash.HashContent(files["a.go"])
	if _, err := store.ApplyExternalChange(context.Background(), ExternalUpdate{
		OperationID: "op-1", Path: "a.go", NewHash: externalHash, SnapshotID: "sha256:next",
		Revision: 2, Kind: domain.FileChangeModified, Origin: domain.FileChangeOriginExternal,
	}); err != nil {
		t.Fatal(err)
	}
	reloaded, err := store.ResolveConflict(context.Background(), ResolveConflictRequest{
		DocumentID: clean.DocumentID, LeaseID: clean.LeaseID, DocumentVersion: clean.Version + 1,
		ExternalContentHash: externalHash, Strategy: ResolveReloadExternal,
	})
	if err != nil {
		t.Fatalf("ResolveConflict(reload_external) error = %v", err)
	}
	if reloaded.Snapshot.State != StateClean || reloaded.Snapshot.Dirty || reloaded.Snapshot.ContentHash != externalHash {
		t.Fatalf("reloaded = %#v, want clean external content", reloaded)
	}

	files["b.go"] = []byte("package b\n")
	dirty := mustOpen(t, store, "b.go")
	dirty, _ = store.Replace(context.Background(), ReplaceRequest{
		DocumentID: dirty.DocumentID, LeaseID: dirty.LeaseID,
		ExpectedVersion: 1, NewVersion: 2, Content: []byte("package b\nfunc Local() {}\n"),
	})
	files["b.go"] = []byte("package b\nfunc External() {}\n")
	externalHash = contenthash.HashContent(files["b.go"])
	if _, err := store.ApplyExternalChange(context.Background(), ExternalUpdate{
		OperationID: "op-2", Path: "b.go", NewHash: externalHash, SnapshotID: "sha256:next2",
		Revision: 3, Kind: domain.FileChangeModified, Origin: domain.FileChangeOriginExternal,
	}); err != nil {
		t.Fatal(err)
	}
	rebased, err := store.ResolveConflict(context.Background(), ResolveConflictRequest{
		DocumentID: dirty.DocumentID, LeaseID: dirty.LeaseID, DocumentVersion: dirty.Version,
		ExternalContentHash: externalHash, Strategy: ResolveKeepLocalRebase,
	})
	if err != nil {
		t.Fatalf("ResolveConflict(keep_local_rebase) error = %v", err)
	}
	if !rebased.WillOverwriteExternalOnSave || !rebased.Snapshot.Dirty || rebased.Snapshot.State != StateLocalDirty || rebased.Snapshot.BaseContentHash != externalHash {
		t.Fatalf("rebased = %#v, want dirty local content rebased on external hash", rebased)
	}
}

func TestDefensiveCopies(t *testing.T) {
	t.Parallel()
	store := newTestStore(t, Config{}, map[string][]byte{"a.go": []byte("package a\n")})
	open := mustOpen(t, store, "a.go")
	open.Content[0] = 'X' // mutate the caller's copy
	open.BaseContent[0] = 'Y'
	again, _ := store.Get(open.DocumentID, nil)
	if again.Content[0] == 'X' || again.BaseContent[0] == 'Y' {
		t.Fatal("store leaked an internal content slice")
	}
}

func TestLimitsRejectWithoutEvictingDirty(t *testing.T) {
	t.Parallel()
	store := newTestStore(t, Config{MaxDocuments: 1}, map[string][]byte{"a.go": []byte("package a\n"), "b.go": []byte("package b\n")})
	mustOpen(t, store, "a.go")
	if _, err := store.Open(context.Background(), OpenRequest{Path: "b.go"}); codeOf(t, err) != apperror.CodeOverlayLimitExceeded {
		t.Fatalf("exceeding MaxDocuments should be OVERLAY_LIMIT_EXCEEDED, got %v", err)
	}

	store2 := newTestStore(t, Config{MaxBytesPerDocument: 12}, map[string][]byte{"a.go": []byte("package a\n")})
	open := mustOpen(t, store2, "a.go")
	big := ReplaceRequest{DocumentID: open.DocumentID, LeaseID: open.LeaseID, ExpectedVersion: 1, NewVersion: 2, Content: []byte(strings.Repeat("x", 100))}
	if _, err := store2.Replace(context.Background(), big); codeOf(t, err) != apperror.CodeRequestTooLarge {
		t.Fatalf("oversized replace should be REQUEST_TOO_LARGE, got %v", err)
	}
}

func TestStrongDistinctIDs(t *testing.T) {
	t.Parallel()
	files := map[string][]byte{}
	for _, name := range []string{"a.go", "b.go", "c.go"} {
		files[name] = []byte("package x\n")
	}
	store := newTestStore(t, Config{}, files)
	seen := map[string]bool{}
	for name := range files {
		snap := mustOpen(t, store, name)
		if len(snap.DocumentID) != 32 || len(snap.LeaseID) != 32 {
			t.Fatalf("ids not 16-byte hex: %q %q", snap.DocumentID, snap.LeaseID)
		}
		if seen[string(snap.DocumentID)] || seen[string(snap.LeaseID)] {
			t.Fatal("id collision")
		}
		seen[string(snap.DocumentID)] = true
		seen[string(snap.LeaseID)] = true
	}
}

func TestConcurrentOpsAreRaceFree(t *testing.T) {
	t.Parallel()
	files := map[string][]byte{}
	for _, name := range []string{"a.go", "b.go", "c.go", "d.go"} {
		files[name] = []byte("package x\n")
	}
	store := newTestStore(t, Config{}, files)
	var wg sync.WaitGroup
	for name := range files {
		wg.Add(1)
		go func(path string) {
			defer wg.Done()
			snap, err := store.Open(context.Background(), OpenRequest{Path: path})
			if err != nil {
				return
			}
			version := DocumentVersion(1)
			for i := 0; i < 20; i++ {
				next, err := store.Replace(context.Background(), ReplaceRequest{
					DocumentID: snap.DocumentID, LeaseID: snap.LeaseID,
					ExpectedVersion: version, NewVersion: version + 1, Content: []byte("package x\n//edit\n"),
				})
				if err != nil {
					t.Errorf("replace: %v", err)
					return
				}
				if next.Version != version+1 {
					t.Errorf("version not monotonic: %d -> %d", version, next.Version)
				}
				version = next.Version
				_, _ = store.Get(snap.DocumentID, nil)
			}
		}(name)
	}
	wg.Wait()
}
