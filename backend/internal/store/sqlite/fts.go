package sqlite

import (
	"context"
	"database/sql"
	"sort"
	"strings"
	"time"

	"github.com/ThalesMMS/CodeAtlas/internal/apperror"
	"github.com/ThalesMMS/CodeAtlas/internal/domain"
	"github.com/ThalesMMS/CodeAtlas/internal/lexical"
	"github.com/ThalesMMS/CodeAtlas/internal/storederive"
)

// bm25 column weights (issue #54 policy v1): name highest, qualified second,
// kind/path/signature intermediate, summary/code lowest. The first weight is for
// the UNINDEXED symbol_id column and is ignored by FTS5.
const bm25Weights = "0.0, 8.0, 4.0, 2.0, 2.0, 2.0, 1.0, 1.0"

// Additive boosts mirror the in-memory ranking contract so a name match still wins.
const (
	boostNameExact      = 8.0
	boostNameContains   = 3.0
	boostQualifiedMatch = 1.5
)

// RebuildFTS replaces the complete derived lexical index from the current
// structural snapshot in one transaction. It does not bump the repository
// revision because FTS rows are derived and excluded from SnapshotID.
func (s *Store) RebuildFTS(ctx context.Context) error {
	tx, err := s.db.Writer().BeginTx(ctx, nil)
	if err != nil {
		return apperror.PersistenceFailed(err)
	}
	defer tx.Rollback() //nolint:errcheck
	if err := rebuildFTS(ctx, tx); err != nil {
		return apperror.PersistenceFailed(err)
	}
	if err := tx.Commit(); err != nil {
		return apperror.PersistenceFailed(err)
	}
	return nil
}

// rebuildFTS rebuilds the entire FTS index from the current identities and their
// primary (first by path/line/column) occurrence, in the same write transaction.
// A full rebuild per commit mirrors the in-memory store (which rebuilds its lexical
// index each commit), keeps maintenance explicit (no triggers), and guarantees no
// orphan FTS rows. The FTS table is derived and excluded from the SnapshotID.
func rebuildFTS(ctx context.Context, tx *sql.Tx) error {
	if _, err := tx.ExecContext(ctx, "DELETE FROM fts_symbols"); err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx, "DELETE FROM fts_symbol_rows"); err != nil {
		return err
	}
	rows, err := tx.QueryContext(ctx, `SELECT i.symbol_id, i.name, i.qualified_name, i.kind,
		o.file_path, o.signature, o.doc_comment, o.summary, o.code
		FROM symbol_identities i
		JOIN symbol_occurrences o ON o.symbol_id = i.symbol_id
		ORDER BY i.symbol_id, o.file_path, o.start_line, o.start_col`)
	if err != nil {
		return err
	}
	defer rows.Close()

	insert, err := tx.PrepareContext(ctx, `INSERT INTO fts_symbols
		(symbol_id, name_tokens, qualified_name_tokens, kind_tokens, path_tokens, signature_tokens, summary_tokens, code_tokens)
		VALUES(?,?,?,?,?,?,?,?)`)
	if err != nil {
		return err
	}
	defer insert.Close()

	seen := map[string]struct{}{}
	for rows.Next() {
		var symbolID string
		var symbol domain.Symbol
		if err := rows.Scan(&symbolID, &symbol.Name, &symbol.QualifiedName, &symbol.Kind,
			&symbol.Path, &symbol.Signature, &symbol.DocComment, &symbol.Summary, &symbol.Code); err != nil {
			return err
		}
		if _, ok := seen[symbolID]; ok {
			continue // keep only the primary occurrence
		}
		seen[symbolID] = struct{}{}
		doc := lexical.BuildDocument(symbol)
		result, err := insert.ExecContext(ctx, symbolID,
			doc.NameColumn(), doc.QualifiedColumn(), doc.KindColumn(), doc.PathColumn(),
			doc.SignatureColumn(), doc.SummaryColumn(), doc.CodeColumn())
		if err != nil {
			return err
		}
		rowID, err := result.LastInsertId()
		if err != nil {
			return err
		}
		if _, err := tx.ExecContext(ctx, "INSERT INTO fts_symbol_rows(symbol_id, fts_rowid) VALUES(?,?)", symbolID, rowID); err != nil {
			return err
		}
	}
	if err := rows.Err(); err != nil {
		return err
	}
	_, err = tx.ExecContext(ctx, `INSERT INTO fts_metadata(id, tokenizer_version, built_at) VALUES(1, ?, ?)
		ON CONFLICT(id) DO UPDATE SET tokenizer_version=excluded.tokenizer_version, built_at=excluded.built_at`,
		lexical.Version, time.Now().UTC().Format(time.RFC3339Nano))
	return err
}

