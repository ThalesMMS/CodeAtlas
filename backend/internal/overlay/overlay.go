// Package overlay holds active, unsaved document buffers as ephemeral, versioned
// state, separate from the persisted index snapshot. Hover/See More can then
// explain exactly what the user is editing. It is thread-safe; snapshots are
// immutable and returned by defensive copy; ordering is by version, never by
// clock. Persistence, crash recovery and incremental parsing are out of scope.
package overlay

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"sync"
	"time"
	"unicode/utf8"

	"github.com/ThalesMMS/CodeAtlas/internal/apperror"
	"github.com/ThalesMMS/CodeAtlas/internal/contenthash"
	"github.com/ThalesMMS/CodeAtlas/internal/domain"
)

type (
	DocumentID      string
	LeaseID         string
	DocumentVersion int64
	DocumentState   string
)

const (
	StateClean                DocumentState = "clean"
	StateLocalDirty           DocumentState = "local_dirty"
	StateSaving               DocumentState = "saving"
	StateExternalChangedClean DocumentState = "external_changed_clean"
	StateConflictedDirty      DocumentState = "conflicted_dirty"
	StateExternalDeleted      DocumentState = "external_deleted"
	StateExternalRenamed      DocumentState = "external_renamed"
	StateResolving            DocumentState = "resolving"
	StateClosed               DocumentState = "closed"
)

const (
	ReasonExternalModification = "EXTERNAL_MODIFICATION"
	ReasonExternalDeletion     = "EXTERNAL_DELETION"
	ReasonBecameIgnored        = "BECAME_IGNORED"
	ReasonLanguageUnsupported  = "LANGUAGE_UNSUPPORTED"
)

// DocumentConflict contains conflict metadata only. It never includes local or
// external content.
type DocumentConflict struct {
	DocumentID           DocumentID
	Path                 string
	State                DocumentState
	LocalVersion         DocumentVersion
	LocalContentHash     string
	BaseContentHash      string
	BaseSnapshotID       domain.SnapshotID
	ExternalContentHash  string
	ExternalSnapshotID   domain.SnapshotID
	ExternalRevision     domain.Revision
	DetectedAt           time.Time
	ReasonCode           string
	ReconcileOperationID string
}

func (c *DocumentConflict) copy() *DocumentConflict {
	if c == nil {
		return nil
	}
	clone := *c
	return &clone
}

// OverlaySnapshot is an immutable view of one open document. Content is a private
// copy; callers receive their own copy from every API.
type OverlaySnapshot struct {
	DocumentID      DocumentID
	LeaseID         LeaseID
	Path            string
	Language        string
	Version         DocumentVersion
	Content         []byte
	BaseContent     []byte
	ContentHash     string
	BaseContentHash string
	BaseSnapshotID  domain.SnapshotID
	Dirty           bool
	State           DocumentState
	Conflict        *DocumentConflict
	OpenedAt        time.Time
	UpdatedAt       time.Time
}

func (s OverlaySnapshot) copy() OverlaySnapshot {
	clone := s
	clone.Content = append([]byte(nil), s.Content...)
	clone.BaseContent = append([]byte(nil), s.BaseContent...)
	clone.Conflict = s.Conflict.copy()
	return clone
}

// Workspace is the path/IO boundary the store depends on. The service's Workspace
// satisfies it; the store never writes to disk.
type Workspace interface {
	Resolve(relative string) (string, error)
	Read(relative string) ([]byte, error)
}

// DetectFunc reports the display language for a path and whether it is supported.
type DetectFunc func(path string) (language string, supported bool)

// SnapshotIDFunc returns the current persisted snapshot id to pin as the base.
type SnapshotIDFunc func() domain.SnapshotID

// Config bounds the store. Dirty documents are never evicted to satisfy a limit.
type Config struct {
	MaxDocuments        int
	MaxBytesPerDocument int
	MaxTotalBytes       int
	CleanRetention      time.Duration // retain detached/clean documents at most this long (0 = no auto-retain logic)
	LeaseTTL            time.Duration // expire abandoned writable leases after this idle period (0 = disabled)
}

func (c Config) withDefaults() Config {
	if c.MaxDocuments <= 0 {
		c.MaxDocuments = 256
	}
	if c.MaxBytesPerDocument <= 0 {
		c.MaxBytesPerDocument = 8 << 20
	}
	if c.MaxTotalBytes <= 0 {
		c.MaxTotalBytes = 128 << 20
	}
	return c
}

