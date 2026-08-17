package changeset_test

import (
	"bytes"
	"errors"
	"math"
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
		Embed("s1", []float64{0.1, 0.2, 0.3}).
		Build(fixedTime)
	if err != nil {
		t.Fatalf("Build() error = %v", err)
	}
	if cs.ExpectedVersion() != 7 || cs.IsNoop() {
		t.Fatalf("unexpected change set: version=%d noop=%v", cs.ExpectedVersion(), cs.IsNoop())
	}
	if len(cs.Upserts()) != 1 || len(cs.DeletedPaths()) != 1 || len(cs.Embeddings()) != 1 {
		t.Fatalf("collections = %d/%d/%d, want 1/1/1", len(cs.Upserts()), len(cs.DeletedPaths()), len(cs.Embeddings()))
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
		{
			"embedding for unknown symbol",
			func() *changeset.Builder {
				return changeset.NewBuilder().Upsert(validParsed("a.go", "s1")).Embed("ghost", []float64{0.1})
			},
			changeset.CodeEmbeddingUnknownSym,
		},
		{
			"empty embedding vector",
			func() *changeset.Builder {
				return changeset.NewBuilder().Upsert(validParsed("a.go", "s1")).Embed("s1", []float64{})
			},
			changeset.CodeEmbeddingInvalidVec,
		},
		{
			"non-finite embedding",
			func() *changeset.Builder {
				return changeset.NewBuilder().Upsert(validParsed("a.go", "s1")).Embed("s1", []float64{math.Inf(1)})
			},
			changeset.CodeEmbeddingInvalidVec,
		},
		{
			"embedding dimension mismatch",
			func() *changeset.Builder {
				return changeset.NewBuilder().
					Upsert(validParsed("a.go", "s1", "s2")).
					Embed("s1", []float64{0.1, 0.2}).
					Embed("s2", []float64{0.3})
			},
			changeset.CodeEmbeddingDimMismatch,
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

func TestDeepCopyIsolatesInputMutation(t *testing.T) {
	t.Parallel()
	parsed := validParsed("a.go", "s1")
	vector := []float64{0.1, 0.2}
	cs, err := changeset.NewBuilder().Upsert(parsed).Embed("s1", vector).Build(fixedTime)
	if err != nil {
		t.Fatalf("Build() error = %v", err)
	}

	// Mutate the caller's inputs after construction.
	parsed.Symbols[0].ID = "tampered"
	parsed.Symbols[0].Name = "tampered"
	vector[0] = 99

	got := cs.Upserts()
	if got[0].Symbols[0].ID != "s1" || got[0].Symbols[0].Name != "s1" {
		t.Fatalf("input mutation leaked into change set: %#v", got[0].Symbols[0])
	}
	if cs.Embeddings()["s1"][0] != 0.1 {
		t.Fatalf("embedding mutation leaked: %v", cs.Embeddings()["s1"])
	}
}

func TestAccessorsReturnCopies(t *testing.T) {
	t.Parallel()
	cs, err := changeset.NewBuilder().Upsert(validParsed("a.go", "s1")).Embed("s1", []float64{1, 2}).Build(fixedTime)
	if err != nil {
		t.Fatalf("Build() error = %v", err)
	}
	cs.Upserts()[0].Symbols[0].ID = "tampered"
	cs.DeletedPaths() // no-op but must not panic
	cs.Embeddings()["s1"][0] = 99

	if cs.Upserts()[0].Symbols[0].ID != "s1" {
		t.Fatal("mutating an accessor result changed internal state")
	}
	if cs.Embeddings()["s1"][0] != 1 {
		t.Fatal("mutating embeddings accessor changed internal state")
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
		Embed("a1", []float64{0.5, 0.5}).
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
		Embed("a1", []float64{0.5, 0.5}).
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
