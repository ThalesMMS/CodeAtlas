package service

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sync"
	"time"

	"github.com/ThalesMMS/CodeAtlas/internal/contenthash"
	"github.com/ThalesMMS/CodeAtlas/internal/domain"
	"github.com/ThalesMMS/CodeAtlas/internal/mutation"
	"github.com/ThalesMMS/CodeAtlas/internal/repository"
	jsonstore "github.com/ThalesMMS/CodeAtlas/internal/store"

	_ "github.com/mattn/go-sqlite3"
)

// CodeRecoveryFailed marks an unrecoverable workspace transaction at startup.
const CodeRecoveryFailed = "WORKSPACE_TRANSACTION_RECOVERY_FAILED"

// errSimulatedCrash is returned by Commit when a test crash hook fires after a
// phase. It mimics process death: no rollback or cleanup runs, leaving the
// on-disk state exactly as a crash would for recovery to handle.
var errSimulatedCrash = errors.New("simulated crash")

// RollbackFailedError signals that a source rollback failed mid-commit, which
// must drive the app to FAILED and block functional routes.
type RollbackFailedError struct{ cause error }

func (e *RollbackFailedError) Error() string { return "workspace rollback failed: " + e.cause.Error() }
func (e *RollbackFailedError) Unwrap() error { return e.cause }

// WorkspaceCommitCoordinator publishes a new source file and the new index
// snapshot as one logical operation, guarded by a consistency barrier and a
// recoverable journal. It wraps a SavePreparer so the handler has a single seam.
type WorkspaceCommitCoordinator struct {
	preparer   *SavePreparer
	workspace  *Workspace
	store      repository.Store
	journalDir string
	indexPath  string
	barrier    sync.RWMutex
	mutations  mutation.Registry
}

func NewWorkspaceCommitCoordinator(preparer *SavePreparer, workspace *Workspace, st repository.Store, journalDir, indexPath string) *WorkspaceCommitCoordinator {
	return &WorkspaceCommitCoordinator{
		preparer: preparer, workspace: workspace, store: st, journalDir: journalDir, indexPath: indexPath,
	}
}

// Prepare delegates the side-effect-free preparation to the SavePreparer.
func (c *WorkspaceCommitCoordinator) Prepare(ctx context.Context, request SaveRequest) (*PreparedSave, error) {
	return c.preparer.Prepare(ctx, request)
}

// ReadView returns the source content, its hash and the store version under a
// read lock, so a reader observes a consistent source+index pair.
func (c *WorkspaceCommitCoordinator) ReadView(path string) (content []byte, contentHash string, storeVersion uint64, err error) {
	c.barrier.RLock()
	defer c.barrier.RUnlock()
	content, err = c.workspace.ReadLimited(path, c.preparer.maxFileBytes)
	if err != nil {
		return nil, "", 0, err
	}
	return content, contenthash.HashContent(content), c.store.Version(), nil
}

// SetMutationRegistry installs the ephemeral self-write correlation registry.
func (c *WorkspaceCommitCoordinator) SetMutationRegistry(registry mutation.Registry) {
	c.mutations = registry
}

// Commit publishes the prepared save atomically. crash is a test hook (nil in
// production) that, when it returns true after a phase, simulates a crash.
func (c *WorkspaceCommitCoordinator) Commit(prepared *PreparedSave, crash func(TxPhase) bool) (uint64, error) {
	return c.CommitContext(context.Background(), prepared, crash)
}

