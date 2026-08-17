package lspadapter

import (
	"context"
	"encoding/json"
	"errors"
	"path/filepath"
	"strings"
	"testing"

	"github.com/ThalesMMS/CodeAtlas/internal/domain"
	"github.com/ThalesMMS/CodeAtlas/internal/lspconv"
	"github.com/ThalesMMS/CodeAtlas/internal/lspfacts"
	"github.com/ThalesMMS/CodeAtlas/internal/semantic"
)

func TestDocumentStateLockRespectsCancellation(t *testing.T) {
	t.Parallel()
	state := NewDocumentState()
	unlock, err := state.Lock(context.Background(), "doc-1")
	if err != nil {
		t.Fatal(err)
	}
	defer unlock()

	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := state.Lock(ctx, "doc-1"); !errors.Is(err, context.Canceled) {
		t.Fatalf("Lock() error = %v, want context.Canceled", err)
	}
}

func TestLocationConverterLoadsAndIndexesEachPathOnce(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	source := []byte("first\nsecond\n")
	loads := 0
	core := ProviderCore{
		WorkspaceRoot: root,
		Source: func(_ semantic.SemanticQuery, relative string) ([]byte, error) {
			loads++
			if relative != "target.ts" {
				t.Fatalf("source path = %q", relative)
			}
			return source, nil
		},
	}
	converter := core.NewLocationConverter(semantic.SemanticQuery{Path: "query.ts"}, lspconv.EncodingUTF16)
	location := lspfacts.Location{
		URI: lspconv.PathToURI(filepath.Join(root, "target.ts")),
		Range: lspfacts.Range{
			Start: lspfacts.Position{Line: 1},
			End:   lspfacts.Position{Line: 1, Character: 6},
		},
	}
	for index := 0; index < 20; index++ {
		converted, ok := converter.Convert(location)
		if !ok || converted.Range.Start.Line != 2 {
			t.Fatalf("Convert() = %#v, %v", converted, ok)
		}
	}
	if loads != 1 {
		t.Fatalf("source loaded %d times for one path in one query, want 1", loads)
	}
}

func TestLocationConverterSeedReusesPreparedSource(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	source := []byte("const value = 1\n")
	core := ProviderCore{
		WorkspaceRoot: root,
		Source: func(semantic.SemanticQuery, string) ([]byte, error) {
			t.Fatal("seeded query path was loaded again")
			return nil, nil
		},
	}
	query := semantic.SemanticQuery{Path: "query.ts"}
	converter := core.NewLocationConverter(query, lspconv.EncodingUTF16)
	converter.Seed(query.Path, source, lspconv.LineStarts(source))
	_, ok := converter.Convert(lspfacts.Location{
		URI:   lspconv.PathToURI(filepath.Join(root, query.Path)),
		Range: lspfacts.Range{End: lspfacts.Position{Character: 5}},
	})
	if !ok {
		t.Fatal("Convert() rejected seeded location")
	}
}

func TestDiagnosticStoreInvalidatesOnlyVersionUnknownSets(t *testing.T) {
	t.Parallel()
	store := NewDiagnosticStore()
	unknown := lspfacts.PublishDiagnosticsParams{URI: "file:///unknown.ts", Diagnostics: []lspfacts.Diagnostic{{Message: "unknown"}}}
	knownVersion := int64(4)
	known := lspfacts.PublishDiagnosticsParams{URI: "file:///known.ts", Version: &knownVersion, Diagnostics: []lspfacts.Diagnostic{{Message: "known"}}}
	store.Ingest(mustJSON(t, unknown))
	store.Ingest(mustJSON(t, known))

	store.InvalidateUnknown(unknown.URI)
	store.InvalidateUnknown(known.URI)
	if _, exists := store.Get(unknown.URI); exists {
		t.Fatal("version-unknown diagnostics survived invalidation")
	}
	if set, exists := store.Get(known.URI); !exists || !set.VersionKnown || set.Version != knownVersion {
		t.Fatalf("versioned diagnostics = %#v, %v", set, exists)
	}
}

func TestDiagnosticStoreClearAndNumericCodeBounding(t *testing.T) {
	t.Parallel()
	store := NewDiagnosticStore()
	params := lspfacts.PublishDiagnosticsParams{URI: "file:///main.go", Diagnostics: []lspfacts.Diagnostic{{Message: "problem"}}}
	store.Ingest(mustJSON(t, params))
	store.Clear(params.URI)
	if _, exists := store.Get(params.URI); exists {
		t.Fatal("cleared diagnostics remained cached")
	}

	rawCode := strings.Repeat("9", lspfacts.MaxHoverBytes+100)
	fact := NormalizeDiagnostic(lspfacts.Diagnostic{
		Message: "problem",
		Code:    json.RawMessage(rawCode),
	})
	if fact.Code != lspfacts.BoundString(rawCode) {
		t.Fatalf("numeric diagnostic code was not bounded: %d bytes", len(fact.Code))
	}
}

func TestFinishFactsKeepsDistinctEndRanges(t *testing.T) {
	t.Parallel()
	base := semantic.SemanticFact{Location: semantic.SourceLocation{
		Path: "a.ts", Range: domain.Range{Start: domain.Position{Line: 1, Column: 1}, End: domain.Position{Line: 1, Column: 2}},
	}}
	differentEnd := base
	differentEnd.Location.Range.End.Column = 3
	duplicate := base

	got := FinishFacts([]semantic.SemanticFact{differentEnd, duplicate, base})
	if len(got) != 2 {
		t.Fatalf("FinishFacts() returned %d facts, want 2 distinct full ranges", len(got))
	}
}

func mustJSON(t *testing.T, value any) json.RawMessage {
	t.Helper()
	data, err := json.Marshal(value)
	if err != nil {
		t.Fatal(err)
	}
	return data
}
