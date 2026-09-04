package changeset_test

import (
	"bytes"
	"errors"
	"slices"
	"testing"
	"time"

	"github.com/ThalesMMS/CodeAtlas/internal/changeset"
	"github.com/ThalesMMS/CodeAtlas/internal/domain"
)

var fixedTime = time.Date(2026, 6, 24, 0, 0, 0, 0, time.UTC)

func validParsed(path string, symbolIDs ...string) domain.ParsedFile {
	symbols := make([]domain.Symbol, len(symbolIDs))
	for i, id := range symbolIDs {
		symbols[i] = domain.Symbol{ID: id, Path: path, Name: id, Kind: "function"}
	}
	return domain.ParsedFile{File: domain.File{Path: path, Language: "go"}, Symbols: symbols}
}

func TestBuildValidChangeSet(t *testing.T) {
	t.Parallel()
	cs, err := changeset.NewBuilder().
		WithExpectedVersion(7).
		Upsert(validParsed("pkg/a.go", "s1")).
		Delete("pkg/old.go").
		Build(fixedTime)
	if err != nil {
		t.Fatalf("Build() error = %v", err)
	}
	if cs.ExpectedVersion() != 7 || cs.IsNoop() {
		t.Fatalf("unexpected change set: version=%d noop=%v", cs.ExpectedVersion(), cs.IsNoop())
	}
	if len(cs.Upserts()) != 1 || len(cs.DeletedPaths()) != 1 {
		t.Fatalf("collections = %d/%d, want 1/1", len(cs.Upserts()), len(cs.DeletedPaths()))
	}
	if cs.ID() == "" || !bytes.HasPrefix([]byte(cs.ID()), []byte("sha256:")) {
		t.Fatalf("ID = %q, want sha256:... ", cs.ID())
	}
}

func TestValidationInvariants(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name     string
		build    func() *changeset.Builder
		wantCode string
	}{
		{"empty path", func() *changeset.Builder { return changeset.NewBuilder().Delete("") }, changeset.CodePathInvalid},
		{"absolute path", func() *changeset.Builder { return changeset.NewBuilder().Delete("/etc/passwd") }, changeset.CodePathInvalid},
		{"dotdot escape", func() *changeset.Builder { return changeset.NewBuilder().Delete("../secret.go") }, changeset.CodePathInvalid},
		{"dot path", func() *changeset.Builder { return changeset.NewBuilder().Delete(".") }, changeset.CodePathInvalid},
		{"non-canonical", func() *changeset.Builder { return changeset.NewBuilder().Delete("a/./b.go") }, changeset.CodePathInvalid},
		{"backslash escape", func() *changeset.Builder { return changeset.NewBuilder().Delete(`..\secret.go`) }, changeset.CodePathInvalid},
		{"drive-qualified", func() *changeset.Builder { return changeset.NewBuilder().Delete(`C:\Windows\system.ini`) }, changeset.CodePathInvalid},
		{"external URL", func() *changeset.Builder { return changeset.NewBuilder().Delete("https://example.test/file.go") }, changeset.CodePathInvalid},
		{
			"upsert and delete same path",
			func() *changeset.Builder {
				return changeset.NewBuilder().Upsert(validParsed("a.go", "s1")).Delete("a.go")
			},
			changeset.CodeUpsertDeleteConflict,
		},
		{
			"duplicate upsert",
			func() *changeset.Builder {
				return changeset.NewBuilder().Upsert(validParsed("a.go", "s1")).Upsert(validParsed("a.go", "s2"))
			},
			changeset.CodeDuplicateUpsert,
		},
		{
			"symbol path mismatch",
			func() *changeset.Builder {
				parsed := domain.ParsedFile{File: domain.File{Path: "a.go"}, Symbols: []domain.Symbol{{ID: "s1", Path: "b.go"}}}
				return changeset.NewBuilder().Upsert(parsed)
			},
			changeset.CodeSymbolPathMismatch,
		},
		{
			"duplicate symbol id",
			func() *changeset.Builder {
				return changeset.NewBuilder().Upsert(validParsed("a.go", "dup")).Upsert(validParsed("b.go", "dup"))
			},
			changeset.CodeDuplicateSymbolID,
		},
		{
			"duplicate occurrence id",
			func() *changeset.Builder {
				first := validParsed("a.go", "sym:v1:same")
				first.Symbols[0].OccurrenceID = "occ:v2:same"
				second := validParsed("b.go", "sym:v1:same")
				second.Symbols[0].OccurrenceID = "occ:v2:same"
				return changeset.NewBuilder().Upsert(first).Upsert(second)
			},
			changeset.CodeDuplicateOccurrence,
		},
		{"empty change set", func() *changeset.Builder { return changeset.NewBuilder() }, changeset.CodeEmpty},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			_, err := tc.build().Build(fixedTime)
			var validation *changeset.ValidationError
			if !errors.As(err, &validation) {
				t.Fatalf("error = %v, want *ValidationError", err)
			}
			if validation.Code != tc.wantCode {
				t.Fatalf("code = %q, want %q", validation.Code, tc.wantCode)
			}
		})
	}
}

