package mutation

import (
	"context"
	"sort"
	"sync"
	"time"

	"github.com/ThalesMMS/CodeAtlas/internal/domain"
	"github.com/ThalesMMS/CodeAtlas/internal/pathutil"
)

const (
	defaultMaxTotalEntries    = 1024
	defaultMaxEntriesPerPath  = 8
	defaultMaxTerminalEntries = 128
	defaultTTL                = time.Minute
)

// RegistryConfig bounds the ephemeral mutation registry. A zero value uses
// conservative defaults.
type RegistryConfig struct {
	MaxTotalEntries    int
	MaxEntriesPerPath  int
	MaxTerminalEntries int
	DefaultTTL         time.Duration
	Now                func() time.Time
}

// MemoryRegistry is a bounded, thread-safe internal mutation registry.
type MemoryRegistry struct {
	mu     sync.RWMutex
	config RegistryConfig
	closed bool

	entries map[domain.InternalMutationID]domain.InternalMutation
	byPath  map[string][]domain.InternalMutationID

	terminalOrder              []domain.InternalMutationID
	selfWriteConfirmedTotal    uint64
	externalAfterInternalTotal uint64
}

func NewMemoryRegistry(config RegistryConfig) *MemoryRegistry {
	if config.MaxTotalEntries <= 0 {
		config.MaxTotalEntries = defaultMaxTotalEntries
	}
	if config.MaxEntriesPerPath <= 0 {
		config.MaxEntriesPerPath = defaultMaxEntriesPerPath
	}
	if config.MaxTerminalEntries <= 0 {
		config.MaxTerminalEntries = defaultMaxTerminalEntries
	}
	if config.DefaultTTL <= 0 {
		config.DefaultTTL = defaultTTL
	}
	if config.Now == nil {
		config.Now = func() time.Time { return time.Now().UTC() }
	}
	return &MemoryRegistry{
		config:  config,
		entries: make(map[domain.InternalMutationID]domain.InternalMutation),
		byPath:  make(map[string][]domain.InternalMutationID),
	}
}

func (r *MemoryRegistry) Stage(ctx context.Context, mutation domain.InternalMutation) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	path, err := pathutil.NormalizeWorkspaceRelative(mutation.Path)
	if err != nil {
		return ErrMutationInvalid
	}
	if mutation.ID == "" || mutation.PublishedContentHash == "" {
		return ErrMutationInvalid
	}

	r.mu.Lock()
	defer r.mu.Unlock()
	if r.closed {
		return ErrRegistryClosed
	}
	now := r.now()
	r.expireLocked(now)
	if _, exists := r.entries[mutation.ID]; exists {
		return ErrMutationInvalid
	}
	if r.activeCountLocked() >= r.config.MaxTotalEntries || r.activeCountForPathLocked(path) >= r.config.MaxEntriesPerPath {
		return ErrRegistryFull
	}
	if mutation.RegisteredAt.IsZero() {
		mutation.RegisteredAt = now
	}
	if mutation.ExpiresAt.IsZero() {
		mutation.ExpiresAt = mutation.RegisteredAt.Add(r.config.DefaultTTL)
	}
	mutation.Path = path
	mutation.State = domain.MutationStaged
	r.entries[mutation.ID] = copyMutation(mutation)
	r.byPath[path] = append(r.byPath[path], mutation.ID)
	return nil
}

func (r *MemoryRegistry) MarkPublished(ctx context.Context, id domain.InternalMutationID, result MutationCommitResult) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.closed {
		return ErrRegistryClosed
	}
	now := r.now()
	r.expireLocked(now)
	mutation, exists := r.entries[id]
	if !exists {
		return ErrMutationNotFound
	}
	if mutation.State != domain.MutationStaged {
		return ErrMutationStateInvalid
	}
	committedAt := result.CommittedAt
	if committedAt.IsZero() {
		committedAt = now
	}
	mutation.PublishedSnapshotID = result.SnapshotID
	mutation.PublishedRevision = result.Revision
	mutation.PublishedAt = committedAt
	mutation.ExpiresAt = committedAt.Add(r.config.DefaultTTL)
	mutation.State = domain.MutationPublished
	r.entries[id] = copyMutation(mutation)
	r.expireOlderPathMutationsLocked(mutation.Path, id, mutation.PublishedRevision)
	return nil
}

