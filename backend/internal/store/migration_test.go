package store

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"github.com/ThalesMMS/CodeAtlas/internal/domain"
	codeparser "github.com/ThalesMMS/CodeAtlas/internal/parser"
)

func goFunctionFile(path, hash string, startLine int) domain.ParsedFile {
	fileID := path + "#file"
	fnID := path + "#fn"
	return domain.ParsedFile{
		File: domain.File{Path: path, Language: "go", Hash: hash},
		Symbols: []domain.Symbol{
			{ID: fileID, Path: path, Language: "go", Name: filepath.Base(path), QualifiedName: path, Kind: "file", Range: domain.Range{Start: domain.Position{Line: 1, Column: 1}, End: domain.Position{Line: startLine + 5, Column: 1}}},
			{ID: fnID, Path: path, Language: "go", Name: "Submit", QualifiedName: "pkg.Submit", Kind: "function", Signature: "func Submit()", Code: "func Submit() {}", Range: domain.Range{Start: domain.Position{Line: startLine, Column: 1}, End: domain.Position{Line: startLine + 2, Column: 1}}},
		},
		Edges: []domain.Edge{{FromSymbolID: fileID, ToSymbolID: fnID, ToName: "Submit", Type: "contains", Path: path, Line: startLine}},
	}
}

func symbolIDByName(repository *Store, name string) string {
	for _, symbol := range repository.AllSymbols() {
		if symbol.Name == name {
			return symbol.ID
		}
	}
	return ""
}

func TestLineShiftPreservesSymbolIDChangesOccurrence(t *testing.T) {
	t.Parallel()
	repository, _ := openSnapshotStore(t)
	repository.ReplaceFile(goFunctionFile("pkg/svc.go", "h1", 3))
	id1 := symbolIDByName(repository, "Submit")
	at1, _ := repository.SymbolAt("pkg/svc.go", 4, 1)

	// Insert 100 lines above and change the body.
	repository.ReplaceFile(goFunctionFile("pkg/svc.go", "h2", 103))
	id2 := symbolIDByName(repository, "Submit")
	at2, _ := repository.SymbolAt("pkg/svc.go", 104, 1)

	if id1 == "" || id1 != id2 {
		t.Fatalf("SymbolID changed across a line shift: %q vs %q", id1, id2)
	}
	if at1.Occurrence.ID == at2.Occurrence.ID {
		t.Fatal("OccurrenceID did not change after a 100-line shift")
	}
	if string(at2.Occurrence.SymbolID) != id2 {
		t.Fatal("occurrence does not reference its identity")
	}
}

func TestMultipleOccurrencesPerIdentity(t *testing.T) {
	t.Parallel()
	repository, _ := openSnapshotStore(t)
	// The same logical symbol "pkg.Submit" declared in two files of one package
	// (same Go namespace) is one identity with two occurrences.
	repository.ReplaceFile(goFunctionFile("pkg/a.go", "ha", 2))
	repository.ReplaceFile(goFunctionFile("pkg/b.go", "hb", 9))

	id := symbolIDByName(repository, "Submit")
	occs := repository.OccurrencesForSymbol(domain.SymbolID(id))
	if len(occs) != 2 {
		t.Fatalf("identity has %d occurrences, want 2", len(occs))
	}
	if a, ok := repository.SymbolAt("pkg/a.go", 3, 1); !ok || string(a.Occurrence.SymbolID) != id {
		t.Fatal("occurrence in a.go does not resolve to the shared identity")
	}
	if b, ok := repository.SymbolAt("pkg/b.go", 10, 1); !ok || string(b.Occurrence.SymbolID) != id {
		t.Fatal("occurrence in b.go does not resolve to the shared identity")
	}
}

func TestCanonicalParserBatchPreservesDuplicateOccurrences(t *testing.T) {
	t.Parallel()
	repository, _ := openSnapshotStore(t)
	source := []byte("interface Merge { first(): void }\ninterface Merge { second(): void }\n")
	parsedSymbols, edges, language, err := codeparser.New().Parse("merge.ts", source)
	if err != nil {
		t.Fatal(err)
	}
	repository.ReplaceFile(domain.ParsedFile{
		File:    domain.File{Path: "merge.ts", Language: language, Hash: "sha256:merge"},
		Symbols: parsedSymbols,
		Edges:   edges,
	})
	id := symbolIDByName(repository, "Merge")
	if occurrences := repository.OccurrencesForSymbol(domain.SymbolID(id)); len(occurrences) != 2 {
		t.Fatalf("canonical duplicate identity has %d occurrences, want 2", len(occurrences))
	}
}

func TestDeleteRemovesIdentityOnlyWhenNoOccurrencesRemain(t *testing.T) {
	t.Parallel()
	repository, _ := openSnapshotStore(t)
	repository.ReplaceFile(goFunctionFile("pkg/a.go", "ha", 2))
	repository.ReplaceFile(goFunctionFile("pkg/b.go", "hb", 9))
	id := domain.SymbolID(symbolIDByName(repository, "Submit"))

	repository.DeleteFile("pkg/a.go")
	if len(repository.OccurrencesForSymbol(id)) != 1 {
		t.Fatal("deleting one file dropped the shared identity prematurely")
	}
	if _, ok := repository.GetSymbol(string(id)); !ok {
		t.Fatal("identity removed while an occurrence still exists")
	}

	repository.DeleteFile("pkg/b.go")
	if len(repository.OccurrencesForSymbol(id)) != 0 {
		t.Fatal("identity retained occurrences after deleting the last file")
	}
	if _, ok := repository.GetSymbol(string(id)); ok {
		t.Fatal("identity survived after its last occurrence was deleted")
	}
}

func TestLegacySnapshotTriggersAtomicRebuild(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	path := filepath.Join(dir, "index.json")
	// A pre-v1 snapshot: line-based Symbols, no schema.
	legacy := diskSnapshot{
		Version:      snapshotVersion,
		StoreVersion: 7,
		Symbols:      []domain.Symbol{{ID: "checkout/service.go:4", Path: "checkout/service.go", Name: "Submit", Kind: "function", Range: validatorTestRange()}},
		Edges:        []domain.Edge{{ID: 1, Type: "contains", Path: "checkout/service.go"}},
	}
	data, _ := json.MarshalIndent(legacy, "", "  ")
	if err := os.WriteFile(path, data, 0o600); err != nil {
		t.Fatal(err)
	}

	repository, err := Open(path)
	if err != nil {
		t.Fatalf("Open(legacy) error = %v, want a clean rebuild", err)
	}
	// The store starts empty (to be reindexed) and the legacy file is preserved.
	if files, _, _, _, _ := repository.Counts(); files != 0 {
		t.Fatalf("legacy snapshot was loaded instead of rebuilt: %d files", files)
	}
	if _, err := os.Stat(filepath.Join(dir, "index.json.legacy-schema0.bak")); err != nil {
		t.Fatalf("legacy snapshot backup missing: %v", err)
	}
}
