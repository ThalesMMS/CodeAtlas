package retrieval

import (
	"bytes"
	"context"
	"errors"
	"log/slog"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/ThalesMMS/CodeAtlas/internal/domain"
	"github.com/ThalesMMS/CodeAtlas/internal/repository"
)

func TestSearchReturnsLexicalResultsWhenEmbeddingsDisabled(t *testing.T) {
	t.Parallel()
	repository := indexedStore(t)
	provider := &embeddingProvider{err: errors.New("embedding endpoint should not be called")}
	retriever := NewHybrid(repository, provider, false)

	hits, err := retriever.Search(context.Background(), "submit order", 5)
	if err != nil {
		t.Fatalf("Search() error = %v", err)
	}
	if len(hits) == 0 {
		t.Fatal("Search() returned no lexical hits")
	}
	if provider.calls != 0 {
		t.Fatalf("Embed() calls = %d, want 0", provider.calls)
	}
}

func TestSearchFallsBackToLexicalWhenEmbeddingRequestFails(t *testing.T) {
	t.Parallel()
	repository := indexedStore(t)
	retriever := NewHybrid(repository, &embeddingProvider{err: errors.New("embedding unavailable")}, true)

	hits, err := retriever.Search(context.Background(), "submit order", 5)
	if err != nil {
		t.Fatalf("Search() error = %v", err)
	}
	if len(hits) == 0 || hits[0].Source == "hybrid" || hits[0].SnapshotID == "" {
		t.Fatalf("fallback hits = %#v, want lexical results", hits)
	}
}

func TestSearchDegradationLogUsesStableReasonWithoutProviderError(t *testing.T) {
	t.Parallel()
	repository := indexedStore(t)
	secret := "api_key=secret-that-must-not-be-logged"
	retriever := NewHybrid(repository, &embeddingProvider{err: errors.New(secret)}, true)
	var logs bytes.Buffer
	retriever.SetLogger(slog.New(slog.NewTextHandler(&logs, &slog.HandlerOptions{Level: slog.LevelDebug})))

	if _, err := retriever.Search(context.Background(), "submit order", 5); err != nil {
		t.Fatal(err)
	}
	if _, err := retriever.Search(context.Background(), "submit order", 5); err != nil {
		t.Fatal(err)
	}
	if text := logs.String(); strings.Count(text, "embedding_request_failed") != 1 || strings.Contains(text, secret) {
		t.Fatalf("degradation log = %q", text)
	}
}

func TestSearchFallsBackToLexicalWhenEmbeddingResponseIsEmpty(t *testing.T) {
	t.Parallel()
	repository := indexedStore(t)
	retriever := NewHybrid(repository, &embeddingProvider{}, true)

	hits, err := retriever.Search(context.Background(), "submit order", 5)
	if err != nil || len(hits) == 0 || hits[0].Source == "hybrid" {
		t.Fatalf("Search() = %#v, %v, want lexical fallback", hits, err)
	}
}

func TestSearchFallsBackToLexicalWhenEmbeddingIndexIsIncomplete(t *testing.T) {
	t.Parallel()
	repository := indexedStore(t)
	retriever := NewHybrid(repository, &embeddingProvider{vectors: [][]float64{{0.1, 0.2}}}, true)

	hits, err := retriever.Search(context.Background(), "submit order", 5)
	if err != nil {
		t.Fatalf("Search() error = %v", err)
	}
	if len(hits) == 0 || hits[0].Source == "hybrid" {
		t.Fatalf("fallback hits = %#v, want lexical results", hits)
	}
}

func TestSearchFallsBackToLexicalWhenProviderIsUnavailable(t *testing.T) {
	t.Parallel()
	repository := indexedStore(t)
	provider := &embeddingProvider{unavailable: true}
	retriever := NewHybrid(repository, provider, true)

	hits, err := retriever.Search(context.Background(), "submit order", 5)
	if err != nil || len(hits) == 0 {
		t.Fatalf("Search() = %#v, %v, want lexical fallback", hits, err)
	}
	if provider.calls != 0 {
		t.Fatalf("Embed() calls = %d, want 0 for unavailable provider", provider.calls)
	}
}

