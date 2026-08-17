package scheduler

import (
	"context"
	"errors"
	"reflect"
	"runtime"
	"sync"
	"testing"
	"time"

	"github.com/ThalesMMS/CodeAtlas/internal/capabilities"
	"github.com/ThalesMMS/CodeAtlas/internal/domain"
	"github.com/ThalesMMS/CodeAtlas/internal/watcher"
)

type reconcileCall struct {
	mode     string
	paths    []string
	root     string
	expected uint64
}

type recordingExecutor struct {
	mu                sync.Mutex
	version           uint64
	calls             chan reconcileCall
	block             chan struct{}
	failuresRemaining int
}

func newRecordingExecutor() *recordingExecutor {
	return &recordingExecutor{calls: make(chan reconcileCall, 16)}
}

func (e *recordingExecutor) CurrentRevision() uint64 {
	e.mu.Lock()
	defer e.mu.Unlock()
	return e.version
}

func (e *recordingExecutor) ReconcilePaths(_ context.Context, paths []string, expected uint64) error {
	e.calls <- reconcileCall{mode: domain.ReconcilePaths, paths: append([]string(nil), paths...), expected: expected}
	if err := e.maybeFail(); err != nil {
		return err
	}
	e.advance()
	return nil
}

func (e *recordingExecutor) ReconcileSubtree(_ context.Context, root string, expected uint64) error {
	e.calls <- reconcileCall{mode: domain.ReconcileSubtree, root: root, expected: expected}
	if err := e.maybeFail(); err != nil {
		return err
	}
	e.advance()
	return nil
}

func (e *recordingExecutor) ReconcileFull(_ context.Context, expected uint64) error {
	e.calls <- reconcileCall{mode: domain.ReconcileFull, expected: expected}
	if e.block != nil {
		<-e.block
	}
	if err := e.maybeFail(); err != nil {
		return err
	}
	e.advance()
	return nil
}

func (e *recordingExecutor) maybeFail() error {
	e.mu.Lock()
	defer e.mu.Unlock()
	if e.failuresRemaining > 0 {
		e.failuresRemaining--
		return errors.New("transient reconcile failure")
	}
	return nil
}

func (e *recordingExecutor) advance() {
	e.mu.Lock()
	e.version++
	e.mu.Unlock()
}

