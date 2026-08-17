package store

import (
	"context"
	"fmt"
	"math"
	"sort"

	"github.com/ThalesMMS/CodeAtlas/internal/apperror"
	"github.com/ThalesMMS/CodeAtlas/internal/domain"
)

// SearchDense scores the immutable in-memory embedding index without exposing
// or copying its vectors. Only the bounded result slice escapes the read lock.
func (s *Store) SearchDense(ctx context.Context, queryVector []float64, limit int) ([]domain.SearchHit, error) {
	if limit <= 0 {
		limit = 20
	}
	if err := validateDenseVector(queryVector); err != nil {
		return nil, apperror.InvalidArgument("invalid query vector: "+err.Error(), nil)
	}

	s.mu.RLock()
	defer s.mu.RUnlock()
	if len(s.current.embeddings) == 0 {
		return nil, apperror.EmbeddingUnavailable(fmt.Errorf("embedding index is empty; reindex with embeddings enabled"))
	}
	if dimension := s.current.embeddingMetadata.Dimension; dimension > 0 && dimension != len(queryVector) {
		return nil, apperror.InvalidArgument("query vector dimension does not match the index", nil)
	}

	hits := make([]domain.SearchHit, 0, limit)
	checked := 0
	for id, vector := range s.current.embeddings {
		if checked%256 == 0 && ctx.Err() != nil {
			return nil, apperror.RequestCanceled(ctx.Err())
		}
		checked++
		if len(vector) != len(queryVector) {
			return nil, fmt.Errorf("embedding index has inconsistent dimensions")
		}
		symbol, ok := s.current.symbols[id]
		if !ok {
			continue
		}
		snippet := symbol.DocComment
		if snippet == "" {
			snippet = symbol.Summary
		}
		candidate := domain.SearchHit{Symbol: symbol, Snippet: snippet, Score: denseCosine(queryVector, vector), Source: "dense"}
		insert := sort.Search(len(hits), func(i int) bool { return betterDenseHit(candidate, hits[i]) })
		if insert >= limit {
			continue
		}
		if len(hits) < limit {
			hits = append(hits, domain.SearchHit{})
		}
		copy(hits[insert+1:], hits[insert:len(hits)-1])
		hits[insert] = candidate
	}
	return hits, nil
}

func betterDenseHit(a, b domain.SearchHit) bool {
	if a.Score != b.Score {
		return a.Score > b.Score
	}
	return a.Symbol.ID < b.Symbol.ID
}

func validateDenseVector(vector []float64) error {
	if len(vector) == 0 {
		return fmt.Errorf("vector is empty")
	}
	norm := 0.0
	for _, value := range vector {
		if math.IsNaN(value) || math.IsInf(value, 0) {
			return fmt.Errorf("vector contains non-finite values")
		}
		norm += value * value
	}
	if norm == 0 {
		return fmt.Errorf("vector norm is zero")
	}
	return nil
}

func denseCosine(a, b []float64) float64 {
	var dot, normA, normB float64
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
