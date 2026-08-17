package sqlite

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"errors"
	"sort"
	"time"

	"github.com/ThalesMMS/CodeAtlas/internal/apperror"
	"github.com/ThalesMMS/CodeAtlas/internal/domain"
)

func contentHash(blob []byte) string {
	sum := sha256.Sum256(blob)
	return hex.EncodeToString(sum[:])
}

// DenseHit is one dense-search result. Similarity is exposed for diagnostics;
// cross-source ranking uses Rank (position), never the raw score.
type DenseHit struct {
	SymbolID     domain.SymbolID
	OccurrenceID domain.OccurrenceID
	Similarity   float64
	Rank         int
}

type embeddingMeta struct {
	enabled   bool
	dimension int
	encoding  string
}

func readEmbeddingMeta(ctx context.Context, q rowQueryer) (embeddingMeta, bool, error) {
	rs, err := q.QueryContext(ctx, "SELECT enabled, dimension, encoding FROM embedding_index_metadata WHERE id = 1")
	if err != nil {
		return embeddingMeta{}, false, err
	}
	defer rs.Close()
	if !rs.Next() {
		return embeddingMeta{}, false, rs.Err()
	}
	var meta embeddingMeta
	var enabled int
	if err := rs.Scan(&enabled, &meta.dimension, &meta.encoding); err != nil {
		return embeddingMeta{}, false, err
	}
	meta.enabled = enabled == 1
	return meta, true, nil
}

// SearchDense runs a top-K cosine search by STREAMING vectors from SQLite and
// keeping only a bounded top-K (memory O(K + dimension), not the whole index). It
// validates the query and the index metadata, decodes one vector at a time, and
// resolves occurrences only for the final hits (they are already on the row). A
// disabled index returns an explicit EMBEDDING_UNAVAILABLE error (never an empty
// list that looks like "no matches"); an inconsistent index returns an error.
func (v *readView) SearchDense(ctx context.Context, queryVector []float64, limit int) ([]DenseHit, error) {
	if v.closed {
		return nil, errors.New("sqlite store: read view is closed")
	}
	if limit <= 0 {
		limit = 20
	}
	if err := validateQueryVector(queryVector); err != nil {
		return nil, apperror.InvalidArgument("invalid query vector: "+err.Error(), nil)
	}
	meta, ok, err := readEmbeddingMeta(ctx, v.reader)
	if err != nil {
		return nil, apperror.StoreCorrupted(err)
	}
	if !ok || !meta.enabled {
		return nil, apperror.EmbeddingUnavailable(errors.New("dense index is not enabled"))
	}
	if meta.dimension != len(queryVector) {
		return nil, apperror.InvalidArgument("query vector dimension does not match the index", nil)
	}

	rows, err := v.reader.QueryContext(ctx, "SELECT symbol_id, COALESCE(occurrence_id,''), dimension, vector FROM embeddings")
	if err != nil {
		return nil, apperror.StoreCorrupted(err)
	}
	defer rows.Close()

	collector := newTopK(limit)
	scanned := 0
	for rows.Next() {
		if scanned%256 == 0 && ctx.Err() != nil {
			return nil, apperror.RequestCanceled(ctx.Err())
		}
		scanned++
		var symbolID, occurrenceID string
		var dimension int
		var blob []byte
		if err := rows.Scan(&symbolID, &occurrenceID, &dimension, &blob); err != nil {
			return nil, apperror.StoreCorrupted(err)
		}
		if dimension != meta.dimension {
			return nil, apperror.StoreCorrupted(errors.New("embedding index has inconsistent dimensions"))
		}
		vector, err := decodeVector(blob, dimension)
		if err != nil {
			return nil, apperror.StoreCorrupted(err)
		}
		collector.consider(symbolID, occurrenceID, cosineSimilarity(queryVector, vector))
	}
	if err := rows.Err(); err != nil {
		return nil, apperror.StoreCorrupted(err)
	}
	return collector.hits(), nil
}

// topK keeps the K best candidates in a slice kept sorted by (similarity desc,
// symbolID asc), so memory stays O(K). Insertion is O(K) per candidate.
type topK struct {
	limit      int
	candidates []denseCandidate
}

