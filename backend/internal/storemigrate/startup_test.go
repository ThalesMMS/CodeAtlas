package storemigrate_test

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/ThalesMMS/CodeAtlas/internal/domain"
	"github.com/ThalesMMS/CodeAtlas/internal/repository"
	"github.com/ThalesMMS/CodeAtlas/internal/storemigrate"
)

func TestOpenSQLiteForStartupFreshMigrationCreatesMarkerAndDB(t *testing.T) {
	t.Parallel()
	workspace := t.TempDir()
	dbPath := filepath.Join(workspace, ".codeatlas", "codeatlas.db")
	store, report, err := storemigrate.OpenSQLiteForStartup(context.Background(), storemigrate.StartupOptions{
		WorkspaceRoot: workspace,
		DatabasePath:  dbPath,
		Now:           fixedMigrationNow,
	})
	if err != nil {
		t.Fatalf("OpenSQLiteForStartup: %v", err)
	}
	defer store.Close()
	if !report.Migrated || report.Plan.Mode != storemigrate.ModeFresh {
		t.Fatalf("report = %+v, want fresh migration", report)
	}
	if _, err := os.Stat(dbPath); err != nil {
		t.Fatalf("sqlite db was not published: %v", err)
	}
	if marker, ok, err := storemigrate.ReadMarker(filepath.Dir(dbPath)); err != nil || !ok || marker.Backend != storemigrate.BackendSQLite {
		t.Fatalf("marker = %+v ok=%v err=%v", marker, ok, err)
	}
}

