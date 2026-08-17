package sqlite

import (
	"context"
	"errors"
	"testing"

	"github.com/ThalesMMS/CodeAtlas/internal/apperror"
)

const denseDim = 8

// oneHot returns a unit vector with 1.0 at position i (mod dim).
func oneHot(dim, i int) []float64 {
	vector := make([]float64, dim)
	vector[i%dim] = 1.0
	return vector
}

// seedDenseStore commits the sample files then rebuilds the embedding index with a
// distinct one-hot vector per symbol handle, returning the store and the handles.
func seedDenseStore(t *testing.T) (*Store, []string) {
	t.Helper()
	store := openStore(t)
	if _, err := store.Commit(context.Background(), buildChangeSet(t, 0, sampleFiles())); err != nil {
		t.Fatalf("commit: %v", err)
	}
	view, err := store.OpenReadView(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	handles := make([]string, 0)
	for _, symbol := range view.AllSymbols() {
		handles = append(handles, symbol.ID)
	}
	_ = view.Close()

	metadata, _ := store.Metadata(context.Background())
	vectors := map[string][]float64{}
	for i, handle := range handles {
		vectors[handle] = oneHot(denseDim, i)
	}
	snapshot := EmbeddingSnapshot{Provider: "p", Model: "m", TemplateVersion: "t.v1", Dimension: denseDim, Vectors: vectors}
	if err := store.RebuildEmbeddings(context.Background(), metadata.Revision, snapshot); err != nil {
		t.Fatalf("RebuildEmbeddings: %v", err)
	}
	return store, handles
}

func TestSearchDenseRanksByCosine(t *testing.T) {
	t.Parallel()
	store, handles := seedDenseStore(t)
	if len(handles) < 2 {
		t.Fatalf("need at least two symbols, got %d", len(handles))
	}
	view, err := store.OpenReadView(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	defer view.Close()

	// Query aligned with the first handle's one-hot vector → it must rank first.
	hits, err := view.SearchDense(context.Background(), oneHot(denseDim, 0), 3)
	if err != nil {
		t.Fatalf("SearchDense: %v", err)
	}
	if len(hits) == 0 {
		t.Fatal("expected dense hits")
	}
	if string(hits[0].SymbolID) != handles[0] {
		t.Fatalf("top hit = %q, want %q (hits: %+v)", hits[0].SymbolID, handles[0], hits)
	}
	if hits[0].Rank != 1 || hits[0].Similarity < 0.99 {
		t.Fatalf("top hit rank/similarity wrong: %+v", hits[0])
	}
}

func TestRebuildEmbeddingsUsesBatchedIdentityOccurrenceIndex(t *testing.T) {
	t.Parallel()
	store, handles := seedDenseStore(t)
	metadata, err := store.Metadata(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	vectors := map[string][]float64{"missing-symbol": oneHot(denseDim, 0)}
	for i, handle := range handles {
		vectors[handle] = oneHot(denseDim, i)
	}
	if err := store.RebuildEmbeddings(context.Background(), metadata.Revision, EmbeddingSnapshot{
		Provider: "p", Model: "m", TemplateVersion: "t.v1", Dimension: denseDim, Vectors: vectors,
	}); err != nil {
		t.Fatalf("RebuildEmbeddings: %v", err)
	}
	var stored, missingOccurrences int
	if err := store.db.Reader().QueryRow("SELECT COUNT(*), COUNT(*) FILTER (WHERE occurrence_id IS NULL) FROM embeddings").Scan(&stored, &missingOccurrences); err != nil {
		t.Fatal(err)
	}
	if stored != len(handles) || missingOccurrences != 0 {
		t.Fatalf("stored/missing occurrences = %d/%d, want %d/0", stored, missingOccurrences, len(handles))
	}
}

func TestSearchDenseValidatesQuery(t *testing.T) {
	t.Parallel()
	store, _ := seedDenseStore(t)
	view, _ := store.OpenReadView(context.Background())
	defer view.Close()

	// Wrong dimension.
	if _, err := view.SearchDense(context.Background(), oneHot(denseDim+1, 0), 5); err == nil {
		t.Fatal("expected a dimension-mismatch error")
	}
	// Zero-norm.
	if _, err := view.SearchDense(context.Background(), make([]float64, denseDim), 5); err == nil {
		t.Fatal("expected a zero-norm error")
	}
}

func TestSearchDenseDisabledIsExplicitError(t *testing.T) {
	t.Parallel()
	// A store with no embedding rebuild has a disabled dense index.
	store := openStore(t)
	if _, err := store.Commit(context.Background(), buildChangeSet(t, 0, sampleFiles())); err != nil {
		t.Fatal(err)
	}
	view, _ := store.OpenReadView(context.Background())
	defer view.Close()
	_, err := view.SearchDense(context.Background(), oneHot(denseDim, 0), 5)
	var appErr *apperror.AppError
	if !errors.As(err, &appErr) || appErr.Code != apperror.CodeEmbeddingUnavailable {
		t.Fatalf("disabled dense index should return EMBEDDING_UNAVAILABLE, got %v", err)
	}
}

func TestRebuildEmbeddingsRejectsStaleRevision(t *testing.T) {
	t.Parallel()
	store, handles := seedDenseStore(t)
	vectors := map[string][]float64{}
	for i, handle := range handles {
		vectors[handle] = oneHot(denseDim, i)
	}
	// Revision 0 is stale (structural commit advanced it to 1).
	err := store.RebuildEmbeddings(context.Background(), 0,
		EmbeddingSnapshot{Dimension: denseDim, Vectors: vectors})
	var appErr *apperror.AppError
	if !errors.As(err, &appErr) || appErr.Code != apperror.CodeStoreVersionConflict {
		t.Fatalf("stale revision should conflict, got %v", err)
	}
}
