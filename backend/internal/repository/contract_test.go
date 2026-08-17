package repository_test

import (
	"context"
	"errors"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/ThalesMMS/CodeAtlas/internal/changeset"
	"github.com/ThalesMMS/CodeAtlas/internal/domain"
	codeparser "github.com/ThalesMMS/CodeAtlas/internal/parser"
	"github.com/ThalesMMS/CodeAtlas/internal/repository"
	"github.com/ThalesMMS/CodeAtlas/internal/retrieval"
)

func TestCanonicalParserBatchPreservesDuplicateOccurrencesAcrossBackends(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name string
		open func(*testing.T) repository.Store
	}{
		{"json", func(t *testing.T) repository.Store {
			store, err := repository.OpenJSON(filepath.Join(t.TempDir(), "index.json"))
			if err != nil {
				t.Fatal(err)
			}
			return store
		}},
		{"sqlite", func(t *testing.T) repository.Store {
			store, err := repository.OpenSQLite(context.Background(), repository.SQLiteConfig{WorkspaceRoot: t.TempDir()})
			if err != nil {
				t.Fatal(err)
			}
			return store
		}},
	}
	for _, test := range cases {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			store := test.open(t)
			defer store.Close()
			source := []byte("interface Merge { first(): void }\ninterface Merge { second(): void }\n")
			parsedSymbols, edges, language, err := codeparser.New().Parse("merge.ts", source)
			if err != nil {
				t.Fatal(err)
			}
			change := buildContractChangeSet(t, store.Version(), []domain.ParsedFile{{
				File:    domain.File{Path: "merge.ts", Language: language, Hash: "sha256:merge"},
				Symbols: parsedSymbols, Edges: edges,
			}})
			if _, err := store.CommitPrepared(mustPrepare(t, store, change)); err != nil {
				t.Fatal(err)
			}
			view := store.Snapshot()
			defer view.Close()
			matches := view.SymbolsByName("Merge")
			if len(matches) != 1 {
				t.Fatalf("Merge identities = %d, want 1", len(matches))
			}
			if occurrences := view.OccurrencesForSymbol(domain.SymbolID(matches[0].ID)); len(occurrences) != 2 {
				t.Fatalf("Merge occurrences = %d, want 2", len(occurrences))
			}
		})
	}
}

