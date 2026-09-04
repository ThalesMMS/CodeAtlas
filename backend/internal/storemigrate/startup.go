package storemigrate

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"time"

	"github.com/ThalesMMS/CodeAtlas/internal/changeset"
	"github.com/ThalesMMS/CodeAtlas/internal/domain"
	"github.com/ThalesMMS/CodeAtlas/internal/repository"
)

// supportedJSONSnapshotSchema tracks internal/store's canonical-hash schema.
// Snapshot schema 5 (#163) dropped dense retrieval from the canonical hash; an
// older schema-4 file is no longer identity-compatible and is planned as a
// rebuild rather than imported.
const supportedJSONSnapshotSchema = 5

// StartupOptions configure the SQLite-only production startup path.
type StartupOptions struct {
	WorkspaceRoot             string
	DatabasePath              string
	LegacyJSONPath            string
	ForceRebuild              bool
	RecoverLegacyTransactions func() error
	Now                       func() time.Time
}

// StartupReport describes how startup obtained the SQLite backend.
type StartupReport struct {
	Plan     MigrationPlan `json:"plan"`
	Marker   Marker        `json:"marker"`
	Migrated bool          `json:"migrated"`
}

// OpenSQLiteForStartup enforces the one-way production startup contract:
// a valid marker opens and validates SQLite; a missing marker runs migration;
// an invalid marker or missing/corrupt marked DB fails. JSON is only migration
// input and never a production fallback.
func OpenSQLiteForStartup(ctx context.Context, options StartupOptions) (repository.Store, StartupReport, error) {
	stateDir, err := prepareStartupOptions(&options)
	if err != nil {
		return nil, StartupReport{}, err
	}

	marker, ok, err := ReadMarker(stateDir)
	if err != nil {
		return nil, StartupReport{}, err
	}
	if ok {
		store, err := openMarkedSQLite(ctx, options, marker)
		if err != nil {
			return nil, StartupReport{}, err
		}
		return store, StartupReport{Marker: marker}, nil
	}

	report, recovered, err := recoverInterruptedMigration(ctx, options, stateDir)
	if err != nil {
		return nil, report, err
	}
	if recovered {
		store, err := openMarkedSQLite(ctx, options, report.Marker)
		if err != nil {
			return nil, report, err
		}
		return store, report, nil
	}

	report, err = migrateMissingMarker(ctx, options, stateDir)
	if err != nil {
		return nil, report, err
	}
	store, err := openMarkedSQLite(ctx, options, report.Marker)
	if err != nil {
		return nil, report, err
	}
	return store, report, nil
}

// RebuildSQLite publishes a fresh SQLite database regardless of an existing
// marker. The caller is responsible for scanning the workspace afterwards.
func RebuildSQLite(ctx context.Context, options StartupOptions) (repository.Store, StartupReport, error) {
	stateDir, err := prepareStartupOptions(&options)
	if err != nil {
		return nil, StartupReport{}, err
	}
	options.ForceRebuild = true
	report, err := migrateMissingMarker(ctx, options, stateDir)
	if err != nil {
		return nil, report, err
	}
	store, err := openMarkedSQLite(ctx, options, report.Marker)
	if err != nil {
		return nil, report, err
	}
	return store, report, nil
}

// PlanStartup inspects migration inputs without writing.
func PlanStartup(options StartupOptions) (MigrationPlan, error) {
	if options.DatabasePath == "" {
		return MigrationPlan{}, fmt.Errorf("database path is required")
	}
	if options.LegacyJSONPath == "" {
		options.LegacyJSONPath = filepath.Join(filepath.Dir(options.DatabasePath), "index.json")
	}
	input, err := inspectJSONSource(options.LegacyJSONPath, options.ForceRebuild)
	if err != nil {
		return MigrationPlan{}, err
	}
	return Plan(input), nil
}

// VerifySQLite opens and validates a marked SQLite database without modifying
// JSON state or creating a fallback.
func VerifySQLite(ctx context.Context, workspaceRoot, databasePath string) error {
	if _, err := os.Stat(databasePath); err != nil {
		return fmt.Errorf("sqlite database is missing: %w", err)
	}
	store, err := repository.OpenSQLite(ctx, repository.SQLiteConfig{WorkspaceRoot: workspaceRoot, Path: databasePath})
	if err != nil {
		return err
	}
	defer store.Close()
	return store.Validate(ctx)
}