type denseCandidate struct {
	symbolID     string
	occurrenceID string
	similarity   float64
}

func newTopK(limit int) *topK {
	return &topK{limit: limit, candidates: make([]denseCandidate, 0, limit)}
}

func better(a, b denseCandidate) bool {
	if a.similarity != b.similarity {
		return a.similarity > b.similarity
	}
	return a.symbolID < b.symbolID
}

func (t *topK) consider(symbolID, occurrenceID string, similarity float64) {
	cand := denseCandidate{symbolID: symbolID, occurrenceID: occurrenceID, similarity: similarity}
	if len(t.candidates) >= t.limit && !better(cand, t.candidates[len(t.candidates)-1]) {
		return // worse than the current worst and the set is full
	}
	index := sort.Search(len(t.candidates), func(i int) bool { return better(cand, t.candidates[i]) })
	t.candidates = append(t.candidates, denseCandidate{})
	copy(t.candidates[index+1:], t.candidates[index:])
	t.candidates[index] = cand
	if len(t.candidates) > t.limit {
		t.candidates = t.candidates[:t.limit]
	}
}

func (t *topK) hits() []DenseHit {
	out := make([]DenseHit, 0, len(t.candidates))
	for i, cand := range t.candidates {
		out = append(out, DenseHit{
			SymbolID:     domain.SymbolID(cand.symbolID),
			OccurrenceID: domain.OccurrenceID(cand.occurrenceID),
			Similarity:   cand.similarity,
			Rank:         i + 1,
		})
	}
	return out
}

// Embeddings is a lazy, legacy full-copy accessor kept for the ReadView contract;
// the SQLite retrieval path uses SearchDense (streaming) instead of copying the
// whole index. Errors yield nil to satisfy the infallible interface.
func (v *readView) Embeddings() map[string][]float64 {
	if v.closed {
		return nil
	}
	rows, err := v.reader.QueryContext(v.searchCtx, "SELECT symbol_id, dimension, vector FROM embeddings")
	if err != nil {
		return nil
	}
	defer rows.Close()
	out := map[string][]float64{}
	for rows.Next() {
		var symbolID string
		var dimension int
		var blob []byte
		if err := rows.Scan(&symbolID, &dimension, &blob); err != nil {
			return nil
		}
		vector, err := decodeVector(blob, dimension)
		if err != nil {
			return nil
		}
		out[symbolID] = vector
	}
	return out
}

// EmbeddingSnapshot is a fully generated, validated set of vectors plus its index
// metadata, produced by the prepare phase OUTSIDE any write transaction.
type EmbeddingSnapshot struct {
	Provider        string
	Model           string
	TemplateVersion string
	Dimension       int
	Vectors         map[string][]float64 // SymbolID → vector
}

