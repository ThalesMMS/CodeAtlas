package sqlite

import (
	"context"
	"strings"
	"testing"
	"unicode/utf8"

	"github.com/ThalesMMS/CodeAtlas/internal/domain"
	"github.com/ThalesMMS/CodeAtlas/internal/storederive"
)

func TestFTSSnippetKeepsUTF8Boundaries(t *testing.T) {
	t.Parallel()
	content := strings.Repeat("a", 89) + "é" + strings.Repeat("b", 400)
	got := storederive.Snippet(domain.Symbol{DocComment: content}, []string{"é"})
	if !utf8.ValidString(got) {
		t.Fatalf("Snippet returned invalid UTF-8: %q", got)
	}
}

func searchView(t *testing.T) ReadView {
	t.Helper()
	store := openStore(t)
	if _, err := store.Commit(context.Background(), buildChangeSet(t, 0, sampleFiles())); err != nil {
		t.Fatalf("commit: %v", err)
	}
	view, err := store.OpenReadView(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = view.Close() })
	return view
}

func TestSearchRanksExactNameHighest(t *testing.T) {
	t.Parallel()
	view := searchView(t)
	hits, err := view.Search("Pay", 10)
	if err != nil {
		t.Fatalf("Search: %v", err)
	}
	if len(hits) == 0 {
		t.Fatal("expected hits for 'Pay'")
	}
	if hits[0].Symbol.Name != "Pay" {
		t.Fatalf("top hit = %q, want Pay (hits: %+v)", hits[0].Symbol.Name, hits)
	}
	if hits[0].Source != "fts5" {
		t.Fatalf("source = %q, want fts5", hits[0].Source)
	}
}

func TestStoreSearchMatchesMaterializedReadView(t *testing.T) {
	t.Parallel()
	store := openStore(t)
	if _, err := store.Commit(context.Background(), buildChangeSet(t, 0, sampleFiles())); err != nil {
		t.Fatalf("commit: %v", err)
	}
	direct, err := store.Search(context.Background(), "charges order", 10)
	if err != nil {
		t.Fatalf("Store.Search: %v", err)
	}
	view, err := store.OpenReadView(context.Background())
	if err != nil {
		t.Fatalf("OpenReadView: %v", err)
	}
	defer view.Close() //nolint:errcheck
	materialized, err := view.Search("charges order", 10)
	if err != nil {
		t.Fatalf("ReadView.Search: %v", err)
	}
	if len(direct) != len(materialized) || len(direct) == 0 {
		t.Fatalf("direct/materialized lengths = %d/%d", len(direct), len(materialized))
	}
	for i := range direct {
		if direct[i].Symbol != materialized[i].Symbol || direct[i].Snippet != materialized[i].Snippet ||
			direct[i].Score != materialized[i].Score || direct[i].SnapshotID != materialized[i].SnapshotID {
			t.Fatalf("hit %d differs:\n direct=%+v\n view=%+v", i, direct[i], materialized[i])
		}
	}
}

func TestReadViewCachesNormalizedBoostFields(t *testing.T) {
	t.Parallel()
	view := searchView(t)
	materialized, ok := view.(*readView)
	if !ok {
		t.Fatalf("view type = %T, want *readView", view)
	}
	for id, symbol := range materialized.flat {
		if symbol.Name != "Pay" {
			continue
		}
		fields := materialized.boostFields[id]
		if fields.name != "pay" || fields.qualified != "pkg.service.pay" {
			t.Fatalf("boost fields = %+v", fields)
		}
		return
	}
	t.Fatal("Pay symbol not found")
}

func TestSearchTokenizesIdentifiers(t *testing.T) {
	t.Parallel()
	view := searchView(t)
	// "charge" (lower) should still match the Charge symbol via the tokenizer.
	hits, err := view.Search("charge", 10)
	if err != nil {
		t.Fatalf("Search: %v", err)
	}
	found := false
	for _, hit := range hits {
		if hit.Symbol.Name == "Charge" {
			found = true
		}
	}
	if !found {
		t.Fatalf("expected Charge in results: %+v", hits)
	}
}