func TestAllowEmptyNoop(t *testing.T) {
	t.Parallel()
	cs, err := changeset.NewBuilder().AllowEmpty().Build(fixedTime)
	if err != nil {
		t.Fatalf("Build() error = %v", err)
	}
	if !cs.IsNoop() {
		t.Fatal("expected an explicit no-op change set")
	}
}

func TestIsNoopTracksContentOnly(t *testing.T) {
	t.Parallel()
	// Diagnostics and an expected version are metadata: neither turns an empty
	// change into real work. Only upserts and deletions do.
	metadataOnly, err := changeset.NewBuilder().
		AllowEmpty().
		WithExpectedVersion(9).
		Diagnose(changeset.Diagnostic{Severity: changeset.SeverityWarning, Code: "W1"}).
		Build(fixedTime)
	if err != nil {
		t.Fatalf("Build() error = %v", err)
	}
	if !metadataOnly.IsNoop() {
		t.Fatal("metadata alone must not make a change set non-noop")
	}

	deleteOnly, err := changeset.NewBuilder().Delete("gone.go").Build(fixedTime)
	if err != nil {
		t.Fatalf("Build() error = %v", err)
	}
	if deleteOnly.IsNoop() {
		t.Fatal("a deletion must make the change set non-noop")
	}
}

func TestDeepCopyIsolatesInputMutation(t *testing.T) {
	t.Parallel()
	parsed := validParsed("a.go", "s1")
	cs, err := changeset.NewBuilder().Upsert(parsed).Build(fixedTime)
	if err != nil {
		t.Fatalf("Build() error = %v", err)
	}

	// Mutate the caller's inputs after construction.
	parsed.Symbols[0].ID = "tampered"
	parsed.Symbols[0].Name = "tampered"

	got := cs.Upserts()
	if got[0].Symbols[0].ID != "s1" || got[0].Symbols[0].Name != "s1" {
		t.Fatalf("input mutation leaked into change set: %#v", got[0].Symbols[0])
	}
}

func TestAccessorsReturnCopies(t *testing.T) {
	t.Parallel()
	cs, err := changeset.NewBuilder().
		Upsert(validParsed("a.go", "s1")).
		Delete("old.go").
		Diagnose(changeset.Diagnostic{Severity: changeset.SeverityWarning, Code: "W1"}).
		Build(fixedTime)
	if err != nil {
		t.Fatalf("Build() error = %v", err)
	}
	cs.Upserts()[0].Symbols[0].ID = "tampered"
	cs.DeletedPaths()[0] = "tampered.go"
	cs.Diagnostics()[0].Code = "TAMPERED"
	cs.Canonical()[0] = 'x'

	if cs.Upserts()[0].Symbols[0].ID != "s1" {
		t.Fatal("mutating an accessor result changed internal state")
	}
	if cs.DeletedPaths()[0] != "old.go" {
		t.Fatalf("mutating deleted paths accessor changed internal state: %v", cs.DeletedPaths())
	}
	if cs.Diagnostics()[0].Code != "W1" {
		t.Fatalf("mutating diagnostics accessor changed internal state: %v", cs.Diagnostics())
	}
	if cs.Canonical()[0] == 'x' {
		t.Fatal("mutating canonical accessor changed internal state")
	}
}

