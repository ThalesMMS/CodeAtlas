package spike

import (
	"context"
	"database/sql"
	"fmt"
	"path/filepath"
	"strings"
	"sync"
	"testing"
)

func openWithSchema(t testing.TB, driver Driver) *sql.DB {
	t.Helper()
	path := filepath.Join(t.TempDir(), "spike.db")
	db, err := Open(driver, path)
	if err != nil {
		t.Fatalf("[%s] Open: %v", driver.Key, err)
	}
	t.Cleanup(func() { _ = db.Close() })
	if _, err := db.Exec(Schema); err != nil {
		t.Fatalf("[%s] schema: %v", driver.Key, err)
	}
	return db
}

// TestFunctionalVerification runs the issue's mandatory proofs against EACH driver:
// the pragmas must actually take effect, FTS5 must really work, transactions must
// isolate and roll back, busy must be classifiable, and checkpoint must be safe.
func TestFunctionalVerification(t *testing.T) {
	for _, driver := range Drivers {
		driver := driver
		t.Run(driver.Key, func(t *testing.T) {
			db := openWithSchema(t, driver)

			fk, journal, err := EffectivePragmas(db)
			if err != nil {
				t.Fatalf("read pragmas: %v", err)
			}
			if !fk {
				t.Error("foreign_keys not effective on the connection")
			}
			if !strings.EqualFold(journal, "wal") {
				t.Errorf("journal_mode = %q, want WAL", journal)
			}

			t.Run("ForeignKeysEnforced", func(t *testing.T) { proveForeignKeys(t, db) })
			t.Run("FTS5Works", func(t *testing.T) { proveFTS5(t, db) })
			t.Run("RollbackRemovesAll", func(t *testing.T) { proveRollback(t, db) })
			t.Run("SnapshotIsolation", func(t *testing.T) { proveSnapshotIsolation(t, db) })
			t.Run("BusyClassifiable", func(t *testing.T) { proveBusyClassifiable(t, driver) })
			t.Run("CheckpointSafe", func(t *testing.T) { proveCheckpoint(t, db) })
		})
	}
}

func seedOneFile(t testing.TB, db *sql.DB) int64 {
	t.Helper()
	// Unique per (sub)test so prove* helpers sharing one db do not collide on path.
	path := "seed/" + strings.ReplaceAll(t.Name(), "/", "_") + ".go"
	result, err := db.Exec("INSERT INTO files(path,lang,hash,size) VALUES(?,?,?,?)", path, "go", "deadbeef", 100)
	if err != nil {
		t.Fatalf("seed file: %v", err)
	}
	id, _ := result.LastInsertId()
	return id
}

func proveForeignKeys(t *testing.T, db *sql.DB) {
	// An occurrence pointing at a non-existent file_id must be rejected.
	_, err := db.Exec("INSERT INTO occurrences(symbol_id,file_id,start_line,start_col,end_line,end_col,role) VALUES('sym:x',999999,1,1,1,2,'definition')")
	if err == nil {
		t.Fatal("expected a foreign key violation, got nil")
	}
	if !strings.Contains(strings.ToLower(err.Error()), "foreign") {
		t.Fatalf("error is not a classifiable FK error: %v", err)
	}
}

func proveFTS5(t *testing.T, db *sql.DB) {
	fileID := seedOneFile(t, db)
	if _, err := db.Exec("INSERT INTO identities(symbol_id,name,kind,file_id) VALUES('sym:Pay','PayService','type',?)", fileID); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec("INSERT INTO fts_documents(symbol_id,name,qualified,doc) VALUES('sym:Pay','PayService','a/b.go.PayService','handles payment settlement')"); err != nil {
		t.Fatalf("fts insert: %v", err)
	}
	var symbolID string
	var rank float64
	err := db.QueryRow("SELECT symbol_id, rank FROM fts_documents WHERE fts_documents MATCH ? ORDER BY rank LIMIT 1", "payment").Scan(&symbolID, &rank)
	if err != nil {
		t.Fatalf("FTS5 MATCH query failed (FTS5 not really functional): %v", err)
	}
	if symbolID != "sym:Pay" {
		t.Fatalf("FTS5 returned %q, want sym:Pay", symbolID)
	}
}