func TestSearchPreservesEmbeddingContextErrors(t *testing.T) {
	t.Parallel()
	repository := indexedStore(t)
	retriever := NewHybrid(repository, &embeddingProvider{err: context.DeadlineExceeded}, true)

	if _, err := retriever.Search(context.Background(), "submit order", 5); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("Search() error = %v, want deadline", err)
	}
}

func TestSearchUsesDenseStoreOperationWithoutCopyAccessors(t *testing.T) {
	t.Parallel()
	base := indexedStore(t)
	store := &denseStoreSpy{Store: base}
	retriever := NewHybrid(store, &embeddingProvider{vectors: [][]float64{{0.1, 0.2}}}, true)

	hits, err := retriever.Search(context.Background(), "submit order", 5)
	if err != nil {
		t.Fatalf("Search() error = %v", err)
	}
	if len(hits) == 0 {
		t.Fatal("Search() returned no hybrid hits")
	}
	if store.denseCalls != 1 {
		t.Fatalf("dense calls = %d, want 1", store.denseCalls)
	}
	if store.embeddingCalls != 0 || store.symbolCalls != 0 {
		t.Fatalf("copy accessors called: embeddings=%d symbols=%d", store.embeddingCalls, store.symbolCalls)
	}
}

func TestSearchFallsBackToLexicalWhenDenseSearchFails(t *testing.T) {
	t.Parallel()
	base := indexedStore(t)
	store := &denseStoreSpy{Store: base, denseErr: errors.New("dense index unavailable")}
	retriever := NewHybrid(store, &embeddingProvider{vectors: [][]float64{{0.1, 0.2}}}, true)

	hits, err := retriever.Search(context.Background(), "submit order", 5)
	if err != nil || len(hits) == 0 || hits[0].Source == "hybrid" {
		t.Fatalf("Search() = %#v, %v, want lexical fallback", hits, err)
	}
	if store.denseCalls != 1 {
		t.Fatalf("dense calls = %d, want 1", store.denseCalls)
	}
}

func TestRefreshEmbeddingsCopiesExistingIndexOnce(t *testing.T) {
	t.Parallel()
	base := indexedStore(t)
	store := &denseStoreSpy{Store: base}
	retriever := NewHybrid(store, &embeddingProvider{vectors: [][]float64{{0.1, 0.2}}}, true)

	if err := retriever.RefreshEmbeddings(context.Background(), base.AllSymbols()); err != nil {
		t.Fatalf("RefreshEmbeddings() error = %v", err)
	}
	if store.embeddingCalls != 1 {
		t.Fatalf("embedding copies = %d, want 1", store.embeddingCalls)
	}
}

