package storemigrate

import (
	"os"
	"path/filepath"
	"testing"
)

func TestMarkerRoundTrips(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	marker := Marker{Backend: BackendSQLite, Database: "codeatlas.db", MigrationID: "m1", ActivatedAt: "t", SourceBackup: "index.pre-sqlite.m1.json", SourceChecksum: "sha256:abc"}
	if err := WriteMarker(dir, marker); err != nil {
		t.Fatalf("WriteMarker: %v", err)
	}
	read, ok, err := ReadMarker(dir)
	if err != nil || !ok {
		t.Fatalf("ReadMarker: ok=%v err=%v", ok, err)
	}
	if read.Backend != BackendSQLite || read.Database != "codeatlas.db" || read.Version != markerVersion {
		t.Fatalf("marker did not round-trip: %+v", read)
	}
}

func TestMissingMarkerIsNotAnError(t *testing.T) {
	t.Parallel()
	_, ok, err := ReadMarker(t.TempDir())
	if err != nil || ok {
		t.Fatalf("missing marker should be (false, nil): ok=%v err=%v", ok, err)
	}
}

func TestMarkerRejectsPathTraversal(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	if err := WriteMarker(dir, Marker{Backend: BackendSQLite, Database: "../escape.db"}); err == nil {
		t.Fatal("expected a path-traversal rejection for the database filename")
	}
	if err := WriteMarker(dir, Marker{Backend: BackendSQLite, Database: "sub/codeatlas.db"}); err == nil {
		t.Fatal("expected a rejection for a separator in the database filename")
	}
}

func TestBackupIsImmutableAndChecksummed(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	source := filepath.Join(dir, "index.json")
	if err := os.WriteFile(source, []byte(`{"snapshot":"data"}`), 0o600); err != nil {
		t.Fatal(err)
	}
	name, checksum, err := BackupSource(source, dir, "mig123")
	if err != nil {
		t.Fatalf("BackupSource: %v", err)
	}
	if name != "index.pre-sqlite.mig123.json" {
		t.Fatalf("backup name = %q", name)
	}
	// The original is untouched.
	if data, _ := os.ReadFile(source); string(data) != `{"snapshot":"data"}` {
		t.Fatal("source must not be altered by the backup")
	}
	// The backup matches the recorded checksum.
	if verify, _ := checksumFile(filepath.Join(dir, name)); verify != checksum {
		t.Fatalf("backup checksum mismatch: %q vs %q", verify, checksum)
	}
	// A second backup with the same id must not clobber (exclusive create).
	if _, _, err := BackupSource(source, dir, "mig123"); err == nil {
		t.Fatal("expected an exclusive-create failure on a duplicate backup")
	}
}

func TestPublishSQLiteDatabasePreservesExistingDatabaseBackup(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	tempDB := filepath.Join(dir, "codeatlas.tmp.db")
	finalDB := filepath.Join(dir, "codeatlas.db")
	if err := os.WriteFile(tempDB, []byte("new-db"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(finalDB, []byte("old-db"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(finalDB+"-wal", []byte("old-wal"), 0o600); err != nil {
		t.Fatal(err)
	}

	if err := publishSQLiteDatabase(tempDB, finalDB); err != nil {
		t.Fatalf("publishSQLiteDatabase: %v", err)
	}

	if data, err := os.ReadFile(finalDB); err != nil || string(data) != "new-db" {
		t.Fatalf("final DB = %q err=%v, want new-db", data, err)
	}
	if data, err := os.ReadFile(finalDB + ".previous"); err != nil || string(data) != "old-db" {
		t.Fatalf("backup DB = %q err=%v, want old-db", data, err)
	}
	if data, err := os.ReadFile(finalDB + "-wal.previous"); err != nil || string(data) != "old-wal" {
		t.Fatalf("backup WAL = %q err=%v, want old-wal", data, err)
	}
}

func TestPlannerSelectsConservativeMode(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name  string
		input PlanInput
		want  Mode
	}{
		{"fresh", PlanInput{JSONExists: false}, ModeFresh},
		{"force rebuild", PlanInput{JSONExists: true, JSONSchema: 4, SupportedJSONSchema: 4, IdentityCompatible: true, ForceRebuild: true}, ModeRebuild},
		{"newer schema", PlanInput{JSONExists: true, JSONSchema: 99, SupportedJSONSchema: 4, IdentityCompatible: true}, ModeRebuild},
		{"identity incompatible", PlanInput{JSONExists: true, JSONSchema: 4, SupportedJSONSchema: 4, IdentityCompatible: false}, ModeRebuild},
		{"embeddings incompatible", PlanInput{JSONExists: true, JSONSchema: 4, SupportedJSONSchema: 4, IdentityCompatible: true, EmbeddingsEnabled: true, EmbeddingCompatible: false}, ModeRebuild},
		{"compatible import", PlanInput{JSONExists: true, JSONSchema: 4, SupportedJSONSchema: 4, IdentityCompatible: true}, ModeImport},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			plan := Plan(tc.input)
			if plan.Mode != tc.want {
				t.Fatalf("mode = %q, want %q (reasons: %v)", plan.Mode, tc.want, plan.Reasons)
			}
			if len(plan.Reasons) == 0 {
				t.Fatal("plan must record reasons")
			}
		})
	}
}

func TestCompatibleImportPlansWikiArtifacts(t *testing.T) {
	t.Parallel()
	plan := Plan(PlanInput{
		JSONExists: true, JSONSchema: 4, SupportedJSONSchema: 4,
		IdentityCompatible: true, Counts: Counts{WikiPages: 2},
	})
	if plan.Mode != ModeImport || !plan.ImportArtifacts || plan.EstimatedCounts.WikiPages != 2 {
		t.Fatalf("plan = %+v, want artifact-preserving import", plan)
	}
}

func TestJournalRejectsBackwardPhase(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	journal, err := NewJournal(dir, "mig1", ModeImport, []string{"compatible"}, "t0")
	if err != nil {
		t.Fatalf("NewJournal: %v", err)
	}
	if err := journal.Advance(PhaseTargetCreated, "t1"); err != nil {
		t.Fatalf("Advance(%s): %v", PhaseTargetCreated, err)
	}
	if err := journal.Advance(PhaseBackupCreated, "t2"); err == nil {
		t.Fatal("expected backward phase transition to be rejected")
	}
	read, ok, err := ReadJournal(dir)
	if err != nil || !ok {
		t.Fatalf("ReadJournal: ok=%v err=%v", ok, err)
	}
	if read.Phase != PhaseTargetCreated {
		t.Fatalf("phase after rejected transition = %s, want %s", read.Phase, PhaseTargetCreated)
	}
}

func TestJournalAdvancesPhases(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	journal, err := NewJournal(dir, "mig1", ModeImport, []string{"compatible"}, "t0")
	if err != nil {
		t.Fatalf("NewJournal: %v", err)
	}
	for _, phase := range []Phase{PhaseBackupCreated, PhaseTargetCreated, PhaseDataLoaded, PhaseValidated, PhaseCompleted} {
		if err := journal.Advance(phase, "t1"); err != nil {
			t.Fatalf("Advance(%s): %v", phase, err)
		}
	}
	read, ok, err := ReadJournal(dir)
	if err != nil || !ok {
		t.Fatalf("ReadJournal: ok=%v err=%v", ok, err)
	}
	if read.Phase != PhaseCompleted || read.Mode != ModeImport {
		t.Fatalf("journal did not persist final phase: %+v", read)
	}
}