func proveRollback(t *testing.T, db *sql.DB) {
	seedOneFile(t, db)
	var before int
	db.QueryRow("SELECT count(*) FROM files").Scan(&before)
	tx, err := db.Begin()
	if err != nil {
		t.Fatal(err)
	}
	if _, err := tx.Exec("INSERT INTO files(path,lang,hash,size) VALUES('rollback/me.go','go','x',1)"); err != nil {
		t.Fatal(err)
	}
	if err := tx.Rollback(); err != nil {
		t.Fatal(err)
	}
	var after int
	db.QueryRow("SELECT count(*) FROM files").Scan(&after)
	if after != before {
		t.Fatalf("rollback did not remove the insert: before=%d after=%d", before, after)
	}
}

func proveSnapshotIsolation(t *testing.T, db *sql.DB) {
	ctx := context.Background()
	seedOneFile(t, db)
	var baseline int
	db.QueryRow("SELECT count(*) FROM files").Scan(&baseline)

	writer, err := db.Conn(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer writer.Close()
	reader, err := db.Conn(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer reader.Close()

	if _, err := writer.ExecContext(ctx, "BEGIN IMMEDIATE"); err != nil {
		t.Fatal(err)
	}
	if _, err := writer.ExecContext(ctx, "INSERT INTO files(path,lang,hash,size) VALUES('snap/new.go','go','y',1)"); err != nil {
		t.Fatal(err)
	}
	// The reader, on a different connection, must still see the pre-write snapshot.
	var seen int
	if err := reader.QueryRowContext(ctx, "SELECT count(*) FROM files").Scan(&seen); err != nil {
		t.Fatal(err)
	}
	if seen != baseline {
		t.Fatalf("WAL snapshot leaked an uncommitted write: baseline=%d reader=%d", baseline, seen)
	}
	if _, err := writer.ExecContext(ctx, "COMMIT"); err != nil {
		t.Fatal(err)
	}
	var afterCommit int
	reader.QueryRowContext(ctx, "SELECT count(*) FROM files").Scan(&afterCommit)
	if afterCommit != baseline+1 {
		t.Fatalf("after commit reader sees %d, want %d", afterCommit, baseline+1)
	}
}

func proveBusyClassifiable(t *testing.T, driver Driver) {
	// With a tiny busy_timeout, a second IMMEDIATE write while one is held must
	// surface a classifiable busy/locked error rather than hang.
	path := filepath.Join(t.TempDir(), "busy.db")
	db, err := Open(driver, path)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	if _, err := db.Exec(Schema); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec("PRAGMA busy_timeout = 1"); err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	holder, _ := db.Conn(ctx)
	defer holder.Close()
	other, _ := db.Conn(ctx)
	defer other.Close()
	if _, err := holder.ExecContext(ctx, "PRAGMA busy_timeout = 1"); err != nil {
		t.Fatal(err)
	}
	if _, err := other.ExecContext(ctx, "PRAGMA busy_timeout = 1"); err != nil {
		t.Fatal(err)
	}
	if _, err := holder.ExecContext(ctx, "BEGIN IMMEDIATE"); err != nil {
		t.Fatal(err)
	}
	defer holder.ExecContext(ctx, "ROLLBACK")
	_, err = other.ExecContext(ctx, "BEGIN IMMEDIATE")
	if err == nil {
		t.Fatal("expected a busy/locked error from the contending writer")
	}
	lower := strings.ToLower(err.Error())
	if !strings.Contains(lower, "busy") && !strings.Contains(lower, "locked") {
		t.Fatalf("busy error is not classifiable: %v", err)
	}
}

func proveCheckpoint(t *testing.T, db *sql.DB) {
	seedOneFile(t, db)
	var busy, log, checkpointed int
	if err := db.QueryRow("PRAGMA wal_checkpoint(TRUNCATE)").Scan(&busy, &log, &checkpointed); err != nil {
		t.Fatalf("checkpoint failed: %v", err)
	}
	// The DB must remain readable after the checkpoint.
	var count int
	if err := db.QueryRow("SELECT count(*) FROM files").Scan(&count); err != nil {
		t.Fatalf("DB unreadable after checkpoint: %v", err)
	}
}

// --- Benchmarks (core read/write/search operations, small profile) ---

func benchDriver(b *testing.B, fn func(b *testing.B, db *sql.DB, dataset Dataset)) {
	for _, driver := range Drivers {
		dataset := Generate(ProfileSmall, 42)
		b.Run(driver.Key, func(b *testing.B) {
			db := openWithSchema(b, driver)
			if err := Load(db, dataset); err != nil {
				b.Fatalf("load: %v", err)
			}
			b.ResetTimer()
			fn(b, db, dataset)
		})
	}
}

func BenchmarkLoadFull(b *testing.B) {
	for _, driver := range Drivers {
		dataset := Generate(ProfileSmall, 42)
		b.Run(driver.Key, func(b *testing.B) {
			b.ReportAllocs()
			for i := 0; i < b.N; i++ {
				b.StopTimer()
				db := openWithSchema(b, driver)
				b.StartTimer()
				if err := Load(db, dataset); err != nil {
					b.Fatal(err)
				}
				b.StopTimer()
				_ = db.Close()
				b.StartTimer()
			}
		})
	}
}

func BenchmarkLookupByPath(b *testing.B) {
	benchDriver(b, func(b *testing.B, db *sql.DB, dataset Dataset) {
		b.ReportAllocs()
		for i := 0; i < b.N; i++ {
			path := dataset.Files[i%len(dataset.Files)].Path
			var id int64
			if err := db.QueryRow("SELECT id FROM files WHERE path = ?", path).Scan(&id); err != nil {
				b.Fatal(err)
			}
		}
	})
}

func BenchmarkGraphDepth1(b *testing.B) {
	benchDriver(b, func(b *testing.B, db *sql.DB, dataset Dataset) {
		b.ReportAllocs()
		for i := 0; i < b.N; i++ {
			src := dataset.Symbols[i%len(dataset.Symbols)].SymbolID
			rows, err := db.Query("SELECT dst, kind, confidence FROM edges WHERE src = ?", src)
			if err != nil {
				b.Fatal(err)
			}
			for rows.Next() {
				var dst, kind string
				var confidence float64
				_ = rows.Scan(&dst, &kind, &confidence)
			}
			rows.Close()
		}
	})
}

func BenchmarkFTSSearch(b *testing.B) {
	benchDriver(b, func(b *testing.B, db *sql.DB, dataset Dataset) {
		b.ReportAllocs()
		terms := []string{"User", "Order", "Service", "handles", "module"}
		for i := 0; i < b.N; i++ {
			rows, err := db.Query("SELECT symbol_id FROM fts_documents WHERE fts_documents MATCH ? ORDER BY rank LIMIT 10", terms[i%len(terms)])
			if err != nil {
				b.Fatal(err)
			}
			for rows.Next() {
				var id string
				_ = rows.Scan(&id)
			}
			rows.Close()
		}
	})
}

// BenchmarkConcurrentReadWrite measures a reader pool against a single writer under WAL.
func BenchmarkConcurrentReadWrite(b *testing.B) {
	benchDriver(b, func(b *testing.B, db *sql.DB, dataset Dataset) {
		var wg sync.WaitGroup
		stop := make(chan struct{})
		wg.Add(1)
		go func() {
			defer wg.Done()
			counter := 0
			for {
				select {
				case <-stop:
					return
				default:
					counter++
					_, _ = db.Exec("INSERT INTO edges(kind,src,dst,provenance,confidence) VALUES('calls',?,?,'ast',0.9)",
						fmt.Sprintf("w:%d", counter), "w:dst")
				}
			}
		}()
		b.ResetTimer()
		for i := 0; i < b.N; i++ {
			var count int
			if err := db.QueryRow("SELECT count(*) FROM identities WHERE name = ?", dataset.Symbols[i%len(dataset.Symbols)].Name).Scan(&count); err != nil {
				b.Fatal(err)
			}
		}
		close(stop)
		wg.Wait()
	})
}
