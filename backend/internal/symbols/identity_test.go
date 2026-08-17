package symbols

import (
	"strings"
	"testing"

	"github.com/ThalesMMS/CodeAtlas/internal/domain"
)

func goSym(kind, name, qualified, path, signature, code string, startLine int) domain.Symbol {
	return domain.Symbol{
		Language: "go", Kind: kind, Name: name, QualifiedName: qualified, Path: path,
		Signature: signature, Code: code,
		Range: domain.Range{Start: domain.Position{Line: startLine, Column: 1}, End: domain.Position{Line: startLine + 1, Column: 1}},
	}
}

func TestSymbolIDHasNoPositionalPrefix(t *testing.T) {
	t.Parallel()
	id := Resolve(goSym("function", "Submit", "pkg.Submit", "pkg/svc.go", "func Submit()", "x", 5), "", "fh").Identity.ID
	if !strings.HasPrefix(string(id), "sym:v1:") {
		t.Fatalf("SymbolID = %q, want sym:v1: prefix", id)
	}
}

func TestInsert100LinesKeepsSymbolIDChangesOccurrence(t *testing.T) {
	t.Parallel()
	before := Resolve(goSym("function", "Submit", "pkg.Submit", "pkg/svc.go", "func Submit()", "body", 3), "", "fh1")
	after := Resolve(goSym("function", "Submit", "pkg.Submit", "pkg/svc.go", "func Submit()", "body", 103), "", "fh1")
	if before.Identity.ID != after.Identity.ID {
		t.Fatal("inserting 100 lines changed the SymbolID")
	}
	if before.Occurrence.ID == after.Occurrence.ID {
		t.Fatal("the OccurrenceID did not change after a 100-line shift")
	}
}

func TestBodyChangeKeepsSymbolID(t *testing.T) {
	t.Parallel()
	a := Resolve(goSym("function", "Submit", "pkg.Submit", "pkg/svc.go", "func Submit()", "return 1", 1), "", "fh")
	b := Resolve(goSym("function", "Submit", "pkg.Submit", "pkg/svc.go", "func Submit()", "return 2", 1), "", "fh")
	if a.Identity.ID != b.Identity.ID {
		t.Fatal("a body-only change altered the SymbolID")
	}
}

func TestDocCommentChangeKeepsSymbolIDChangesOccurrence(t *testing.T) {
	t.Parallel()
	base := goSym("function", "Submit", "pkg.Submit", "pkg/svc.go", "func Submit()", "body", 3)
	base.DocComment = "Submit stores an order."
	changed := base
	changed.DocComment = "Submit validates and stores an order."
	before := Resolve(base, "", "fh1")
	after := Resolve(changed, "", "fh2")
	if before.Identity.ID != after.Identity.ID {
		t.Fatal("a doc-comment-only change altered the SymbolID")
	}
	if before.Occurrence.ID == after.Occurrence.ID {
		t.Fatal("a doc-comment-only change did not alter the OccurrenceID")
	}
	if !strings.HasPrefix(string(after.Occurrence.ID), "occ:v2:") {
		t.Fatalf("OccurrenceID = %q, want occ:v2 prefix", after.Occurrence.ID)
	}
}

func TestGoSignatureChangeKeepsIDChangesFingerprint(t *testing.T) {
	t.Parallel()
	a := Resolve(goSym("function", "Submit", "pkg.Submit", "pkg/svc.go", "func Submit()", "x", 1), "", "fh")
	b := Resolve(goSym("function", "Submit", "pkg.Submit", "pkg/svc.go", "func Submit(id int)", "x", 1), "", "fh")
	if a.Identity.ID != b.Identity.ID {
		t.Fatal("a Go signature change altered the SymbolID (Go has no overloads)")
	}
	if a.Identity.SignatureFingerprint == b.Identity.SignatureFingerprint {
		t.Fatal("the signature fingerprint did not change with the signature")
	}
}

func TestRenameAndParentMoveChangeID(t *testing.T) {
	t.Parallel()
	base := Resolve(goSym("method", "Submit", "Submit", "pkg/svc.go", "func Submit()", "x", 1), "sym:parentA", "fh")
	renamed := Resolve(goSym("method", "Send", "Send", "pkg/svc.go", "func Send()", "x", 1), "sym:parentA", "fh")
	moved := Resolve(goSym("method", "Submit", "Submit", "pkg/svc.go", "func Submit()", "x", 1), "sym:parentB", "fh")
	if base.Identity.ID == renamed.Identity.ID {
		t.Fatal("rename did not change the SymbolID")
	}
	if base.Identity.ID == moved.Identity.ID {
		t.Fatal("moving to a different parent did not change the SymbolID")
	}
}