func TestStoreContractJSONAndSQLite(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name string
		open func(t *testing.T) repository.Store
	}{
		{
			name: "json",
			open: func(t *testing.T) repository.Store {
				t.Helper()
				store, err := repository.OpenJSON(filepath.Join(t.TempDir(), "index.json"))
				if err != nil {
					t.Fatalf("OpenJSON: %v", err)
				}
				return store
			},
		},
		{
			name: "sqlite",
			open: func(t *testing.T) repository.Store {
				t.Helper()
				store, err := repository.OpenSQLite(context.Background(), repository.SQLiteConfig{
					WorkspaceRoot: t.TempDir(),
				})
				if err != nil {
					t.Fatalf("OpenSQLite: %v", err)
				}
				return store
			},
		},
	}
	for _, tc := range cases {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			store := tc.open(t)
			t.Cleanup(func() { _ = store.Close() })

			change := buildContractChangeSet(t, store.Version(), contractFiles())
			prepared, err := store.Prepare(change)
			if err != nil {
				t.Fatalf("Prepare: %v", err)
			}
			result, err := store.CommitPrepared(prepared)
			if err != nil {
				t.Fatalf("CommitPrepared: %v", err)
			}
			if result.Revision != 1 || result.SnapshotID == "" || result.NoOp {
				t.Fatalf("commit result = %+v, want revision 1 and snapshot id", result)
			}

			view := store.Snapshot()
			defer view.Close()
			if view.Metadata().Revision != 1 {
				t.Fatalf("view revision = %d, want 1", view.Metadata().Revision)
			}
			hits, err := view.Search("Pay", 10)
			if err != nil {
				t.Fatalf("Search: %v", err)
			}
			if len(hits) == 0 || hits[0].Symbol.Name != "Pay" {
				t.Fatalf("hits = %+v, want Pay first", hits)
			}
			if _, ok := view.GetSymbol(hits[0].Symbol.ID); !ok {
				t.Fatalf("GetSymbol(%q) failed", hits[0].Symbol.ID)
			}
			resolvedPay, ok := view.ResolveSymbol("receiver.Pay()", "pkg/util.go")
			if !ok || resolvedPay.Name != "Pay" {
				t.Fatalf("ResolveSymbol(qualified Pay) = %+v/%v, want Pay", resolvedPay, ok)
			}
			pay := hits[0].Symbol
			byOccurrence, ok := view.GetSymbolByOccurrence(pay.OccurrenceID)
			if !ok || byOccurrence.ID != pay.ID {
				t.Fatalf("GetSymbolByOccurrence(%q) = %+v/%v, want %q", pay.OccurrenceID, byOccurrence, ok, pay.ID)
			}
			occurrences := view.OccurrencesForSymbol(domain.SymbolID(pay.ID))
			if len(occurrences) != 2 {
				t.Fatalf("OccurrencesForSymbol(%q) = %d, want 2", pay.ID, len(occurrences))
			}
			for _, occurrence := range occurrences {
				resolved, found := view.GetSymbolByOccurrence(string(occurrence.ID))
				if !found || resolved.OccurrenceID != string(occurrence.ID) || resolved.Path != occurrence.Path {
					t.Fatalf("GetSymbolByOccurrence(%q) = %+v/%v, want exact occurrence at %q", occurrence.ID, resolved, found, occurrence.Path)
				}
			}
			if !containsContractSymbol(view.SymbolsByName(pay.Name), pay.ID) {
				t.Fatalf("SymbolsByName(%q) does not contain %q", pay.Name, pay.ID)
			}
			if !containsContractSymbol(view.SymbolsByPath(pay.Path), pay.ID) {
				t.Fatalf("SymbolsByPath(%q) does not contain %q", pay.Path, pay.ID)
			}
			if edges := view.EdgesForSymbol(pay.ID); len(edges) == 0 {
				t.Fatalf("EdgesForSymbol(%q) returned no edges", pay.ID)
			}
			nodes, edges := view.Graph([]string{hits[0].Symbol.ID}, 2, 20)
			if len(nodes) == 0 || len(edges) == 0 {
				t.Fatalf("graph nodes/edges = %d/%d, want non-empty", len(nodes), len(edges))
			}
			assertContractGraphClosed(t, nodes, edges)
			services := view.SymbolsByName("Service")
			if len(services) == 0 {
				t.Fatal("Service seed not found")
			}
			depthOneNodes, depthOneEdges := view.Graph([]string{services[0].ID}, 1, 20)
			if containsContractName(depthOneNodes, "Charge") {
				t.Fatalf("depth 1 reached Charge: %+v", depthOneNodes)
			}
			assertContractGraphClosed(t, depthOneNodes, depthOneEdges)
			depthTwoNodes, depthTwoEdges := view.Graph([]string{services[0].ID}, 2, 20)
			if !containsContractName(depthTwoNodes, "Charge") {
				t.Fatalf("depth 2 did not reach Charge: %+v", depthTwoNodes)
			}
			assertContractGraphClosed(t, depthTwoNodes, depthTwoEdges)
			limitedNodes, limitedEdges := view.Graph([]string{services[0].ID}, 2, 1)
			if len(limitedNodes) != 1 {
				t.Fatalf("maxNodes=1 returned %d nodes", len(limitedNodes))
			}
			assertContractGraphClosed(t, limitedNodes, limitedEdges)
			if len(view.AllEdges()) == 0 {
				t.Fatal("AllEdges returned no edges")
			}
			if hash, ok := store.FileHash("pkg/svc.go"); !ok || hash == "" {
				t.Fatalf("FileHash = %q/%v, want hash", hash, ok)
			}
			if paths := store.KnownPaths(); len(paths) != 2 || paths[0] != "pkg/svc.go" || paths[1] != "pkg/util.go" {
				t.Fatalf("KnownPaths = %v, want sorted contract files", paths)
			}
			files, symbols, edgesCount, _, _ := store.Counts()
			if files != 2 || symbols == 0 || edgesCount == 0 {
				t.Fatalf("Counts = %d/%d/%d, want indexed content", files, symbols, edgesCount)
			}

			indexedSymbols := view.AllSymbols()
			vectors := make(map[string][]float64, len(indexedSymbols))
			for i, symbol := range indexedSymbols {
				vector := []float64{0, 1, 0}
				if i == 0 {
					vector = []float64{1, 0, 0}
				}
				vectors[symbol.ID] = vector
			}
			prepared, err = store.PrepareEmbeddingRebuild(vectors, retrieval.DesiredMetadata("provider", "model", 3))
			if err != nil {
				t.Fatalf("PrepareEmbeddingRebuild: %v", err)
			}
			if _, err := store.CommitPrepared(prepared); err != nil {
				t.Fatalf("CommitPrepared embeddings: %v", err)
			}
			searcher, ok := store.(repository.DenseSearcher)
			if !ok {
				t.Fatalf("%T does not implement repository.DenseSearcher", store)
			}
			denseHits, err := searcher.SearchDense(context.Background(), []float64{1, 0, 0}, 2)
			if err != nil {
				t.Fatalf("SearchDense: %v", err)
			}
			if len(denseHits) == 0 || denseHits[0].Source != "dense" {
				t.Fatalf("dense hits = %+v, want a dense result", denseHits)
			}
			wantSnippet := denseHits[0].Symbol.DocComment
			if wantSnippet == "" {
				wantSnippet = denseHits[0].Symbol.Summary
			}
			if denseHits[0].Snippet != wantSnippet {
				t.Fatalf("dense snippet = %q, want %q", denseHits[0].Snippet, wantSnippet)
			}
		})
	}
}

