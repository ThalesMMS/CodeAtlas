package watcher

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"math"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"

	sharedignore "github.com/ThalesMMS/CodeAtlas/internal/ignore"
)

const (
	DefaultEventBufferSize = 256
	DefaultErrorBufferSize = 64
)

type Option func(*watcherOptions)

type watcherOptions struct {
	native          NativeWatcher
	eventBufferSize int
	errorBufferSize int
	now             func() time.Time
}

func WithNativeWatcher(native NativeWatcher) Option {
	return func(options *watcherOptions) { options.native = native }
}

func WithEventBufferSize(size int) Option {
	return func(options *watcherOptions) { options.eventBufferSize = size }
}

func WithErrorBufferSize(size int) Option {
	return func(options *watcherOptions) { options.errorBufferSize = size }
}

type workspaceWatcher struct {
	root    string
	native  NativeWatcher
	matcher sharedignore.Matcher
	now     func() time.Time

	mu              sync.RWMutex
	closeMu         sync.Mutex
	state           WatcherState
	watched         map[string]string
	sequence        uint64
	eventsReceived  uint64
	errorsReceived  uint64
	overflowCount   uint64
	desyncCount     uint64
	lastError       string
	startedAt       time.Time
	lastEventAt     time.Time
	operationCounts map[WatchOperation]uint64
	rescanPending   bool
	pendingReason   string

	events     chan WorkspaceEvent
	errors     chan error
	rescanWake chan struct{}
	ctx        context.Context
	cancel     context.CancelFunc
	wg         sync.WaitGroup
	closed     bool
}

func New(root string, opts ...Option) (*workspaceWatcher, error) {
	options := watcherOptions{
		eventBufferSize: DefaultEventBufferSize,
		errorBufferSize: DefaultErrorBufferSize,
		now:             func() time.Time { return time.Now().UTC() },
	}
	for _, opt := range opts {
		opt(&options)
	}
	if options.eventBufferSize < 1 {
		options.eventBufferSize = 1
	}
	if options.errorBufferSize < 1 {
		options.errorBufferSize = 1
	}

	absRoot, err := filepath.Abs(root)
	if err != nil {
		return nil, watcherError(ErrCodeCreateFailed, "invalid workspace root", err)
	}
	if evaluated, err := filepath.EvalSymlinks(absRoot); err == nil {
		absRoot = evaluated
	}
	info, err := os.Stat(absRoot)
	if err != nil {
		return nil, watcherError(ErrCodeCreateFailed, "workspace root is not accessible", err)
	}
	if !info.IsDir() {
		return nil, watcherError(ErrCodeCreateFailed, "workspace root is not a directory", nil)
	}

	native := options.native
	if native == nil {
		native, err = NewFsnotifyWatcher()
		if err != nil {
			return nil, err
		}
	}

	return &workspaceWatcher{
		root: absRoot, native: native, matcher: sharedignore.LoadMatcher(absRoot), now: options.now,
		state: StateNew, watched: map[string]string{}, operationCounts: map[WatchOperation]uint64{},
		events: make(chan WorkspaceEvent, options.eventBufferSize), errors: make(chan error, options.errorBufferSize),
		rescanWake: make(chan struct{}, 1),
	}, nil
}

func (w *workspaceWatcher) Start(ctx context.Context) error {
	w.mu.Lock()
	if w.state != StateNew {
		w.mu.Unlock()
		return watcherError(ErrCodeCreateFailed, "watcher can only be started once", nil)
	}
	w.state = StateStarting
	w.startedAt = w.now()
	w.ctx, w.cancel = context.WithCancel(ctx)
	w.mu.Unlock()

	if err := w.addDirectoryRecursive(w.root, false); err != nil {
		w.setFailed(err)
		return err
	}

	w.mu.Lock()
	w.state = StateRunning
	w.mu.Unlock()

	w.wg.Add(2)
	go w.run()
	go w.runRescanNotifier()
	return nil
}

