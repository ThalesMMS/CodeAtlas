package sqlite

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"fmt"
	"hash"
	"io"
	"sort"

	"github.com/ThalesMMS/CodeAtlas/internal/domain"
)

// Schema 3 replaces full-row streaming on every commit with an incremental,
// deterministic bucketed Merkle index. Derived FTS/embeddings remain excluded.
const snapshotSchema = 3

// rowQueryer is satisfied by both *sql.DB and *sql.Tx.
type rowQueryer interface {
	QueryContext(ctx context.Context, query string, args ...any) (*sql.Rows, error)
}

type snapshotSection struct {
	tag      string
	queryAll string
	queryOne string
}

var snapshotSections = []snapshotSection{
	{
		tag:      "files",
		queryAll: "SELECT path, path, language, content_hash, size FROM files ORDER BY path",
		queryOne: "SELECT path, language, content_hash, size FROM files WHERE path = ?",
	},
	{
		tag:      "identities",
		queryAll: "SELECT symbol_id, symbol_id, language, kind, name, qualified_name, COALESCE(parent_symbol_id,''), signature_fingerprint FROM symbol_identities ORDER BY symbol_id",
		queryOne: "SELECT symbol_id, language, kind, name, qualified_name, COALESCE(parent_symbol_id,''), signature_fingerprint FROM symbol_identities WHERE symbol_id = ?",
	},
	{
		tag:      "occurrences",
		queryAll: "SELECT occurrence_id, occurrence_id, symbol_id, file_path, start_line, start_col, end_line, end_col, signature, code, doc_comment, summary, body_hash, file_hash FROM symbol_occurrences ORDER BY occurrence_id",
		queryOne: "SELECT occurrence_id, symbol_id, file_path, start_line, start_col, end_line, end_col, signature, code, doc_comment, summary, body_hash, file_hash FROM symbol_occurrences WHERE occurrence_id = ?",
	},
	{
		tag:      "relations",
		queryAll: "SELECT canonical_key, kind, subject_symbol_id, COALESCE(object_symbol_id,''), COALESCE(external_key,''), direction, resolution, ranking_confidence FROM relations ORDER BY canonical_key",
		queryOne: "SELECT kind, subject_symbol_id, COALESCE(object_symbol_id,''), COALESCE(external_key,''), direction, resolution, ranking_confidence FROM relations WHERE canonical_key = ?",
	},
}

// computeSnapshotID is the full rebuild boundary used for a fresh database or a
// snapshot-schema upgrade. Normal commits call refreshSnapshotID instead.
func computeSnapshotID(ctx context.Context, q rowQueryer) (domain.SnapshotID, error) {
	if tx, ok := q.(*sql.Tx); ok {
		return rebuildSnapshotIndex(ctx, tx)
	}
	db, ok := q.(*sql.DB)
	if !ok {
		return "", fmt.Errorf("snapshot index rebuild requires a database or transaction")
	}
	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		return "", err
	}
	defer tx.Rollback() //nolint:errcheck
	result, err := rebuildSnapshotIndex(ctx, tx)
	if err != nil {
		return "", err
	}
	if err := tx.Commit(); err != nil {
		return "", err
	}
	return result, nil
}

func rebuildSnapshotIndex(ctx context.Context, tx *sql.Tx) (domain.SnapshotID, error) {
	if _, err := tx.ExecContext(ctx, "DELETE FROM snapshot_leaves"); err != nil {
		return "", err
	}
	if _, err := tx.ExecContext(ctx, "DELETE FROM snapshot_buckets"); err != nil {
		return "", err
	}
	buckets := map[int]struct{}{}
	for _, section := range snapshotSections {
		rows, err := tx.QueryContext(ctx, section.queryAll)
		if err != nil {
			return "", err
		}
		for rows.Next() {
			values, err := scanRawValues(rows)
			if err != nil {
				rows.Close()
				return "", err
			}
			key := string(values[0])
			bucket, err := upsertSnapshotLeaf(ctx, tx, section.tag, key, values[1:])
			if err != nil {
				rows.Close()
				return "", err
			}
			buckets[bucket] = struct{}{}
		}
		if err := rows.Close(); err != nil {
			return "", err
		}
	}
	if err := recomputeSnapshotBuckets(ctx, tx, buckets); err != nil {
		return "", err
	}
	return snapshotRoot(ctx, tx)
}

func refreshSnapshotID(ctx context.Context, tx *sql.Tx, impact commitImpact) (domain.SnapshotID, error) {
	keys := map[string]map[string]struct{}{
		"files": impact.paths, "identities": impact.symbolIDs,
		"occurrences": impact.occurrenceIDs, "relations": impact.relationKeys,
	}
	buckets := map[int]struct{}{}
	for _, section := range snapshotSections {
		for key := range keys[section.tag] {
			bucket := snapshotBucket(section.tag, key)
			buckets[bucket] = struct{}{}
			values, found, err := querySnapshotLeaf(ctx, tx, section, key)
			if err != nil {
				return "", err
			}
			if !found {
				if _, err := tx.ExecContext(ctx, "DELETE FROM snapshot_leaves WHERE section = ? AND entity_key = ?", section.tag, key); err != nil {
					return "", err
				}
				continue
			}
			if _, err := upsertSnapshotLeaf(ctx, tx, section.tag, key, values); err != nil {
				return "", err
			}
		}
	}
	if err := recomputeSnapshotBuckets(ctx, tx, buckets); err != nil {
		return "", err
	}
	return snapshotRoot(ctx, tx)
}