func (r *MemoryRegistry) Match(ctx context.Context, observation domain.FileObservation) (domain.MutationMatch, error) {
	if err := ctx.Err(); err != nil {
		return domain.MutationMatch{}, err
	}
	path, err := pathutil.NormalizeWorkspaceRelative(observation.Path)
	if err != nil {
		return domain.MutationMatch{}, ErrMutationInvalid
	}

	r.mu.Lock()
	defer r.mu.Unlock()
	if r.closed {
		return domain.MutationMatch{}, ErrRegistryClosed
	}
	now := r.now()
	r.expireLocked(now)
	published := r.publishedForPathLocked(path)
	if len(published) == 0 {
		if !observation.Exists {
			return domain.MutationMatch{Classification: domain.ObservationRemoved}, nil
		}
		return domain.MutationMatch{Classification: domain.ObservationUnchanged}, nil
	}
	sort.Slice(published, func(a, b int) bool {
		left, right := published[a], published[b]
		if left.PublishedRevision != right.PublishedRevision {
			return left.PublishedRevision > right.PublishedRevision
		}
		if !left.PublishedAt.Equal(right.PublishedAt) {
			return left.PublishedAt.After(right.PublishedAt)
		}
		return left.ID > right.ID
	})
	latest := published[0]
	if observation.Exists &&
		observation.ContentHash != "" &&
		observation.ContentHash == latest.PublishedContentHash &&
		observation.StoreContentHash == latest.PublishedContentHash &&
		observation.StoreRevision >= latest.PublishedRevision {
		return domain.MutationMatch{
			Matched:        true,
			MutationID:     latest.ID,
			Classification: domain.ObservationSelfWriteConfirmed,
		}, nil
	}
	classification := domain.ObservationExternalChange
	if !observation.Exists {
		classification = domain.ObservationRemoved
	}
	return domain.MutationMatch{MutationID: latest.ID, Classification: classification}, nil
}

func (r *MemoryRegistry) MarkObserved(ctx context.Context, id domain.InternalMutationID, sequence uint64) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.closed {
		return ErrRegistryClosed
	}
	mutation, exists := r.entries[id]
	if !exists {
		return ErrMutationNotFound
	}
	if mutation.State != domain.MutationPublished {
		return ErrMutationStateInvalid
	}
	mutation.State = domain.MutationObserved
	mutation.ObservedAt = r.now()
	mutation.ObservedSequence = sequence
	r.recordTerminalLocked(mutation)
	r.selfWriteConfirmedTotal++
	return nil
}

func (r *MemoryRegistry) MarkExternal(ctx context.Context, id domain.InternalMutationID) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.closed {
		return ErrRegistryClosed
	}
	mutation, exists := r.entries[id]
	if !exists {
		return nil
	}
	if mutation.State != domain.MutationPublished {
		return nil
	}
	mutation.State = domain.MutationExpired
	r.recordTerminalLocked(mutation)
	r.externalAfterInternalTotal++
	return nil
}

func (r *MemoryRegistry) Cancel(ctx context.Context, id domain.InternalMutationID) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.closed {
		return ErrRegistryClosed
	}
	mutation, exists := r.entries[id]
	if !exists {
		return nil
	}
	if mutation.State == domain.MutationStaged || mutation.State == domain.MutationPublished {
		delete(r.entries, id)
		r.removePathIDLocked(mutation.Path, id)
	}
	return nil
}

func (r *MemoryRegistry) Expire(now time.Time) []domain.InternalMutation {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.expireLocked(now)
}

