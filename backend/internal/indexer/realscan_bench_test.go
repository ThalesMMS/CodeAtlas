package indexer

import (
	"context"
	"os"
	"testing"
	"time"

	"github.com/ThalesMMS/CodeAtlas/internal/ai"
	codeparser "github.com/ThalesMMS/CodeAtlas/internal/parser"
	"github.com/ThalesMMS/CodeAtlas/internal/repository"
	"github.com/ThalesMMS/CodeAtlas/internal/retrieval"
)

// TestRealWorkspaceScan is an opt-in measurement of a full initial scan into a
// fresh SQLite store. Set CODEATLAS_BENCH_WORKSPACE to a repository root; the
// test reports phase timing and never runs in the default suite.
func TestRealWorkspaceScan(t *testing.T) {
	root := os.Getenv("CODEATLAS_BENCH_WORKSPACE")
	if root == "" {
		t.Skip("set CODEATLAS_BENCH_WORKSPACE to measure a real initial scan")
	}
	ctx := context.Background()
	store, err := repository.OpenSQLite(ctx, repository.SQLiteConfig{WorkspaceRoot: t.TempDir()})
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	retriever := retrieval.NewHybrid(store, ai.Disabled{}, false)
	idx := New(root, 1_500_000, codeparser.New(), store, retriever)
	start := time.Now()
	if err := idx.Scan(ctx); err != nil {
		t.Fatalf("Scan: %v", err)
	}
	files, symbols, edges, _, _ := store.Counts()
	t.Logf("scan of %s took %s: files=%d symbols=%d edges=%d", root, time.Since(start).Round(time.Millisecond), files, symbols, edges)
}
