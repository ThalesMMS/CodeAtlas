package repository

import (
	"context"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/ThalesMMS/CodeAtlas/internal/changeset"
	"github.com/ThalesMMS/CodeAtlas/internal/domain"
	sqlitestore "github.com/ThalesMMS/CodeAtlas/internal/store/sqlite"
)

func TestJSONPublishCodemapFailsExplicitly(t *testing.T) {
	t.Parallel()
	store, err := OpenJSON(filepath.Join(t.TempDir(), "index.json"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })

	_, err = store.PublishCodemap(domain.Codemap{Artifact: domain.ArtifactMetadata{ID: "fake-success"}})
	if err == nil || !strings.Contains(err.Error(), "does not support artifact persistence") {
		t.Fatalf("PublishCodemap error = %v, want explicit unsupported persistence error", err)
	}
}

func TestSQLitePublishCodemapCreatesArtifactHead(t *testing.T) {
	t.Parallel()
	store, err := OpenSQLite(context.Background(), SQLiteConfig{WorkspaceRoot: t.TempDir()})
	if err != nil {
		t.Fatalf("OpenSQLite: %v", err)
	}
	defer store.Close()
	sqliteStore := store.(*sqliteAdapter)

	parsed := domain.ParsedFile{
		File: domain.File{Path: "pkg/pay.go", Language: "go", Hash: "h1", Size: 10, ModifiedAt: time.Unix(1000, 0).UTC()},
		Symbols: []domain.Symbol{{
			ID: "sym-Pay", Path: "pkg/pay.go", Name: "Pay", QualifiedName: "pkg.Pay", Kind: "func", Language: "go",
			Range: domain.Range{Start: domain.Position{Line: 1, Column: 1}, End: domain.Position{Line: 2, Column: 1}},
		}},
	}
	change, err := changeset.NewBuilder().WithExpectedVersion(store.Version()).Upsert(parsed).Build(time.Unix(2000, 0).UTC())
	if err != nil {
		t.Fatalf("build changeset: %v", err)
	}
	prepared, err := store.Prepare(change)
	if err != nil {
		t.Fatalf("Prepare: %v", err)
	}
	if _, err := store.CommitPrepared(prepared); err != nil {
		t.Fatalf("CommitPrepared: %v", err)
	}
	symbol := store.AllSymbols()[0]

	metadata, err := store.PublishCodemap(domain.Codemap{
		Query:               "pay",
		Title:               "Payments",
		Overview:            "Payment flow",
		Provider:            "test-provider",
		GeneratedAt:         time.Unix(3000, 0).UTC(),
		SnapshotID:          store.SnapshotID(),
		ContextPackHash:     "ctx:pay",
		PolicyVersion:       "codemap-policy/v1",
		OutputSchemaVersion: "codemap-schema/v1",
		Artifact: domain.ArtifactMetadata{
			Type: "codemap", Key: "query/pay", PromptVersion: "codemap.v1", Model: "test-model",
			Dependencies: []domain.Dependency{{Kind: "symbol", SymbolID: domain.SymbolID(symbol.ID), ContentHash: "symbol-hash"}},
		},
	})
	if err != nil {
		t.Fatalf("PublishCodemap: %v", err)
	}
	if metadata.ID == "" || metadata.Revision != 1 || metadata.Status != domain.ArtifactCurrent {
		t.Fatalf("metadata = %+v, want current revision 1", metadata)
	}
	heads, err := sqliteStore.inner.Artifacts().ListHeads(context.Background(), sqlitestore.ArtifactFilter{Type: "codemap"})
	if err != nil {
		t.Fatalf("ListHeads: %v", err)
	}
	if len(heads) != 1 || heads[0].Key != "query/pay" || heads[0].Status != sqlitestore.StatusCurrent || string(heads[0].CurrentID) != string(metadata.ID) {
		t.Fatalf("heads = %+v, metadata=%+v", heads, metadata)
	}
	loaded, err := store.CodemapByArtifactIDContext(context.Background(), metadata.ID)
	if err != nil {
		t.Fatalf("CodemapByArtifactIDContext: %v", err)
	}
	if loaded.Title != "Payments" || loaded.Artifact.ID != metadata.ID || loaded.Artifact.Status != domain.ArtifactCurrent {
		t.Fatalf("loaded codemap = %+v", loaded)
	}
	if loaded.SnapshotID != store.SnapshotID() || loaded.Artifact.InputSnapshotID != store.SnapshotID() {
		t.Fatalf("loaded snapshot = %q artifact snapshot = %q", loaded.SnapshotID, loaded.Artifact.InputSnapshotID)
	}

	summaries, err := store.ListCodemapsContext(context.Background())
	if err != nil {
		t.Fatalf("ListCodemapsContext: %v", err)
	}
	if len(summaries) != 1 {
		t.Fatalf("summaries = %+v, want one entry", summaries)
	}
	summary := summaries[0]
	if summary.ArtifactID != metadata.ID || summary.Title != "Payments" || summary.Query != "pay" ||
		summary.Status != string(domain.ArtifactCurrent) || summary.Revision != 1 || summary.Provider != "test-provider" {
		t.Fatalf("summary = %+v", summary)
	}
	if summary.NodeCount != 0 || summary.EdgeCount != 0 {
		t.Fatalf("summary counts = %+v, want zero for an empty graph", summary)
	}
}
