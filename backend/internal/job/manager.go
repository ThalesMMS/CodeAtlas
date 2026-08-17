package job

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"sort"
	"sync"
	"time"

	"github.com/ThalesMMS/CodeAtlas/internal/domain"
)

type DedupPolicy string

const (
	DedupReject   DedupPolicy = "reject"
	DedupCoalesce DedupPolicy = "coalesce"
)

type Runner func(context.Context, ProgressReporter) (Result, error)

type ProgressReporter interface {
	Report(stage, message string, progress domain.Progress) error
}

type Result struct {
	ArtifactID string
	SnapshotID domain.SnapshotID
	Payload    any
}

type Spec struct {
	Type            string
	Key             string
	InputSnapshotID domain.SnapshotID
	ClientRequestID string
	DedupPolicy     DedupPolicy
	Timeout         time.Duration
	Runner          Runner
}

type Config struct {
	MaxQueued          int
	GlobalConcurrency  int
	PerTypeConcurrency map[string]int
	RetainCompleted    int
	TerminalTTL        time.Duration
	EventBuffer        int
	DefaultTimeout     time.Duration
	TypeTimeouts       map[string]time.Duration
	Now                func() time.Time
}

func DefaultConfig() Config {
	return Config{
		MaxQueued:          32,
		GlobalConcurrency:  2,
		PerTypeConcurrency: map[string]int{"deepwiki.refresh": 1, "codemap.generate": 1, "repository.reindex": 1, "embeddings.rebuild": 1, "fts.rebuild": 1},
		RetainCompleted:    64,
		TerminalTTL:        30 * time.Minute,
		EventBuffer:        32,
		DefaultTimeout:     5 * time.Minute,
		TypeTimeouts: map[string]time.Duration{
			"deepwiki.refresh": 15 * time.Minute,
		},
	}
}

type Manager struct {
	mu            sync.Mutex
	config        Config
	broker        *broker
	jobs          map[domain.JobID]*record
	order         []domain.JobID
	queue         []domain.JobID
	activeKey     map[string]domain.JobID
	running       int
	runningByType map[string]int
	stopped       bool
	wg            sync.WaitGroup
}

type record struct {
	spec     Spec
	snapshot domain.JobSnapshot
	cancel   context.CancelFunc
	result   any
}

func NewManager(config Config) *Manager {
	defaults := DefaultConfig()
	if config.MaxQueued <= 0 {
		config.MaxQueued = defaults.MaxQueued
	}
	if config.GlobalConcurrency <= 0 {
		config.GlobalConcurrency = defaults.GlobalConcurrency
	}
	if config.PerTypeConcurrency == nil {
		config.PerTypeConcurrency = defaults.PerTypeConcurrency
	}
	if config.RetainCompleted <= 0 {
		config.RetainCompleted = defaults.RetainCompleted
	}
	if config.TerminalTTL <= 0 {
		config.TerminalTTL = defaults.TerminalTTL
	}
	if config.EventBuffer <= 0 {
		config.EventBuffer = defaults.EventBuffer
	}
	if config.DefaultTimeout <= 0 {
		config.DefaultTimeout = defaults.DefaultTimeout
	}
	if config.TypeTimeouts == nil {
		config.TypeTimeouts = defaults.TypeTimeouts
	}
	if config.Now == nil {
		config.Now = func() time.Time { return time.Now().UTC() }
	}
	return &Manager{
		config: config, broker: newBroker(config.EventBuffer), jobs: make(map[domain.JobID]*record),
		activeKey: make(map[string]domain.JobID), runningByType: make(map[string]int),
	}
}

func (m *Manager) Submit(ctx context.Context, spec Spec) (domain.JobSnapshot, error) {
	if err := ctx.Err(); err != nil {
		return domain.JobSnapshot{}, err
	}
	if spec.Type == "" || spec.Key == "" || spec.Runner == nil {
		return domain.JobSnapshot{}, ErrInvalidSpec
	}
	if spec.DedupPolicy == "" {
		spec.DedupPolicy = DedupCoalesce
	}
	m.mu.Lock()
	m.evictTerminalLocked(m.config.Now())
	if m.stopped {
		m.mu.Unlock()
		return domain.JobSnapshot{}, ErrStopped
	}
	key := activeKey(spec.Type, spec.Key)
	if existingID, ok := m.activeKey[key]; ok {
		existing := cloneSnapshot(m.jobs[existingID].snapshot)
		m.mu.Unlock()
		if spec.DedupPolicy == DedupCoalesce {
			return existing, nil
		}
		return domain.JobSnapshot{}, ErrRevisionConflict
	}
	if len(m.queue) >= m.config.MaxQueued {
		m.mu.Unlock()
		return domain.JobSnapshot{}, ErrQueueFull
	}
	id, err := newJobID()
	if err != nil {
		m.mu.Unlock()
		return domain.JobSnapshot{}, err
	}
	now := m.config.Now()
	rec := &record{spec: spec, snapshot: domain.JobSnapshot{
		ID: id, Type: spec.Type, Key: spec.Key, State: domain.JobQueued, InputSnapshotID: spec.InputSnapshotID,
		ClientRequestID: spec.ClientRequestID, CreatedAt: now, Revision: 1,
	}}
	m.jobs[id] = rec
	m.order = append(m.order, id)
	m.queue = append(m.queue, id)
	m.activeKey[key] = id
	snapshot := cloneSnapshot(rec.snapshot)
	m.mu.Unlock()

	m.publish("job.queued", snapshot)
	m.dispatch()
	return snapshot, nil
}