func containsContractSymbol(symbols []domain.Symbol, id string) bool {
	for _, symbol := range symbols {
		if symbol.ID == id {
			return true
		}
	}
	return false
}

func containsContractName(symbols []domain.Symbol, name string) bool {
	for _, symbol := range symbols {
		if symbol.Name == name {
			return true
		}
	}
	return false
}

func assertContractGraphClosed(t *testing.T, nodes []domain.Symbol, edges []domain.Edge) {
	t.Helper()
	nodeIDs := make(map[string]struct{}, len(nodes))
	for _, node := range nodes {
		nodeIDs[node.ID] = struct{}{}
	}
	for _, edge := range edges {
		if _, ok := nodeIDs[edge.FromSymbolID]; !ok {
			t.Fatalf("edge %+v has source outside node set", edge)
		}
		if edge.ToSymbolID != "" {
			if _, ok := nodeIDs[edge.ToSymbolID]; !ok {
				t.Fatalf("edge %+v has target outside node set", edge)
			}
		}
	}
}

func TestStoreContractCompositeViewRejectsStaleFile(t *testing.T) {
	t.Parallel()
	store, err := repository.OpenSQLite(context.Background(), repository.SQLiteConfig{WorkspaceRoot: t.TempDir()})
	if err != nil {
		t.Fatalf("OpenSQLite: %v", err)
	}
	t.Cleanup(func() { _ = store.Close() })

	if _, err := store.CommitPrepared(mustPrepare(t, store, buildContractChangeSet(t, store.Version(), contractFiles()))); err != nil {
		t.Fatalf("seed commit: %v", err)
	}
	baseSnapshot := store.SnapshotID()
	baseHash, _ := store.FileHash("pkg/svc.go")

	updated := contractFiles()
	updated[0].File.Hash = "h-svc-updated"
	updated[0].Symbols[1].Code = "func (s *Service) Pay() { charge(); audit() }"
	if _, err := store.CommitPrepared(mustPrepare(t, store, buildContractChangeSet(t, store.Version(), updated))); err != nil {
		t.Fatalf("update commit: %v", err)
	}

	_, err = store.CompositeView(updated[0], repository.OverlayContext{
		DocumentID:      "doc",
		Path:            "pkg/svc.go",
		Version:         1,
		ContentHash:     "overlay-hash",
		BaseContentHash: baseHash,
		BaseSnapshotID:  baseSnapshot,
	})
	if !errors.Is(err, repository.ErrDocumentBaseStale) {
		t.Fatalf("CompositeView error = %v, want ErrDocumentBaseStale", err)
	}
}