func (c *WorkspaceCommitCoordinator) CommitContext(ctx context.Context, prepared *PreparedSave, crash func(TxPhase) bool) (uint64, error) {
	if prepared == nil || prepared.noop {
		return c.store.Version(), nil
	}
	if err := ctx.Err(); err != nil {
		return 0, err
	}
	crashed := func(phase TxPhase) bool { return crash != nil && crash(phase) }

	sourceFile, err := c.workspace.Resolve(prepared.path)
	if err != nil {
		return 0, err
	}
	sourceTemp := sourceFile + ".cab-tmp"
	sourceBackup := sourceFile + ".cab-bak"

	// Heavy staging off the barrier.
	if err := writeFileSync(sourceTemp, prepared.content); err != nil {
		return 0, err
	}
	journal := &txJournal{
		Version: journalSchemaVersion, TransactionID: fmt.Sprintf("tx-%d", prepared.preparedIndex.NextVersion()),
		Path: prepared.path, SourceFile: sourceFile, SourceTemp: sourceTemp, SourceBackup: sourceBackup,
		IndexOfficial:   c.indexPath,
		ExpectedOldHash: prepared.expectedHash, NewHash: prepared.newHash,
		ExpectedStoreVersion: prepared.preparedIndex.ExpectedVersion(), NextStoreVersion: prepared.preparedIndex.NextVersion(),
		Phase: PhasePrepared,
	}

	c.barrier.Lock()
	defer c.barrier.Unlock()

	// Revalidate the source under the exclusive lock; the store version is
	// revalidated by PublishStaged.
	current, err := os.ReadFile(sourceFile)
	if err != nil {
		c.abortStaging(sourceTemp)
		return 0, err
	}
	if contenthash.HashContent(current) != prepared.expectedHash {
		c.abortStaging(sourceTemp)
		return 0, saveError(CodeFileChangedOnDisk, 409, "the file changed on disk during preparation")
	}

	if err := writeJournalAtomic(c.journalDir, journal); err != nil {
		c.abortStaging(sourceTemp)
		return 0, err
	}
	if crashed(PhasePrepared) {
		return 0, errSimulatedCrash
	}

	mutationID, staged, err := c.stageInternalMutation(journal, prepared)
	if err != nil {
		mutationID = ""
		staged = false
	}
	cancelMutation := func() {
		if staged && c.mutations != nil {
			_ = c.mutations.Cancel(context.Background(), mutationID)
			staged = false
		}
	}

	if err := copyFileSync(sourceFile, sourceBackup); err != nil {
		cancelMutation()
		c.abortStaging(sourceTemp)
		removeJournalArtifacts(c.journalDir, journal.TransactionID)
		return 0, err
	}
	if err := os.Rename(sourceTemp, sourceFile); err != nil {
		cancelMutation()
		c.abortStaging("")
		_ = os.Remove(sourceBackup)
		removeJournalArtifacts(c.journalDir, journal.TransactionID)
		return 0, err
	}
	journal.Phase = PhaseSourcePublished
	if err := writeJournalAtomic(c.journalDir, journal); err != nil {
		cancelMutation()
		return 0, c.rollback(journal, err)
	}
	if crashed(PhaseSourcePublished) {
		return 0, errSimulatedCrash
	}

	result, err := c.store.CommitPreparedContext(ctx, prepared.preparedIndex)
	if err != nil {
		cancelMutation()
		return 0, c.rollback(journal, mapPublishError(err))
	}
	journal.Phase = PhaseCommitted
	_ = writeJournalAtomic(c.journalDir, journal)
	if crashed(PhaseCommitted) {
		return 0, errSimulatedCrash
	}
	if staged && c.mutations != nil {
		if err := c.mutations.MarkPublished(context.Background(), mutationID, mutation.MutationCommitResult{
			SnapshotID:    result.SnapshotID,
			Revision:      result.Revision,
			CommittedAt:   time.Now().UTC(),
			AffectedPaths: []string{prepared.path},
			TransactionID: journal.TransactionID,
		}); err != nil {
			_ = c.mutations.Cancel(context.Background(), mutationID)
		}
	}

	_ = os.Remove(sourceBackup)
	removeJournalArtifacts(c.journalDir, journal.TransactionID)
	return uint64(result.Revision), nil
}

