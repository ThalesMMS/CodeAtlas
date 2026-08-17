package service

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

const journalSchemaVersion = 1

// TxPhase is the durable phase of a workspace save transaction.
type TxPhase string

const (
	PhasePrepared        TxPhase = "prepared"
	PhaseSourcePublished TxPhase = "source_published"
	PhaseCommitted       TxPhase = "committed"
)

// txJournal is the versioned manifest persisted under .codeatlas/transactions.
// Every path is derived and controlled by CodeAtlas; recovery validates them and
// never treats an external journal as an arbitrary path.
type txJournal struct {
	Version              int     `json:"version"`
	TransactionID        string  `json:"transactionId"`
	Path                 string  `json:"path"`
	SourceFile           string  `json:"sourceFile"`
	SourceTemp           string  `json:"sourceTemp"`
	SourceBackup         string  `json:"sourceBackup"`
	IndexOfficial        string  `json:"indexOfficial"`
	IndexTemp            string  `json:"indexTemp"`
	ExpectedOldHash      string  `json:"expectedOldHash"`
	NewHash              string  `json:"newHash"`
	ExpectedStoreVersion uint64  `json:"expectedStoreVersion"`
	NextStoreVersion     uint64  `json:"nextStoreVersion"`
	Phase                TxPhase `json:"phase"`
}

// validate rejects a journal whose schema, paths or fields are inconsistent so a
// tampered or external file is never acted upon.
func (j *txJournal) validate(workspaceRoot, indexOfficial string) error {
	if j.Version != journalSchemaVersion {
		return fmt.Errorf("unsupported journal version %d", j.Version)
	}
	if j.TransactionID == "" || j.Phase == "" {
		return errors.New("journal missing id or phase")
	}
	if filepath.Clean(j.IndexOfficial) != filepath.Clean(indexOfficial) {
		return errors.New("journal index path does not match this store")
	}
	// Source paths in the journal are symlink-resolved (Workspace.Resolve), so
	// compare against the resolved workspace root.
	resolvedRoot := workspaceRoot
	if resolved, err := filepath.EvalSymlinks(workspaceRoot); err == nil {
		resolvedRoot = resolved
	}
	for _, p := range []string{j.SourceFile, j.SourceTemp, j.SourceBackup} {
		if p != "" && !withinRoot(resolvedRoot, p) {
			return fmt.Errorf("journal path escapes workspace: %s", filepath.Base(p))
		}
	}
	for _, p := range []string{j.IndexTemp} {
		if p != "" && !withinRoot(filepath.Dir(indexOfficial), p) {
			return errors.New("journal index temp escapes the state directory")
		}
	}
	return nil
}

func withinRoot(root, candidate string) bool {
	relative, err := filepath.Rel(root, candidate)
	return err == nil && relative != ".." && !strings.HasPrefix(relative, ".."+string(filepath.Separator))
}

// writeJournalAtomic persists a journal manifest with a temp+fsync+rename so a
// reader never observes a partially written phase.
func writeJournalAtomic(journalDir string, journal *txJournal) error {
	if err := os.MkdirAll(journalDir, 0o700); err != nil {
		return err
	}
	data, err := json.MarshalIndent(journal, "", "  ")
	if err != nil {
		return err
	}
	final := filepath.Join(journalDir, journal.TransactionID+".json")
	temp := final + ".tmp"
	file, err := os.OpenFile(temp, os.O_CREATE|os.O_TRUNC|os.O_WRONLY, 0o600)
	if err != nil {
		return err
	}
	if _, err := file.Write(data); err != nil {
		_ = file.Close()
		_ = os.Remove(temp)
		return err
	}
	if err := file.Sync(); err != nil {
		_ = file.Close()
		_ = os.Remove(temp)
		return err
	}
	if err := file.Close(); err != nil {
		_ = os.Remove(temp)
		return err
	}
	return os.Rename(temp, final)
}

// listJournals returns the committed journal manifests in the directory (ignoring
// temps), sorted for deterministic recovery.
func listJournals(journalDir string) ([]string, error) {
	matches, err := filepath.Glob(filepath.Join(journalDir, "*.json"))
	if err != nil {
		return nil, err
	}
	return matches, nil
}

func readJournal(path string) (*txJournal, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	var journal txJournal
	if err := json.Unmarshal(data, &journal); err != nil {
		return nil, err
	}
	return &journal, nil
}

func removeJournalArtifacts(journalDir, transactionID string) {
	_ = os.Remove(filepath.Join(journalDir, transactionID+".json"))
	_ = os.Remove(filepath.Join(journalDir, transactionID+".json.tmp"))
}