func (w *workspaceWatcher) Events() <-chan WorkspaceEvent { return w.events }
func (w *workspaceWatcher) Errors() <-chan error          { return w.errors }

func (w *workspaceWatcher) AcknowledgeResync() {
	w.mu.Lock()
	defer w.mu.Unlock()
	if w.state == StateDesynchronized && strings.HasPrefix(w.lastError, ErrCodeBackpressureOverflow+":") {
		w.state = StateRunning
		w.lastError = ""
	}
}

func (w *workspaceWatcher) Snapshot() WatcherSnapshot {
	w.mu.RLock()
	defer w.mu.RUnlock()
	dirs := make([]string, 0, len(w.watched))
	for rel := range w.watched {
		dirs = append(dirs, rel)
	}
	sort.Strings(dirs)
	counts := make(map[string]uint64, len(w.operationCounts))
	for op, count := range w.operationCounts {
		counts[op.String()] = count
	}
	return WatcherSnapshot{
		State: w.state, WatchedDirs: dirs, WatchedDirectoryCount: len(dirs),
		CurrentSequence: w.sequence, EventsReceived: w.eventsReceived, ErrorsReceived: w.errorsReceived,
		OverflowCount: w.overflowCount, DesyncCount: w.desyncCount, LastError: w.lastError,
		StartedAt: w.startedAt, LastEventAt: w.lastEventAt, OperationCounts: counts,
	}
}

func (w *workspaceWatcher) Close() error {
	w.closeMu.Lock()
	defer w.closeMu.Unlock()

	w.mu.Lock()
	if w.closed || w.state == StateClosed {
		w.mu.Unlock()
		return nil
	}
	w.closed = true
	if w.state != StateDesynchronized && w.state != StateFailed {
		w.state = StateClosing
	}
	cancel := w.cancel
	w.mu.Unlock()

	if cancel != nil {
		cancel()
	}
	err := w.native.Close()
	w.wg.Wait()
	close(w.events)
	close(w.errors)

	w.mu.Lock()
	w.state = StateClosed
	w.mu.Unlock()
	return err
}

func (w *workspaceWatcher) run() {
	defer w.wg.Done()
	for {
		select {
		case <-w.ctx.Done():
			return
		case event, ok := <-w.native.Events():
			if !ok {
				w.handleChannelClosed(ErrCodeEventChannelClosed)
				return
			}
			w.handleNativeEvent(event)
		case err, ok := <-w.native.Errors():
			if !ok {
				w.handleChannelClosed(ErrCodeErrorChannelClosed)
				return
			}
			w.sendError(watcherError(ErrCodeDesynchronized, "native watcher error", err))
		}
	}
}

// runRescanNotifier turns a dropped event into a durable rescan signal. It is a
// separate long-lived goroutine so the native event loop remains responsive
// while the public event buffer is full. Multiple drops are coalesced, but a
// new drop during delivery causes another signal so no overflow generation is
// acknowledged by an older rescan.
func (w *workspaceWatcher) runRescanNotifier() {
	defer w.wg.Done()
	for {
		select {
		case <-w.ctx.Done():
			return
		case <-w.rescanWake:
			if !w.deliverPendingRescan() {
				return
			}
		}
	}
}

func (w *workspaceWatcher) deliverPendingRescan() bool {
	for {
		w.mu.RLock()
		if !w.rescanPending {
			w.mu.RUnlock()
			return true
		}
		generation := w.overflowCount
		reason := w.pendingReason
		w.mu.RUnlock()

		event := w.nextEvent("", WatchRescanRequired, KindUnknown, SourceWatcherInternal, reason)
		select {
		case <-w.ctx.Done():
			return false
		case w.events <- event:
			w.recordEvent(event)
		}

		w.mu.Lock()
		if w.overflowCount == generation {
			w.rescanPending = false
			w.pendingReason = ""
			w.mu.Unlock()
			return true
		}
		w.mu.Unlock()
	}
}