// OpenRequest opens a workspace file as an editable overlay.
type OpenRequest struct {
	Path string
}

// ReplaceRequest replaces a document's whole content (no patches in v1).
type ReplaceRequest struct {
	DocumentID      DocumentID
	LeaseID         LeaseID
	ExpectedVersion DocumentVersion
	NewVersion      DocumentVersion
	Content         []byte
}

// ExternalUpdate is the overlay-facing view of one published file change.
type ExternalUpdate struct {
	OperationID string
	Path        string
	OldHash     string
	NewHash     string
	SnapshotID  domain.SnapshotID
	Revision    domain.Revision
	Kind        string
	Origin      string
}

type DocumentTransition struct {
	DocumentID DocumentID
	Path       string
	From       DocumentState
	To         DocumentState
	Conflict   *DocumentConflict
}

type ResolveStrategy string

const (
	ResolveReloadExternal  ResolveStrategy = "reload_external"
	ResolveKeepLocalRebase ResolveStrategy = "keep_local_rebase"
)

type ResolveConflictRequest struct {
	DocumentID          DocumentID
	LeaseID             LeaseID
	DocumentVersion     DocumentVersion
	ExternalContentHash string
	Strategy            ResolveStrategy
}

type ResolveConflictResult struct {
	Snapshot                    OverlaySnapshot
	WillOverwriteExternalOnSave bool
}

// OverlayStore is the document-overlay contract.
type OverlayStore interface {
	Open(ctx context.Context, req OpenRequest) (OverlaySnapshot, error)
	Replace(ctx context.Context, req ReplaceRequest) (OverlaySnapshot, error)
	Get(documentID DocumentID, version *DocumentVersion) (OverlaySnapshot, error)
	Renew(documentID DocumentID, leaseID LeaseID, version *DocumentVersion) (OverlaySnapshot, error)
	Reclaim(documentID DocumentID, leaseID LeaseID) (OverlaySnapshot, error)
	MarkSaved(documentID DocumentID, leaseID LeaseID, version DocumentVersion, newBaseHash string, snapshotID domain.SnapshotID) (OverlaySnapshot, error)
	ApplyExternalChange(ctx context.Context, update ExternalUpdate) ([]DocumentTransition, error)
	ApplyWorkspaceChange(ctx context.Context, change domain.PublishedWorkspaceChange) ([]DocumentTransition, error)
	ResolveConflict(ctx context.Context, req ResolveConflictRequest) (ResolveConflictResult, error)
	Close(documentID DocumentID, leaseID LeaseID, discard bool) error
}

// Store is an in-memory OverlayStore. A single mutex guards all state; critical
// sections do only validation, hashing and a content copy — never parsing or
// callbacks — so holding it is cheap.
type Store struct {
	ws         Workspace
	detect     DetectFunc
	snapshotID SnapshotIDFunc
	now        func() time.Time
	config     Config

	mu               sync.Mutex
	documents        map[DocumentID]OverlaySnapshot
	byPath           map[string]DocumentID
	resolving        map[DocumentID]bool
	leaseTimers      map[DocumentID]leaseTimer
	leaseGenerations map[DocumentID]uint64
	afterFunc        func(time.Duration, func()) leaseTimer
	onExpire         func(OverlaySnapshot)
	totalBytes       int
}

type leaseTimer interface {
	Stop() bool
}

// NewStore builds a store. now may be nil (defaults to time.Now); it is only used
// for OpenedAt/UpdatedAt metadata, never to order versions.
func NewStore(ws Workspace, detect DetectFunc, snapshotID SnapshotIDFunc, now func() time.Time, config Config) *Store {
	if now == nil {
		now = time.Now
	}
	return &Store{
		ws:               ws,
		detect:           detect,
		snapshotID:       snapshotID,
		now:              now,
		config:           config.withDefaults(),
		documents:        make(map[DocumentID]OverlaySnapshot),
		byPath:           make(map[string]DocumentID),
		resolving:        make(map[DocumentID]bool),
		leaseTimers:      make(map[DocumentID]leaseTimer),
		leaseGenerations: make(map[DocumentID]uint64),
		afterFunc: func(delay time.Duration, callback func()) leaseTimer {
			return time.AfterFunc(delay, callback)
		},
	}
}

