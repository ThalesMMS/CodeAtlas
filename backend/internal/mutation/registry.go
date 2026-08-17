package mutation

import (
	"context"
	"errors"
	"time"

	"github.com/ThalesMMS/CodeAtlas/internal/domain"
)

var (
	ErrRegistryClosed       = errors.New("mutation registry is closed")
	ErrRegistryFull         = errors.New("mutation registry is full")
	ErrMutationNotFound     = errors.New("mutation not found")
	ErrMutationStateInvalid = errors.New("mutation state invalid")
	ErrMutationInvalid      = errors.New("mutation invalid")
)

// MutationCommitResult records the repository state published by a successful
// coordinated save.
type MutationCommitResult struct {
	SnapshotID     domain.SnapshotID
	Revision       domain.Revision
	CommittedAt    time.Time
	AffectedPaths  []string
	TransactionID  string
	StoreContentID string
}

// Registry correlates CodeAtlas-authored writes with later final-state
// reconciliation observations. Implementations are in-memory and ephemeral.
type Registry interface {
	Stage(context.Context, domain.InternalMutation) error
	MarkPublished(context.Context, domain.InternalMutationID, MutationCommitResult) error
	Match(context.Context, domain.FileObservation) (domain.MutationMatch, error)
	MarkObserved(context.Context, domain.InternalMutationID, uint64) error
	MarkExternal(context.Context, domain.InternalMutationID) error
	Cancel(context.Context, domain.InternalMutationID) error
	Expire(time.Time) []domain.InternalMutation
	Snapshot() domain.MutationRegistrySnapshot
	Close()
}
