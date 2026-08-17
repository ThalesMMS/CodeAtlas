package indexer

import (
	"errors"

	"github.com/ThalesMMS/CodeAtlas/internal/domain"
	"github.com/ThalesMMS/CodeAtlas/internal/textutil"
)

// ScanState is the lifecycle state of the batch indexer.
type ScanState string

const (
	ScanIdle       ScanState = "idle"
	ScanPreparing  ScanState = "preparing"
	ScanCommitting ScanState = "committing"
	ScanFailed     ScanState = "failed"
	ScanComplete   ScanState = "complete"
)

// ErrIndexingInProgress is returned by Scan when another scan holds the run
// lock. Periodic callers treat it as a skip; the manual reindex endpoint maps it
// to 409 INDEXING_IN_PROGRESS.
var ErrIndexingInProgress = errors.New("INDEXING_IN_PROGRESS")

func (i *Indexer) setState(state ScanState) {
	i.statusMu.Lock()
	i.scanState = state
	if state == ScanComplete {
		i.lastError = "" // a successful cycle clears the retained failure
	}
	i.statusMu.Unlock()
}

// failPrepare records a sanitized preparation failure and emits a progress-level
// failed event (no visible change was published).
func (i *Indexer) failPrepare(err error) error {
	return i.failWith("index.prepare.failed", err)
}

// failCommit records a sanitized commit failure and emits a commit-level failed
// event. A commit failure never reports a new store version.
func (i *Indexer) failCommit(err error) error {
	return i.failWith("index.commit.failed", err)
}

func (i *Indexer) failWith(eventType string, err error) error {
	message := sanitizeIndexError(err)
	i.statusMu.Lock()
	i.scanState = ScanFailed
	i.lastError = message
	i.statusMu.Unlock()
	i.publishEvent(domain.IndexEvent{Type: eventType, Error: &domain.PublicError{Code: "INDEX_FAILED", Message: message}})
	return errors.New(message)
}

// Status reports whether a scan is actively preparing/committing and the last
// retained error.
func (i *Indexer) Status() (indexing bool, lastError string) {
	i.statusMu.RLock()
	defer i.statusMu.RUnlock()
	return i.scanState == ScanPreparing || i.scanState == ScanCommitting, i.lastError
}

// State returns the detailed scan state.
func (i *Indexer) State() ScanState {
	i.statusMu.RLock()
	defer i.statusMu.RUnlock()
	return i.scanState
}

// sanitizeIndexError collapses whitespace and bounds the length of an error
// surfaced to clients or logs.
func sanitizeIndexError(err error) string {
	if err == nil {
		return ""
	}
	return textutil.CompactMessage(err.Error(), 400)
}