// refreshFTS replaces only rows whose identities or primary occurrences may
// have changed. A tokenizer-version mismatch remains the explicit full-rebuild
// boundary.
func refreshFTS(ctx context.Context, tx *sql.Tx, symbolIDs map[string]struct{}) error {
	var version string
	err := tx.QueryRowContext(ctx, "SELECT tokenizer_version FROM fts_metadata WHERE id = 1").Scan(&version)
	if err == sql.ErrNoRows || (err == nil && version != lexical.Version) {
		return rebuildFTS(ctx, tx)
	}
	if err != nil {
		return err
	}
	for symbolID := range symbolIDs {
		var rowID int64
		err := tx.QueryRowContext(ctx, "SELECT fts_rowid FROM fts_symbol_rows WHERE symbol_id = ?", symbolID).Scan(&rowID)
		if err != nil && err != sql.ErrNoRows {
			return err
		}
		if err == nil {
			if _, err := tx.ExecContext(ctx, "DELETE FROM fts_symbols WHERE rowid = ?", rowID); err != nil {
				return err
			}
			if _, err := tx.ExecContext(ctx, "DELETE FROM fts_symbol_rows WHERE symbol_id = ?", symbolID); err != nil {
				return err
			}
		}
		var symbol domain.Symbol
		err = tx.QueryRowContext(ctx, `SELECT i.name, i.qualified_name, i.kind,
			o.file_path, o.signature, o.doc_comment, o.summary, o.code
			FROM symbol_identities i
			JOIN symbol_occurrences o ON o.symbol_id = i.symbol_id
			WHERE i.symbol_id = ?
			ORDER BY o.file_path, o.start_line, o.start_col LIMIT 1`, symbolID).Scan(
			&symbol.Name, &symbol.QualifiedName, &symbol.Kind, &symbol.Path,
			&symbol.Signature, &symbol.DocComment, &symbol.Summary, &symbol.Code)
		if err == sql.ErrNoRows {
			continue
		}
		if err != nil {
			return err
		}
		doc := lexical.BuildDocument(symbol)
		result, err := tx.ExecContext(ctx, `INSERT INTO fts_symbols
			(symbol_id, name_tokens, qualified_name_tokens, kind_tokens, path_tokens, signature_tokens, summary_tokens, code_tokens)
			VALUES(?,?,?,?,?,?,?,?)`, symbolID, doc.NameColumn(), doc.QualifiedColumn(), doc.KindColumn(), doc.PathColumn(),
			doc.SignatureColumn(), doc.SummaryColumn(), doc.CodeColumn())
		if err != nil {
			return err
		}
		rowID, err = result.LastInsertId()
		if err != nil {
			return err
		}
		if _, err := tx.ExecContext(ctx, "INSERT INTO fts_symbol_rows(symbol_id, fts_rowid) VALUES(?,?)", symbolID, rowID); err != nil {
			return err
		}
	}
	_, err = tx.ExecContext(ctx, "UPDATE fts_metadata SET built_at = ? WHERE id = 1", time.Now().UTC().Format(time.RFC3339Nano))
	return err
}

type ftsHit struct {
	symbolID string
	base     float64 // higher = better (negated bm25)
}

type ftsSymbolHit struct {
	symbol domain.Symbol
	base   float64
}

// Search queries FTS5 and hydrates only the bounded candidate symbols under one
// short read transaction. It avoids the full structural ReadView used by graph
// and ContextPack operations while preserving snapshot provenance in each hit.
func (s *Store) Search(ctx context.Context, query string, limit int) ([]domain.SearchHit, error) {
	if limit <= 0 {
		limit = 20
	}
	matchExpr := lexical.MatchExpression(query)
	if matchExpr == "" {
		return nil, nil
	}
	tx, err := s.db.Reader().BeginTx(ctx, &sql.TxOptions{ReadOnly: true})
	if err != nil {
		return nil, apperror.DatabaseOpenFailed(err)
	}
	defer tx.Rollback() //nolint:errcheck

	metadata, err := readMetadata(ctx, tx)
	if err != nil {
		return nil, err
	}
	candidates, err := queryFTSSymbols(ctx, tx, matchExpr, limit*4)
	if err != nil {
		return nil, apperror.StoreCorrupted(err)
	}
	return rankFTSSymbols(query, limit, metadata.ID, candidates), nil
}

