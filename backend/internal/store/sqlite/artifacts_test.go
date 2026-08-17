package sqlite

import (
	"context"
	"errors"
	"fmt"
	"testing"
	"unicode/utf8"

	"github.com/ThalesMMS/CodeAtlas/internal/apperror"
)

func TestBoundPreservesUTF8AtByteLimit(t *testing.T) {
	t.Parallel()
	if got := bound("123456789érest", 10); !utf8.ValidString(got) {
		t.Fatalf("bound returned invalid UTF-8")
	}
}

// artifactFixture commits the sample files and returns the artifact store, the
// current SnapshotID and the real symbol handles for dependencies.
func artifactFixture(t *testing.T) (*ArtifactStore, string, []string) {
	t.Helper()
	store := openStore(t)
	if _, err := store.Commit(context.Background(), buildChangeSet(t, 0, sampleFiles())); err != nil {
		t.Fatalf("commit: %v", err)
	}
	metadata, _ := store.Metadata(context.Background())
	view, _ := store.OpenReadView(context.Background())
	var handles []string
	for _, symbol := range view.AllSymbols() {
		handles = append(handles, symbol.ID)
	}
	_ = view.Close()
	if len(handles) < 2 {
		t.Fatalf("need at least two symbols, got %d", len(handles))
	}
	return store.Artifacts(), string(metadata.ID), handles
}

func candidate(snapshotID, key, symbolHandle string) ArtifactCandidate {
	return ArtifactCandidate{
		Type: "deepwiki", Key: key, InputSnapshotID: snapshotID, ContextPackHash: "cp1",
		PolicyVersion: "p1", PromptVersion: "pr1", OutputSchemaVersion: "o1", RendererVersion: "r1",
		Provider: "prov", Model: "model", Title: "Overview", Payload: `{"sections":[]}`,
		RenderedMarkdown: "# Overview", Metadata: `{}`,
		Dependencies: []ArtifactDependency{
			{Kind: "symbol", SymbolID: symbolHandle, ContentHash: "h1"},
			{Kind: "snapshot", ContentHash: snapshotID},
		},
	}
}

func TestPublishCreatesVersionAndHead(t *testing.T) {
	t.Parallel()
	artifacts, snapshotID, handles := artifactFixture(t)
	published, err := artifacts.Publish(context.Background(), candidate(snapshotID, "overview", handles[0]))
	if err != nil {
		t.Fatalf("Publish: %v", err)
	}
	if published.Revision != 1 || published.PayloadHash == "" {
		t.Fatalf("unexpected artifact: %+v", published)
	}
	if len(published.Dependencies) != 2 {
		t.Fatalf("dependencies not persisted: %+v", published.Dependencies)
	}
	head, err := artifacts.GetHead(context.Background(), "deepwiki", "overview")
	if err != nil {
		t.Fatal(err)
	}
	if head.Status != StatusCurrent || head.CurrentID != published.ID || head.LatestRevision != 1 {
		t.Fatalf("head not advanced: %+v", head)
	}
}

func TestRepublishBumpsRevision(t *testing.T) {
	t.Parallel()
	artifacts, snapshotID, handles := artifactFixture(t)
	first, _ := artifacts.Publish(context.Background(), candidate(snapshotID, "overview", handles[0]))
	second, err := artifacts.Publish(context.Background(), candidate(snapshotID, "overview", handles[0]))
	if err != nil {
		t.Fatalf("republish: %v", err)
	}
	if second.Revision != 2 {
		t.Fatalf("revision = %d, want 2", second.Revision)
	}
	head, _ := artifacts.GetHead(context.Background(), "deepwiki", "overview")
	if head.CurrentID != second.ID || head.CurrentID == first.ID {
		t.Fatalf("head should point to the latest version: %+v", head)
	}
	// The first version remains retrievable (immutable history).
	if _, err := artifacts.Get(context.Background(), first.ID); err != nil {
		t.Fatalf("first version should still exist: %v", err)
	}
}

func TestPublishRejectsStaleSnapshot(t *testing.T) {
	t.Parallel()
	artifacts, _, handles := artifactFixture(t)
	_, err := artifacts.Publish(context.Background(), candidate("sha256:wrong", "overview", handles[0]))
	var appErr *apperror.AppError
	if !errors.As(err, &appErr) || appErr.Code != apperror.CodeArtifactInputSnapshotStale {
		t.Fatalf("expected ARTIFACT_INPUT_SNAPSHOT_STALE, got %v", err)
	}
}

func TestPublishValidatesFileAndSymbolDependenciesInBatches(t *testing.T) {
	t.Parallel()
	t.Run("valid mixed dependencies", func(t *testing.T) {
		artifacts, snapshotID, handles := artifactFixture(t)
		input := candidate(snapshotID, "valid-batch", handles[0])
		input.Dependencies = []ArtifactDependency{
			{Kind: "symbol", SymbolID: handles[0]},
			{Kind: "symbol", SymbolID: handles[1]},
			{Kind: "file", Path: "pkg/svc.go", ContentHash: "h-svc"},
			{Kind: "file", Path: "pkg/util.go", ContentHash: "h-util"},
		}
		if _, err := artifacts.Publish(context.Background(), input); err != nil {
			t.Fatalf("Publish valid batch: %v", err)
		}
	})

	tests := []struct {
		name       string
		dependency ArtifactDependency
	}{
		{name: "missing symbol", dependency: ArtifactDependency{Kind: "symbol", SymbolID: "missing"}},
		{name: "missing file", dependency: ArtifactDependency{Kind: "file", Path: "missing.go"}},
		{name: "changed file", dependency: ArtifactDependency{Kind: "file", Path: "pkg/svc.go", ContentHash: "old-hash"}},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			artifacts, snapshotID, handles := artifactFixture(t)
			input := candidate(snapshotID, "invalid-batch", handles[0])
			input.Dependencies = append(input.Dependencies,
				ArtifactDependency{Kind: "file", Path: "pkg/util.go", ContentHash: "h-util"},
				ArtifactDependency{Kind: "symbol", SymbolID: handles[1]},
				tc.dependency,
			)
			_, err := artifacts.Publish(context.Background(), input)
			var appErr *apperror.AppError
			if !errors.As(err, &appErr) || appErr.Code != apperror.CodeArtifactInputSnapshotStale {
				t.Fatalf("Publish error = %v, want ARTIFACT_INPUT_SNAPSHOT_STALE", err)
			}
		})
	}
}