func prepareStartupOptions(options *StartupOptions) (string, error) {
	if options.WorkspaceRoot == "" {
		return "", fmt.Errorf("workspace root is required")
	}
	if options.DatabasePath == "" {
		return "", fmt.Errorf("database path is required")
	}
	stateDir := filepath.Dir(options.DatabasePath)
	if options.LegacyJSONPath == "" {
		options.LegacyJSONPath = filepath.Join(stateDir, "index.json")
	}
	if options.Now == nil {
		options.Now = func() time.Time { return time.Now().UTC() }
	}
	if err := os.MkdirAll(stateDir, 0o700); err != nil {
		return "", err
	}
	if options.RecoverLegacyTransactions != nil {
		if err := options.RecoverLegacyTransactions(); err != nil {
			return "", fmt.Errorf("recover legacy JSON transaction journal: %w", err)
		}
	}
	return stateDir, nil
}

func openMarkedSQLite(ctx context.Context, options StartupOptions, marker Marker) (repository.Store, error) {
	if marker.Backend != BackendSQLite {
		return nil, fmt.Errorf("marker backend %q is not supported", marker.Backend)
	}
	expectedName := filepath.Base(options.DatabasePath)
	if marker.Database != expectedName {
		return nil, fmt.Errorf("marker database %q does not match configured database %q", marker.Database, expectedName)
	}
	if _, err := os.Stat(options.DatabasePath); err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil, fmt.Errorf("marked sqlite database is missing: %w", err)
		}
		return nil, err
	}
	store, err := repository.OpenSQLite(ctx, repository.SQLiteConfig{WorkspaceRoot: options.WorkspaceRoot, Path: options.DatabasePath})
	if err != nil {
		return nil, err
	}
	if err := store.Validate(ctx); err != nil {
		_ = store.Close()
		return nil, err
	}
	return store, nil
}

func recoverInterruptedMigration(ctx context.Context, options StartupOptions, stateDir string) (StartupReport, bool, error) {
	journal, ok, err := ReadJournal(stateDir)
	if err != nil {
		return StartupReport{}, false, fmt.Errorf("STORE_MIGRATION_RECOVERY_FAILED: %w", err)
	}
	if !ok {
		return StartupReport{}, false, nil
	}

	tempDB := filepath.Join(stateDir, fmt.Sprintf("codeatlas.%s.tmp.db", journal.MigrationID))
	switch journal.Phase {
	case PhasePlanned, PhaseBackupCreated, PhaseTargetCreated, PhaseDataLoaded, PhaseIndexesBuilt:
		removeSQLiteFiles(tempDB)
		return StartupReport{}, false, nil
	case PhaseValidated:
		if err := VerifySQLite(ctx, options.WorkspaceRoot, tempDB); err != nil {
			removeSQLiteFiles(tempDB)
			return StartupReport{}, false, nil
		}
		if err := publishSQLiteDatabase(tempDB, options.DatabasePath); err != nil {
			return StartupReport{Plan: planFromJournal(journal)}, true, fmt.Errorf("STORE_MIGRATION_RECOVERY_FAILED: publish validated target: %w", err)
		}
		if err := journal.Advance(PhaseDatabasePublished, options.Now().UTC().Format(time.RFC3339Nano)); err != nil {
			return StartupReport{Plan: planFromJournal(journal)}, true, err
		}
		return completeRecoveredPublication(journal, options, stateDir)
	case PhaseDatabasePublished:
		if err := VerifySQLite(ctx, options.WorkspaceRoot, options.DatabasePath); err != nil {
			return StartupReport{Plan: planFromJournal(journal)}, true, fmt.Errorf("STORE_MIGRATION_RECOVERY_FAILED: final database invalid after publication: %w", err)
		}
		return completeRecoveredPublication(journal, options, stateDir)
	case PhaseMarkerPublished, PhaseCompleted:
		return StartupReport{Plan: planFromJournal(journal)}, true, fmt.Errorf("STORE_MIGRATION_RECOVERY_FAILED: journal is %s but backend marker is missing", journal.Phase)
	default:
		return StartupReport{Plan: planFromJournal(journal)}, true, fmt.Errorf("STORE_MIGRATION_RECOVERY_FAILED: unknown phase %q", journal.Phase)
	}
}

