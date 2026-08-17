package store

import (
	"path/filepath"
	"strings"
	"testing"

	"github.com/ThalesMMS/CodeAtlas/internal/domain"
)

func goFile(path, hash string, symbols []domain.Symbol) domain.ParsedFile {
	return domain.ParsedFile{
		File:    domain.File{Path: path, Language: "go", Hash: hash},
		Symbols: symbols,
	}
}

func sym(id, path, name string, line int) domain.Symbol {
	return domain.Symbol{
		ID: id, Path: path, Name: name, QualifiedName: name, Kind: "function", Language: "go",
		Range: domain.Range{Start: domain.Position{Line: line, Column: 1}, End: domain.Position{Line: line + 1, Column: 1}},
	}
}

func compositeFixture(t *testing.T) (*Store, domain.SnapshotID) {
	t.Helper()
	repository, err := Open(filepath.Join(t.TempDir(), "index.json"))
	if err != nil {
		t.Fatal(err)
	}
	repository.ReplaceFile(goFile("a.go", "hashA1", []domain.Symbol{sym("a-1", "a.go", "Alpha", 1)}))
	repository.ReplaceFile(goFile("b.go", "hashB", []domain.Symbol{sym("b-1", "b.go", "Beta", 1)}))
	return repository, repository.SnapshotID()
}

func names(symbols []domain.Symbol) map[string]bool {
	out := map[string]bool{}
	for _, s := range symbols {
		out[s.Name] = true
	}
	return out
}

func TestCompositeViewReplacesActiveFileWithoutMutatingStore(t *testing.T) {
	t.Parallel()
	repository, activeID := compositeFixture(t)

	ephemeral := goFile("a.go", "hashA2", []domain.Symbol{
		sym("a-1", "a.go", "Alpha", 1),
		sym("a-2", "a.go", "AlphaNew", 5),
	})
	overlay := OverlayContext{DocumentID: "doc1", Path: "a.go", Version: 2, ContentHash: "sha256:c2", BaseContentHash: "hashA1", BaseSnapshotID: activeID}

	view, err := repository.CompositeView(ephemeral, overlay)
	if err != nil {
		t.Fatalf("CompositeView: %v", err)
	}
	got := names(view.AllSymbols())
	if !got["AlphaNew"] || !got["Beta"] {
		t.Fatalf("composite missing ephemeral or persisted symbols: %v", got)
	}

	// The Store itself is never mutated by building a composite view.
	persisted := names(repository.Snapshot().AllSymbols())
	if persisted["AlphaNew"] {
		t.Fatal("ephemeral symbol leaked into the persisted store")
	}

	// ViewHash is deterministic and is NOT a SnapshotID.
	if !strings.HasPrefix(view.ViewHash(), "view:") {
		t.Fatalf("ViewHash %q should be a view hash, not a snapshot id", view.ViewHash())
	}
	second, _ := repository.CompositeView(ephemeral, overlay)
	if second.ViewHash() != view.ViewHash() {
		t.Fatal("ViewHash is not deterministic")
	}
	if view.Rebased() {
		t.Fatal("no rebase expected when base == active snapshot")
	}
}

func TestCompositeViewRebasesWhenOtherFilesChanged(t *testing.T) {
	t.Parallel()
	repository, baseID := compositeFixture(t)

	// Another file changes, advancing the active snapshot past the overlay base,
	// but a.go's persisted hash is unchanged → safe to rebase.
	repository.ReplaceFile(goFile("b.go", "hashB2", []domain.Symbol{sym("b-1", "b.go", "BetaV2", 1)}))
	if repository.SnapshotID() == baseID {
		t.Fatal("snapshot should have advanced")
	}

	ephemeral := goFile("a.go", "hashA2", []domain.Symbol{sym("a-2", "a.go", "AlphaNew", 5)})
	overlay := OverlayContext{DocumentID: "doc1", Path: "a.go", Version: 2, ContentHash: "sha256:c2", BaseContentHash: "hashA1", BaseSnapshotID: baseID}

	view, err := repository.CompositeView(ephemeral, overlay)
	if err != nil {
		t.Fatalf("rebase should succeed: %v", err)
	}
	if !view.Rebased() {
		t.Fatal("expected Rebased() true after the global snapshot advanced")
	}
	got := names(view.AllSymbols())
	if !got["AlphaNew"] || !got["BetaV2"] {
		t.Fatalf("rebased composite should have ephemeral a.go + new b.go: %v", got)
	}
}

func TestCompositeViewRejectsStaleBaseWhenFileChanged(t *testing.T) {
	t.Parallel()
	repository, baseID := compositeFixture(t)

	// a.go itself changed in the index since the overlay opened (hash differs from
	// the overlay base) → the unsaved buffer must not be composited.
	repository.ReplaceFile(goFile("a.go", "hashA-CHANGED", []domain.Symbol{sym("a-1", "a.go", "AlphaDisk", 1)}))

	ephemeral := goFile("a.go", "hashA2", []domain.Symbol{sym("a-2", "a.go", "AlphaNew", 5)})
	overlay := OverlayContext{DocumentID: "doc1", Path: "a.go", Version: 2, ContentHash: "sha256:c2", BaseContentHash: "hashA1", BaseSnapshotID: baseID}

	if _, err := repository.CompositeView(ephemeral, overlay); err != ErrDocumentBaseStale {
		t.Fatalf("CompositeView = %v, want ErrDocumentBaseStale", err)
	}
}