func TestSearchIndexesAndReturnsDocComment(t *testing.T) {
	t.Parallel()
	view := searchView(t)
	hits, err := view.Search("charges order", 10)
	if err != nil {
		t.Fatal(err)
	}
	for _, hit := range hits {
		if hit.Symbol.Name == "Pay" {
			if hit.Symbol.DocComment != "Pay charges the order." || hit.Snippet != "Pay charges the order." {
				t.Fatalf("doc comment not preserved in hit: %+v", hit)
			}
			return
		}
	}
	t.Fatalf("Pay not found by doc comment: %+v", hits)
}

func TestSearchIsInjectionSafe(t *testing.T) {
	t.Parallel()
	view := searchView(t)
	// FTS5 operators / quotes in raw input must not error the query.
	if _, err := view.Search(`Pay" OR x*`, 10); err != nil {
		t.Fatalf("injection-shaped query should not error: %v", err)
	}
}

func TestSearchEmptyQueryReturnsNothing(t *testing.T) {
	t.Parallel()
	view := searchView(t)
	hits, err := view.Search("  !", 10) // no usable tokens
	if err != nil {
		t.Fatalf("Search: %v", err)
	}
	if len(hits) != 0 {
		t.Fatalf("expected no hits for a token-less query, got %d", len(hits))
	}
}

func TestSearchAfterCloseErrors(t *testing.T) {
	t.Parallel()
	view := searchView(t)
	_ = view.Close()
	if _, err := view.Search("Pay", 10); err == nil {
		t.Fatal("Search after Close should error")
	}
}

func TestCommitRefreshesOnlyAffectedFTSSymbols(t *testing.T) {
	t.Parallel()
	store := openStore(t)
	files := sampleFiles()
	if _, err := store.Commit(context.Background(), buildChangeSet(t, 0, files)); err != nil {
		t.Fatal(err)
	}
	rowID := func(name string) int64 {
		var id int64
		if err := store.db.Reader().QueryRow(`SELECT rows.fts_rowid FROM fts_symbol_rows rows
			JOIN symbol_identities i ON i.symbol_id = rows.symbol_id WHERE i.name = ?`, name).Scan(&id); err != nil {
			t.Fatal(err)
		}
		return id
	}
	untouchedRowID := rowID("Charge")
	changedRowID := rowID("Pay")

	files[0].File.Hash = "h-svc-v2"
	files[0].Symbols[1].DocComment = "Pay authorizes the order."
	if _, err := store.Commit(context.Background(), buildChangeSet(t, 1, files[:1])); err != nil {
		t.Fatal(err)
	}
	if got := rowID("Charge"); got != untouchedRowID {
		t.Fatalf("untouched FTS rowid = %d, want %d", got, untouchedRowID)
	}
	if got := rowID("Pay"); got == changedRowID {
		t.Fatalf("affected FTS rowid remained %d; row was not replaced", got)
	}
	hits, err := store.Search(context.Background(), "authorizes", 10)
	if err != nil || len(hits) == 0 || hits[0].Symbol.Name != "Pay" {
		t.Fatalf("updated FTS search = %+v, %v", hits, err)
	}
}

func TestRebuildFTSRestoresDerivedIndex(t *testing.T) {
	t.Parallel()
	store := openStore(t)
	if _, err := store.Commit(context.Background(), buildChangeSet(t, 0, sampleFiles())); err != nil {
		t.Fatal(err)
	}
	if _, err := store.db.Writer().ExecContext(context.Background(), "DELETE FROM fts_symbols"); err != nil {
		t.Fatal(err)
	}
	if hits, err := store.Search(context.Background(), "Pay", 10); err != nil || len(hits) != 0 {
		t.Fatalf("search before rebuild = %+v, %v; want empty", hits, err)
	}

	if err := store.RebuildFTS(context.Background()); err != nil {
		t.Fatalf("RebuildFTS() error = %v", err)
	}
	hits, err := store.Search(context.Background(), "Pay", 10)
	if err != nil || len(hits) == 0 || hits[0].Symbol.Name != "Pay" {
		t.Fatalf("search after rebuild = %+v, %v", hits, err)
	}
}