func completeRecoveredPublication(journal Journal, options StartupOptions, stateDir string) (StartupReport, bool, error) {
	journal.ErrorCode = ""
	marker := Marker{
		Version: markerVersion,
		Backend: BackendSQLite, Database: filepath.Base(options.DatabasePath),
		MigrationID: journal.MigrationID, ActivatedAt: options.Now().UTC().Format(time.RFC3339Nano),
		SourceBackup: journal.SourceBackup, SourceChecksum: journal.SourceChecksum,
	}
	if err := WriteMarker(stateDir, marker); err != nil {
		return StartupReport{Plan: planFromJournal(journal)}, true, fmt.Errorf("STORE_MIGRATION_RECOVERY_FAILED: publish recovered marker: %w", err)
	}
	if err := journal.Advance(PhaseMarkerPublished, options.Now().UTC().Format(time.RFC3339Nano)); err != nil {
		return StartupReport{Plan: planFromJournal(journal), Marker: marker}, true, err
	}
	if err := journal.Advance(PhaseCompleted, options.Now().UTC().Format(time.RFC3339Nano)); err != nil {
		return StartupReport{Plan: planFromJournal(journal), Marker: marker}, true, err
	}
	return StartupReport{Plan: planFromJournal(journal), Marker: marker, Migrated: true}, true, nil
}

func planFromJournal(journal Journal) MigrationPlan {
	return MigrationPlan{Mode: journal.Mode, Reasons: append([]string(nil), journal.Reasons...)}
}

func migrateMissingMarker(ctx context.Context, options StartupOptions, stateDir string) (StartupReport, error) {
	now := options.Now().UTC()
	migrationID := fmt.Sprintf("mig-%d", now.UnixNano())
	plan, err := PlanStartup(options)
	if err != nil {
		return StartupReport{}, err
	}
	journal, err := NewJournal(stateDir, migrationID, plan.Mode, plan.Reasons, now.Format(time.RFC3339Nano))
	if err != nil {
		return StartupReport{Plan: plan}, err
	}
	fail := func(code string, cause error) (StartupReport, error) {
		_ = journal.Fail(code, options.Now().UTC().Format(time.RFC3339Nano))
		return StartupReport{Plan: plan}, cause
	}

	var backupName, checksum string
	if plan.Mode != ModeFresh {
		backupName, checksum, err = BackupSource(options.LegacyJSONPath, stateDir, migrationID)
		if err != nil {
			return fail("SOURCE_BACKUP_FAILED", err)
		}
		journal.SourceBackup = backupName
		journal.SourceChecksum = checksum
		if err := journal.Advance(PhaseBackupCreated, options.Now().UTC().Format(time.RFC3339Nano)); err != nil {
			return fail("JOURNAL_FAILED", err)
		}
	}

	tempDB := filepath.Join(stateDir, fmt.Sprintf("codeatlas.%s.tmp.db", migrationID))
	removeSQLiteFiles(tempDB)
	tempStore, err := repository.OpenSQLite(ctx, repository.SQLiteConfig{WorkspaceRoot: options.WorkspaceRoot, Path: tempDB})
	if err != nil {
		return fail("TARGET_CREATE_FAILED", err)
	}
	if err := journal.Advance(PhaseTargetCreated, options.Now().UTC().Format(time.RFC3339Nano)); err != nil {
		_ = tempStore.Close()
		return fail("JOURNAL_FAILED", err)
	}

	if plan.Mode == ModeImport {
		if err := importCompatibleJSON(ctx, options.LegacyJSONPath, tempStore); err != nil {
			_ = tempStore.Close()
			return fail("DATA_IMPORT_FAILED", err)
		}
	}
	if err := journal.Advance(PhaseDataLoaded, options.Now().UTC().Format(time.RFC3339Nano)); err != nil {
		_ = tempStore.Close()
		return fail("JOURNAL_FAILED", err)
	}
	if err := journal.Advance(PhaseIndexesBuilt, options.Now().UTC().Format(time.RFC3339Nano)); err != nil {
		_ = tempStore.Close()
		return fail("JOURNAL_FAILED", err)
	}
	if err := tempStore.Validate(ctx); err != nil {
		_ = tempStore.Close()
		return fail("VALIDATION_FAILED", err)
	}
	if err := journal.Advance(PhaseValidated, options.Now().UTC().Format(time.RFC3339Nano)); err != nil {
		_ = tempStore.Close()
		return fail("JOURNAL_FAILED", err)
	}
	if err := tempStore.Close(); err != nil {
		return fail("TARGET_CLOSE_FAILED", err)
	}

	if err := publishSQLiteDatabase(tempDB, options.DatabasePath); err != nil {
		return fail("DATABASE_PUBLISH_FAILED", err)
	}
	if err := journal.Advance(PhaseDatabasePublished, options.Now().UTC().Format(time.RFC3339Nano)); err != nil {
		return fail("JOURNAL_FAILED", err)
	}

	marker := Marker{
		Version: markerVersion,
		Backend: BackendSQLite, Database: filepath.Base(options.DatabasePath),
		MigrationID: migrationID, ActivatedAt: options.Now().UTC().Format(time.RFC3339Nano),
		SourceBackup: backupName, SourceChecksum: checksum,
	}
	if err := WriteMarker(stateDir, marker); err != nil {
		return fail("MARKER_PUBLISH_FAILED", err)
	}
	if err := journal.Advance(PhaseMarkerPublished, options.Now().UTC().Format(time.RFC3339Nano)); err != nil {
		return fail("JOURNAL_FAILED", err)
	}
	if err := journal.Advance(PhaseCompleted, options.Now().UTC().Format(time.RFC3339Nano)); err != nil {
		return fail("JOURNAL_FAILED", err)
	}
	return StartupReport{Plan: plan, Marker: marker, Migrated: true}, nil
}

