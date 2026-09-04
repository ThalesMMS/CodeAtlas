package store

import (
	"errors"
	"fmt"

	"github.com/ThalesMMS/CodeAtlas/internal/changeset"
)

// ErrVersionConflict is returned by CommitPrepared when the active version no
// longer matches the version the commit was prepared against. Callers must
// re-prepare; no merge is attempted.
var ErrVersionConflict = errors.New("STORE_VERSION_CONFLICT")

// PreparedCommit is the result of Prepare: a fully-applied candidate state plus
// the optimistic-control versions. The candidate is internal and cannot be
// mutated by the caller.
type PreparedCommit struct {
	ChangeSetID     string
	ExpectedVersion uint64
	NextVersion     uint64

	noop      bool
	candidate *state
}

// IsNoop reports whether committing this prepared commit will change nothing.
func (p *PreparedCommit) IsNoop() bool { return p.noop }

// Prepare computes the next state by applying a ChangeSet to a deep clone of the
// active state, without mutating the active store. The active state's counters,
// search, graph and getters are untouched until CommitPrepared swaps the state.
func (s *Store) Prepare(change *changeset.ChangeSet) (*PreparedCommit, error) {
	if change == nil {
		return nil, errors.New("nil change set")
	}

	s.mu.RLock()
	candidate := s.current.clone()
	s.mu.RUnlock()

	expectedVersion := candidate.version
	if change.IsNoop() {
		return &PreparedCommit{
			ChangeSetID:     change.ID(),
			ExpectedVersion: expectedVersion,
			NextVersion:     expectedVersion,
			noop:            true,
		}, nil
	}

	for _, path := range change.DeletedPaths() {
		candidate.deleteFile(path)
	}
	for _, parsed := range change.Upserts() {
		candidate.replaceFile(parsed)
	}
	candidate.indexedAt = change.CreatedAt()
	candidate.finalize()
	candidate.version = bumpVersion(expectedVersion)

	return &PreparedCommit{
		ChangeSetID:     change.ID(),
		ExpectedVersion: expectedVersion,
		NextVersion:     expectedVersion + 1,
		candidate:       candidate,
	}, nil
}

// StagedCommit is a serialized, durably-written candidate snapshot waiting to be
// published. Staging is the heavy half of a commit and happens off any caller
// lock; PublishStaged is the short atomic half. It lets a coordinator interleave
// a source-file publish between the two halves.
type StagedCommit struct {
	prepared *PreparedCommit
	tempName string
	noop     bool
}

func (s *StagedCommit) IsNoop() bool            { return s.noop }
func (s *StagedCommit) ExpectedVersion() uint64 { return s.prepared.ExpectedVersion }
func (s *StagedCommit) NextVersion() uint64     { return s.prepared.NextVersion }
func (s *StagedCommit) IndexTempPath() string   { return s.tempName }

// StageCommit serializes the candidate and writes it to a temp file without
// taking the store lock or publishing anything. A serialize/write/sync/close
// failure returns an error and removes the temp.
func (s *Store) StageCommit(prepared *PreparedCommit) (*StagedCommit, error) {
	if prepared == nil {
		return nil, errors.New("nil prepared commit")
	}
	if prepared.noop {
		return &StagedCommit{prepared: prepared, noop: true}, nil
	}
	// An authoritatively-invalid candidate is never written or published.
	if violations := ValidateState(prepared.candidate); HasBlockingViolations(violations) {
		return nil, fmt.Errorf("refusing to stage invalid candidate: %s", ViolationSummary(violations))
	}
	data, err := encodeSnapshot(prepared.candidate)
	if err != nil {
		return nil, err
	}
	tempName, err := s.persister.writeTemp(data)
	if err != nil {
		return nil, err
	}
	return &StagedCommit{prepared: prepared, tempName: tempName}, nil
}

// PublishStaged atomically replaces the official snapshot with a staged temp and
// swaps the in-memory state, under a short write lock. It returns
// ErrVersionConflict (and discards the temp) when the active version advanced
// since Prepare. The in-memory swap follows the official publish, so a failure
// leaves both disk and memory at the previous state.
func (s *Store) PublishStaged(staged *StagedCommit) error {
	if staged == nil {
		return errors.New("nil staged commit")
	}
	if staged.noop {
		return nil
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.current.version != staged.prepared.ExpectedVersion {
		s.persister.remove(staged.tempName)
		return ErrVersionConflict
	}
	if err := s.persister.replace(staged.tempName); err != nil {
		return err
	}
	s.current = staged.prepared.candidate
	return nil
}

// DiscardStaged removes a staged temp that will not be published.
func (s *Store) DiscardStaged(staged *StagedCommit) {
	if staged != nil && !staged.noop && staged.tempName != "" {
		s.persister.remove(staged.tempName)
	}
}

// CommitPrepared stages and publishes a prepared commit in one call.
func (s *Store) CommitPrepared(prepared *PreparedCommit) error {
	staged, err := s.StageCommit(prepared)
	if err != nil {
		return err
	}
	return s.PublishStaged(staged)
}
