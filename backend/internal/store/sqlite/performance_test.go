package sqlite

import (
	"context"
	"testing"
)

func BenchmarkPointReadPaths(b *testing.B) {
	store, err := OpenStore(context.Background(), Config{WorkspaceRoot: b.TempDir()})
	if err != nil {
		b.Fatal(err)
	}
	b.Cleanup(func() { _ = store.Close() })
	if _, err := store.Commit(context.Background(), buildChangeSet(b, 0, sampleFiles())); err != nil {
		b.Fatal(err)
	}

	b.Run("FileHash", func(b *testing.B) {
		for range b.N {
			if _, found, err := store.FileHash(context.Background(), "pkg/svc.go"); err != nil || !found {
				b.Fatalf("FileHash found/error = %v/%v", found, err)
			}
		}
	})
	b.Run("Counts", func(b *testing.B) {
		for range b.N {
			if _, err := store.Counts(context.Background()); err != nil {
				b.Fatal(err)
			}
		}
	})
	b.Run("Search", func(b *testing.B) {
		for range b.N {
			if _, err := store.Search(context.Background(), "Pay", 10); err != nil {
				b.Fatal(err)
			}
		}
	})
	b.Run("FullReadView", func(b *testing.B) {
		for range b.N {
			view, err := store.OpenReadView(context.Background())
			if err != nil {
				b.Fatal(err)
			}
			_ = view.Close()
		}
	})
}