func inspectJSONSource(path string, forceRebuild bool) (PlanInput, error) {
	input := PlanInput{
		SupportedJSONSchema: supportedJSONSnapshotSchema,
		TargetSchema:        3,
		ForceRebuild:        forceRebuild,
	}
	data, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		return input, nil
	}
	if err != nil {
		return input, err
	}
	input.JSONExists = true
	var snapshot struct {
		Version        int                       `json:"version"`
		SnapshotSchema int                       `json:"snapshotSchema"`
		SnapshotID     domain.SnapshotID         `json:"snapshotId"`
		Files          []domain.File             `json:"files"`
		Identities     []domain.SymbolIdentity   `json:"identities"`
		Occurrences    []domain.SymbolOccurrence `json:"occurrences"`
		Symbols        []domain.Symbol           `json:"symbols"`
		Edges          []domain.Edge             `json:"edges"`
		Wiki           []domain.WikiPage         `json:"wiki"`
	}
	if err := json.Unmarshal(data, &snapshot); err != nil {
		input.IdentityCompatible = false
		return input, nil
	}
	input.JSONSchema = snapshot.SnapshotSchema
	input.SnapshotID = string(snapshot.SnapshotID)
	input.IdentityCompatible = snapshot.Version == 1 && snapshot.SnapshotSchema == supportedJSONSnapshotSchema && len(snapshot.Symbols) == 0
	input.Counts = Counts{
		Files: len(snapshot.Files), Symbols: len(snapshot.Identities), Edges: len(snapshot.Edges),
		WikiPages: len(snapshot.Wiki),
	}
	return input, nil
}

