package store_test

import (
	"math"
	"path/filepath"
	"testing"

	"github.com/ThalesMMS/CodeAtlas/internal/domain"
	"github.com/ThalesMMS/CodeAtlas/internal/store"
)

// functionSymbolID returns the v1 SymbolID of the first function symbol, since
// ingestion re-keys the parser's legacy ids.
func functionSymbolID(t *testing.T, repository *store.Store) string {
	t.Helper()
	for _, symbol := range repository.AllSymbols() {
		if symbol.Kind == "function" {
			return symbol.ID
		}
	}
	t.Fatal("no function symbol found")
	return ""
}

func embeddingMetadata(dimension int) domain.EmbeddingIndexMetadata {
	return domain.EmbeddingIndexMetadata{
		Enabled:         true,
		Provider:        "openai-compatible",
		Model:           "default",
		Dimension:       dimension,
		TemplateVersion: "v1",
		Distance:        "cosine",
		BuiltAt:         buildTime,
	}
}

func TestPrepareEmbeddingRebuildRejectsUnknownSymbol(t *testing.T) {
	t.Parallel()
	repository := openStore(t)
	repository.ReplaceFile(parsedFile("a.go"))

	_, err := repository.PrepareEmbeddingRebuild(map[string][]float64{"ghost": {0.1, 0.2}}, embeddingMetadata(2))
	if err == nil {
		t.Fatal("PrepareEmbeddingRebuild() accepted a vector for an unknown symbol")
	}
}

func TestPrepareEmbeddingRebuildRejectsWrongDimension(t *testing.T) {
	t.Parallel()
	repository := openStore(t)
	repository.ReplaceFile(parsedFile("a.go"))

	_, err := repository.PrepareEmbeddingRebuild(map[string][]float64{"a.go:fn": {0.1, 0.2, 0.3}}, embeddingMetadata(2))
	if err == nil {
		t.Fatal("PrepareEmbeddingRebuild() accepted a vector whose length != declared dimension")
	}
}

func TestPrepareEmbeddingRebuildRejectsNonFinite(t *testing.T) {
	t.Parallel()
	repository := openStore(t)
	repository.ReplaceFile(parsedFile("a.go"))

	_, err := repository.PrepareEmbeddingRebuild(map[string][]float64{"a.go:fn": {math.Inf(1), 0.2}}, embeddingMetadata(2))
	if err == nil {
		t.Fatal("PrepareEmbeddingRebuild() accepted a non-finite embedding value")
	}
}

func TestDeleteFileRemovesOrphanEmbeddings(t *testing.T) {
	t.Parallel()
	repository := openStore(t)
	repository.ReplaceFile(parsedFile("a.go"))
	fnID := functionSymbolID(t, repository)
	prepared, err := repository.PrepareEmbeddingRebuild(map[string][]float64{fnID: {0.1, 0.2}}, embeddingMetadata(2))
	if err != nil {
		t.Fatalf("PrepareEmbeddingRebuild() error = %v", err)
	}
	if err := repository.CommitPrepared(prepared); err != nil {
		t.Fatalf("CommitPrepared() error = %v", err)
	}
	if repository.EmbeddingCount() != 1 {
		t.Fatalf("EmbeddingCount = %d before delete, want 1", repository.EmbeddingCount())
	}

	repository.DeleteFile("a.go")
	if repository.EmbeddingCount() != 0 {
		t.Fatalf("EmbeddingCount = %d after delete, want 0 (orphan vector left behind)", repository.EmbeddingCount())
	}
}

func TestReplaceFileRemovesOrphanEmbeddings(t *testing.T) {
	t.Parallel()
	repository := openStore(t)
	repository.ReplaceFile(parsedFile("a.go"))
	fnID := functionSymbolID(t, repository)
	prepared, err := repository.PrepareEmbeddingRebuild(map[string][]float64{fnID: {0.1, 0.2}}, embeddingMetadata(2))
	if err != nil {
		t.Fatalf("PrepareEmbeddingRebuild() error = %v", err)
	}
	if err := repository.CommitPrepared(prepared); err != nil {
		t.Fatalf("CommitPrepared() error = %v", err)
	}

	// Re-index the same file without the function symbol: its vector is now orphaned.
	repository.ReplaceFile(domain.ParsedFile{
		File:    domain.File{Path: "a.go", Language: "go", Hash: "a.go-v2"},
		Symbols: []domain.Symbol{{ID: "a.go:file", Path: "a.go", Name: "a.go", Kind: "file", Range: domain.Range{Start: domain.Position{Line: 1, Column: 1}, End: domain.Position{Line: 2, Column: 1}}}},
	})
	if repository.EmbeddingCount() != 0 {
		t.Fatalf("EmbeddingCount = %d after re-index, want 0 (orphan vector for removed symbol)", repository.EmbeddingCount())
	}
}

func TestEmbeddingMetadataAndVectorsPersistAcrossReopen(t *testing.T) {
	t.Parallel()
	path := filepath.Join(t.TempDir(), "index.json")
	repository, err := store.Open(path)
	if err != nil {
		t.Fatalf("Open() error = %v", err)
	}
	repository.ReplaceFile(parsedFile("a.go"))

	fnID := functionSymbolID(t, repository)
	metadata := embeddingMetadata(3)
	prepared, err := repository.PrepareEmbeddingRebuild(map[string][]float64{fnID: {0.1, 0.2, 0.3}}, metadata)
	if err != nil {
		t.Fatalf("PrepareEmbeddingRebuild() error = %v", err)
	}
	if err := repository.CommitPrepared(prepared); err != nil {
		t.Fatalf("CommitPrepared() error = %v", err)
	}
	if err := repository.Persist(); err != nil {
		t.Fatalf("Persist() error = %v", err)
	}

	reopened, err := store.Open(path)
	if err != nil {
		t.Fatalf("Open(reopen) error = %v", err)
	}
	if got := reopened.EmbeddingCount(); got != 1 {
		t.Fatalf("EmbeddingCount after reopen = %d, want 1", got)
	}
	got := reopened.EmbeddingMetadata()
	want := metadata
	if got.Enabled != want.Enabled || got.Provider != want.Provider || got.Model != want.Model ||
		got.Dimension != want.Dimension || got.TemplateVersion != want.TemplateVersion || got.Distance != want.Distance {
		t.Fatalf("metadata after reopen = %+v, want %+v", got, want)
	}
	if !got.BuiltAt.Equal(want.BuiltAt) {
		t.Fatalf("BuiltAt after reopen = %v, want %v", got.BuiltAt, want.BuiltAt)
	}
}