func (m *Manager) Get(id domain.JobID) (domain.JobSnapshot, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.evictTerminalLocked(m.config.Now())
	rec, ok := m.jobs[id]
	if !ok {
		return domain.JobSnapshot{}, ErrNotFound
	}
	return cloneSnapshot(rec.snapshot), nil
}

func (m *Manager) Result(id domain.JobID) (any, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	rec, ok := m.jobs[id]
	if !ok {
		return nil, ErrNotFound
	}
	if rec.snapshot.State != domain.JobSucceeded || rec.result == nil {
		return nil, ErrResultUnavailable
	}
	return rec.result, nil
}

func (m *Manager) List(filter domain.JobFilter) []domain.JobSnapshot {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.evictTerminalLocked(m.config.Now())
	out := make([]domain.JobSnapshot, 0, len(m.jobs))
	for _, id := range m.order {
		rec, ok := m.jobs[id]
		if !ok {
			continue
		}
		if filter.Type != "" && rec.snapshot.Type != filter.Type {
			continue
		}
		if filter.State != "" && rec.snapshot.State != filter.State {
			continue
		}
		out = append(out, cloneSnapshot(rec.snapshot))
	}
	sort.SliceStable(out, func(i, j int) bool {
		return out[i].CreatedAt.After(out[j].CreatedAt)
	})
	if filter.Limit > 0 && len(out) > filter.Limit {
		out = out[:filter.Limit]
	}
	return out
}

func (m *Manager) Cancel(_ context.Context, id domain.JobID, expectedRevision *uint64) (domain.JobSnapshot, error) {
	m.mu.Lock()
	rec, ok := m.jobs[id]
	if !ok {
		m.mu.Unlock()
		return domain.JobSnapshot{}, ErrNotFound
	}
	if expectedRevision != nil && rec.snapshot.Revision != *expectedRevision {
		snapshot := cloneSnapshot(rec.snapshot)
		m.mu.Unlock()
		return snapshot, ErrRevisionConflict
	}
	switch rec.snapshot.State {
	case domain.JobQueued:
		m.removeQueuedLocked(id)
		m.finishLocked(rec, domain.JobCanceled, nil, Result{})
		snapshot := cloneSnapshot(rec.snapshot)
		m.mu.Unlock()
		m.publish("job.canceled", snapshot)
		m.dispatch()
		return snapshot, nil
	case domain.JobRunning:
		rec.snapshot.State = domain.JobCanceling
		rec.snapshot.Revision++
		snapshot := cloneSnapshot(rec.snapshot)
		cancel := rec.cancel
		m.mu.Unlock()
		m.publish("job.canceling", snapshot)
		if cancel != nil {
			cancel()
		}
		return snapshot, nil
	case domain.JobCanceling:
		snapshot := cloneSnapshot(rec.snapshot)
		m.mu.Unlock()
		return snapshot, nil
	default:
		snapshot := cloneSnapshot(rec.snapshot)
		m.mu.Unlock()
		return snapshot, nil
	}
}

func (m *Manager) Subscribe() (<-chan domain.JobEvent, func()) {
	return m.broker.subscribe()
}

// SetEventSink installs the server-owned durable stream sink.
func (m *Manager) SetEventSink(sink func(domain.JobEvent)) { m.broker.setSink(sink) }