func (w *workspaceWatcher) handleNativeEvent(native NativeEvent) {
	event, err := w.normalizeEvent(native, SourceFSNotify, "")
	if err != nil {
		w.sendError(err)
		return
	}

	if event.Kind == KindDirectory && (event.Operations.Has(WatchCreate) || event.Operations.Has(WatchRename)) {
		if err := w.addDirectoryRecursive(native.Name, true); err != nil {
			w.sendError(err)
		}
	}
	if event.Operations.Has(WatchRemove) || event.Operations.Has(WatchRename) {
		w.removeDirectoryAndDescendants(native.Name)
	}
	w.sendEvent(event)
}

func (w *workspaceWatcher) handleChannelClosed(code string) {
	w.markDesynchronized(code, "native watcher channel closed")
	event := w.nextEvent("", WatchRescanRequired, KindUnknown, SourceWatcherInternal, code)
	w.sendEvent(event)
	w.sendError(watcherError(code, "native watcher channel closed", nil))
}

func (w *workspaceWatcher) addDirectoryRecursive(root string, emitExistingFiles bool) error {
	return filepath.WalkDir(root, func(path string, entry fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		relative, err := w.relativePath(path)
		if err != nil {
			return err
		}
		if entry.IsDir() {
			if relative != "." && w.ignoredDir(relative, entry.Name()) {
				return filepath.SkipDir
			}
			return w.addWatch(path, relative)
		}
		if emitExistingFiles && !w.matcher.Ignored(relative, false) {
			event := w.nextEvent(relative, WatchCreate, kindForPath(path), SourceWatcherInternal, "")
			w.sendEvent(event)
		}
		return nil
	})
}

func (w *workspaceWatcher) addWatch(path, relative string) error {
	clean := filepath.Clean(path)
	if err := w.native.Add(clean); err != nil {
		return addFailedError(relative, err)
	}
	w.mu.Lock()
	w.watched[relative] = clean
	w.mu.Unlock()
	return nil
}

func (w *workspaceWatcher) removeDirectoryAndDescendants(path string) {
	relative, err := w.relativePath(path)
	if err != nil {
		return
	}
	toRemove := make([]string, 0)
	w.mu.Lock()
	for rel, absolute := range w.watched {
		if rel == relative || strings.HasPrefix(rel, relative+"/") {
			delete(w.watched, rel)
			toRemove = append(toRemove, absolute)
		}
	}
	w.mu.Unlock()
	for _, absolute := range toRemove {
		_ = w.native.Remove(absolute)
	}
}

func (w *workspaceWatcher) ignoredDir(relative, name string) bool {
	if _, ignored := sharedignore.DefaultIgnoredDirectories[name]; ignored {
		return true
	}
	return w.matcher.Ignored(relative, true)
}

func (w *workspaceWatcher) normalizeEvent(native NativeEvent, source, reason string) (WorkspaceEvent, error) {
	relative, err := w.relativePath(native.Name)
	if err != nil {
		return WorkspaceEvent{}, watcherError(ErrCodePathOutsideWorkspace, "event path is outside workspace", err)
	}
	operations := nativeOperations(native.Op)
	if operations == 0 {
		operations = WatchMetadata
	}
	return w.nextEvent(relative, operations, kindForPath(native.Name), source, reason), nil
}

func nativeOperations(op uint32) WatchOperation {
	var operations WatchOperation
	if op&NativeCreate != 0 {
		operations |= WatchCreate
	}
	if op&NativeWrite != 0 {
		operations |= WatchWrite
	}
	if op&NativeRemove != 0 {
		operations |= WatchRemove
	}
	if op&NativeRename != 0 {
		operations |= WatchRename
	}
	if op&NativeChmod != 0 {
		operations |= WatchMetadata
	}
	return operations
}