func (c *WorkspaceCommitCoordinator) stageInternalMutation(journal *txJournal, prepared *PreparedSave) (domain.InternalMutationID, bool, error) {
	if c.mutations == nil {
		return "", false, nil
	}
	mutationID := domain.InternalMutationID(journal.TransactionID + ":" + prepared.path)
	err := c.mutations.Stage(context.Background(), domain.InternalMutation{
		ID:                   mutationID,
		TransactionID:        journal.TransactionID,
		Path:                 prepared.path,
		PreviousContentHash:  prepared.expectedHash,
		PublishedContentHash: prepared.newHash,
		RegisteredAt:         time.Now().UTC(),
	})
	if err != nil {
		return "", false, err
	}
	return mutationID, true, nil
}

// rollback restores the old source after the index failed to publish, keeping
// old/old. A failed rollback returns a RollbackFailedError.
func (c *WorkspaceCommitCoordinator) rollback(journal *txJournal, cause error) error {
	if err := os.Rename(journal.SourceBackup, journal.SourceFile); err != nil {
		return &RollbackFailedError{cause: err}
	}
	_ = os.Remove(journal.SourceTemp)
	removeJournalArtifacts(c.journalDir, journal.TransactionID)
	return cause
}

func (c *WorkspaceCommitCoordinator) abortStaging(sourceTemp string) {
	if sourceTemp != "" {
		_ = os.Remove(sourceTemp)
	}
}

func mapPublishError(err error) error {
	if errors.Is(err, repository.ErrVersionConflict) {
		return saveError(CodeSaveStoreConflict, 409, "the index changed during the commit")
	}
	return err
}

// RecoverTransactions replays pending workspace transactions at startup, before
// the store loads. It is idempotent for each phase and reports an unrecoverable
// state as an error carrying CodeRecoveryFailed.
func RecoverTransactions(workspaceRoot, indexOfficial, journalDir string) error {
	return RecoverTransactionsForIndexes(workspaceRoot, journalDir, indexOfficial)
}

// RecoverTransactionsForIndexes replays pending workspace transactions for one
// of the accepted repository files. It is used during the JSON→SQLite transition
// to recover legacy JSON journals before opening the SQLite backend while still
// accepting journals produced by the new SQLite save path.
func RecoverTransactionsForIndexes(workspaceRoot, journalDir string, indexOfficials ...string) error {
	return RecoverTransactionsForIndexesObserved(workspaceRoot, journalDir, nil, indexOfficials...)
}

// RecoverTransactionsForIndexesObserved reports each journal successfully
// recovered and removed. No callback is made when startup finds no journals.
func RecoverTransactionsForIndexesObserved(workspaceRoot, journalDir string, onRecovered func(), indexOfficials ...string) error {
	journals, err := listJournals(journalDir)
	if err != nil {
		return err
	}
	accepted := make(map[string]string, len(indexOfficials))
	for _, indexOfficial := range indexOfficials {
		accepted[filepath.Clean(indexOfficial)] = indexOfficial
	}
	for _, path := range journals {
		journal, err := readJournal(path)
		if err != nil {
			return recoveryFailed("unreadable journal")
		}
		indexOfficial, ok := accepted[filepath.Clean(journal.IndexOfficial)]
		if !ok {
			return recoveryFailed("journal index path does not match this store")
		}
		if err := journal.validate(workspaceRoot, indexOfficial); err != nil {
			return recoveryFailed("invalid journal: " + err.Error())
		}
		if err := recoverOne(journal); err != nil {
			return recoveryFailed(err.Error())
		}
		removeJournalArtifacts(journalDir, journal.TransactionID)
		if onRecovered != nil {
			onRecovered()
		}
	}
	return nil
}