func (m *Manager) Shutdown(ctx context.Context) error {
	m.mu.Lock()
	m.stopped = true
	var canceled []domain.JobSnapshot
	for _, id := range append([]domain.JobID(nil), m.queue...) {
		if rec, ok := m.jobs[id]; ok {
			m.finishLocked(rec, domain.JobCanceled, nil, Result{})
			canceled = append(canceled, cloneSnapshot(rec.snapshot))
		}
	}
	m.queue = nil
	var cancels []context.CancelFunc
	for _, rec := range m.jobs {
		if rec.snapshot.State == domain.JobRunning || rec.snapshot.State == domain.JobCanceling {
			if rec.snapshot.State == domain.JobRunning {
				rec.snapshot.State = domain.JobCanceling
				rec.snapshot.Revision++
				canceled = append(canceled, cloneSnapshot(rec.snapshot))
			}
			if rec.cancel != nil {
				cancels = append(cancels, rec.cancel)
			}
		}
	}
	m.mu.Unlock()
	for _, snapshot := range canceled {
		eventType := "job.canceled"
		if snapshot.State == domain.JobCanceling {
			eventType = "job.canceling"
		}
		m.publish(eventType, snapshot)
	}
	for _, cancel := range cancels {
		cancel()
	}
	done := make(chan struct{})
	go func() {
		m.wg.Wait()
		close(done)
	}()
	select {
	case <-done:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

func (m *Manager) dispatch() {
	type start struct {
		rec      *record
		snapshot domain.JobSnapshot
		ctx      context.Context
	}
	var starts []start
	m.mu.Lock()
	for {
		id, ok := m.popStartableLocked()
		if !ok {
			break
		}
		rec := m.jobs[id]
		timeout := rec.spec.Timeout
		if timeout <= 0 && m.config.TypeTimeouts != nil {
			timeout = m.config.TypeTimeouts[rec.spec.Type]
		}
		if timeout <= 0 {
			timeout = m.config.DefaultTimeout
		}
		ctx, cancel := context.WithTimeout(context.Background(), timeout)
		rec.cancel = cancel
		now := m.config.Now()
		rec.snapshot.State = domain.JobRunning
		rec.snapshot.StartedAt = now
		rec.snapshot.Revision++
		m.running++
		m.runningByType[rec.snapshot.Type]++
		m.wg.Add(1)
		starts = append(starts, start{rec: rec, snapshot: cloneSnapshot(rec.snapshot), ctx: ctx})
	}
	m.mu.Unlock()
	for _, item := range starts {
		m.publish("job.started", item.snapshot)
		go m.run(item.ctx, item.rec)
	}
}

func (m *Manager) popStartableLocked() (domain.JobID, bool) {
	for index, id := range m.queue {
		if !m.canStartLocked(m.jobs[id]) {
			continue
		}
		copy(m.queue[index:], m.queue[index+1:])
		m.queue = m.queue[:len(m.queue)-1]
		return id, true
	}
	return "", false
}

func (m *Manager) canStartLocked(rec *record) bool {
	if rec == nil || m.running >= m.config.GlobalConcurrency {
		return false
	}
	limit := m.config.PerTypeConcurrency[rec.snapshot.Type]
	return limit <= 0 || m.runningByType[rec.snapshot.Type] < limit
}

func (m *Manager) run(ctx context.Context, rec *record) {
	defer m.wg.Done()
	defer rec.cancel()
	reporter := &reporter{manager: m, jobID: rec.snapshot.ID}
	var result Result
	var err error
	func() {
		defer func() {
			if recovered := recover(); recovered != nil {
				err = panicError(recovered)
			}
		}()
		result, err = rec.spec.Runner(ctx, reporter)
	}()
	m.complete(rec.snapshot.ID, result, err)
}

func (m *Manager) complete(id domain.JobID, result Result, err error) {
	m.mu.Lock()
	rec, ok := m.jobs[id]
	if !ok {
		m.mu.Unlock()
		return
	}
	state := domain.JobSucceeded
	eventType := "job.succeeded"
	if err != nil {
		switch {
		case isPanicError(err):
			state, eventType = domain.JobFailed, "job.failed"
			err = errors.New("JOB_PANIC")
		case errors.Is(err, ErrStale):
			state, eventType = domain.JobStale, "job.stale"
		case errors.Is(err, context.Canceled) && rec.snapshot.State == domain.JobCanceling:
			state, eventType = domain.JobCanceled, "job.canceled"
		default:
			state, eventType = domain.JobFailed, "job.failed"
		}
	}
	m.finishLocked(rec, state, err, result)
	snapshot := cloneSnapshot(rec.snapshot)
	m.running--
	if m.running < 0 {
		m.running = 0
	}
	m.runningByType[rec.snapshot.Type]--
	if m.runningByType[rec.snapshot.Type] <= 0 {
		delete(m.runningByType, rec.snapshot.Type)
	}
	m.mu.Unlock()
	m.publish(eventType, snapshot)
	m.dispatch()
}

func (m *Manager) finishLocked(rec *record, state domain.JobState, err error, result Result) {
	rec.snapshot.State = state
	rec.snapshot.FinishedAt = m.config.Now()
	rec.snapshot.Revision++
	if state == domain.JobSucceeded {
		rec.snapshot.Error = nil
		rec.snapshot.ResultArtifactID = result.ArtifactID
		rec.snapshot.ResultSnapshotID = result.SnapshotID
		rec.result = result.Payload
	} else if err != nil {
		if err.Error() == "JOB_PANIC" {
			rec.snapshot.Error = &domain.PublicError{Code: "JOB_PANIC", Message: "Job failed with an internal panic."}
		} else {
			rec.snapshot.Error = publicError(err)
		}
	}
	delete(m.activeKey, activeKey(rec.spec.Type, rec.spec.Key))
}

func (m *Manager) removeQueuedLocked(id domain.JobID) {
	out := m.queue[:0]
	for _, queuedID := range m.queue {
		if queuedID != id {
			out = append(out, queuedID)
		}
	}
	m.queue = out
}

func (m *Manager) evictTerminalLocked(now time.Time) {
	if m.config.RetainCompleted <= 0 {
		return
	}
	var terminals []domain.JobID
	remove := map[domain.JobID]struct{}{}
	for _, id := range m.order {
		rec := m.jobs[id]
		if rec == nil {
			remove[id] = struct{}{}
			continue
		}
		if !isTerminal(rec.snapshot.State) {
			continue
		}
		if m.config.TerminalTTL > 0 && !rec.snapshot.FinishedAt.IsZero() && now.Sub(rec.snapshot.FinishedAt) > m.config.TerminalTTL {
			delete(m.jobs, id)
			remove[id] = struct{}{}
			continue
		}
		terminals = append(terminals, id)
	}
	if len(terminals) > m.config.RetainCompleted {
		for _, id := range terminals[:len(terminals)-m.config.RetainCompleted] {
			remove[id] = struct{}{}
			delete(m.jobs, id)
		}
	}
	if len(remove) == 0 {
		return
	}
	out := m.order[:0]
	for _, id := range m.order {
		if _, ok := remove[id]; !ok {
			out = append(out, id)
		}
	}
	m.order = out
}

func (m *Manager) publish(eventType string, snapshot domain.JobSnapshot) {
	m.broker.publish(domain.JobEvent{Type: eventType, Job: snapshot})
}

func activeKey(jobType, key string) string { return jobType + "\x00" + key }

func isTerminal(state domain.JobState) bool {
	switch state {
	case domain.JobCanceled, domain.JobSucceeded, domain.JobFailed, domain.JobStale:
		return true
	default:
		return false
	}
}

func cloneSnapshot(input domain.JobSnapshot) domain.JobSnapshot {
	out := input
	if input.Progress.Percent != nil {
		value := *input.Progress.Percent
		out.Progress.Percent = &value
	}
	if input.Error != nil {
		copied := *input.Error
		out.Error = &copied
	}
	return out
}

func newJobID() (domain.JobID, error) {
	var bytes [16]byte
	if _, err := rand.Read(bytes[:]); err != nil {
		return "", err
	}
	return domain.JobID("job:" + hex.EncodeToString(bytes[:])), nil
}

type reporter struct {
	manager *Manager
	jobID   domain.JobID
}

func (r *reporter) Report(stage, message string, progress domain.Progress) error {
	m := r.manager
	m.mu.Lock()
	rec, ok := m.jobs[r.jobID]
	if !ok {
		m.mu.Unlock()
		return ErrNotFound
	}
	if rec.snapshot.State != domain.JobRunning && rec.snapshot.State != domain.JobCanceling {
		m.mu.Unlock()
		return ErrStopped
	}
	if stage == "" {
		stage = rec.snapshot.Stage
	}
	if stage == rec.snapshot.Stage && progress.Completed < rec.snapshot.Progress.Completed {
		m.mu.Unlock()
		return fmt.Errorf("%w: progress regressed", ErrInvalidSpec)
	}
	progress = normalizeProgress(progress)
	rec.snapshot.Stage = stage
	rec.snapshot.Message = message
	rec.snapshot.Progress = progress
	rec.snapshot.Revision++
	snapshot := cloneSnapshot(rec.snapshot)
	m.mu.Unlock()
	m.publish("job.progress", snapshot)
	return nil
}

func normalizeProgress(progress domain.Progress) domain.Progress {
	if progress.Indeterminate || progress.Total <= 0 {
		progress.Percent = nil
		return progress
	}
	if progress.Completed < 0 {
		progress.Completed = 0
	}
	if progress.Completed > progress.Total {
		progress.Completed = progress.Total
	}
	percent := float64(progress.Completed) * 100 / float64(progress.Total)
	progress.Percent = &percent
	return progress
}
