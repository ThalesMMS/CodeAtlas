package storemigrate

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
)

// Phase is a migration phase recorded in the journal, in order.
type Phase string

const (
	PhasePlanned           Phase = "planned"
	PhaseBackupCreated     Phase = "backup_created"
	PhaseTargetCreated     Phase = "target_created"
	PhaseDataLoaded        Phase = "data_loaded"
	PhaseIndexesBuilt      Phase = "indexes_built"
	PhaseValidated         Phase = "validated"
	PhaseDatabasePublished Phase = "database_published"
	PhaseMarkerPublished   Phase = "marker_published"
	PhaseCompleted         Phase = "completed"
)

var phaseOrder = map[Phase]int{
	PhasePlanned:           0,
	PhaseBackupCreated:     1,
	PhaseTargetCreated:     2,
	PhaseDataLoaded:        3,
	PhaseIndexesBuilt:      4,
	PhaseValidated:         5,
	PhaseDatabasePublished: 6,
	PhaseMarkerPublished:   7,
	PhaseCompleted:         8,
}

// Journal is the versioned, atomically-written migration record. It never stores
// source code, prompts, or secrets.
type Journal struct {
	Version        int      `json:"version"`
	MigrationID    string   `json:"migrationId"`
	Mode           Mode     `json:"mode"`
	Phase          Phase    `json:"phase"`
	StartedAt      string   `json:"startedAt"`
	UpdatedAt      string   `json:"updatedAt"`
	SourceBackup   string   `json:"sourceBackup,omitempty"`
	SourceChecksum string   `json:"sourceChecksum,omitempty"`
	Reasons        []string `json:"reasons,omitempty"`
	ErrorCode      string   `json:"errorCode,omitempty"`
	dir            string
}

// NewJournal starts a journal in the `planned` phase and persists it.
func NewJournal(stateDir, migrationID string, mode Mode, reasons []string, now string) (*Journal, error) {
	journal := &Journal{
		Version: markerVersion, MigrationID: migrationID, Mode: mode, Phase: PhasePlanned,
		StartedAt: now, UpdatedAt: now, Reasons: reasons, dir: stateDir,
	}
	return journal, journal.persist()
}

// Advance records a new phase and persists the journal.
func (j *Journal) Advance(phase Phase, now string) error {
	currentOrder, ok := phaseOrder[j.Phase]
	if !ok {
		return fmt.Errorf("unknown current migration phase %q", j.Phase)
	}
	nextOrder, ok := phaseOrder[phase]
	if !ok {
		return fmt.Errorf("unknown migration phase %q", phase)
	}
	if nextOrder < currentOrder {
		return fmt.Errorf("migration journal cannot move backward from %s to %s", j.Phase, phase)
	}
	j.Phase = phase
	j.UpdatedAt = now
	return j.persist()
}

// Fail records a sanitized error code without leaving a partial phase claim.
func (j *Journal) Fail(errorCode, now string) error {
	j.ErrorCode = errorCode
	j.UpdatedAt = now
	return j.persist()
}

func (j *Journal) persist() error {
	data, err := json.MarshalIndent(j, "", "  ")
	if err != nil {
		return err
	}
	return atomicWrite(filepath.Join(j.dir, JournalFileName), data, 0o600)
}

// ReadJournal reads a prior journal if present (for recovery/diagnostics).
func ReadJournal(stateDir string) (Journal, bool, error) {
	data, err := os.ReadFile(filepath.Join(stateDir, JournalFileName))
	if os.IsNotExist(err) {
		return Journal{}, false, nil
	}
	if err != nil {
		return Journal{}, false, err
	}
	var journal Journal
	if err := json.Unmarshal(data, &journal); err != nil {
		return Journal{}, false, err
	}
	journal.dir = stateDir
	return journal, true, nil
}