// SetExpireHandler installs the server cleanup hook invoked after an abandoned
// overlay is removed. The callback runs outside the store mutex.
func (s *Store) SetExpireHandler(handler func(OverlaySnapshot)) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.onExpire = handler
}

func (s *Store) Open(_ context.Context, req OpenRequest) (OverlaySnapshot, error) {
	if _, err := s.ws.Resolve(req.Path); err != nil {
		return OverlaySnapshot{}, err
	}
	language, supported := s.detect(req.Path)
	if !supported {
		return OverlaySnapshot{}, apperror.UnsupportedLanguage(req.Path)
	}
	content, err := s.ws.Read(req.Path)
	if err != nil {
		return OverlaySnapshot{}, apperror.FileNotFound(req.Path, err)
	}
	if len(content) > s.config.MaxBytesPerDocument {
		return OverlaySnapshot{}, apperror.RequestTooLarge(int64(s.config.MaxBytesPerDocument))
	}
	baseSnapshot := s.snapshotID()
	hash := contenthash.HashContent(content)

	s.mu.Lock()
	defer s.mu.Unlock()
	if existing, ok := s.byPath[req.Path]; ok {
		_ = existing
		return OverlaySnapshot{}, apperror.DocumentAlreadyOpen(req.Path)
	}
	if len(s.documents) >= s.config.MaxDocuments {
		return OverlaySnapshot{}, apperror.OverlayLimitExceeded("max open documents reached")
	}
	if s.totalBytes+2*len(content) > s.config.MaxTotalBytes {
		return OverlaySnapshot{}, apperror.OverlayLimitExceeded("max total overlay bytes reached")
	}

	now := s.now().UTC()
	snapshot := OverlaySnapshot{
		DocumentID:      DocumentID(randomID()),
		LeaseID:         LeaseID(randomID()),
		Path:            req.Path,
		Language:        language,
		Version:         1,
		Content:         append([]byte(nil), content...),
		BaseContent:     append([]byte(nil), content...),
		ContentHash:     hash,
		BaseContentHash: hash,
		BaseSnapshotID:  baseSnapshot,
		Dirty:           false,
		State:           StateClean,
		OpenedAt:        now,
		UpdatedAt:       now,
	}
	s.documents[snapshot.DocumentID] = snapshot
	s.byPath[req.Path] = snapshot.DocumentID
	s.totalBytes += snapshotBytes(snapshot)
	s.scheduleLeaseLocked(snapshot.DocumentID)
	return snapshot.copy(), nil
}

func (s *Store) Replace(_ context.Context, req ReplaceRequest) (OverlaySnapshot, error) {
	if len(req.Content) > s.config.MaxBytesPerDocument {
		return OverlaySnapshot{}, apperror.RequestTooLarge(int64(s.config.MaxBytesPerDocument))
	}
	if !utf8.Valid(req.Content) {
		return OverlaySnapshot{}, apperror.InvalidArgumentMessage("content", "Content is not valid UTF-8.", nil)
	}

	s.mu.Lock()
	defer s.mu.Unlock()
	current, ok := s.documents[req.DocumentID]
	if !ok {
		return OverlaySnapshot{}, apperror.DocumentNotFound(string(req.DocumentID))
	}
	if req.LeaseID != current.LeaseID {
		return OverlaySnapshot{}, apperror.DocumentNotFound(string(req.DocumentID))
	}
	if req.ExpectedVersion != current.Version {
		return OverlaySnapshot{}, apperror.DocumentVersionConflict(current.Path)
	}
	// Versions advance by exactly one; never derived from a clock.
	if req.NewVersion != current.Version+1 {
		return OverlaySnapshot{}, apperror.DocumentVersionConflict(current.Path)
	}
	delta := len(req.Content) - len(current.Content)
	if s.totalBytes+delta > s.config.MaxTotalBytes {
		return OverlaySnapshot{}, apperror.OverlayLimitExceeded("max total overlay bytes reached")
	}
	// A dirty buffer must also be saveable: MarkSaved replaces BaseContent with
	// Content after the workspace commit, when rejecting would be too late to
	// preserve atomic editor state.
	savedDelta := 2*len(req.Content) - snapshotBytes(current)
	if s.totalBytes+savedDelta > s.config.MaxTotalBytes {
		return OverlaySnapshot{}, apperror.OverlayLimitExceeded("max total overlay bytes reached")
	}

	hash := contenthash.HashContent(req.Content)
	next := current
	next.Version = req.NewVersion
	next.Content = append([]byte(nil), req.Content...)
	next.ContentHash = hash
	next.Dirty = hash != current.BaseContentHash
	if current.State == StateConflictedDirty || current.State == StateExternalDeleted || current.State == StateExternalRenamed || current.State == StateResolving {
		next.State = current.State
	} else if next.Dirty {
		next.State = StateLocalDirty
		next.Conflict = nil
	} else {
		next.State = StateClean
		next.Conflict = nil
	}
	next.UpdatedAt = s.now().UTC()
	s.documents[req.DocumentID] = next
	s.totalBytes += delta
	s.scheduleLeaseLocked(req.DocumentID)
	return next.copy(), nil
}

