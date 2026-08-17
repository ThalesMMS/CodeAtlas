package sqlite

import (
	"context"
	"errors"
	"path/filepath"
	"sort"
	"testing"
	"time"

	"github.com/ThalesMMS/CodeAtlas/internal/apperror"
	"github.com/ThalesMMS/CodeAtlas/internal/changeset"
	"github.com/ThalesMMS/CodeAtlas/internal/domain"
	"github.com/ThalesMMS/CodeAtlas/internal/store"
)

func openStore(t *testing.T) *Store {
	t.Helper()
	s, err := OpenStore(context.Background(), Config{WorkspaceRoot: t.TempDir()})
	if err != nil {
		t.Fatalf("OpenStore: %v", err)
	}
	t.Cleanup(func() { _ = s.Close() })
	return s
}

// sampleFiles builds two parsed files with a logical parent (contains) and a call
// edge, exercising identity derivation and the relation/evidence mapping.
func sampleFiles() []domain.ParsedFile {
	rng := func(l int) domain.Range {
		return domain.Range{Start: domain.Position{Line: l, Column: 1}, End: domain.Position{Line: l + 2, Column: 2}}
	}
	svc := domain.ParsedFile{
		File: domain.File{Path: "pkg/svc.go", Language: "go", Hash: "h-svc", Size: 200, ModifiedAt: time.Unix(1000, 0).UTC()},
		Symbols: []domain.Symbol{
			{ID: "L-Service", Path: "pkg/svc.go", Name: "Service", Kind: "type", QualifiedName: "pkg.Service", Language: "go", Range: rng(1), Signature: "type Service struct"},
			{ID: "L-Pay", Path: "pkg/svc.go", Name: "Pay", Kind: "method", QualifiedName: "pkg.Service.Pay", Language: "go", Range: rng(5), Signature: "func (s *Service) Pay()", Code: "func (s *Service) Pay() { charge() }", DocComment: "Pay charges the order."},
		},
		Edges: []domain.Edge{
			{FromSymbolID: "L-Service", ToSymbolID: "L-Pay", Type: "contains", Path: "pkg/svc.go", Line: 5},
			{FromSymbolID: "L-Pay", ToName: "Charge", Type: "calls", Path: "pkg/svc.go", Line: 6, Confidence: 0.9},
		},
	}
	util := domain.ParsedFile{
		File: domain.File{Path: "pkg/util.go", Language: "go", Hash: "h-util", Size: 80, ModifiedAt: time.Unix(2000, 0).UTC()},
		Symbols: []domain.Symbol{
			{ID: "L-Charge", Path: "pkg/util.go", Name: "Charge", Kind: "func", QualifiedName: "pkg.Charge", Language: "go", Range: rng(1), Signature: "func Charge()"},
		},
	}
	return []domain.ParsedFile{svc, util}
}

func buildChangeSet(t testing.TB, expected uint64, files []domain.ParsedFile) *changeset.ChangeSet {
	t.Helper()
	builder := changeset.NewBuilder().WithExpectedVersion(expected)
	for _, file := range files {
		builder.Upsert(file)
	}
	change, err := builder.Build(time.Unix(3000, 0).UTC())
	if err != nil {
		t.Fatalf("build changeset: %v", err)
	}
	return change
}