func (w *workspaceWatcher) nextEvent(path string, operations WatchOperation, kind, source, reason string) WorkspaceEvent {
	w.mu.Lock()
	defer w.mu.Unlock()
	if w.sequence == math.MaxUint64 {
		w.sequence = 1
	} else {
		w.sequence++
	}
	return WorkspaceEvent{
		Sequence: w.sequence, Path: path, Operations: operations, Kind: kind,
		ObservedAt: w.now(), Source: source, ReasonCode: reason,
	}
}

func (w *workspaceWatcher) sendEvent(event WorkspaceEvent) bool {
	select {
	case w.events <- event:
		w.recordEvent(event)
		return true
	default:
		w.mu.Lock()
		w.overflowCount++
		w.mu.Unlock()
		if !event.Operations.Has(WatchRescanRequired) {
			w.markDesynchronized(ErrCodeBackpressureOverflow, "watcher event buffer overflow")
		}
		reason := event.ReasonCode
		if reason == "" {
			reason = ErrCodeBackpressureOverflow
		}
		w.queueRescan(reason)
		return false
	}
}

func (w *workspaceWatcher) queueRescan(reason string) {
	w.mu.Lock()
	if w.rescanPending {
		w.mu.Unlock()
		return
	}
	w.rescanPending = true
	w.pendingReason = reason
	w.mu.Unlock()
	select {
	case w.rescanWake <- struct{}{}:
	default:
	}
}

func (w *workspaceWatcher) recordEvent(event WorkspaceEvent) {
	w.mu.Lock()
	defer w.mu.Unlock()
	w.eventsReceived++
	w.lastEventAt = event.ObservedAt
	for _, op := range []WatchOperation{WatchCreate, WatchWrite, WatchRemove, WatchRename, WatchMetadata, WatchRescanRequired} {
		if event.Operations.Has(op) {
			w.operationCounts[op]++
		}
	}
}

func (w *workspaceWatcher) sendError(err error) {
	select {
	case w.errors <- err:
		w.mu.Lock()
		w.errorsReceived++
		w.lastError = sanitizeError(err)
		w.mu.Unlock()
	default:
		w.mu.Lock()
		w.errorsReceived++
		w.lastError = ErrCodeBackpressureOverflow
		w.mu.Unlock()
	}
}

func (w *workspaceWatcher) markDesynchronized(code, message string) {
	w.mu.Lock()
	defer w.mu.Unlock()
	if w.state != StateDesynchronized {
		w.desyncCount++
	}
	w.state = StateDesynchronized
	w.lastError = code + ": " + message
}

func (w *workspaceWatcher) setFailed(err error) {
	w.mu.Lock()
	defer w.mu.Unlock()
	w.state = StateFailed
	w.lastError = sanitizeError(err)
}

func (w *workspaceWatcher) relativePath(path string) (string, error) {
	abs, err := filepath.Abs(path)
	if err != nil {
		return "", err
	}
	abs = filepath.Clean(abs)
	rel, err := filepath.Rel(w.root, abs)
	if err != nil {
		return "", err
	}
	if rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) || filepath.IsAbs(rel) {
		return "", fmt.Errorf("%s is outside workspace", filepath.Base(abs))
	}
	return filepath.ToSlash(rel), nil
}

func kindForPath(path string) string {
	info, err := os.Lstat(path)
	if err != nil {
		return KindUnknown
	}
	mode := info.Mode()
	switch {
	case mode&os.ModeSymlink != 0:
		return KindSymlink
	case mode.IsDir():
		return KindDirectory
	case mode.IsRegular():
		return KindFile
	default:
		return KindUnknown
	}
}

func sanitizeError(err error) string {
	if err == nil {
		return ""
	}
	var watcherErr *WatcherError
	if strings.Contains(err.Error(), string(filepath.Separator)) {
		if errors.As(err, &watcherErr) && watcherErr.Code != "" {
			return watcherErr.Code
		}
		return "watcher error"
	}
	return err.Error()
}