func (s *Store) Get(documentID DocumentID, version *DocumentVersion) (OverlaySnapshot, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	current, ok := s.documents[documentID]
	if !ok {
		return OverlaySnapshot{}, apperror.DocumentNotFound(string(documentID))
	}
	if version != nil && *version != current.Version {
		return OverlaySnapshot{}, apperror.DocumentVersionConflict(current.Path)
	}
	return current.copy(), nil
}

// Renew validates ownership and extends the bounded lease lifetime. Ordinary
// internal reads use Get and therefore cannot keep an abandoned editor alive.
func (s *Store) Renew(documentID DocumentID, leaseID LeaseID, version *DocumentVersion) (OverlaySnapshot, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	current, ok := s.documents[documentID]
	if !ok || current.LeaseID != leaseID {
		return OverlaySnapshot{}, apperror.DocumentNotFound(string(documentID))
	}
	if version != nil && *version != current.Version {
		return OverlaySnapshot{}, apperror.DocumentVersionConflict(current.Path)
	}
	current.UpdatedAt = s.now().UTC()
	s.documents[documentID] = current
	s.scheduleLeaseLocked(documentID)
	return current.copy(), nil
}

// Reclaim atomically transfers an existing browser lease to a newly loaded
// page. Rotating the token makes a delayed pagehide DELETE from the previous
// page harmless: it can no longer close the document after the new page has
// successfully reclaimed it.
func (s *Store) Reclaim(documentID DocumentID, leaseID LeaseID) (OverlaySnapshot, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	current, ok := s.documents[documentID]
	if !ok || current.LeaseID != leaseID {
		return OverlaySnapshot{}, apperror.DocumentNotFound(string(documentID))
	}
	current.LeaseID = LeaseID(randomID())
	current.UpdatedAt = s.now().UTC()
	s.documents[documentID] = current
	s.scheduleLeaseLocked(documentID)
	return current.copy(), nil
}

func (s *Store) MarkSaved(documentID DocumentID, leaseID LeaseID, version DocumentVersion, newBaseHash string, snapshotID domain.SnapshotID) (OverlaySnapshot, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	current, ok := s.documents[documentID]
	if !ok {
		return OverlaySnapshot{}, apperror.DocumentNotFound(string(documentID))
	}
	if leaseID != current.LeaseID || version != current.Version {
		return OverlaySnapshot{}, apperror.DocumentVersionConflict(current.Path)
	}
	baseDelta := len(current.Content) - len(current.BaseContent)
	if s.totalBytes+baseDelta > s.config.MaxTotalBytes {
		return OverlaySnapshot{}, apperror.OverlayLimitExceeded("max total overlay bytes reached")
	}
	next := current
	next.BaseContentHash = newBaseHash
	next.BaseContent = append([]byte(nil), next.Content...)
	next.BaseSnapshotID = snapshotID
	next.Dirty = next.ContentHash != newBaseHash
	if next.Dirty {
		next.State = StateLocalDirty
	} else {
		next.State = StateClean
	}
	next.Conflict = nil
	next.UpdatedAt = s.now().UTC()
	s.documents[documentID] = next
	s.totalBytes += baseDelta
	s.scheduleLeaseLocked(documentID)
	return next.copy(), nil
}

func (s *Store) ApplyWorkspaceChange(ctx context.Context, change domain.PublishedWorkspaceChange) ([]DocumentTransition, error) {
	transitions := make([]DocumentTransition, 0)
	for _, file := range change.Changes {
		if file.Origin == domain.FileChangeOriginInternal {
			continue
		}
		next, err := s.ApplyExternalChange(ctx, ExternalUpdate{
			OperationID: change.OperationID,
			Path:        file.Path,
			OldHash:     file.OldHash,
			NewHash:     file.NewHash,
			SnapshotID:  change.SnapshotID,
			Revision:    change.Revision,
			Kind:        file.Kind,
			Origin:      file.Origin,
		})
		if err != nil {
			return transitions, err
		}
		transitions = append(transitions, next...)
	}
	return transitions, nil
}