func TestHybridUsesDenseOnlyWhenRuntimeAvailable(t *testing.T) {
	tests := []struct {
		name      string
		configure func(*testing.T, *EmbeddingRuntime, *embeddingProvider)
		wantDense bool
	}{
		{name: "disabled", configure: func(_ *testing.T, _ *EmbeddingRuntime, _ *embeddingProvider) {}},
		{name: "rebuilding", configure: func(t *testing.T, runtime *EmbeddingRuntime, provider *embeddingProvider) {
			prepared, err := runtime.Prepare(context.Background(), EmbeddingConfiguration{Provider: provider, Enabled: true, Model: "m", BaseURL: "https://example.test/v1"}, domain.EmbeddingIndexMetadata{}, 0)
			if err != nil {
				t.Fatal(err)
			}
			prepared.Activate()
			provider.calls = 0
		}},
		{name: "unavailable", configure: func(t *testing.T, runtime *EmbeddingRuntime, provider *embeddingProvider) {
			prepared, err := runtime.Prepare(context.Background(), EmbeddingConfiguration{Provider: provider, Enabled: true, Model: "m", BaseURL: "https://example.test/v1"}, domain.EmbeddingIndexMetadata{}, 0)
			if err != nil {
				t.Fatal(err)
			}
			prepared.Activate()
			runtime.MarkFailed(prepared.Fingerprint())
			provider.calls = 0
		}},
		{name: "available", wantDense: true, configure: func(_ *testing.T, runtime *EmbeddingRuntime, _ *embeddingProvider) {
			runtime.forceAvailableForCompatibility()
		}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			store := &denseStoreSpy{Store: indexedStore(t)}
			provider := &embeddingProvider{vectors: [][]float64{{0.1, 0.2}}}
			runtime := NewEmbeddingRuntime(provider, false)
			test.configure(t, runtime, provider)
			retriever := NewHybridWithRuntime(store, runtime)

			hits, err := retriever.Search(context.Background(), "submit order", 5)
			if err != nil || len(hits) == 0 {
				t.Fatalf("Search = %#v, %v", hits, err)
			}
			if got := store.denseCalls > 0; got != test.wantDense {
				t.Fatalf("dense used = %v, want %v", got, test.wantDense)
			}
			if got := provider.calls > 0; got != test.wantDense {
				t.Fatalf("query embedding used = %v, want %v", got, test.wantDense)
			}
			vectors, err := retriever.GenerateEmbeddings(context.Background(), []domain.Symbol{{ID: "x", Kind: "function"}})
			if err != nil {
				t.Fatal(err)
			}
			if got := len(vectors) > 0; got != test.wantDense {
				t.Fatalf("incremental embedding used = %v, want %v", got, test.wantDense)
			}
		})
	}
}

func indexedStore(t *testing.T) repository.Store {
	t.Helper()
	repository, err := repository.OpenJSON(filepath.Join(t.TempDir(), "index.json"))
	if err != nil {
		t.Fatal(err)
	}
	repository.ReplaceFile(domain.ParsedFile{
		File: domain.File{Path: "service.go", Language: "go", Hash: "service", IndexedAt: time.Now()},
		Symbols: []domain.Symbol{
			{
				ID: "submit", Path: "service.go", Name: "SubmitOrder", QualifiedName: "service.go::SubmitOrder", Kind: "function",
				Range:     domain.Range{Start: domain.Position{Line: 1, Column: 1}, End: domain.Position{Line: 3, Column: 1}},
				Signature: "func SubmitOrder()", Summary: "Submits an order.", Code: "func SubmitOrder() {}",
			},
		},
	})
	return repository
}

type embeddingProvider struct {
	vectors     [][]float64
	err         error
	calls       int
	unavailable bool
}

type denseStoreSpy struct {
	repository.Store
	denseCalls     int
	embeddingCalls int
	symbolCalls    int
	denseErr       error
}

func (s *denseStoreSpy) SearchDense(context.Context, []float64, int) ([]domain.SearchHit, error) {
	s.denseCalls++
	if s.denseErr != nil {
		return nil, s.denseErr
	}
	hits, err := s.Store.Search("submit", 1)
	if err != nil {
		return nil, err
	}
	if len(hits) == 0 {
		return nil, errors.New("submit symbol not found")
	}
	hit := hits[0]
	hit.Score = 0.9
	hit.Source = "dense"
	return []domain.SearchHit{hit}, nil
}

func (s *denseStoreSpy) Embeddings() map[string][]float64 {
	s.embeddingCalls++
	return s.Store.Embeddings()
}

func (s *denseStoreSpy) AllSymbols() []domain.Symbol {
	s.symbolCalls++
	return s.Store.AllSymbols()
}

func (p *embeddingProvider) Name() string { return "embedding-test" }

func (p *embeddingProvider) Available() bool { return !p.unavailable }

func (p *embeddingProvider) Complete(context.Context, string, string, int) (string, error) {
	return "", errors.New("completion endpoint should not be called")
}

func (p *embeddingProvider) Embed(context.Context, []string) ([][]float64, error) {
	p.calls++
	if p.err != nil {
		return nil, p.err
	}
	return p.vectors, nil
}
