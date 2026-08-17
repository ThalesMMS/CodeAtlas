package scheduler

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/ThalesMMS/CodeAtlas/internal/capabilities"
	"github.com/ThalesMMS/CodeAtlas/internal/domain"
	"github.com/ThalesMMS/CodeAtlas/internal/watcher"
)

type WatcherFactory func() (watcher.WorkspaceWatcher, error)

type Options struct {
	Mode              string
	Debounce          time.Duration
	MaxBatchDelay     time.Duration
	ReconcileInterval time.Duration
	PollInterval      time.Duration
	Clock             Clock
	Executor          ReconcileExecutor
	WatcherFactory    WatcherFactory
	CapabilitySink    capabilities.ResultReceiver
	Logger            *slog.Logger
}

type Controller struct {
	mode              string
	effectiveMode     string
	debounce          time.Duration
	maxBatchDelay     time.Duration
	reconcileInterval time.Duration
	pollInterval      time.Duration
	clock             Clock
	executor          ReconcileExecutor
	watcherFactory    WatcherFactory
	logger            *slog.Logger
	capabilitySink    capabilities.ResultReceiver

	queue     *RequestQueue
	debouncer *Debouncer
	reqSeq    atomic.Uint64
	started   chan struct{}
	startOnce sync.Once
	pollOnce  sync.Once

	mu                    sync.Mutex
	state                 string
	currentRequestID      string
	currentScope          domain.ReconcileScope
	lastCommit            time.Time
	lastNoop              time.Time
	lastError             string
	lastPeriodicReconcile time.Time
	watcherMissedChanges  int
	fallbackReason        string
	runtimeWatcherFailure string
	native                watcher.WorkspaceWatcher
}

func NewController(opts Options) (*Controller, error) {
	if opts.Executor == nil {
		return nil, errors.New("scheduler: executor is required")
	}
	mode := opts.Mode
	if mode == "" {
		mode = ModeAuto
	}
	switch mode {
	case ModeAuto, ModeNative, ModePolling:
	default:
		return nil, fmt.Errorf("scheduler: invalid watch mode %q", mode)
	}
	clock := opts.Clock
	if clock == nil {
		clock = RealClock()
	}
	if opts.Debounce <= 0 {
		opts.Debounce = 250 * time.Millisecond
	}
	if opts.MaxBatchDelay <= 0 {
		opts.MaxBatchDelay = 2 * time.Second
	}
	if opts.ReconcileInterval <= 0 {
		opts.ReconcileInterval = 5 * time.Minute
	}
	if opts.PollInterval <= 0 {
		opts.PollInterval = 2 * time.Second
	}
	logger := opts.Logger
	if logger == nil {
		logger = slog.Default()
	}
	controller := &Controller{
		mode: mode, effectiveMode: mode, debounce: opts.Debounce, maxBatchDelay: opts.MaxBatchDelay,
		reconcileInterval: opts.ReconcileInterval, pollInterval: opts.PollInterval,
		clock: clock, executor: opts.Executor, watcherFactory: opts.WatcherFactory,
		capabilitySink: opts.CapabilitySink, logger: logger,
		queue: NewRequestQueue(), state: domain.SchedulerIdle, started: make(chan struct{}),
	}
	controller.debouncer = NewDebouncer(DebounceConfig{
		Clock:       clock,
		QuietWindow: opts.Debounce,
		MaxDelay:    opts.MaxBatchDelay,
		Flush: func(req domain.ReconcileRequest) {
			controller.enqueue(req)
		},
	})
	return controller, nil
}

func (c *Controller) Run(ctx context.Context) {
	runCtx, cancel := context.WithCancel(ctx)
	defer cancel()
	defer c.queue.Close()

	workerDone := make(chan struct{})
	go func() {
		defer close(workerDone)
		c.worker(runCtx)
	}()

	switch c.mode {
	case ModePolling:
		c.activatePolling(runCtx, "")
	case ModeNative, ModeAuto:
		if err := c.startNative(runCtx); err != nil {
			if c.mode == ModeNative {
				c.setFailed(err)
			} else {
				c.activatePolling(runCtx, sanitize(err))
			}
		} else {
			c.setEffectiveMode(ModeNative, "")
			c.scheduleRecurring(runCtx, c.reconcileInterval, func() {
				c.enqueueFull(ReasonPeriodic, 0)
			})
		}
	}
	c.startOnce.Do(func() { close(c.started) })

	<-runCtx.Done()
	if c.native != nil {
		_ = c.native.Close()
	}
	<-workerDone
}