func (s *Store) ApplyExternalChange(ctx context.Context, update ExternalUpdate) ([]DocumentTransition, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if update.Origin == domain.FileChangeOriginInternal {
		return nil, nil
	}
	if update.Kind == domain.FileChangeRemoved || update.Kind == domain.FileChangeBecameIgnored || update.Kind == domain.FileChangeBecameUnsupported {
		return s.applyExternalRemoval(update), nil
	}

	s.mu.Lock()
	documentID, ok := s.byPath[update.Path]
	if !ok {
		s.mu.Unlock()
		return nil, nil
	}
	current := s.documents[documentID]
	if current.ContentHash == update.NewHash && current.BaseContentHash == update.NewHash {
		s.mu.Unlock()
		return nil, nil
	}
	if current.Dirty || current.State == StateLocalDirty || current.State == StateConflictedDirty {
		transition := s.markConflictLocked(current, update, StateConflictedDirty, ReasonExternalModification)
		s.mu.Unlock()
		return []DocumentTransition{transition}, nil
	}
	version := current.Version
	s.mu.Unlock()

	content, err := s.ws.Read(update.Path)
	if err != nil {
		return s.applyExternalRemoval(ExternalUpdate{
			OperationID: update.OperationID,
			Path:        update.Path,
			OldHash:     update.OldHash,
			SnapshotID:  update.SnapshotID,
			Revision:    update.Revision,
			Kind:        domain.FileChangeRemoved,
			Origin:      update.Origin,
		}), nil
	}
	hash := contenthash.HashContent(content)
	if update.NewHash != "" && hash != update.NewHash {
		update.NewHash = hash
	}

	s.mu.Lock()
	defer s.mu.Unlock()
	current, ok = s.documents[documentID]
	if !ok || current.Path != update.Path {
		return nil, nil
	}
	if current.Version != version || current.Dirty || current.State == StateLocalDirty || current.State == StateConflictedDirty {
		transition := s.markConflictLocked(current, update, StateConflictedDirty, ReasonExternalModification)
		return []DocumentTransition{transition}, nil
	}
	if len(content) > s.config.MaxBytesPerDocument {
		return nil, apperror.RequestTooLarge(int64(s.config.MaxBytesPerDocument))
	}
	delta := 2*len(content) - snapshotBytes(current)
	if s.totalBytes+delta > s.config.MaxTotalBytes {
		return nil, apperror.OverlayLimitExceeded("max total overlay bytes reached")
	}
	next := current
	next.Version++
	next.Content = append([]byte(nil), content...)
	next.BaseContent = append([]byte(nil), content...)
	next.ContentHash = hash
	next.BaseContentHash = hash
	next.BaseSnapshotID = update.SnapshotID
	next.Dirty = false
	next.State = StateExternalChangedClean
	next.Conflict = s.conflictFor(next, update, StateExternalChangedClean, ReasonExternalModification)
	next.UpdatedAt = s.now().UTC()
	s.totalBytes += delta
	s.documents[documentID] = next
	return []DocumentTransition{{DocumentID: next.DocumentID, Path: next.Path, From: current.State, To: next.State, Conflict: next.Conflict.copy()}}, nil
}

func (s *Store) applyExternalRemoval(update ExternalUpdate) []DocumentTransition {
	s.mu.Lock()
	defer s.mu.Unlock()
	documentID, ok := s.byPath[update.Path]
	if !ok {
		return nil
	}
	current := s.documents[documentID]
	reason := ReasonExternalDeletion
	if update.Kind == domain.FileChangeBecameIgnored {
		reason = ReasonBecameIgnored
	} else if update.Kind == domain.FileChangeBecameUnsupported {
		reason = ReasonLanguageUnsupported
	}
	to := StateExternalDeleted
	transition := s.markConflictLocked(current, update, to, reason)
	return []DocumentTransition{transition}
}