func TestOpenSQLiteForStartupValidMarkerRequiresDatabase(t *testing.T) {
	t.Parallel()
	workspace := t.TempDir()
	stateDir := filepath.Join(workspace, ".codeatlas")
	if err := os.MkdirAll(stateDir, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := storemigrate.WriteMarker(stateDir, storemigrate.Marker{
		Backend: storemigrate.BackendSQLite, Database: "codeatlas.db", MigrationID: "m1", ActivatedAt: "t",
	}); err != nil {
		t.Fatal(err)
	}
	_, _, err := storemigrate.OpenSQLiteForStartup(context.Background(), storemigrate.StartupOptions{
		WorkspaceRoot: workspace,
		DatabasePath:  filepath.Join(stateDir, "codeatlas.db"),
	})
	if err == nil {
		t.Fatal("expected missing marked DB to fail startup")
	}
}

func TestOpenSQLiteForStartupRejectsInvalidMarker(t *testing.T) {
	t.Parallel()
	workspace := t.TempDir()
	stateDir := filepath.Join(workspace, ".codeatlas")
	if err := os.MkdirAll(stateDir, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(stateDir, storemigrate.MarkerFileName), []byte(`{not-json`), 0o600); err != nil {
		t.Fatal(err)
	}
	_, _, err := storemigrate.OpenSQLiteForStartup(context.Background(), storemigrate.StartupOptions{
		WorkspaceRoot: workspace,
		DatabasePath:  filepath.Join(stateDir, "codeatlas.db"),
	})
	if err == nil {
		t.Fatal("expected invalid marker to fail startup")
	}
}

func TestOpenSQLiteForStartupValidMarkerRejectsCorruptDatabase(t *testing.T) {
	t.Parallel()
	workspace := t.TempDir()
	stateDir := filepath.Join(workspace, ".codeatlas")
	if err := os.MkdirAll(stateDir, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := storemigrate.WriteMarker(stateDir, storemigrate.Marker{
		Backend: storemigrate.BackendSQLite, Database: "codeatlas.db", MigrationID: "m1", ActivatedAt: "t",
	}); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(stateDir, "codeatlas.db"), []byte("not sqlite"), 0o600); err != nil {
		t.Fatal(err)
	}
	_, _, err := storemigrate.OpenSQLiteForStartup(context.Background(), storemigrate.StartupOptions{
		WorkspaceRoot: workspace,
		DatabasePath:  filepath.Join(stateDir, "codeatlas.db"),
	})
	if err == nil {
		t.Fatal("expected corrupt marked DB to fail startup")
	}
}

func TestOpenSQLiteForStartupImportsAllOccurrencesAndWikiArtifacts(t *testing.T) {
	t.Parallel()
	workspace := t.TempDir()
	stateDir := filepath.Join(workspace, ".codeatlas")
	jsonPath := filepath.Join(stateDir, "index.json")
	legacy, err := repository.OpenJSON(jsonPath)
	if err != nil {
		t.Fatal(err)
	}
	if err := legacy.ReplaceFile(testParsedFile()); err != nil {
		t.Fatal(err)
	}
	if err := legacy.ReplaceFile(testParsedFileAt("pkg/util.go", 8)); err != nil {
		t.Fatal(err)
	}
	legacySymbols := legacy.AllSymbols()
	if len(legacySymbols) == 0 {
		t.Fatal("legacy store has no symbols")
	}
	prepared, err := legacy.PrepareEmbeddingRebuild(
		map[string][]float64{legacySymbols[0].ID: {1, 0, 0}},
		domain.EmbeddingIndexMetadata{Dimension: 3},
	)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := legacy.CommitPrepared(prepared); err != nil {
		t.Fatal(err)
	}
	if err := legacy.ReplaceWikiPages([]domain.WikiPage{{Slug: "overview", Title: "Old", Markdown: "legacy", Provider: "p"}}); err != nil {
		t.Fatal(err)
	}

	dbPath := filepath.Join(stateDir, "codeatlas.db")
	store, report, err := storemigrate.OpenSQLiteForStartup(context.Background(), storemigrate.StartupOptions{
		WorkspaceRoot:  workspace,
		DatabasePath:   dbPath,
		LegacyJSONPath: jsonPath,
		Now:            fixedMigrationNow,
	})
	if err != nil {
		t.Fatalf("OpenSQLiteForStartup: %v", err)
	}
	defer store.Close()
	if report.Plan.Mode != storemigrate.ModeImport {
		t.Fatalf("mode = %s, want import (reasons: %v)", report.Plan.Mode, report.Plan.Reasons)
	}
	if !report.Plan.ImportArtifacts {
		t.Fatal("migration plan did not report the Wiki artifact import")
	}
	files, symbolCount, edges, wiki, _ := store.Counts()
	if files != 2 || symbolCount == 0 || edges == 0 {
		t.Fatalf("structural counts = %d/%d/%d, want imported content", files, symbolCount, edges)
	}
	pages := store.WikiPages()
	if wiki != 1 || len(pages) != 1 || pages[0].Slug != "overview" || pages[0].Markdown != "legacy" {
		t.Fatalf("legacy wiki was not preserved: count=%d pages=%v", wiki, pages)
	}
	if store.EmbeddingCount() != 0 {
		t.Fatalf("legacy embeddings were imported: %d", store.EmbeddingCount())
	}
	view := store.Snapshot()
	defer view.Close()
	var pay domain.Symbol
	for _, symbol := range view.AllSymbols() {
		if symbol.Name == "Pay" {
			pay = symbol
			break
		}
	}
	if pay.ID == "" {
		t.Fatal("Pay identity was not imported")
	}
	if occurrences := view.OccurrencesForSymbol(domain.SymbolID(pay.ID)); len(occurrences) != 2 {
		t.Fatalf("Pay occurrences after migration = %d, want 2", len(occurrences))
	}
	backup := filepath.Join(stateDir, report.Marker.SourceBackup)
	if _, err := os.Stat(backup); err != nil {
		t.Fatalf("legacy JSON backup missing: %v", err)
	}
}

func TestOpenSQLiteForStartupCompletesDatabasePublishedRecovery(t *testing.T) {
	t.Parallel()
	workspace := t.TempDir()
	stateDir := filepath.Join(workspace, ".codeatlas")
	dbPath := filepath.Join(stateDir, "codeatlas.db")
	existing, err := repository.OpenSQLite(context.Background(), repository.SQLiteConfig{
		WorkspaceRoot: workspace,
		Path:          dbPath,
	})
	if err != nil {
		t.Fatalf("OpenSQLite seed: %v", err)
	}
	if err := existing.Close(); err != nil {
		t.Fatalf("close seed SQLite: %v", err)
	}
	journal, err := storemigrate.NewJournal(stateDir, "mig-interrupted", storemigrate.ModeFresh, []string{"fresh"}, "t0")
	if err != nil {
		t.Fatalf("NewJournal: %v", err)
	}
	for _, phase := range []storemigrate.Phase{
		storemigrate.PhaseTargetCreated,
		storemigrate.PhaseDataLoaded,
		storemigrate.PhaseIndexesBuilt,
		storemigrate.PhaseValidated,
		storemigrate.PhaseDatabasePublished,
	} {
		if err := journal.Advance(phase, "t1"); err != nil {
			t.Fatalf("Advance(%s): %v", phase, err)
		}
	}
	if err := journal.Fail("MARKER_PUBLISH_FAILED", "t2"); err != nil {
		t.Fatalf("Fail: %v", err)
	}

	store, report, err := storemigrate.OpenSQLiteForStartup(context.Background(), storemigrate.StartupOptions{
		WorkspaceRoot: workspace,
		DatabasePath:  dbPath,
		Now:           fixedMigrationNow,
	})
	if err != nil {
		t.Fatalf("OpenSQLiteForStartup recovery: %v", err)
	}
	defer store.Close()
	if report.Marker.MigrationID != "mig-interrupted" {
		t.Fatalf("marker migration id = %q, want interrupted migration id", report.Marker.MigrationID)
	}
	read, ok, err := storemigrate.ReadJournal(stateDir)
	if err != nil || !ok {
		t.Fatalf("ReadJournal: ok=%v err=%v", ok, err)
	}
	if read.Phase != storemigrate.PhaseCompleted {
		t.Fatalf("journal phase = %s, want completed", read.Phase)
	}
	if read.ErrorCode != "" {
		t.Fatalf("journal error code = %q, want cleared after successful recovery", read.ErrorCode)
	}
}

func fixedMigrationNow() time.Time {
	return time.Unix(1234, 0).UTC()
}

func testParsedFile() domain.ParsedFile {
	return testParsedFileAt("pkg/svc.go", 1)
}

func testParsedFileAt(path string, line int) domain.ParsedFile {
	rng := domain.Range{Start: domain.Position{Line: line, Column: 1}, End: domain.Position{Line: line + 1, Column: 1}}
	return domain.ParsedFile{
		File: domain.File{Path: path, Language: "go", Hash: "h-" + path, Size: 10, ModifiedAt: time.Unix(1000, 0).UTC()},
		Symbols: []domain.Symbol{
			{ID: path + "#Service", Path: path, Name: "Service", QualifiedName: "pkg.Service", Kind: "type", Language: "go", Range: rng},
			{ID: path + "#Pay", Path: path, Name: "Pay", QualifiedName: "pkg.Service.Pay", Kind: "method", Language: "go", Range: rng, Code: "func Pay() {}"},
		},
		Edges: []domain.Edge{
			{FromSymbolID: path + "#Service", ToSymbolID: path + "#Pay", Type: "contains", Path: path, Line: line, Confidence: 1},
		},
	}
}