// queryFTSRanked runs the bm25-weighted MATCH and returns candidates best-first.
func queryFTSRanked(ctx context.Context, reader *sql.DB, matchExpr string, limit int) ([]ftsHit, error) {
	query := "SELECT symbol_id, bm25(fts_symbols, " + bm25Weights + ") AS score FROM fts_symbols WHERE fts_symbols MATCH ? ORDER BY score LIMIT ?"
	rows, err := reader.QueryContext(ctx, query, matchExpr, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var hits []ftsHit
	for rows.Next() {
		var symbolID string
		var score float64
		if err := rows.Scan(&symbolID, &score); err != nil {
			return nil, err
		}
		hits = append(hits, ftsHit{symbolID: symbolID, base: -score}) // bm25: lower is better
	}
	return hits, rows.Err()
}

func queryFTSSymbols(ctx context.Context, q rowQueryer, matchExpr string, limit int) ([]ftsSymbolHit, error) {
	query := `SELECT fts_symbols.symbol_id, bm25(fts_symbols, ` + bm25Weights + `) AS score,
		i.language, i.kind, i.name, i.qualified_name,
		o.occurrence_id, o.file_path, o.start_line, o.start_col, o.end_line, o.end_col,
		o.signature, o.code, o.doc_comment, o.summary
		FROM fts_symbols
		JOIN symbol_identities i ON i.symbol_id = fts_symbols.symbol_id
		JOIN symbol_occurrences o ON o.occurrence_id = (
			SELECT primary_occurrence.occurrence_id
			FROM symbol_occurrences primary_occurrence
			WHERE primary_occurrence.symbol_id = fts_symbols.symbol_id
			ORDER BY primary_occurrence.file_path, primary_occurrence.start_line, primary_occurrence.start_col
			LIMIT 1
		)
		WHERE fts_symbols MATCH ?
		ORDER BY score, fts_symbols.symbol_id
		LIMIT ?`
	rows, err := q.QueryContext(ctx, query, matchExpr, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	candidates := make([]ftsSymbolHit, 0, limit)
	for rows.Next() {
		var candidate ftsSymbolHit
		var score float64
		if err := rows.Scan(
			&candidate.symbol.ID, &score,
			&candidate.symbol.Language, &candidate.symbol.Kind, &candidate.symbol.Name, &candidate.symbol.QualifiedName,
			&candidate.symbol.OccurrenceID, &candidate.symbol.Path,
			&candidate.symbol.Range.Start.Line, &candidate.symbol.Range.Start.Column,
			&candidate.symbol.Range.End.Line, &candidate.symbol.Range.End.Column,
			&candidate.symbol.Signature, &candidate.symbol.Code, &candidate.symbol.DocComment, &candidate.symbol.Summary,
		); err != nil {
			return nil, err
		}
		candidate.base = -score
		candidates = append(candidates, candidate)
	}
	return candidates, rows.Err()
}

func rankFTSSymbols(query string, limit int, snapshotID domain.SnapshotID, candidates []ftsSymbolHit) []domain.SearchHit {
	queryTokens := lexical.BuildQuery(query)
	queryLower := strings.ToLower(strings.TrimSpace(query))
	for i := range candidates {
		name := strings.ToLower(candidates[i].symbol.Name)
		qualified := strings.ToLower(candidates[i].symbol.QualifiedName)
		if name == queryLower {
			candidates[i].base += boostNameExact
		} else if strings.Contains(name, queryLower) {
			candidates[i].base += boostNameContains
		}
		if strings.Contains(qualified, queryLower) {
			candidates[i].base += boostQualifiedMatch
		}
	}
	sort.SliceStable(candidates, func(i, j int) bool {
		if candidates[i].base != candidates[j].base {
			return candidates[i].base > candidates[j].base
		}
		return candidates[i].symbol.ID < candidates[j].symbol.ID
	})
	if len(candidates) > limit {
		candidates = candidates[:limit]
	}
	out := make([]domain.SearchHit, 0, len(candidates))
	for _, candidate := range candidates {
		out = append(out, domain.SearchHit{
			Symbol: candidate.symbol, Snippet: storederive.Snippet(candidate.symbol, queryTokens),
			Score: candidate.base, Source: "fts5", SnapshotID: snapshotID,
		})
	}
	return out
}
