package retrieval

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"math"
	"sort"
	"sync"

	"github.com/ThalesMMS/CodeAtlas/internal/ai"
	"github.com/ThalesMMS/CodeAtlas/internal/apperror"
	"github.com/ThalesMMS/CodeAtlas/internal/domain"
	"github.com/ThalesMMS/CodeAtlas/internal/repository"
)

type Hybrid struct {
	store    repository.Store
	provider ai.Provider
	enabled  bool
	logger   *slog.Logger
	// degradationLogged prevents a remote outage from producing one warning per
	// debounced search keystroke. Reasons are stable and logged once per process.
	degradationLogged sync.Map
}

// SetLogger enables sanitized diagnostics for optional dense-search degradation.
// It must be called during startup, before Search is served.
func (h *Hybrid) SetLogger(logger *slog.Logger) { h.logger = logger }

func NewHybrid(store repository.Store, provider ai.Provider, enabled bool) *Hybrid {
	return &Hybrid{store: store, provider: provider, enabled: enabled}
}

func (h *Hybrid) Search(ctx context.Context, query string, limit int) ([]domain.SearchHit, error) {
	if limit <= 0 {
		limit = 20
	}
	lexical, err := h.store.SearchContext(ctx, query, limit*3)
	if err != nil {
		return nil, err
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if !h.enabled {
		return limitLexical(lexical, limit), nil
	}
	if !h.provider.Available() {
		return h.degradedLexical(lexical, limit, "provider_unavailable"), nil
	}

	vectors, err := h.provider.Embed(ctx, []string{query})
	if err != nil {
		if contextError := preserveSearchContextError(ctx, err); contextError != nil {
			return nil, contextError
		}
		return h.degradedLexical(lexical, limit, "embedding_request_failed"), nil
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if len(vectors) == 0 || len(vectors[0]) == 0 {
		return h.degradedLexical(lexical, limit, "embedding_response_empty"), nil
	}

	denseLimit := limit * 3
	dense, err := h.searchDense(ctx, vectors[0], denseLimit)
	if err != nil {
		if contextError := preserveSearchContextError(ctx, err); contextError != nil {
			return nil, contextError
		}
		return h.degradedLexical(lexical, limit, "dense_search_failed"), nil
	}

	type combined struct {
		symbol  domain.Symbol
		lexical float64
		dense   float64
		snippet string
	}
	combinedByID := make(map[string]*combined)
	maxLexical := 0.0
	for _, hit := range lexical {
		if hit.Score > maxLexical {
			maxLexical = hit.Score
		}
		combinedByID[hit.Symbol.ID] = &combined{symbol: hit.Symbol, lexical: hit.Score, snippet: hit.Snippet}
	}
	for _, hit := range dense {
		id := hit.Symbol.ID
		entry := combinedByID[id]
		if entry == nil {
			entry = &combined{symbol: hit.Symbol, snippet: hit.Snippet}
			combinedByID[id] = entry
		}
		entry.dense = hit.Score
	}

	result := make([]domain.SearchHit, 0, len(combinedByID))
	for _, entry := range combinedByID {
		lexicalScore := 0.0
		if maxLexical > 0 {
			lexicalScore = entry.lexical / maxLexical
		}
		denseScore := (entry.dense + 1) / 2
		score := 0.68*lexicalScore + 0.32*denseScore
		result = append(result, domain.SearchHit{
			Symbol: entry.symbol, Snippet: entry.snippet, Score: score, Source: "hybrid",
		})
	}
	sort.Slice(result, func(i, j int) bool { return result[i].Score > result[j].Score })
	if len(result) > limit {
		result = result[:limit]
	}
	metadata, err := h.store.SnapshotMetadataContext(ctx)
	if err != nil {
		return nil, err
	}
	snapshotID := metadata.ID
	for i := range result {
		result[i].SnapshotID = snapshotID
	}
	return result, nil
}

func limitLexical(hits []domain.SearchHit, limit int) []domain.SearchHit {
	if len(hits) > limit {
		return hits[:limit]
	}
	return hits
}

func (h *Hybrid) degradedLexical(hits []domain.SearchHit, limit int, reason string) []domain.SearchHit {
	if h.logger != nil {
		if _, loaded := h.degradationLogged.LoadOrStore(reason, struct{}{}); !loaded {
			h.logger.Warn("hybrid search degraded to lexical", "reason", reason)
		}
	}
	return limitLexical(hits, limit)
}

func preserveSearchContextError(ctx context.Context, err error) error {
	if contextError := ctx.Err(); contextError != nil {
		return contextError
	}
	if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
		return err
	}
	return nil
}

func (h *Hybrid) RefreshEmbeddings(ctx context.Context, symbols []domain.Symbol) error {
	if !h.enabled {
		return nil
	}
	existing := h.store.Embeddings()
	pending := make([]domain.Symbol, 0)
	for _, symbol := range symbols {
		if symbol.Kind == "file" {
			continue
		}
		if _, exists := existing[symbol.ID]; !exists {
			pending = append(pending, symbol)
		}
	}
	vectors, err := h.GenerateEmbeddings(ctx, pending)
	if err != nil {
		return err
	}
	if len(vectors) == 0 {
		return nil
	}
	// Reject the whole batch before publishing if any vector is empty, non-finite
	// or has a dimension different from the established index — never mix.
	metadata, err := h.store.EmbeddingMetadataContext(ctx)
	if err != nil {
		return err
	}
	if err := validateIncrementalVectors(vectors, metadata.Dimension); err != nil {
		return err
	}
	merged := existing
	for id, vector := range vectors {
		merged[id] = vector
	}
	if metadata.Dimension == 0 {
		for _, vector := range merged {
			metadata = DesiredMetadata("openai-compatible", "", len(vector))
			break
		}
	}
	prepared, err := h.store.PrepareEmbeddingRebuildContext(ctx, merged, metadata)
	if err != nil {
		return err
	}
	_, err = h.store.CommitPreparedContext(ctx, prepared)
	return err
}

func (h *Hybrid) searchDense(ctx context.Context, queryVector []float64, limit int) ([]domain.SearchHit, error) {
	if searcher, ok := h.store.(repository.DenseSearcher); ok {
		return searcher.SearchDense(ctx, queryVector, limit)
	}

	// Compatibility fallback for third-party/test Store implementations that do
	// not yet expose the optimized capability.
	embeddings := h.store.Embeddings()
	if len(embeddings) == 0 {
		return nil, apperror.EmbeddingUnavailable(fmt.Errorf("embedding index is empty; reindex with embeddings enabled"))
	}
	symbols := h.store.AllSymbols()
	symbolByID := make(map[string]domain.Symbol, len(symbols))
	for _, symbol := range symbols {
		symbolByID[symbol.ID] = symbol
	}
	hits := make([]domain.SearchHit, 0, len(embeddings))
	for id, vector := range embeddings {
		symbol, ok := symbolByID[id]
		if !ok {
			continue
		}
		hits = append(hits, domain.SearchHit{
			Symbol: symbol, Snippet: symbol.Summary, Score: cosine(queryVector, vector), Source: "dense",
		})
	}
	sort.Slice(hits, func(i, j int) bool {
		if hits[i].Score != hits[j].Score {
			return hits[i].Score > hits[j].Score
		}
		return hits[i].Symbol.ID < hits[j].Symbol.ID
	})
	if len(hits) > limit {
		hits = hits[:limit]
	}
	return hits, nil
}

// GenerateEmbeddings produces vectors for the given non-file symbols without
// touching the store. It validates that the provider returns exactly one vector
// per input. Callers (e.g. a batched index commit) embed the vectors in a
// ChangeSet rather than mutating the store during preparation.
func (h *Hybrid) GenerateEmbeddings(ctx context.Context, symbols []domain.Symbol) (map[string][]float64, error) {
	if !h.enabled {
		return nil, nil
	}
	if !h.provider.Available() {
		return nil, ai.ErrUnavailable
	}
	targets := make([]domain.Symbol, 0, len(symbols))
	for _, symbol := range symbols {
		if symbol.Kind != "file" {
			targets = append(targets, symbol)
		}
	}
	result := make(map[string][]float64, len(targets))
	const batchSize = 16
	for start := 0; start < len(targets); start += batchSize {
		end := start + batchSize
		if end > len(targets) {
			end = len(targets)
		}
		texts := make([]string, 0, end-start)
		for _, symbol := range targets[start:end] {
			texts = append(texts, SymbolEmbeddingTextV2(symbol))
		}
		vectors, err := h.provider.Embed(ctx, texts)
		if err != nil {
			return nil, err
		}
		if len(vectors) != end-start {
			return nil, fmt.Errorf("embedding count mismatch: got %d vectors for %d symbols", len(vectors), end-start)
		}
		for i, vector := range vectors {
			result[targets[start+i].ID] = vector
		}
	}
	return result, nil
}

func cosine(a, b []float64) float64 {
	if len(a) == 0 || len(a) != len(b) {
		return 0
	}
	dot, normA, normB := 0.0, 0.0, 0.0
	for i := range a {
		dot += a[i] * b[i]
		normA += a[i] * a[i]
		normB += b[i] * b[i]
	}
	if normA == 0 || normB == 0 {
		return 0
	}
	return dot / (math.Sqrt(normA) * math.Sqrt(normB))
}