func (c *Controller) Started() <-chan struct{} { return c.started }

func (c *Controller) RequestFull(ctx context.Context, reason string) error {
	if reason == "" {
		reason = ReasonManual
	}
	req := c.newFullRequest(reason, 10)
	done := c.queue.EnqueueWait(req)
	c.setQueued()
	select {
	case err := <-done:
		return err
	case <-ctx.Done():
		return ctx.Err()
	}
}

func (c *Controller) State() domain.SchedulerState {
	pendingPaths, pendingFull := c.queue.Snapshot()
	c.mu.Lock()
	defer c.mu.Unlock()
	return domain.SchedulerState{
		Mode: c.mode, EffectiveMode: c.effectiveMode, State: c.state,
		CurrentRequestID: c.currentRequestID, CurrentScope: c.currentScope,
		PendingPathCount: pendingPaths, PendingFull: pendingFull,
		LastCommit: c.lastCommit, LastNoop: c.lastNoop, LastError: c.lastError,
		LastPeriodicReconcile: c.lastPeriodicReconcile, WatcherMissedChanges: c.watcherMissedChanges,
		FallbackReason: c.fallbackReason, PollInterval: c.pollInterval, ReconcileInterval: c.reconcileInterval,
	}
}

func (c *Controller) startNative(ctx context.Context) error {
	if c.watcherFactory == nil {
		return errors.New("native watcher factory is not configured")
	}
	w, err := c.watcherFactory()
	if err != nil {
		return err
	}
	if err := w.Start(ctx); err != nil {
		_ = w.Close()
		return err
	}
	c.native = w
	go c.consumeWatcher(ctx, w)
	return nil
}

func (c *Controller) consumeWatcher(ctx context.Context, w watcher.WorkspaceWatcher) {
	for {
		select {
		case <-ctx.Done():
			return
		case event, ok := <-w.Events():
			if !ok {
				c.handleWatcherRuntimeFailure(ctx, w, watcher.ErrCodeEventChannelClosed)
				return
			}
			hint := hintFromWatcherEvent(event)
			if hint.Operations.Has(domain.OpRescanRequired) {
				reason := firstNonEmpty(event.ReasonCode, ReasonWatcherDesync)
				if isTerminalWatcherReason(reason) {
					c.handleWatcherRuntimeFailure(ctx, w, reason)
					return
				}
				c.handleDesync(reason)
				continue
			}
			if c.debouncer.Submit(hint) {
				c.setDebouncing()
			}
		case err, ok := <-w.Errors():
			if !ok {
				c.handleWatcherRuntimeFailure(ctx, w, watcher.ErrCodeErrorChannelClosed)
				return
			}
			if isTerminalWatcherError(err) {
				c.handleWatcherRuntimeFailure(ctx, w, sanitize(err))
				return
			}
			c.handleDesync(sanitize(err))
		}
	}
}

func (c *Controller) handleWatcherRuntimeFailure(ctx context.Context, w watcher.WorkspaceWatcher, reason string) {
	if ctx.Err() != nil {
		return
	}
	reason = firstNonEmpty(reason, ReasonWatcherDesync)
	_ = w.Close()
	if c.mode == ModeAuto {
		c.handleDesync(reason)
		c.activatePolling(ctx, reason)
		return
	}
	c.mu.Lock()
	c.runtimeWatcherFailure = reason
	c.mu.Unlock()
	c.setFailed(errors.New(reason))
}

func (c *Controller) activatePolling(ctx context.Context, reason string) {
	c.setEffectiveMode(ModePolling, reason)
	c.pollOnce.Do(func() {
		c.scheduleRecurring(ctx, c.pollInterval, func() {
			c.enqueueFull(ReasonPolling, 0)
		})
	})
}

func isTerminalWatcherReason(reason string) bool {
	return reason == watcher.ErrCodeEventChannelClosed || reason == watcher.ErrCodeErrorChannelClosed
}