func TestControllerPollingTicksCoalesceWhileReconcileActive(t *testing.T) {
	clock := NewManualClock(time.Unix(1700000000, 0).UTC())
	executor := newRecordingExecutor()
	executor.block = make(chan struct{})
	controller, err := NewController(Options{
		Mode:              ModePolling,
		PollInterval:      time.Second,
		ReconcileInterval: time.Hour,
		Clock:             clock,
		Executor:          executor,
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

	clock.Advance(time.Second)
	first := waitReconcileCall(t, executor.calls)
	if first.mode != domain.ReconcileFull {
		t.Fatalf("first call = %#v, want full", first)
	}

	clock.Advance(3 * time.Second)
	close(executor.block)
	second := waitReconcileCall(t, executor.calls)
	if second.mode != domain.ReconcileFull {
		t.Fatalf("second call = %#v, want one coalesced full", second)
	}
	assertNoReconcileCall(t, executor.calls)

	cancel()
	<-done
}

func TestControllerNativeWatcherEventsDebounceToSinglePathRequest(t *testing.T) {
	clock := NewManualClock(time.Unix(1700000000, 0).UTC())
	executor := newRecordingExecutor()
	fakeWatcher := newFakeWorkspaceWatcher()
	controller, err := NewController(Options{
		Mode:              ModeNative,
		Debounce:          250 * time.Millisecond,
		MaxBatchDelay:     2 * time.Second,
		ReconcileInterval: time.Hour,
		Clock:             clock,
		Executor:          executor,
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
	<-fakeWatcher.started

	fakeWatcher.events <- watcher.WorkspaceEvent{Sequence: 1, Path: "pkg/a.go", Operations: watcher.WatchWrite, Kind: watcher.KindFile}
	fakeWatcher.events <- watcher.WorkspaceEvent{Sequence: 2, Path: "./pkg/a.go", Operations: watcher.WatchCreate, Kind: watcher.KindFile}
	waitDebouncerMaxSequence(t, controller, 2)
	waitSchedulerState(t, controller, domain.SchedulerDebouncing)
	clock.Advance(249 * time.Millisecond)
	assertNoReconcileCall(t, executor.calls)
	clock.Advance(time.Millisecond)

	call := waitReconcileCall(t, executor.calls)
	if call.mode != domain.ReconcilePaths || !reflect.DeepEqual(call.paths, []string{"pkg/a.go"}) {
		t.Fatalf("call = %#v, want one path reconcile", call)
	}
	cancel()
	<-done
}

func TestControllerWatcherRescanRunsFullAndAcknowledgesRecovery(t *testing.T) {
	clock := NewManualClock(time.Unix(1700000000, 0).UTC())
	executor := newRecordingExecutor()
	fakeWatcher := newFakeWorkspaceWatcher()
	controller, err := NewController(Options{
		Mode:              ModeNative,
		ReconcileInterval: time.Hour,
		Clock:             clock,
		Executor:          executor,
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
	<-fakeWatcher.started

	fakeWatcher.events <- watcher.WorkspaceEvent{
		Sequence: 3, Operations: watcher.WatchRescanRequired,
		Kind: watcher.KindUnknown, ReasonCode: watcher.ErrCodeBackpressureOverflow,
	}
	if call := waitReconcileCall(t, executor.calls); call.mode != domain.ReconcileFull {
		t.Fatalf("call = %#v, want full reconcile", call)
	}
	select {
	case <-fakeWatcher.acked:
	case <-time.After(2 * time.Second):
		t.Fatal("watcher resync was not acknowledged after successful full reconcile")
	}

	cancel()
	<-done
}

func TestControllerAutoFallsBackToPollingWhenWatcherDiesAtRuntime(t *testing.T) {
	clock := NewManualClock(time.Unix(1700000000, 0).UTC())
	executor := newRecordingExecutor()
	fakeWatcher := newFakeWorkspaceWatcher()
	registry := capabilities.NewRegistry()
	controller, err := NewController(Options{
		Mode:              ModeAuto,
		PollInterval:      time.Second,
		ReconcileInterval: time.Hour,
		Clock:             clock,
		Executor:          executor,
		CapabilitySink:    registry,
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
	<-fakeWatcher.started

	fakeWatcher.events <- terminalWatcherEvent()
	if call := waitReconcileCall(t, executor.calls); call.mode != domain.ReconcileFull {
		t.Fatalf("runtime failure call = %#v, want immediate full reconcile", call)
	}
	waitEffectiveMode(t, controller, ModePolling)
	state := controller.State()
	if state.FallbackReason == "" {
		t.Fatalf("state = %#v, want observable runtime fallback reason", state)
	}
	assertWatchCapability(t, registry, capabilities.CapabilityAvailable, ModePolling)

	clock.Advance(time.Second)
	if call := waitReconcileCall(t, executor.calls); call.mode != domain.ReconcileFull {
		t.Fatalf("polling call = %#v, want full reconcile", call)
	}

	cancel()
	<-done
}

func TestControllerNativeFailsWhenWatcherDiesAtRuntime(t *testing.T) {
	clock := NewManualClock(time.Unix(1700000000, 0).UTC())
	executor := newRecordingExecutor()
	fakeWatcher := newFakeWorkspaceWatcher()
	registry := capabilities.NewRegistry()
	controller, err := NewController(Options{
		Mode:              ModeNative,
		ReconcileInterval: time.Hour,
		Clock:             clock,
		Executor:          executor,
		CapabilitySink:    registry,
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
	<-fakeWatcher.started

	fakeWatcher.events <- terminalWatcherEvent()
	waitSchedulerState(t, controller, domain.SchedulerFailed)
	assertWatchCapability(t, registry, capabilities.CapabilityUnavailable, ModeNative)
	assertNoReconcileCall(t, executor.calls)

	cancel()
	<-done
}

func terminalWatcherEvent() watcher.WorkspaceEvent {
	return watcher.WorkspaceEvent{
		Sequence: 4, Operations: watcher.WatchRescanRequired,
		Kind: watcher.KindUnknown, ReasonCode: watcher.ErrCodeEventChannelClosed,
	}
}

func waitEffectiveMode(t *testing.T, controller *Controller, want string) {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if controller.State().EffectiveMode == want {
			return
		}
		runtime.Gosched()
	}
	t.Fatalf("effective mode = %s, want %s", controller.State().EffectiveMode, want)
}

func assertWatchCapability(t *testing.T, registry *capabilities.Registry, wantState capabilities.CapabilityState, wantMode string) {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	var results []capabilities.Result
	for time.Now().Before(deadline) {
		results = registry.Results()
		if len(results) == 1 && results[0].ID == capabilities.CapabilityWorkspaceWatch &&
			results[0].State == wantState && results[0].Metadata["effectiveMode"] == wantMode {
			return
		}
		runtime.Gosched()
	}
	t.Fatalf("capabilities = %#v, want workspace-watch state=%s effectiveMode=%s", results, wantState, wantMode)
}

func waitDebouncerMaxSequence(t *testing.T, controller *Controller, want uint64) {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		controller.debouncer.mu.Lock()
		got := controller.debouncer.maxSequence
		controller.debouncer.mu.Unlock()
		if got >= want {
			return
		}
		runtime.Gosched()
	}
	controller.debouncer.mu.Lock()
	got := controller.debouncer.maxSequence
	controller.debouncer.mu.Unlock()
	t.Fatalf("debouncer max sequence = %d, want at least %d", got, want)
}

func waitSchedulerState(t *testing.T, controller *Controller, want string) {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if controller.State().State == want {
			return
		}
		runtime.Gosched()
	}
	t.Fatalf("scheduler state = %s, want %s", controller.State().State, want)
}

func TestControllerAutoFallsBackToPollingWhenNativeCannotStart(t *testing.T) {
	clock := NewManualClock(time.Unix(1700000000, 0).UTC())
	executor := newRecordingExecutor()
	controller, err := NewController(Options{
		Mode:              ModeAuto,
		PollInterval:      time.Second,
		ReconcileInterval: time.Hour,
		Clock:             clock,
		Executor:          executor,
		WatcherFactory: func() (watcher.WorkspaceWatcher, error) {
			return nil, errors.New("native unavailable")
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

	clock.Advance(time.Second)
	call := waitReconcileCall(t, executor.calls)
	if call.mode != domain.ReconcileFull {
		t.Fatalf("call = %#v, want polling full reconcile", call)
	}
	state := controller.State()
	if state.EffectiveMode != ModePolling || state.FallbackReason == "" {
		t.Fatalf("state = %#v, want explicit polling fallback", state)
	}
	cancel()
	<-done
}

func TestControllerRetriesFailedReconcileBeforeCompletingRequest(t *testing.T) {
	clock := NewManualClock(time.Unix(1700000000, 0).UTC())
	executor := newRecordingExecutor()
	executor.failuresRemaining = 1
	controller, err := NewController(Options{
		Mode:              ModePolling,
		PollInterval:      time.Hour,
		ReconcileInterval: time.Hour,
		Clock:             clock,
		Executor:          executor,
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

	requestDone := make(chan error, 1)
	go func() { requestDone <- controller.RequestFull(ctx, ReasonManual) }()

	first := waitReconcileCall(t, executor.calls)
	assertNoReconcileCall(t, executor.calls)
	clock.Advance(249 * time.Millisecond)
	assertNoReconcileCall(t, executor.calls)
	clock.Advance(time.Millisecond)
	second := waitReconcileCall(t, executor.calls)
	if first.mode != domain.ReconcileFull || second.mode != domain.ReconcileFull {
		t.Fatalf("calls = %#v %#v, want two full attempts", first, second)
	}
	select {
	case err := <-requestDone:
		if err != nil {
			t.Fatalf("RequestFull() error = %v, want retry success", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("RequestFull did not complete")
	}

	cancel()
	<-done
}

func waitReconcileCall(t *testing.T, calls <-chan reconcileCall) reconcileCall {
	t.Helper()
	select {
	case call := <-calls:
		return call
	case <-time.After(2 * time.Second):
		t.Fatal("timed out waiting for reconcile call")
		return reconcileCall{}
	}
}

func assertNoReconcileCall(t *testing.T, calls <-chan reconcileCall) {
	t.Helper()
	select {
	case call := <-calls:
		t.Fatalf("unexpected reconcile call: %#v", call)
	default:
	}
}

type fakeWorkspaceWatcher struct {
	events  chan watcher.WorkspaceEvent
	errs    chan error
	started chan struct{}
	acked   chan struct{}
	once    sync.Once
	ackOnce sync.Once
}

func newFakeWorkspaceWatcher() *fakeWorkspaceWatcher {
	return &fakeWorkspaceWatcher{
		events:  make(chan watcher.WorkspaceEvent),
		errs:    make(chan error),
		started: make(chan struct{}),
		acked:   make(chan struct{}),
	}
}

func (w *fakeWorkspaceWatcher) Start(context.Context) error {
	w.once.Do(func() { close(w.started) })
	return nil
}

func (w *fakeWorkspaceWatcher) Events() <-chan watcher.WorkspaceEvent { return w.events }
func (w *fakeWorkspaceWatcher) Errors() <-chan error                  { return w.errs }
func (w *fakeWorkspaceWatcher) Snapshot() watcher.WatcherSnapshot {
	return watcher.WatcherSnapshot{State: watcher.StateRunning}
}
func (w *fakeWorkspaceWatcher) AcknowledgeResync() { w.ackOnce.Do(func() { close(w.acked) }) }
func (w *fakeWorkspaceWatcher) Close() error       { return nil }