func (r *MemoryRegistry) Snapshot() domain.MutationRegistrySnapshot {
	r.mu.RLock()
	defer r.mu.RUnlock()
	snapshot := domain.MutationRegistrySnapshot{
		PathsTracked:               len(r.byPath),
		SelfWriteConfirmedTotal:    r.selfWriteConfirmedTotal,
		ExternalAfterInternalTotal: r.externalAfterInternalTotal,
		Entries:                    make([]domain.InternalMutation, 0, len(r.entries)),
	}
	for _, mutation := range r.entries {
		switch mutation.State {
		case domain.MutationStaged:
			snapshot.StagedCount++
		case domain.MutationPublished:
			snapshot.PublishedCount++
		case domain.MutationObserved:
			snapshot.ObservedCount++
		case domain.MutationExpired:
			snapshot.ExpiredCount++
		}
		snapshot.Entries = append(snapshot.Entries, copyMutation(mutation))
	}
	sort.Slice(snapshot.Entries, func(a, b int) bool { return snapshot.Entries[a].ID < snapshot.Entries[b].ID })
	return snapshot
}

func (r *MemoryRegistry) Close() {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.closed = true
}

func (r *MemoryRegistry) now() time.Time {
	return r.config.Now().UTC()
}

func (r *MemoryRegistry) expireLocked(now time.Time) []domain.InternalMutation {
	expired := make([]domain.InternalMutation, 0)
	for _, mutation := range r.entries {
		if mutation.State != domain.MutationStaged && mutation.State != domain.MutationPublished {
			continue
		}
		if mutation.ExpiresAt.IsZero() || now.Before(mutation.ExpiresAt) {
			continue
		}
		mutation.State = domain.MutationExpired
		r.recordTerminalLocked(mutation)
		expired = append(expired, copyMutation(mutation))
	}
	sort.Slice(expired, func(a, b int) bool { return expired[a].ID < expired[b].ID })
	return expired
}

func (r *MemoryRegistry) activeCountLocked() int {
	count := 0
	for _, mutation := range r.entries {
		if mutation.State == domain.MutationStaged || mutation.State == domain.MutationPublished {
			count++
		}
	}
	return count
}

func (r *MemoryRegistry) activeCountForPathLocked(path string) int {
	count := 0
	for _, id := range r.byPath[path] {
		mutation := r.entries[id]
		if mutation.State == domain.MutationStaged || mutation.State == domain.MutationPublished {
			count++
		}
	}
	return count
}

func (r *MemoryRegistry) publishedForPathLocked(path string) []domain.InternalMutation {
	ids := r.byPath[path]
	out := make([]domain.InternalMutation, 0, len(ids))
	for _, id := range ids {
		mutation, exists := r.entries[id]
		if exists && mutation.State == domain.MutationPublished {
			out = append(out, copyMutation(mutation))
		}
	}
	return out
}

func (r *MemoryRegistry) removePathIDLocked(path string, id domain.InternalMutationID) {
	ids := r.byPath[path]
	if len(ids) == 0 {
		return
	}
	filtered := ids[:0]
	for _, candidate := range ids {
		if candidate != id {
			filtered = append(filtered, candidate)
		}
	}
	if len(filtered) == 0 {
		delete(r.byPath, path)
		return
	}
	r.byPath[path] = filtered
}

func (r *MemoryRegistry) expireOlderPathMutationsLocked(path string, keep domain.InternalMutationID, revision domain.Revision) {
	for _, id := range append([]domain.InternalMutationID(nil), r.byPath[path]...) {
		if id == keep {
			continue
		}
		mutation, exists := r.entries[id]
		if !exists || mutation.State != domain.MutationPublished {
			continue
		}
		if mutation.PublishedRevision != 0 && mutation.PublishedRevision > revision {
			continue
		}
		mutation.State = domain.MutationExpired
		r.recordTerminalLocked(mutation)
	}
}

func (r *MemoryRegistry) recordTerminalLocked(mutation domain.InternalMutation) {
	r.entries[mutation.ID] = copyMutation(mutation)
	r.removePathIDLocked(mutation.Path, mutation.ID)
	r.terminalOrder = append(r.terminalOrder, mutation.ID)
	for len(r.terminalOrder) > r.config.MaxTerminalEntries {
		oldest := r.terminalOrder[0]
		r.terminalOrder = r.terminalOrder[1:]
		if entry, exists := r.entries[oldest]; exists &&
			(entry.State == domain.MutationObserved || entry.State == domain.MutationExpired) {
			delete(r.entries, oldest)
		}
	}
}

func copyMutation(mutation domain.InternalMutation) domain.InternalMutation {
	return mutation
}
