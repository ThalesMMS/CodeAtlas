package sqlite

import (
	"context"
	"strings"
	"testing"
)

// The commit impact query runs once per touched path in every commit. A plan
// that scans the relations table turns a large initial commit into O(paths ×
// relations) work, which kept a 4k-file workspace on the bootstrap screen for
// hours. Every branch must be served by an index.
func TestImpactRelationsQueryNeverScansRelations(t *testing.T) {
	store, err := OpenStore(context.Background(), Config{WorkspaceRoot: t.TempDir()})
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()

	rows, err := store.db.Writer().QueryContext(context.Background(), "EXPLAIN QUERY PLAN "+impactRelationsQuery, "a.swift", "a.swift", "a.swift")
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	var plan []string
	for rows.Next() {
		var id, parent, notUsed int
		var detail string
		if err := rows.Scan(&id, &parent, &notUsed, &detail); err != nil {
			t.Fatal(err)
		}
		plan = append(plan, detail)
	}
	if err := rows.Err(); err != nil {
		t.Fatal(err)
	}
	if len(plan) == 0 {
		t.Fatal("empty query plan")
	}
	for _, step := range plan {
		if strings.HasPrefix(step, "SCAN") {
			t.Fatalf("impact query performs a table scan: %q\nfull plan:\n%s", step, strings.Join(plan, "\n"))
		}
	}
}