func (s *Store) markConflictLocked(current OverlaySnapshot, update ExternalUpdate, state DocumentState, reason string) DocumentTransition {
	next := current
	next.State = state
	next.Conflict = s.conflictFor(current, update, state, reason)
	next.UpdatedAt = s.now().UTC()
	s.documents[current.DocumentID] = next
	return DocumentTransition{DocumentID: current.DocumentID, Path: current.Path, From: current.State, To: state, Conflict: next.Conflict.copy()}
}

func (s *Store) conflictFor(current OverlaySnapshot, update ExternalUpdate, state DocumentState, reason string) *DocumentConflict {
	return &DocumentConflict{
		DocumentID:           current.DocumentID,
		Path:                 current.Path,
		State:                state,
		LocalVersion:         current.Version,
		LocalContentHash:     current.ContentHash,
		BaseContentHash:      current.BaseContentHash,
		BaseSnapshotID:       current.BaseSnapshotID,
		ExternalContentHash:  update.NewHash,
		ExternalSnapshotID:   update.SnapshotID,
		ExternalRevision:     update.Revision,
		DetectedAt:           s.now().UTC(),
		ReasonCode:           reason,
		ReconcileOperationID: update.OperationID,
	}
}

func (s *Store) ResolveConflict(ctx context.Context, req ResolveConflictRequest) (ResolveConflictResult, error) {
	if err := ctx.Err(); err != nil {
		return ResolveConflictResult{}, err
	}
	s.mu.Lock()
	current, ok := s.documents[req.DocumentID]
	if !ok {
		s.mu.Unlock()
		return ResolveConflictResult{}, apperror.DocumentNotFound(string(req.DocumentID))
	}
	if current.LeaseID != req.LeaseID || current.Version != req.DocumentVersion {
		s.mu.Unlock()
		return ResolveConflictResult{}, apperror.DocumentVersionConflict(current.Path)
	}
	if current.Conflict == nil {
		s.mu.Unlock()
		return ResolveConflictResult{}, apperror.DocumentConflictNotFound(string(req.DocumentID))
	}
	if current.Conflict.ExternalContentHash != req.ExternalContentHash {
		s.mu.Unlock()
		return ResolveConflictResult{}, apperror.DocumentConflictChanged(string(req.DocumentID))
	}
	if s.resolving[req.DocumentID] {
		s.mu.Unlock()
		return ResolveConflictResult{}, apperror.DocumentResolutionInProgress(string(req.DocumentID))
	}
	switch req.Strategy {
	case ResolveReloadExternal, ResolveKeepLocalRebase:
	default:
		s.mu.Unlock()
		return ResolveConflictResult{}, apperror.InvalidArgumentMessage("strategy", "Unknown resolution strategy.", nil)
	}
	externalDeleted := current.State == StateExternalDeleted
	s.resolving[req.DocumentID] = true
	s.mu.Unlock()
	defer func() {
		s.mu.Lock()
		delete(s.resolving, req.DocumentID)
		s.mu.Unlock()
	}()

	var externalContent []byte
	if !externalDeleted {
		var err error
		externalContent, err = s.ws.Read(current.Path)
		if err != nil {
			return ResolveConflictResult{}, apperror.FileNotFound(current.Path, err)
		}
		if contenthash.HashContent(externalContent) != req.ExternalContentHash {
			return ResolveConflictResult{}, apperror.DocumentConflictChanged(string(req.DocumentID))
		}
	}

	s.mu.Lock()
	defer s.mu.Unlock()
	latest, ok := s.documents[req.DocumentID]
	if !ok {
		return ResolveConflictResult{}, apperror.DocumentNotFound(string(req.DocumentID))
	}
	if latest.LeaseID != req.LeaseID || latest.Version != req.DocumentVersion {
		return ResolveConflictResult{}, apperror.DocumentVersionConflict(latest.Path)
	}
	if latest.Conflict == nil || latest.Conflict.ExternalContentHash != req.ExternalContentHash {
		return ResolveConflictResult{}, apperror.DocumentConflictChanged(string(req.DocumentID))
	}
	if len(externalContent) > s.config.MaxBytesPerDocument {
		return ResolveConflictResult{}, apperror.RequestTooLarge(int64(s.config.MaxBytesPerDocument))
	}
	next := latest
	next.Version++
	next.UpdatedAt = s.now().UTC()
	next.BaseContentHash = req.ExternalContentHash
	next.BaseContent = append([]byte(nil), externalContent...)
	next.BaseSnapshotID = latest.Conflict.ExternalSnapshotID
	next.Conflict = nil
	switch req.Strategy {
	case ResolveReloadExternal:
		next.Content = append([]byte(nil), externalContent...)
		next.ContentHash = req.ExternalContentHash
		next.Dirty = false
		next.State = StateClean
	case ResolveKeepLocalRebase:
		next.Dirty = next.ContentHash != req.ExternalContentHash
		if next.Dirty {
			next.State = StateLocalDirty
		} else {
			next.State = StateClean
		}
	}
	delta := snapshotBytes(next) - snapshotBytes(latest)
	if s.totalBytes+delta > s.config.MaxTotalBytes {
		return ResolveConflictResult{}, apperror.OverlayLimitExceeded("max total overlay bytes reached")
	}
	if next.Dirty {
		savedDelta := 2*len(next.Content) - snapshotBytes(latest)
		if s.totalBytes+savedDelta > s.config.MaxTotalBytes {
			return ResolveConflictResult{}, apperror.OverlayLimitExceeded("max total overlay bytes reached")
		}
	}
	s.totalBytes += delta
	s.documents[req.DocumentID] = next
	s.scheduleLeaseLocked(req.DocumentID)
	return ResolveConflictResult{Snapshot: next.copy(), WillOverwriteExternalOnSave: req.Strategy == ResolveKeepLocalRebase && next.Dirty}, nil
}