func isTerminalWatcherError(err error) bool {
	var watcherErr *watcher.WatcherError
	return errors.As(err, &watcherErr) && isTerminalWatcherReason(watcherErr.Code)
}

func hintFromWatcherEvent(event watcher.WorkspaceEvent) domain.ChangeHint {
	var operations domain.WatchOperation
	if event.Operations.Has(watcher.WatchCreate) {
		operations |= domain.OpCreate
	}
	if event.Operations.Has(watcher.WatchWrite) {
		operations |= domain.OpWrite
	}
	if event.Operations.Has(watcher.WatchRemove) {
		operations |= domain.OpRemove
	}
	if event.Operations.Has(watcher.WatchRename) {
		operations |= domain.OpRename
	}
	if event.Operations.Has(watcher.WatchMetadata) {
		operations |= domain.OpChmod
	}
	if event.Operations.Has(watcher.WatchRescanRequired) {
		operations |= domain.OpRescanRequired
	}
	return domain.ChangeHint{
		Sequence: event.Sequence, Path: event.Path, Operations: operations,
		Kind: event.Kind, ReasonCode: event.ReasonCode,
	}
}

func (c *Controller) handleDesync(reason string) {
	req := c.newFullRequest(firstNonEmpty(reason, ReasonWatcherDesync), 5)
	c.queue.Enqueue(req)
	c.mu.Lock()
	c.state = domain.SchedulerDesynchronized
	c.lastError = firstNonEmpty(reason, ReasonWatcherDesync)
	c.mu.Unlock()
}

func (c *Controller) scheduleRecurring(ctx context.Context, interval time.Duration, fn func()) {
	var schedule func()
	schedule = func() {
		c.clock.AfterFunc(interval, func() {
			if ctx.Err() != nil {
				return
			}
			fn()
			schedule()
		})
	}
	schedule()
}

func (c *Controller) worker(ctx context.Context) {
	for {
		req, ok := c.queue.Dequeue(ctx)
		if !ok {
			return
		}
		c.setReconciling(req)
		before := c.executor.CurrentRevision()
		err := c.executeWithRetry(ctx, req)
		after := c.executor.CurrentRevision()
		c.finish(req, before, after, err)
		c.queue.Complete(err)
	}
}

func (c *Controller) executeWithRetry(ctx context.Context, req domain.ReconcileRequest) error {
	const maxAttempts = 2
	const retryBackoff = 250 * time.Millisecond
	var err error
	for attempt := 1; attempt <= maxAttempts; attempt++ {
		err = c.execute(ctx, req)
		if err == nil || ctx.Err() != nil {
			return err
		}
		if attempt < maxAttempts && !c.waitRetry(ctx, retryBackoff) {
			return ctx.Err()
		}
	}
	return err
}

func (c *Controller) waitRetry(ctx context.Context, delay time.Duration) bool {
	ready := make(chan struct{})
	timer := c.clock.AfterFunc(delay, func() { close(ready) })
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return false
	case <-ready:
		return true
	}
}

func (c *Controller) execute(ctx context.Context, req domain.ReconcileRequest) error {
	expected := c.executor.CurrentRevision()
	switch req.Scope.Mode {
	case domain.ReconcilePaths:
		return c.executor.ReconcilePaths(ctx, req.Scope.Paths, expected)
	case domain.ReconcileSubtree:
		return c.executor.ReconcileSubtree(ctx, req.Scope.Root, expected)
	default:
		return c.executor.ReconcileFull(ctx, expected)
	}
}

func (c *Controller) finish(req domain.ReconcileRequest, before, after uint64, err error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.currentRequestID = ""
	c.currentScope = domain.ReconcileScope{}
	if c.runtimeWatcherFailure != "" {
		c.state = domain.SchedulerFailed
		c.lastError = c.runtimeWatcherFailure
		return
	}
	if err != nil {
		c.state = domain.SchedulerFailed
		c.lastError = sanitize(err)
		return
	}
	now := c.clock.Now()
	c.lastError = ""
	if after != before {
		c.lastCommit = now
		if contains(req.Scope.ReasonCodes, ReasonPeriodic) {
			c.watcherMissedChanges++
		}
	} else {
		c.lastNoop = now
	}
	if contains(req.Scope.ReasonCodes, ReasonPeriodic) || contains(req.Scope.ReasonCodes, ReasonPolling) {
		c.lastPeriodicReconcile = now
	}
	if req.Scope.Mode == domain.ReconcileFull && c.native != nil {
		c.native.AcknowledgeResync()
	}
	if c.queue.HasPending() {
		c.state = domain.SchedulerQueued
	} else {
		c.state = domain.SchedulerIdle
	}
}