// RebuildEmbeddings replaces the entire embedding index in one short write
// transaction, after the caller generated and validated all vectors outside the
// transaction. It verifies the structural revision is still the one the vectors
// were generated against (else SNAPSHOT_STALE), validates every vector before any
// SQL, replaces the table and the metadata, and marks the index enabled. The
// structural revision is NOT bumped (embeddings are a derived index).
func (s *Store) RebuildEmbeddings(ctx context.Context, expectedRevision domain.Revision, snapshot EmbeddingSnapshot) error {
	if snapshot.Dimension <= 0 {
		return apperror.InvalidArgument("embedding snapshot dimension must be positive", nil)
	}
	// Validate every vector before opening the transaction.
	encoded := make(map[string][]byte, len(snapshot.Vectors))
	for _, symbolID := range sortedKeys(snapshot.Vectors) {
		vector := snapshot.Vectors[symbolID]
		if len(vector) != snapshot.Dimension {
			return apperror.InvalidArgument("embedding vector dimension mismatch", nil)
		}
		blob, err := encodeVector(vector)
		if err != nil {
			return apperror.InvalidArgument("invalid embedding vector: "+err.Error(), nil)
		}
		encoded[symbolID] = blob
	}

	tx, err := s.db.Writer().BeginTx(ctx, nil)
	if err != nil {
		return apperror.PersistenceFailed(err)
	}
	defer tx.Rollback() //nolint:errcheck

	var currentRevision int64
	if err := tx.QueryRowContext(ctx, "SELECT revision FROM repository_state WHERE id = 1").Scan(&currentRevision); err != nil {
		return apperror.StoreCorrupted(err)
	}
	if domain.Revision(currentRevision) != expectedRevision {
		return apperror.StoreVersionConflict()
	}

	if err := txExec(ctx, tx, "DELETE FROM embeddings"); err != nil {
		return apperror.PersistenceFailed(err)
	}
	primaryOccurrences := map[string]string{}
	if len(snapshot.Vectors) > 0 {
		primaryOccurrences, err = loadPrimaryOccurrences(ctx, tx)
		if err != nil {
			return apperror.StoreCorrupted(err)
		}
	}
	stored := 0
	for _, symbolID := range sortedKeys(snapshot.Vectors) {
		blob := encoded[symbolID]
		occurrenceID, exists := primaryOccurrences[symbolID]
		if !exists {
			continue // skip a vector for a symbol that is not in this snapshot
		}
		sum := contentHash(blob)
		if err := txExec(ctx, tx, "INSERT INTO embeddings(symbol_id, occurrence_id, content_hash, dimension, vector) VALUES(?,?,?,?,?)",
			symbolID, nullIfEmpty(occurrenceID), sum, snapshot.Dimension, blob); err != nil {
			return apperror.PersistenceFailed(err)
		}
		stored++
	}
	if err := txExec(ctx, tx, `INSERT INTO embedding_index_metadata
		(id, enabled, provider, model, dimension, template_version, distance, encoding, built_at)
		VALUES(1, ?, ?, ?, ?, ?, 'cosine', ?, ?)
		ON CONFLICT(id) DO UPDATE SET enabled=excluded.enabled, provider=excluded.provider, model=excluded.model,
			dimension=excluded.dimension, template_version=excluded.template_version, encoding=excluded.encoding, built_at=excluded.built_at`,
		boolToInt(stored > 0), snapshot.Provider, snapshot.Model, snapshot.Dimension, snapshot.TemplateVersion,
		VectorEncodingVersion, time.Now().UTC().Format(time.RFC3339Nano)); err != nil {
		return apperror.PersistenceFailed(err)
	}
	if err := tx.Commit(); err != nil {
		return apperror.PersistenceFailed(err)
	}
	return nil
}

// EmbeddingMetadata returns the persisted dense-index singleton. A database with
// no metadata row is treated as disabled/empty rather than corrupted.
func (s *Store) EmbeddingMetadata(ctx context.Context) (domain.EmbeddingIndexMetadata, error) {
	var (
		enabled                                             int
		provider, model, templateVersion, distance, builtAt string
		dimension                                           int
	)
	err := s.db.Reader().QueryRowContext(ctx, `SELECT enabled, provider, model, dimension, template_version, distance, built_at
		FROM embedding_index_metadata WHERE id = 1`).Scan(&enabled, &provider, &model, &dimension, &templateVersion, &distance, &builtAt)
	if errors.Is(err, sql.ErrNoRows) {
		return domain.EmbeddingIndexMetadata{}, nil
	}
	if err != nil {
		return domain.EmbeddingIndexMetadata{}, apperror.StoreCorrupted(err)
	}
	parsedBuiltAt, _ := time.Parse(time.RFC3339Nano, builtAt)
	return domain.EmbeddingIndexMetadata{
		Enabled: enabled == 1, Provider: provider, Model: model, Dimension: dimension,
		TemplateVersion: templateVersion, Distance: distance, BuiltAt: parsedBuiltAt,
	}, nil
}

// EmbeddingCount returns the number of persisted vectors.
func (s *Store) EmbeddingCount(ctx context.Context) (int, error) {
	var count int
	if err := s.db.Reader().QueryRowContext(ctx, "SELECT COUNT(*) FROM embeddings").Scan(&count); err != nil {
		return 0, apperror.StoreCorrupted(err)
	}
	return count, nil
}

func boolToInt(b bool) int {
	if b {
		return 1
	}
	return 0
}