func TestSQLiteCompositeViewUsesNativeUnsavedOverlay(t *testing.T) {
	t.Parallel()
	store, err := repository.OpenSQLite(context.Background(), repository.SQLiteConfig{WorkspaceRoot: t.TempDir()})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })
	if _, err := store.CommitPrepared(mustPrepare(t, store, buildContractChangeSet(t, store.Version(), contractFiles()))); err != nil {
		t.Fatal(err)
	}
	baseSnapshot := store.SnapshotID()
	baseHash, _ := store.FileHash("pkg/svc.go")
	rng := domain.Range{Start: domain.Position{Line: 8, Column: 1}, End: domain.Position{Line: 10, Column: 2}}
	overlayFile := domain.ParsedFile{
		File: domain.File{Path: "pkg/svc.go", Language: "go", Hash: "overlay-hash", Size: 240},
		Symbols: []domain.Symbol{
			{ID: "overlay-Service", Path: "pkg/svc.go", Name: "Service", Kind: "type", QualifiedName: "pkg.Service", Language: "go", Range: domain.Range{Start: domain.Position{Line: 1, Column: 1}, End: domain.Position{Line: 3, Column: 2}}, Signature: "type Service struct"},
			{ID: "overlay-Authorize", Path: "pkg/svc.go", Name: "Authorize", Kind: "method", QualifiedName: "pkg.Service.Authorize", Language: "go", Range: rng, Signature: "func (s *Service) Authorize()", Code: "func (s *Service) Authorize() {}"},
		},
		Edges: []domain.Edge{{FromSymbolID: "overlay-Service", ToSymbolID: "overlay-Authorize", Type: "contains", Path: "pkg/svc.go", Line: 8, Confidence: 1}},
	}
	view, err := store.CompositeView(overlayFile, repository.OverlayContext{
		DocumentID: "doc-native", Path: "pkg/svc.go", Version: 2,
		ContentHash: "overlay-hash", BaseContentHash: baseHash, BaseSnapshotID: baseSnapshot,
	})
	if err != nil {
		t.Fatal(err)
	}
	defer view.Close()
	if view.Rebased() || !strings.HasPrefix(view.ViewHash(), "view:") || view.Metadata().ID != baseSnapshot {
		t.Fatalf("composite metadata = hash %q rebased %v snapshot %q", view.ViewHash(), view.Rebased(), view.Metadata().ID)
	}
	resolved, ok := view.SymbolAt("pkg/svc.go", 8, 2)
	if !ok || resolved.Identity.Name != "Authorize" {
		t.Fatalf("overlay SymbolAt = %+v/%v", resolved, ok)
	}
	hits, err := view.Search("Authorize", 10)
	if err != nil || len(hits) == 0 || hits[0].Symbol.Name != "Authorize" {
		t.Fatalf("overlay Search = %+v, %v", hits, err)
	}
	for _, symbol := range view.SymbolsByPath("pkg/svc.go") {
		if symbol.Name == "Pay" {
			t.Fatalf("replaced file retained persisted symbol: %+v", symbol)
		}
	}
	persisted := store.Snapshot()
	defer persisted.Close()
	if _, found := persisted.SymbolAt("pkg/svc.go", 8, 2); found {
		t.Fatal("native composite mutated the persisted snapshot")
	}
}

func TestSQLiteContextMethodsPropagateCancellationAndDatabaseErrors(t *testing.T) {
	t.Parallel()
	store, err := repository.OpenSQLite(context.Background(), repository.SQLiteConfig{WorkspaceRoot: t.TempDir()})
	if err != nil {
		t.Fatal(err)
	}
	change := buildContractChangeSet(t, store.Version(), contractFiles())
	cancelled, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := store.SnapshotContext(cancelled); !errors.Is(err, context.Canceled) {
		t.Fatalf("SnapshotContext cancellation = %v", err)
	}
	if _, err := store.PrepareContext(cancelled, change); !errors.Is(err, context.Canceled) {
		t.Fatalf("PrepareContext cancellation = %v", err)
	}
	if _, _, err := store.FileHashContext(cancelled, "pkg/svc.go"); !errors.Is(err, context.Canceled) {
		t.Fatalf("FileHashContext cancellation = %v", err)
	}
	if err := store.Close(); err != nil {
		t.Fatal(err)
	}
	if _, err := store.SnapshotContext(context.Background()); err == nil {
		t.Fatal("SnapshotContext returned an empty view after database close")
	}
	if _, err := store.PrepareContext(context.Background(), change); err == nil || errors.Is(err, repository.ErrVersionConflict) {
		t.Fatalf("PrepareContext after close = %v, want database error rather than version conflict", err)
	}
}