func (c *Controller) enqueue(req domain.ReconcileRequest) {
	c.queue.Enqueue(req)
	c.setQueued()
}

func (c *Controller) enqueueFull(reason string, priority int) {
	c.enqueue(c.newFullRequest(reason, priority))
}

func (c *Controller) newFullRequest(reason string, priority int) domain.ReconcileRequest {
	return domain.ReconcileRequest{
		ID:        fmt.Sprintf("reconcile-%d", c.reqSeq.Add(1)),
		CreatedAt: c.clock.Now(),
		Priority:  priority,
		Scope: domain.ReconcileScope{
			Mode:        domain.ReconcileFull,
			ReasonCodes: []string{reason},
		},
	}
}

func (c *Controller) setEffectiveMode(mode, fallbackReason string) {
	c.mu.Lock()
	c.effectiveMode = mode
	c.fallbackReason = fallbackReason
	if c.state == "" || c.state == domain.SchedulerFailed {
		c.state = domain.SchedulerIdle
	}
	c.mu.Unlock()
	c.publishCapability(capabilities.CapabilityAvailable, "", "")
}

func (c *Controller) setDebouncing() {
	c.mu.Lock()
	if c.runtimeWatcherFailure == "" && c.state != domain.SchedulerReconciling && c.state != domain.SchedulerDesynchronized {
		c.state = domain.SchedulerDebouncing
	}
	c.mu.Unlock()
}

func (c *Controller) setQueued() {
	c.mu.Lock()
	if c.runtimeWatcherFailure == "" && c.state != domain.SchedulerReconciling && c.state != domain.SchedulerDesynchronized {
		c.state = domain.SchedulerQueued
	}
	c.mu.Unlock()
}

func (c *Controller) setReconciling(req domain.ReconcileRequest) {
	c.mu.Lock()
	if c.runtimeWatcherFailure == "" {
		c.state = domain.SchedulerReconciling
	}
	c.currentRequestID = req.ID
	c.currentScope = req.Scope
	c.mu.Unlock()
}

func (c *Controller) setFailed(err error) {
	message := sanitize(err)
	c.mu.Lock()
	c.state = domain.SchedulerFailed
	c.lastError = message
	c.mu.Unlock()
	c.publishCapability(capabilities.CapabilityUnavailable, "WATCHER_UNAVAILABLE", message)
}

func (c *Controller) publishCapability(state capabilities.CapabilityState, code, message string) {
	if c.capabilitySink == nil {
		return
	}
	snapshot := c.State()
	metadata := map[string]string{
		"mode":          snapshot.Mode,
		"effectiveMode": snapshot.EffectiveMode,
	}
	if snapshot.FallbackReason != "" {
		metadata["fallback"] = snapshot.EffectiveMode
		metadata["fallbackReason"] = snapshot.FallbackReason
	}
	c.capabilitySink.UpdateCapability(capabilities.Result{
		ID:          capabilities.CapabilityWorkspaceWatch,
		Requirement: capabilities.Optional,
		State:       state,
		CheckedAt:   c.clock.Now(),
		ErrorCode:   code,
		Message:     message,
		Metadata:    metadata,
	})
}

func sanitize(err error) string {
	if err == nil {
		return ""
	}
	return strings.Join(strings.Fields(err.Error()), " ")
}

func firstNonEmpty(values ...string) string {
	for _, value := range values {
		if strings.TrimSpace(value) != "" {
			return strings.TrimSpace(value)
		}
	}
	return ""
}

func contains(values []string, target string) bool {
	for _, value := range values {
		if value == target {
			return true
		}
	}
	return false
}