func TestStoreStartsAtRevisionZero(t *testing.T) {
	t.Parallel()
	s := openStore(t)
	metadata, err := s.Metadata(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if metadata.Revision != 0 {
		t.Fatalf("revision = %d, want 0", metadata.Revision)
	}
	if metadata.IdentityAlgorithm == "" || metadata.Schema != snapshotSchema {
		t.Fatalf("metadata not populated: %+v", metadata)
	}
}

func TestCommitBumpsRevisionDeterministically(t *testing.T) {
	t.Parallel()
	s := openStore(t)
	result, err := s.Commit(context.Background(), buildChangeSet(t, 0, sampleFiles()))
	if err != nil {
		t.Fatalf("Commit: %v", err)
	}
	if result.Revision != 1 || result.NoOp {
		t.Fatalf("unexpected result: %+v", result)
	}
	metadata, _ := s.Metadata(context.Background())
	if metadata.Revision != 1 || metadata.ID != result.SnapshotID {
		t.Fatalf("metadata not advanced: %+v vs %+v", metadata, result)
	}

	// A second store committing identical content yields the SAME content-addressed id.
	other := openStore(t)
	otherResult, err := other.Commit(context.Background(), buildChangeSet(t, 0, sampleFiles()))
	if err != nil {
		t.Fatal(err)
	}
	if otherResult.SnapshotID != result.SnapshotID {
		t.Fatalf("snapshot id not content-addressed: %q vs %q", otherResult.SnapshotID, result.SnapshotID)
	}
}

func TestIncrementalSnapshotMatchesFullRebuild(t *testing.T) {
	t.Parallel()
	store := openStore(t)
	files := sampleFiles()
	if _, err := store.Commit(context.Background(), buildChangeSet(t, 0, files)); err != nil {
		t.Fatal(err)
	}
	files[0].File.Hash = "h-svc-v2"
	files[0].Symbols[1].Code = "func (s *Service) Pay() { charge(); audit() }"
	result, err := store.Commit(context.Background(), buildChangeSet(t, 1, files[:1]))
	if err != nil {
		t.Fatal(err)
	}

	tx, err := store.db.Writer().BeginTx(context.Background(), nil)
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback() //nolint:errcheck
	rebuilt, err := rebuildSnapshotIndex(context.Background(), tx)
	if err != nil {
		t.Fatal(err)
	}
	if rebuilt != result.SnapshotID {
		t.Fatalf("incremental snapshot = %q, full rebuild = %q", result.SnapshotID, rebuilt)
	}
}

func TestCollectCommitImpactIncludesNestedIdentityDescendants(t *testing.T) {
	t.Parallel()
	store := openStore(t)
	ctx := context.Background()
	tx, err := store.db.Writer().BeginTx(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback() //nolint:errcheck

	if _, err := tx.ExecContext(ctx, `INSERT INTO files(path, language, content_hash, size, modified_at, indexed_at)
		VALUES('parent.go', 'go', 'h-parent', 1, 't', 't')`); err != nil {
		t.Fatal(err)
	}
	identities := []struct {
		id, parent string
	}{
		{id: "root"},
		{id: "child", parent: "root"},
		{id: "grandchild", parent: "child"},
	}
	for _, identity := range identities {
		var parent any
		if identity.parent != "" {
			parent = identity.parent
		}
		if _, err := tx.ExecContext(ctx, `INSERT INTO symbol_identities
			(symbol_id, language, kind, name, qualified_name, parent_symbol_id, signature_fingerprint)
			VALUES(?, 'go', 'func', ?, ?, ?, '')`, identity.id, identity.id, identity.id, parent); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := tx.ExecContext(ctx, `INSERT INTO symbol_occurrences
		(occurrence_id, symbol_id, file_path, start_line, start_col, end_line, end_col, signature, code, summary, body_hash, file_hash)
		VALUES('occ-root', 'root', 'parent.go', 1, 1, 1, 1, '', '', '', '', 'h-parent')`); err != nil {
		t.Fatal(err)
	}

	impact, err := collectCommitImpact(ctx, tx, []string{"parent.go"})
	if err != nil {
		t.Fatal(err)
	}
	for _, symbolID := range []string{"root", "child", "grandchild"} {
		if _, found := impact.symbolIDs[symbolID]; !found {
			t.Fatalf("nested descendant %q missing from commit impact", symbolID)
		}
	}
}

func TestPointReadProjections(t *testing.T) {
	t.Parallel()
	store := openStore(t)
	if _, err := store.Commit(context.Background(), buildChangeSet(t, 0, sampleFiles())); err != nil {
		t.Fatalf("Commit: %v", err)
	}

	hash, found, err := store.FileHash(context.Background(), "pkg/svc.go")
	if err != nil || !found || hash != "h-svc" {
		t.Fatalf("FileHash = %q/%v/%v, want h-svc/true/nil", hash, found, err)
	}
	if _, found, err := store.FileHash(context.Background(), "missing.go"); err != nil || found {
		t.Fatalf("missing FileHash found/error = %v/%v, want false/nil", found, err)
	}

	counts, err := store.Counts(context.Background())
	if err != nil {
		t.Fatalf("Counts: %v", err)
	}
	if counts.Files != 2 || counts.Symbols != 3 || counts.Edges != 2 || counts.WikiPages != 0 {
		t.Fatalf("Counts = %+v, want files=2 symbols=3 edges=2 wiki=0", counts)
	}
	if counts.IndexedAt.IsZero() {
		t.Fatal("IndexedAt is zero")
	}
	metadata, err := store.Metadata(context.Background())
	if err != nil || !metadata.CreatedAt.Equal(counts.IndexedAt) {
		t.Fatalf("Metadata.CreatedAt = %s/%v, want indexed time %s", metadata.CreatedAt, err, counts.IndexedAt)
	}
}

func TestCommitUpsertEdgeReturningReusesConflictingRelation(t *testing.T) {
	t.Parallel()
	store := openStore(t)
	if _, err := store.Commit(context.Background(), buildChangeSet(t, 0, sampleFiles())); err != nil {
		t.Fatalf("first commit: %v", err)
	}
	if _, err := store.Commit(context.Background(), buildChangeSet(t, 1, sampleFiles())); err != nil {
		t.Fatalf("second commit through ON CONFLICT RETURNING: %v", err)
	}

	var relations, evidence int
	if err := store.db.Reader().QueryRow("SELECT COUNT(*) FROM relations").Scan(&relations); err != nil {
		t.Fatal(err)
	}
	if err := store.db.Reader().QueryRow("SELECT COUNT(*) FROM relation_evidence").Scan(&evidence); err != nil {
		t.Fatal(err)
	}
	if relations != 2 || evidence != 2 {
		t.Fatalf("relations/evidence = %d/%d, want stable 2/2 after conflict update", relations, evidence)
	}
}

func TestCommitRejectsStaleExpectedVersion(t *testing.T) {
	t.Parallel()
	s := openStore(t)
	if _, err := s.Commit(context.Background(), buildChangeSet(t, 0, sampleFiles())); err != nil {
		t.Fatal(err)
	}
	// Now at revision 1; a changeset still expecting 0 must conflict.
	_, err := s.Commit(context.Background(), buildChangeSet(t, 0, sampleFiles()))
	var appErr *apperror.AppError
	if !errors.As(err, &appErr) || appErr.Code != apperror.CodeStoreVersionConflict {
		t.Fatalf("expected version conflict, got %v", err)
	}
}

func TestReadViewClosesAndIsPinned(t *testing.T) {
	t.Parallel()
	s := openStore(t)
	if _, err := s.Commit(context.Background(), buildChangeSet(t, 0, sampleFiles())); err != nil {
		t.Fatal(err)
	}
	view, err := s.OpenReadView(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(view.AllSymbols()) == 0 {
		t.Fatal("expected symbols in the view")
	}
	if _, err := view.Search("Service", 10); err != nil {
		t.Fatalf("Search should work via FTS5: %v", err)
	}
	if err := view.Close(); err != nil {
		t.Fatal(err)
	}
	if err := view.Close(); err != nil {
		t.Fatalf("Close must be idempotent: %v", err)
	}
	if len(view.AllSymbols()) != 0 {
		t.Fatal("methods after Close must return empty")
	}
}

func TestReadViewPreservesBoundedProjectFileContent(t *testing.T) {
	t.Parallel()
	s := openStore(t)
	project := domain.ParsedFile{File: domain.File{
		Path: "README.md", Language: "markdown", Hash: "h-readme", Size: 9000,
		Content: "bounded preview", ContentTruncated: true,
	}}
	if _, err := s.Commit(context.Background(), buildChangeSet(t, 0, []domain.ParsedFile{project})); err != nil {
		t.Fatal(err)
	}
	view, err := s.OpenReadView(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	defer view.Close() //nolint:errcheck
	files := view.Files()
	if len(files) != 1 || files[0].Content != "bounded preview" || !files[0].ContentTruncated {
		t.Fatalf("files = %+v", files)
	}
}

// TestParityWithInMemoryStore is the core proof: the same ChangeSet committed to
// the in-memory store and the SQLite store yields the same structural facts.
func TestParityWithInMemoryStore(t *testing.T) {
	t.Parallel()
	files := sampleFiles()

	memory, err := store.Open(filepath.Join(t.TempDir(), "index.json"))
	if err != nil {
		t.Fatalf("in-memory Open: %v", err)
	}
	prepared, err := memory.Prepare(buildChangeSet(t, 0, files))
	if err != nil {
		t.Fatalf("in-memory Prepare: %v", err)
	}
	if err := memory.CommitPrepared(prepared); err != nil {
		t.Fatalf("in-memory CommitPrepared: %v", err)
	}
	memView := memory.Snapshot()

	sqliteStore := openStore(t)
	if _, err := sqliteStore.Commit(context.Background(), buildChangeSet(t, 0, files)); err != nil {
		t.Fatalf("sqlite Commit: %v", err)
	}
	sqlView, err := sqliteStore.OpenReadView(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	defer sqlView.Close()

	// 1) Same set of symbol handles with the same identity fields.
	memSymbols := indexByID(memView.AllSymbols())
	sqlSymbols := indexByID(sqlView.AllSymbols())
	if len(memSymbols) != len(sqlSymbols) {
		t.Fatalf("symbol count differs: memory=%d sqlite=%d", len(memSymbols), len(sqlSymbols))
	}
	for id, memSym := range memSymbols {
		sqlSym, ok := sqlSymbols[id]
		if !ok {
			t.Fatalf("sqlite missing symbol handle %q", id)
		}
		if memSym.Name != sqlSym.Name || memSym.Kind != sqlSym.Kind ||
			memSym.QualifiedName != sqlSym.QualifiedName || memSym.Language != sqlSym.Language ||
			memSym.DocComment != sqlSym.DocComment {
			t.Fatalf("identity mismatch for %q:\n memory=%+v\n sqlite=%+v", id, memSym, sqlSym)
		}
	}

	// 2) Same edge set (from -> to-name : type).
	memEdges := edgeKeySet(memAllEdges(t, memView, memSymbols))
	sqlEdges := edgeKeySet(sqlView.AllEdges())
	for key := range memEdges {
		if _, ok := sqlEdges[key]; !ok {
			t.Fatalf("sqlite missing edge %q (have %v)", key, keysOf(sqlEdges))
		}
	}

	// 3) SymbolAt resolves to the same handle at a known method position.
	memResolved, memOK := memView.SymbolAt("pkg/svc.go", 6, 2)
	sqlResolved, sqlOK := sqlView.SymbolAt("pkg/svc.go", 6, 2)
	if memOK != sqlOK {
		t.Fatalf("SymbolAt presence differs: memory=%v sqlite=%v", memOK, sqlOK)
	}
	if memOK && memResolved.Occurrence.SymbolID != sqlResolved.Occurrence.SymbolID {
		t.Fatalf("SymbolAt resolves differently: memory=%q sqlite=%q", memResolved.Occurrence.SymbolID, sqlResolved.Occurrence.SymbolID)
	}
}

func TestOpenStoreInvalidatesOlderSnapshotSchema(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	workspace := t.TempDir()
	first, err := OpenStore(ctx, Config{WorkspaceRoot: workspace})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := first.Commit(ctx, buildChangeSet(t, 0, sampleFiles())); err != nil {
		t.Fatal(err)
	}
	if _, err := first.db.Writer().ExecContext(ctx, "UPDATE repository_state SET snapshot_schema = ?", snapshotSchema-1); err != nil {
		t.Fatal(err)
	}
	if err := first.Close(); err != nil {
		t.Fatal(err)
	}

	reopened, err := OpenStore(ctx, Config{WorkspaceRoot: workspace})
	if err != nil {
		t.Fatal(err)
	}
	defer reopened.Close()
	metadata, err := reopened.Metadata(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if metadata.Schema != snapshotSchema || metadata.Revision != 2 {
		t.Fatalf("metadata after invalidation = %+v", metadata)
	}
	paths, err := reopened.KnownPaths(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(paths) != 0 {
		t.Fatalf("old file hashes survived schema invalidation: %v", paths)
	}
}

func indexByID(symbols []domain.Symbol) map[string]domain.Symbol {
	out := make(map[string]domain.Symbol, len(symbols))
	for _, symbol := range symbols {
		out[symbol.ID] = symbol
	}
	return out
}

func handles(symbols map[string]domain.Symbol) []string {
	out := make([]string, 0, len(symbols))
	for id := range symbols {
		out = append(out, id)
	}
	sort.Strings(out)
	return out
}

// memAllEdges collects edges from the in-memory view via a wide Graph walk (the
// in-memory ReadView has no AllEdges; Graph over every handle covers them).
func memAllEdges(t *testing.T, view store.ReadView, symbols map[string]domain.Symbol) []domain.Edge {
	t.Helper()
	_, edges := view.Graph(handles(symbols), 5, 10000)
	return edges
}

func edgeKeySet(edges []domain.Edge) map[string]struct{} {
	out := make(map[string]struct{}, len(edges))
	for _, edge := range edges {
		// Normalize on the resolved target (handle when known, else the name): the
		// in-memory store keeps the parser's original ToName on resolved edges while
		// the SQLite store derives it from the identity; both agree on ToSymbolID.
		target := edge.ToSymbolID
		if target == "" {
			target = edge.ToName
		}
		out[edge.FromSymbolID+"->"+target+":"+edge.Type] = struct{}{}
	}
	return out
}

func keysOf(set map[string]struct{}) []string {
	out := make([]string, 0, len(set))
	for key := range set {
		out = append(out, key)
	}
	sort.Strings(out)
	return out
}