func TestStoreContractEmbeddingRebuild(t *testing.T) {
	t.Parallel()
	store, err := repository.OpenSQLite(context.Background(), repository.SQLiteConfig{WorkspaceRoot: t.TempDir()})
	if err != nil {
		t.Fatalf("OpenSQLite: %v", err)
	}
	t.Cleanup(func() { _ = store.Close() })
	if _, err := store.CommitPrepared(mustPrepare(t, store, buildContractChangeSet(t, store.Version(), contractFiles()))); err != nil {
		t.Fatalf("seed commit: %v", err)
	}

	vectors := map[string][]float64{}
	for _, symbol := range store.AllSymbols() {
		if symbol.Kind == domain.KindFile {
			continue
		}
		vectors[symbol.ID] = []float64{1, 0, 0}
	}
	prepared, err := store.PrepareEmbeddingRebuild(vectors, retrieval.DesiredMetadata("provider", "model", 3))
	if err != nil {
		t.Fatalf("PrepareEmbeddingRebuild: %v", err)
	}
	if _, err := store.CommitPrepared(prepared); err != nil {
		t.Fatalf("CommitPrepared embeddings: %v", err)
	}
	if store.EmbeddingCount() != len(vectors) {
		t.Fatalf("EmbeddingCount = %d, want %d", store.EmbeddingCount(), len(vectors))
	}
	metadata := store.EmbeddingMetadata()
	if metadata.Provider != "provider" || metadata.Model != "model" || metadata.Dimension != 3 {
		t.Fatalf("metadata = %+v", metadata)
	}
}

func mustPrepare(t *testing.T, store repository.Store, change *changeset.ChangeSet) repository.PreparedCommit {
	t.Helper()
	prepared, err := store.Prepare(change)
	if err != nil {
		t.Fatalf("Prepare: %v", err)
	}
	return prepared
}

func buildContractChangeSet(t *testing.T, expected uint64, files []domain.ParsedFile) *changeset.ChangeSet {
	t.Helper()
	builder := changeset.NewBuilder().WithExpectedVersion(expected)
	for _, file := range files {
		builder.Upsert(file)
	}
	change, err := builder.Build(time.Unix(3000, 0).UTC())
	if err != nil {
		t.Fatalf("build changeset: %v", err)
	}
	return change
}

func contractFiles() []domain.ParsedFile {
	rng := func(l int) domain.Range {
		return domain.Range{Start: domain.Position{Line: l, Column: 1}, End: domain.Position{Line: l + 1, Column: 2}}
	}
	return []domain.ParsedFile{
		{
			File: domain.File{Path: "pkg/svc.go", Language: "go", Hash: "h-svc", Size: 200, ModifiedAt: time.Unix(1000, 0).UTC()},
			Symbols: []domain.Symbol{
				{ID: "legacy-Service", Path: "pkg/svc.go", Name: "Service", Kind: "type", QualifiedName: "pkg.Service", Language: "go", Range: rng(1), Signature: "type Service struct"},
				{ID: "legacy-Pay", Path: "pkg/svc.go", Name: "Pay", Kind: "method", QualifiedName: "pkg.Service.Pay", Language: "go", Range: rng(5), Signature: "func (s *Service) Pay()", Code: "func (s *Service) Pay() { Charge() }"},
			},
			Edges: []domain.Edge{
				{FromSymbolID: "legacy-Service", ToSymbolID: "legacy-Pay", Type: "contains", Path: "pkg/svc.go", Line: 5, Confidence: 1},
				{FromSymbolID: "legacy-Pay", ToName: "Charge", Type: "calls", Path: "pkg/svc.go", Line: 6, Confidence: 0.9},
			},
		},
		{
			File: domain.File{Path: "pkg/util.go", Language: "go", Hash: "h-util", Size: 80, ModifiedAt: time.Unix(2000, 0).UTC()},
			Symbols: []domain.Symbol{
				{ID: "legacy-Service-again", Path: "pkg/util.go", Name: "Service", Kind: "type", QualifiedName: "pkg.Service", Language: "go", Range: rng(10), Signature: "type Service struct"},
				{ID: "legacy-Pay-again", Path: "pkg/util.go", Name: "Pay", Kind: "method", QualifiedName: "pkg.Service.Pay", Language: "go", Range: rng(14), Signature: "func (s *Service) Pay()", Code: "func (s *Service) Pay() { Charge() }"},
				{ID: "legacy-Charge", Path: "pkg/util.go", Name: "Charge", Kind: "func", QualifiedName: "pkg.Charge", Language: "go", Range: rng(1), Signature: "func Charge()"},
			},
			Edges: []domain.Edge{
				{FromSymbolID: "legacy-Service-again", ToSymbolID: "legacy-Pay-again", Type: "contains", Path: "pkg/util.go", Line: 14, Confidence: 1},
			},
		},
	}
}
