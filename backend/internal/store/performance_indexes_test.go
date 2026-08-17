package store

import (
	"path/filepath"
	"reflect"
	"testing"

	"github.com/ThalesMMS/CodeAtlas/internal/domain"
)

func TestKnownPathsUsesSortedDerivedSnapshotAndDefensiveCopies(t *testing.T) {
	t.Parallel()
	repository, err := Open(filepath.Join(t.TempDir(), "index.json"))
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	repository.ReplaceFile(domain.ParsedFile{File: domain.File{Path: "z/z.go", Hash: "z"}})
	repository.ReplaceFile(domain.ParsedFile{File: domain.File{Path: "a/a.go", Hash: "a"}})

	want := []string{"a/a.go", "z/z.go"}
	if got := repository.KnownPaths(); !reflect.DeepEqual(got, want) {
		t.Fatalf("KnownPaths = %v, want %v", got, want)
	}
	if !reflect.DeepEqual(repository.current.knownPaths, want) {
		t.Fatalf("derived knownPaths = %v, want %v", repository.current.knownPaths, want)
	}
	first := repository.KnownPaths()
	first[0] = "mutated"
	if got := repository.KnownPaths(); !reflect.DeepEqual(got, want) {
		t.Fatalf("caller mutation changed cached paths: %v", got)
	}

	repository.DeleteFile("a/a.go")
	if got := repository.KnownPaths(); !reflect.DeepEqual(got, []string{"z/z.go"}) {
		t.Fatalf("KnownPaths after delete = %v, want [z/z.go]", got)
	}
}

func TestLexicalIndexCachesNormalizedBoostFields(t *testing.T) {
	t.Parallel()
	index := newLexicalIndex()
	symbol := domain.Symbol{ID: "submit", Name: "SubmitOrder", QualifiedName: "checkout.Service.SubmitOrder"}
	index.add(symbol)
	index.finalize()

	fields := index.boostFields[symbol.ID]
	if fields.name != "submitorder" || fields.qualified != "checkout.service.submitorder" {
		t.Fatalf("boost fields = %+v", fields)
	}
	hits := index.search("SubmitOrder", 1, map[string]domain.Symbol{symbol.ID: symbol})
	if len(hits) != 1 || hits[0].Score <= 8 {
		t.Fatalf("exact-name boost missing from hits: %+v", hits)
	}
}