func TestFileMoveAcrossPackagesChangesIdentity(t *testing.T) {
	t.Parallel()
	// Same package directory, different file → same identity (Go).
	a := Resolve(goSym("function", "Submit", "pkg.Submit", "pkg/a.go", "func Submit()", "x", 1), "", "fh")
	b := Resolve(goSym("function", "Submit", "pkg.Submit", "pkg/b.go", "func Submit()", "x", 1), "", "fh")
	if a.Identity.ID != b.Identity.ID {
		t.Fatal("a same-package file move changed the Go identity")
	}
	// Different package directory → new identity.
	c := Resolve(goSym("function", "Submit", "pkg.Submit", "other/a.go", "func Submit()", "x", 1), "", "fh")
	if a.Identity.ID == c.Identity.ID {
		t.Fatal("moving to another package did not change the identity")
	}
}

func TestTypeScriptOverloadsAreDeterministic(t *testing.T) {
	t.Parallel()
	ts := func(sig string) domain.Symbol {
		return domain.Symbol{Language: "typescript", Kind: "method", Name: "m", QualifiedName: "C.m", Path: "c.ts", Signature: sig}
	}
	inputs := []Input{
		{Symbol: ts("m(a: string): void"), ParentID: "sym:C"},
		{Symbol: ts("m(a: number): void"), ParentID: "sym:C"},
	}
	first, err := NewResolver().ResolveBatch(inputs)
	if err != nil {
		t.Fatal(err)
	}
	if first[0].Identity.ID == first[1].Identity.ID {
		t.Fatal("TS overloads collapsed to one identity")
	}
	// Re-resolving in reverse order yields the same id set (order-independent).
	second, err := NewResolver().ResolveBatch([]Input{inputs[1], inputs[0]})
	if err != nil {
		t.Fatal(err)
	}
	ids := map[domain.SymbolID]bool{first[0].Identity.ID: true, first[1].Identity.ID: true}
	if !ids[second[0].Identity.ID] || !ids[second[1].Identity.ID] {
		t.Fatal("overload ids changed with parse order")
	}
}

func TestDuplicateDeclarationSharesOneIdentity(t *testing.T) {
	t.Parallel()
	// Same key, same signature (interface merge / duplicate) → one identity, two occurrences.
	inputs := []Input{
		{Symbol: domain.Symbol{Language: "typescript", Kind: "interface", Name: "I", QualifiedName: "I", Path: "a.ts", Signature: "interface I"}},
		{Symbol: domain.Symbol{Language: "typescript", Kind: "interface", Name: "I", QualifiedName: "I", Path: "a.ts", Signature: "interface I"}},
	}
	resolved, err := NewResolver().ResolveBatch(inputs)
	if err != nil {
		t.Fatal(err)
	}
	if resolved[0].Identity.ID != resolved[1].Identity.ID {
		t.Fatal("duplicate declaration produced two identities instead of one")
	}
}

func TestAnonymousSymbolHasNoLineBasedIdentity(t *testing.T) {
	t.Parallel()
	anon := Resolve(domain.Symbol{Language: "javascript", Kind: "function", Name: "(arrow)", QualifiedName: "", Path: "a.js", Code: "() => {}",
		Range: domain.Range{Start: domain.Position{Line: 9, Column: 1}, End: domain.Position{Line: 9, Column: 8}}}, "", "fh")
	if anon.Identity.ID != "" || anon.Occurrence.SymbolID != "" || !anon.Occurrence.OccurrenceOnly {
		t.Fatalf("anonymous symbol gained a logical identity: %+v", anon)
	}
	if strings.HasPrefix(string(anon.Occurrence.ID), "sym:") {
		t.Fatal("anonymous occurrence id uses a sym: identity prefix")
	}
}

func TestHashCollisionIsDetectedNotOverwritten(t *testing.T) {
	t.Parallel()
	if err := validateNoCollision("canonical-key-A", "canonical-key-B"); err == nil {
		t.Fatal("distinct canonical keys with the same id were not flagged as a collision")
	}
	if err := validateNoCollision("same-key", "same-key"); err != nil {
		t.Fatalf("identical keys were wrongly flagged as a collision: %v", err)
	}

	// A resolver must reject a second registration of the same id under a new key.
	resolver := NewResolver()
	if err := resolver.register("sym:v1:fixed", "key-1"); err != nil {
		t.Fatal(err)
	}
	if err := resolver.register("sym:v1:fixed", "key-2"); err == nil {
		t.Fatal("resolver overwrote an existing id with a different canonical key")
	}
}