func TestDependencyLoadersChunkBeyondSQLiteVariableLimit(t *testing.T) {
	t.Parallel()
	artifacts, _, handles := artifactFixture(t)
	tx, err := artifacts.db.Writer().BeginTx(context.Background(), nil)
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback() //nolint:errcheck

	paths := make([]string, 0, dependencyQueryBatchSize+2)
	symbols := make([]string, 0, dependencyQueryBatchSize+2)
	for index := 0; index < dependencyQueryBatchSize+1; index++ {
		paths = append(paths, fmt.Sprintf("missing/%d.go", index))
		symbols = append(symbols, fmt.Sprintf("missing-symbol-%d", index))
	}
	paths = append(paths, "pkg/svc.go")
	symbols = append(symbols, handles[0])

	hashes, err := loadFileHashes(context.Background(), tx, paths)
	if err != nil {
		t.Fatalf("loadFileHashes: %v", err)
	}
	existing, err := loadExistingSymbols(context.Background(), tx, symbols)
	if err != nil {
		t.Fatalf("loadExistingSymbols: %v", err)
	}
	if hashes["pkg/svc.go"] != "h-svc" {
		t.Fatalf("file hash batch result = %v", hashes)
	}
	if _, ok := existing[handles[0]]; !ok {
		t.Fatalf("symbol batch result omitted %q", handles[0])
	}
}

func TestRecordFailurePreservesCurrent(t *testing.T) {
	t.Parallel()
	artifacts, snapshotID, handles := artifactFixture(t)
	published, _ := artifacts.Publish(context.Background(), candidate(snapshotID, "overview", handles[0]))
	err := artifacts.RecordFailure(context.Background(),
		GenerationAttempt{Type: "deepwiki", Key: "overview", InputSnapshotID: snapshotID},
		apperror.ProviderTimeout(errors.New("upstream slow")))
	if err != nil {
		t.Fatal(err)
	}
	head, _ := artifacts.GetHead(context.Background(), "deepwiki", "overview")
	if head.CurrentID != published.ID {
		t.Fatalf("a refresh failure must preserve the current version: %+v", head)
	}
	if head.Status != StatusCurrent || head.LastErrorCode == "" {
		t.Fatalf("expected current status with a recorded error: %+v", head)
	}
}

func TestRecordFailureWithoutPriorIsFailed(t *testing.T) {
	t.Parallel()
	artifacts, snapshotID, _ := artifactFixture(t)
	err := artifacts.RecordFailure(context.Background(),
		GenerationAttempt{Type: "deepwiki", Key: "fresh", InputSnapshotID: snapshotID},
		apperror.ModelOutputInvalid(errors.New("bad json")))
	if err != nil {
		t.Fatal(err)
	}
	head, _ := artifacts.GetHead(context.Background(), "deepwiki", "fresh")
	if head.Status != StatusFailed || head.CurrentID != "" {
		t.Fatalf("a first-time failure should be failed with no current: %+v", head)
	}
}

func TestInvalidateMarksDependentHeadsStale(t *testing.T) {
	t.Parallel()
	artifacts, snapshotID, handles := artifactFixture(t)
	// Dependent head (depends on `handle`) and an independent head (snapshot dep only
	// uses a different key but the snapshot dep makes it stale on any change — so use
	// a head with only an unrelated symbol dep to prove independence).
	if _, err := artifacts.Publish(context.Background(), candidate(snapshotID, "overview", handles[0])); err != nil {
		t.Fatal(err)
	}
	independent := candidate(snapshotID, "isolated", handles[0])
	independent.Dependencies = []ArtifactDependency{{Kind: "symbol", SymbolID: handles[1], ContentHash: "h"}}
	if _, err := artifacts.Publish(context.Background(), independent); err != nil {
		t.Fatal(err)
	}

	result, err := artifacts.Invalidate(context.Background(), PublishedChangeSet{ChangedSymbolIDs: []string{handles[0]}})
	if err != nil {
		t.Fatalf("Invalidate: %v", err)
	}
	staleKeys := map[string]bool{}
	for _, head := range result.StaleHeads {
		staleKeys[head.Key] = true
	}
	if !staleKeys["overview"] {
		t.Fatal("the head depending on the changed symbol should be stale")
	}
	if staleKeys["isolated"] {
		t.Fatal("the independent head should NOT be stale")
	}
	// The stale head preserves its current artifact.
	head, _ := artifacts.GetHead(context.Background(), "deepwiki", "overview")
	if head.Status != StatusStale || head.CurrentID == "" {
		t.Fatalf("stale head must keep its current version: %+v", head)
	}
}

func TestGetHeadUnknownIsNotGenerated(t *testing.T) {
	t.Parallel()
	artifacts, _, _ := artifactFixture(t)
	head, err := artifacts.GetHead(context.Background(), "deepwiki", "never")
	if err != nil {
		t.Fatal(err)
	}
	if head.Status != StatusNotGenerated {
		t.Fatalf("status = %q, want not_generated", head.Status)
	}
}