func TestCanonicalRepresentationIsDeterministic(t *testing.T) {
	t.Parallel()
	// Same logical content, different insertion order and createdAt.
	first, err := changeset.NewBuilder().
		WithExpectedVersion(3).
		Upsert(validParsed("z.go", "z1")).
		Upsert(validParsed("a.go", "a1")).
		Delete("b.go").
		Delete("a/old.go").
		Build(fixedTime)
	if err != nil {
		t.Fatalf("first Build() error = %v", err)
	}
	second, err := changeset.NewBuilder().
		WithExpectedVersion(3).
		Upsert(validParsed("a.go", "a1")).
		Upsert(validParsed("z.go", "z1")).
		Delete("a/old.go").
		Delete("b.go").
		Build(fixedTime.Add(48 * time.Hour))
	if err != nil {
		t.Fatalf("second Build() error = %v", err)
	}

	if first.ID() != second.ID() {
		t.Fatalf("IDs differ for identical content: %q vs %q", first.ID(), second.ID())
	}
	if !bytes.Equal(first.Canonical(), second.Canonical()) {
		t.Fatalf("canonical bytes differ:\n%s\n%s", first.Canonical(), second.Canonical())
	}
}

func TestCanonicalRepresentationSeparatesContent(t *testing.T) {
	t.Parallel()
	base, err := changeset.NewBuilder().
		WithExpectedVersion(3).
		Upsert(validParsed("a.go", "a1")).
		Build(fixedTime)
	if err != nil {
		t.Fatalf("base Build() error = %v", err)
	}
	otherVersion, err := changeset.NewBuilder().
		WithExpectedVersion(4).
		Upsert(validParsed("a.go", "a1")).
		Build(fixedTime)
	if err != nil {
		t.Fatalf("otherVersion Build() error = %v", err)
	}
	otherContent, err := changeset.NewBuilder().
		WithExpectedVersion(3).
		Upsert(validParsed("a.go", "a2")).
		Build(fixedTime)
	if err != nil {
		t.Fatalf("otherContent Build() error = %v", err)
	}

	if base.ID() == otherVersion.ID() {
		t.Fatal("expected version must contribute to the change set identity")
	}
	if base.ID() == otherContent.ID() {
		t.Fatal("upserted symbols must contribute to the change set identity")
	}
}