func recoverOne(journal *txJournal) error {
	switch journal.Phase {
	case PhasePrepared:
		// Source was never published: discard staging, keep old/old.
		_ = os.Remove(journal.SourceTemp)
		_ = os.Remove(journal.IndexTemp)
		_ = os.Remove(journal.SourceBackup)
		return nil
	case PhaseSourcePublished:
		officialVersion, _ := snapshotVersion(journal.IndexOfficial)
		if officialVersion == journal.NextStoreVersion {
			cleanupTransactionFiles(journal) // already rolled forward (new/new)
			return nil
		}
		if indexTempValid(journal.IndexTemp) {
			if err := os.Rename(journal.IndexTemp, journal.IndexOfficial); err != nil {
				return err
			}
			cleanupTransactionFiles(journal) // roll forward -> new/new
			return nil
		}
		if fileExists(journal.SourceBackup) {
			if err := os.Rename(journal.SourceBackup, journal.SourceFile); err != nil {
				return err
			}
			cleanupTransactionFiles(journal) // roll back -> old/old
			return nil
		}
		if officialVersion == journal.ExpectedStoreVersion && sourceHashEquals(journal.SourceFile, journal.ExpectedOldHash) {
			cleanupTransactionFiles(journal) // already rolled back (old/old)
			return nil
		}
		return errors.New("ambiguous state: neither a new index nor a backup exists")
	case PhaseCommitted:
		cleanupTransactionFiles(journal)
		return nil
	default:
		return fmt.Errorf("unknown journal phase: %s", journal.Phase)
	}
}

func cleanupTransactionFiles(journal *txJournal) {
	_ = os.Remove(journal.SourceBackup)
	_ = os.Remove(journal.SourceTemp)
	_ = os.Remove(journal.IndexTemp)
}

func recoveryFailed(message string) error {
	return saveError(CodeRecoveryFailed, 500, message)
}

func writeFileSync(path string, data []byte) error {
	file, err := os.OpenFile(path, os.O_CREATE|os.O_TRUNC|os.O_WRONLY, 0o600)
	if err != nil {
		return err
	}
	if _, err := file.Write(data); err != nil {
		_ = file.Close()
		_ = os.Remove(path)
		return err
	}
	if err := file.Sync(); err != nil {
		_ = file.Close()
		_ = os.Remove(path)
		return err
	}
	return file.Close()
}

func copyFileSync(source, destination string) error {
	input, err := os.Open(source)
	if err != nil {
		return err
	}
	defer input.Close() //nolint:errcheck
	output, err := os.OpenFile(destination, os.O_CREATE|os.O_TRUNC|os.O_WRONLY, 0o600)
	if err != nil {
		return err
	}
	fail := func(cause error) error {
		_ = output.Close()
		_ = os.Remove(destination)
		return cause
	}
	if _, err := io.Copy(output, input); err != nil {
		return fail(err)
	}
	if err := output.Sync(); err != nil {
		return fail(err)
	}
	if err := output.Close(); err != nil {
		_ = os.Remove(destination)
		return err
	}
	return nil
}

func fileExists(path string) bool {
	_, err := os.Stat(path)
	return err == nil
}

func indexTempValid(path string) bool {
	if _, err := os.Stat(path); err != nil {
		return false
	}
	return jsonstore.ValidateSnapshot(path) == nil
}

func sourceHashEquals(path, expected string) bool {
	data, err := os.ReadFile(path)
	if err != nil {
		return false
	}
	return contenthash.HashContent(data) == expected
}

func snapshotVersion(path string) (uint64, bool) {
	data, err := os.ReadFile(path)
	if err != nil {
		return 0, false
	}
	if len(data) >= len("SQLite format 3") && string(data[:len("SQLite format 3")]) == "SQLite format 3" {
		return sqliteSnapshotVersion(path)
	}
	var payload struct {
		StoreVersion uint64 `json:"storeVersion"`
	}
	if json.Unmarshal(data, &payload) != nil {
		return 0, false
	}
	return payload.StoreVersion, true
}

func sqliteSnapshotVersion(path string) (uint64, bool) {
	db, err := sql.Open("sqlite3", "file:"+path+"?mode=ro&_query_only=true")
	if err != nil {
		return 0, false
	}
	defer db.Close()
	var revision uint64
	if err := db.QueryRow("SELECT revision FROM repository_state WHERE id = 1").Scan(&revision); err != nil {
		return 0, false
	}
	return revision, true
}