func importCompatibleJSON(ctx context.Context, jsonPath string, target repository.Store) error {
	source, err := repository.OpenJSON(jsonPath)
	if err != nil {
		return err
	}
	defer source.Close()

	view := source.Snapshot()
	defer view.Close()
	files := view.Files()
	symbolsByPath := exportAllOccurrences(view)
	edgesByPath := make(map[string][]domain.Edge)
	for _, edge := range view.AllEdges() {
		edgesByPath[edge.Path] = append(edgesByPath[edge.Path], edge)
	}
	sort.Slice(files, func(i, j int) bool { return files[i].Path < files[j].Path })

	builder := changeset.NewBuilder().WithExpectedVersion(target.Version())
	for _, file := range files {
		builder.Upsert(domain.ParsedFile{
			File:    file,
			Symbols: append([]domain.Symbol(nil), symbolsByPath[file.Path]...),
			Edges:   append([]domain.Edge(nil), edgesByPath[file.Path]...),
		})
	}
	change, err := builder.AllowEmpty().Build(time.Now().UTC())
	if err != nil {
		return err
	}
	prepared, err := target.Prepare(change)
	if err != nil {
		return err
	}
	_, err = target.CommitPrepared(prepared)
	if err != nil {
		return err
	}
	return target.ReplaceWikiPages(source.WikiPages())
}

func exportAllOccurrences(view repository.ReadView) map[string][]domain.Symbol {
	byPath := make(map[string][]domain.Symbol)
	for _, primary := range view.AllSymbols() {
		occurrences := view.OccurrencesForSymbol(domain.SymbolID(primary.ID))
		if len(occurrences) == 0 {
			// Occurrence-only symbols already use their physical occurrence as
			// the flat handle and are not indexed by SymbolID.
			if primary.OccurrenceID != "" {
				byPath[primary.Path] = append(byPath[primary.Path], primary)
			}
			continue
		}
		identity := domain.SymbolIdentity{
			ID: domain.SymbolID(primary.ID), Language: primary.Language, Kind: primary.Kind,
			Name: primary.Name, QualifiedName: primary.QualifiedName,
		}
		for _, occurrence := range occurrences {
			flat := (domain.ResolvedSymbol{Identity: identity, Occurrence: occurrence}).ToSymbol()
			byPath[occurrence.Path] = append(byPath[occurrence.Path], flat)
		}
	}
	for path := range byPath {
		sort.Slice(byPath[path], func(i, j int) bool {
			if byPath[path][i].ID != byPath[path][j].ID {
				return byPath[path][i].ID < byPath[path][j].ID
			}
			return byPath[path][i].OccurrenceID < byPath[path][j].OccurrenceID
		})
	}
	return byPath
}

func publishSQLiteDatabase(tempDB, finalDB string) error {
	if _, err := os.Stat(tempDB); err != nil {
		return err
	}
	if err := backupExistingSQLiteFiles(finalDB); err != nil {
		return err
	}
	if err := os.Rename(tempDB, finalDB); err != nil {
		return err
	}
	removeSQLiteFiles(tempDB)
	if dirFile, err := os.Open(filepath.Dir(finalDB)); err == nil {
		_ = dirFile.Sync()
		_ = dirFile.Close()
	}
	return nil
}

func backupExistingSQLiteFiles(finalDB string) error {
	for _, path := range []string{finalDB, finalDB + "-wal", finalDB + "-shm"} {
		if _, err := os.Stat(path); errors.Is(err, os.ErrNotExist) {
			continue
		} else if err != nil {
			return err
		}
		backup, err := nextPublishBackupPath(path)
		if err != nil {
			return err
		}
		if err := os.Rename(path, backup); err != nil {
			return err
		}
	}
	if dirFile, err := os.Open(filepath.Dir(finalDB)); err == nil {
		_ = dirFile.Sync()
		_ = dirFile.Close()
	}
	return nil
}

func nextPublishBackupPath(path string) (string, error) {
	base := path + ".previous"
	for i := 0; i < 1000; i++ {
		candidate := base
		if i > 0 {
			candidate = fmt.Sprintf("%s.%d", base, i)
		}
		if _, err := os.Stat(candidate); errors.Is(err, os.ErrNotExist) {
			return candidate, nil
		} else if err != nil {
			return "", err
		}
	}
	return "", fmt.Errorf("no available publish backup path for %s", filepath.Base(path))
}

func removeSQLiteFiles(path string) {
	_ = os.Remove(path)
	_ = os.Remove(path + "-wal")
	_ = os.Remove(path + "-shm")
}