func (s *Store) Close(documentID DocumentID, leaseID LeaseID, discard bool) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	current, ok := s.documents[documentID]
	if !ok {
		return apperror.DocumentNotFound(string(documentID))
	}
	if leaseID != current.LeaseID {
		return apperror.DocumentNotFound(string(documentID))
	}
	if current.Dirty && !discard {
		return apperror.DocumentDirty(string(documentID))
	}
	s.removeLocked(current)
	return nil
}

// CloseAll discards every document without persisting; used on shutdown.
func (s *Store) CloseAll() {
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, timer := range s.leaseTimers {
		timer.Stop()
	}
	s.documents = make(map[DocumentID]OverlaySnapshot)
	s.byPath = make(map[string]DocumentID)
	s.resolving = make(map[DocumentID]bool)
	s.leaseTimers = make(map[DocumentID]leaseTimer)
	s.leaseGenerations = make(map[DocumentID]uint64)
	s.totalBytes = 0
}

func (s *Store) removeLocked(snapshot OverlaySnapshot) {
	if timer := s.leaseTimers[snapshot.DocumentID]; timer != nil {
		timer.Stop()
	}
	delete(s.leaseTimers, snapshot.DocumentID)
	delete(s.leaseGenerations, snapshot.DocumentID)
	delete(s.documents, snapshot.DocumentID)
	if id, ok := s.byPath[snapshot.Path]; ok && id == snapshot.DocumentID {
		delete(s.byPath, snapshot.Path)
	}
	s.totalBytes -= snapshotBytes(snapshot)
	if s.totalBytes < 0 {
		s.totalBytes = 0
	}
}

func snapshotBytes(snapshot OverlaySnapshot) int {
	return len(snapshot.Content) + len(snapshot.BaseContent)
}

func (s *Store) scheduleLeaseLocked(documentID DocumentID) {
	if s.config.LeaseTTL <= 0 {
		return
	}
	if timer := s.leaseTimers[documentID]; timer != nil {
		timer.Stop()
	}
	s.leaseGenerations[documentID]++
	generation := s.leaseGenerations[documentID]
	s.leaseTimers[documentID] = s.afterFunc(s.config.LeaseTTL, func() {
		s.expireLease(documentID, generation)
	})
}

func (s *Store) expireLease(documentID DocumentID, generation uint64) {
	s.mu.Lock()
	if s.leaseGenerations[documentID] != generation {
		s.mu.Unlock()
		return
	}
	snapshot, ok := s.documents[documentID]
	if !ok {
		s.mu.Unlock()
		return
	}
	handler := s.onExpire
	s.removeLocked(snapshot)
	s.mu.Unlock()
	if handler != nil {
		handler(snapshot.copy())
	}
}

// randomID returns a strong random identifier. It never encodes a clock and is
// bounded in length. A failure to read the OS RNG is fatal-by-panic (it should
// never happen and a weak id would be a security regression).
func randomID() string {
	buf := make([]byte, 16)
	if _, err := rand.Read(buf); err != nil {
		panic(fmt.Sprintf("overlay: crypto/rand failed: %v", err))
	}
	return hex.EncodeToString(buf)
}