// TestCanonicalNormalizesWithinFile pins the intra-file ordering that
// normalizeUpserts imposes before hashing. The only fixture that ever put more
// than one symbol in a single file was a vector-dimension case removed with dense
// retrieval, which left the symbol and edge sort comparators unexercised
// even though they are pure lexical-index behaviour. Ordering matters more now,
// not less: canonicalBytes no longer hashes vectors, so upserts and deletedPaths
// are the whole of a ChangeSet's identity.
func TestCanonicalNormalizesWithinFile(t *testing.T) {
	t.Parallel()
	build := func(symbols []domain.Symbol, edges []domain.Edge) *changeset.ChangeSet {
		t.Helper()
		parsed := domain.ParsedFile{
			File:    domain.File{Path: "pkg/a.go", Language: "go"},
			Symbols: symbols,
			Edges:   edges,
		}
		cs, err := changeset.NewBuilder().Upsert(parsed).Build(fixedTime)
		if err != nil {
			t.Fatalf("Build() error = %v", err)
		}
		return cs
	}
	sym := func(id, occurrence string) domain.Symbol {
		return domain.Symbol{ID: id, OccurrenceID: occurrence, Path: "pkg/a.go", Name: id, Kind: "function"}
	}
	edge := func(from, to string, line int) domain.Edge {
		return domain.Edge{FromSymbolID: from, ToSymbolID: to, ToName: to, Type: "calls", Path: "pkg/a.go", Line: line}
	}

	scrambled := build(
		[]domain.Symbol{sym("s2", "occ:2"), sym("s1", "occ:1b"), sym("s1", "occ:1a")},
		[]domain.Edge{edge("s2", "s1", 20), edge("s1", "s2", 10)},
	)
	ordered := build(
		[]domain.Symbol{sym("s1", "occ:1a"), sym("s1", "occ:1b"), sym("s2", "occ:2")},
		[]domain.Edge{edge("s1", "s2", 10), edge("s2", "s1", 20)},
	)

	if scrambled.ID() != ordered.ID() {
		t.Fatalf("input ordering leaked into identity: %q vs %q", scrambled.ID(), ordered.ID())
	}
	if !bytes.Equal(scrambled.Canonical(), ordered.Canonical()) {
		t.Fatalf("canonical bytes differ:\n%s\n%s", scrambled.Canonical(), ordered.Canonical())
	}

	got := scrambled.Upserts()[0]
	gotSymbols := make([]string, len(got.Symbols))
	for i, symbol := range got.Symbols {
		gotSymbols[i] = symbol.ID + "/" + symbol.OccurrenceID
	}
	wantSymbols := []string{"s1/occ:1a", "s1/occ:1b", "s2/occ:2"}
	if !slices.Equal(gotSymbols, wantSymbols) {
		t.Fatalf("symbols = %v, want %v (sorted by id, then occurrence id)", gotSymbols, wantSymbols)
	}
	gotEdges := make([]string, len(got.Edges))
	for i, e := range got.Edges {
		gotEdges[i] = e.FromSymbolID + "->" + e.ToSymbolID
	}
	wantEdges := []string{"s1->s2", "s2->s1"}
	if !slices.Equal(gotEdges, wantEdges) {
		t.Fatalf("edges = %v, want %v", gotEdges, wantEdges)
	}
}

// TestDeepCopyIsolatesEdgeMutation covers the Edges arm of cloneParsedFile, the
// sibling of the Symbols arm already guarded by TestDeepCopyIsolatesInputMutation.
func TestDeepCopyIsolatesEdgeMutation(t *testing.T) {
	t.Parallel()
	parsed := validParsed("a.go", "s1")
	parsed.Edges = []domain.Edge{{FromSymbolID: "s1", ToName: "helper", Type: "calls", Path: "a.go", Line: 3}}
	cs, err := changeset.NewBuilder().Upsert(parsed).Build(fixedTime)
	if err != nil {
		t.Fatalf("Build() error = %v", err)
	}

	parsed.Edges[0].ToName = "tampered"

	if got := cs.Upserts()[0].Edges[0].ToName; got != "helper" {
		t.Fatalf("edge mutation leaked into change set: ToName = %q, want %q", got, "helper")
	}
}

func TestHasErrorsReflectsDiagnostics(t *testing.T) {
	t.Parallel()
	clean, _ := changeset.NewBuilder().
		Upsert(validParsed("a.go", "s1")).
		Diagnose(changeset.Diagnostic{Severity: changeset.SeverityWarning, Code: "W1"}).
		Build(fixedTime)
	if clean.HasErrors() {
		t.Fatal("warning diagnostic must not count as a blocking error")
	}

	blocked, _ := changeset.NewBuilder().
		Upsert(validParsed("a.go", "s1")).
		Diagnose(changeset.Diagnostic{Severity: changeset.SeverityError, Stage: "parse", Code: "E1", Message: "boom"}).
		Build(fixedTime)
	if !blocked.HasErrors() {
		t.Fatal("error diagnostic must block")
	}
}