func querySnapshotLeaf(ctx context.Context, tx *sql.Tx, section snapshotSection, key string) ([][]byte, bool, error) {
	rows, err := tx.QueryContext(ctx, section.queryOne, key)
	if err != nil {
		return nil, false, err
	}
	defer rows.Close()
	if !rows.Next() {
		return nil, false, rows.Err()
	}
	values, err := scanRawValues(rows)
	return values, true, err
}

func scanRawValues(rows *sql.Rows) ([][]byte, error) {
	columns, err := rows.Columns()
	if err != nil {
		return nil, err
	}
	holders := make([]any, len(columns))
	for i := range holders {
		holders[i] = new(sql.RawBytes)
	}
	if err := rows.Scan(holders...); err != nil {
		return nil, err
	}
	values := make([][]byte, len(holders))
	for i, holder := range holders {
		values[i] = append([]byte(nil), []byte(*(holder.(*sql.RawBytes)))...)
	}
	return values, nil
}

func upsertSnapshotLeaf(ctx context.Context, tx *sql.Tx, section, key string, values [][]byte) (int, error) {
	digest := sha256.New()
	writeField(digest, section)
	writeField(digest, key)
	for _, value := range values {
		writeBytes(digest, value)
	}
	bucket := snapshotBucket(section, key)
	_, err := tx.ExecContext(ctx, `INSERT INTO snapshot_leaves(section, entity_key, bucket, digest)
		VALUES(?,?,?,?) ON CONFLICT(section, entity_key) DO UPDATE SET bucket=excluded.bucket, digest=excluded.digest`,
		section, key, bucket, hex.EncodeToString(digest.Sum(nil)))
	return bucket, err
}

func snapshotBucket(section, key string) int {
	sum := sha256.Sum256([]byte(section + "\x00" + key))
	return int(sum[0])
}

func recomputeSnapshotBuckets(ctx context.Context, tx *sql.Tx, buckets map[int]struct{}) error {
	ordered := make([]int, 0, len(buckets))
	for bucket := range buckets {
		ordered = append(ordered, bucket)
	}
	sort.Ints(ordered)
	for _, bucket := range ordered {
		rows, err := tx.QueryContext(ctx, "SELECT section, entity_key, digest FROM snapshot_leaves WHERE bucket = ? ORDER BY section, entity_key", bucket)
		if err != nil {
			return err
		}
		digest := sha256.New()
		count := 0
		for rows.Next() {
			var section, key, leaf string
			if err := rows.Scan(&section, &key, &leaf); err != nil {
				rows.Close()
				return err
			}
			writeField(digest, section)
			writeField(digest, key)
			writeField(digest, leaf)
			count++
		}
		if err := rows.Close(); err != nil {
			return err
		}
		if count == 0 {
			if _, err := tx.ExecContext(ctx, "DELETE FROM snapshot_buckets WHERE bucket = ?", bucket); err != nil {
				return err
			}
			continue
		}
		if _, err := tx.ExecContext(ctx, `INSERT INTO snapshot_buckets(bucket, digest) VALUES(?,?)
			ON CONFLICT(bucket) DO UPDATE SET digest=excluded.digest`, bucket, hex.EncodeToString(digest.Sum(nil))); err != nil {
			return err
		}
	}
	return nil
}

func snapshotRoot(ctx context.Context, tx *sql.Tx) (domain.SnapshotID, error) {
	digest := sha256.New()
	writeField(digest, fmt.Sprintf("schema=%d", snapshotSchema))
	rows, err := tx.QueryContext(ctx, "SELECT bucket, digest FROM snapshot_buckets ORDER BY bucket")
	if err != nil {
		return "", err
	}
	defer rows.Close()
	for rows.Next() {
		var bucket int
		var value string
		if err := rows.Scan(&bucket, &value); err != nil {
			return "", err
		}
		writeField(digest, fmt.Sprintf("bucket=%d", bucket))
		writeField(digest, value)
	}
	if err := rows.Err(); err != nil {
		return "", err
	}
	return domain.SnapshotID("sha256:" + hex.EncodeToString(digest.Sum(nil))), nil
}

func writeField(digest hash.Hash, value string) {
	writeBytes(digest, []byte(value))
	_, _ = io.WriteString(digest, "\x1e")
}

func writeBytes(digest hash.Hash, value []byte) {
	var lengthPrefix [8]byte
	n := uint64(len(value))
	for i := 0; i < 8; i++ {
		lengthPrefix[i] = byte(n >> (8 * i))
	}
	digest.Write(lengthPrefix[:])
	digest.Write(value)
	_, _ = io.WriteString(digest, "\x1f")
}
