package indexer

import (
	"context"
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/ThalesMMS/CodeAtlas/internal/ai"
	"github.com/ThalesMMS/CodeAtlas/internal/contenthash"
	"github.com/ThalesMMS/CodeAtlas/internal/domain"
	"github.com/ThalesMMS/CodeAtlas/internal/mutation"
	codeparser "github.com/ThalesMMS/CodeAtlas/internal/parser"
	"github.com/ThalesMMS/CodeAtlas/internal/repository"
	"github.com/ThalesMMS/CodeAtlas/internal/retrieval"
	"github.com/ThalesMMS/CodeAtlas/internal/service"
)

func newTestIndexer(t *testing.T, parser Parser, provider ai.Provider, embeddings bool) (*Indexer, string, repository.Store) {
	t.Helper()
	root := t.TempDir()
	repository, err := repository.OpenJSON(filepath.Join(t.TempDir(), "index.json"))
	if err != nil {
		t.Fatalf("Open() error = %v", err)
	}
	if provider == nil {
		provider = ai.Disabled{}
	}
	retriever := retrieval.NewHybrid(repository, provider, embeddings)
	return New(root, 1_500_000, parser, repository, retriever), root, repository
}

func writeSource(t *testing.T, root, relative, content string) {
	t.Helper()
	path := filepath.Join(root, filepath.FromSlash(relative))
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
}

func waitIndexEventType(t *testing.T, channel <-chan domain.IndexEvent, eventType string) domain.IndexEvent {
	t.Helper()
	deadline := time.After(2 * time.Second)
	select {
	case event := <-channel:
		if event.Type == eventType {
			return event
		}
	case <-deadline:
		t.Fatalf("timed out waiting for index event %s", eventType)
	}
	for {
		select {
		case event := <-channel:
			if event.Type == eventType {
				return event
			}
		case <-deadline:
			t.Fatalf("timed out waiting for index event %s", eventType)
			return domain.IndexEvent{}
		}
	}
}

const goSource = "package sample\n\nfunc Run() int { return 1 }\n"

func TestNormalizeRequestedPathsUsesCanonicalWorkspacePaths(t *testing.T) {
	t.Parallel()
	paths, err := normalizeRequestedPaths([]string{`pkg\b.go`, "./pkg/a.go", "pkg/a.go"})
	if err != nil {
		t.Fatalf("normalizeRequestedPaths() error = %v", err)
	}
	if got, want := strings.Join(paths, ","), "pkg/a.go,pkg/b.go"; got != want {
		t.Fatalf("normalizeRequestedPaths() = %q, want %q", got, want)
	}
	if _, err := normalizeRequestedPaths([]string{`..\secret.go`}); err == nil {
		t.Fatal("normalizeRequestedPaths() accepted a workspace escape")
	}
}

type failingParser struct{}

func (failingParser) Parse(string, []byte) ([]domain.Symbol, []domain.Edge, string, error) {
	return nil, nil, "", errors.New("synthetic parse failure")
}

type selectiveFailParser struct {
	delegate Parser
	badPath  string
}

func (p selectiveFailParser) Parse(path string, source []byte) ([]domain.Symbol, []domain.Edge, string, error) {
	if path == p.badPath {
		return nil, nil, "", errors.New("synthetic parse failure")
	}
	return p.delegate.Parse(path, source)
}

type blockingParser struct {
	started chan struct{}
	release chan struct{}
	once    sync.Once
}

func (p *blockingParser) Parse(path string, _ []byte) ([]domain.Symbol, []domain.Edge, string, error) {
	p.once.Do(func() { close(p.started) })
	<-p.release
	return []domain.Symbol{{ID: path, Path: path, Name: path, Kind: "file", Range: domain.Range{Start: domain.Position{Line: 1, Column: 1}, End: domain.Position{Line: 2, Column: 1}}}}, nil, "go", nil
}

type failingEmbedProvider struct{ ai.Disabled }

func (failingEmbedProvider) Available() bool { return true }
func (failingEmbedProvider) Embed(context.Context, []string) ([][]float64, error) {
	return nil, errors.New("synthetic embedding failure")
}

func TestScanHappyPathAddsUpdatesDeletes(t *testing.T) {
	t.Parallel()
	indexer, root, repository := newTestIndexer(t, codeparser.New(), nil, false)
	writeSource(t, root, "a.go", goSource)
	writeSource(t, root, "b.go", goSource)

	if err := indexer.Scan(context.Background()); err != nil {
		t.Fatalf("first Scan() error = %v", err)
	}
	if _, ok := repository.FileHash("a.go"); !ok {
		t.Fatal("a.go not indexed")
	}
	versionAfterFirst := repository.Version()

	// Update a.go, delete b.go, add c.go — all in one batch.
	writeSource(t, root, "a.go", "package sample\n\nfunc Run() int { return 2 }\n")
	if err := os.Remove(filepath.Join(root, "b.go")); err != nil {
		t.Fatal(err)
	}
	writeSource(t, root, "c.go", goSource)

	if err := indexer.Scan(context.Background()); err != nil {
		t.Fatalf("second Scan() error = %v", err)
	}
	if _, ok := repository.FileHash("b.go"); ok {
		t.Fatal("b.go was not deleted in the batch")
	}
	if _, ok := repository.FileHash("c.go"); !ok {
		t.Fatal("c.go was not added in the batch")
	}
	if repository.Version() <= versionAfterFirst {
		t.Fatal("version did not advance after a real change")
	}
	if indexer.State() != ScanComplete {
		t.Fatalf("state = %s, want complete", indexer.State())
	}
}

func TestScanIndexesSwiftSources(t *testing.T) {
	t.Parallel()
	indexer, root, repository := newTestIndexer(t, codeparser.New(), nil, false)
	writeSource(t, root, "Sources/Commerce/Order.swift", "import Foundation\nstruct Order { let id: String }\n")
	if err := indexer.Scan(context.Background()); err != nil {
		t.Fatalf("Scan() error = %v", err)
	}
	view := repository.Snapshot()
	defer view.Close()
	var file domain.File
	var ok bool
	for _, candidate := range view.Files() {
		if candidate.Path == "Sources/Commerce/Order.swift" {
			file, ok = candidate, true
			break
		}
	}
	if !ok || file.Language != "swift" {
		t.Fatalf("indexed Swift file = %#v, found=%v", file, ok)
	}
	var foundOrder bool
	for _, symbol := range view.SymbolsByPath("Sources/Commerce/Order.swift") {
		if symbol.Name == "Order" && symbol.Kind == domain.KindType {
			foundOrder = true
		}
	}
	if !foundOrder {
		t.Fatalf("Swift Order symbol missing: %#v", view.SymbolsByPath("Sources/Commerce/Order.swift"))
	}
}

func TestScanIndexesPythonSources(t *testing.T) {
	t.Parallel()
	indexer, root, repository := newTestIndexer(t, codeparser.New(), nil, false)
	writeSource(t, root, "commerce/order.py", "class Order:\n    id: str\n")
	if err := indexer.Scan(context.Background()); err != nil {
		t.Fatalf("Scan() error = %v", err)
	}
	view := repository.Snapshot()
	defer view.Close()
	var file domain.File
	var ok bool
	for _, candidate := range view.Files() {
		if candidate.Path == "commerce/order.py" {
			file, ok = candidate, true
			break
		}
	}
	if !ok || file.Language != "python" {
		t.Fatalf("indexed Python file = %#v, found=%v", file, ok)
	}
	for _, symbol := range view.SymbolsByPath("commerce/order.py") {
		if symbol.Name == "Order" && symbol.Kind == domain.KindClass {
			return
		}
	}
	t.Fatalf("Python Order symbol missing: %#v", view.SymbolsByPath("commerce/order.py"))
}

func TestScanIndexesRustSources(t *testing.T) {
	t.Parallel()
	indexer, root, repository := newTestIndexer(t, codeparser.New(), nil, false)
	writeSource(t, root, "src/order.rs", "pub struct Order { pub id: String }\n")
	if err := indexer.Scan(context.Background()); err != nil {
		t.Fatalf("Scan() error = %v", err)
	}
	view := repository.Snapshot()
	defer view.Close()
	var file domain.File
	var ok bool
	for _, candidate := range view.Files() {
		if candidate.Path == "src/order.rs" {
			file, ok = candidate, true
			break
		}
	}
	if !ok || file.Language != "rust" {
		t.Fatalf("indexed Rust file = %#v, found=%v", file, ok)
	}
	for _, symbol := range view.SymbolsByPath("src/order.rs") {
		if symbol.Name == "Order" && symbol.Kind == domain.KindType {
			return
		}
	}
	t.Fatalf("Rust Order symbol missing: %#v", view.SymbolsByPath("src/order.rs"))
}

func TestScanNoChangesIsNoop(t *testing.T) {
	t.Parallel()
	indexer, root, repository := newTestIndexer(t, codeparser.New(), nil, false)
	writeSource(t, root, "a.go", goSource)
	if err := indexer.Scan(context.Background()); err != nil {
		t.Fatalf("Scan() error = %v", err)
	}
	version := repository.Version()
	if err := indexer.Scan(context.Background()); err != nil {
		t.Fatalf("second Scan() error = %v", err)
	}
	if repository.Version() != version {
		t.Fatalf("no-op scan changed version to %d, want %d", repository.Version(), version)
	}
}

func TestScanIndexesAllowlistedProjectFilesAndNeverEnv(t *testing.T) {
	t.Parallel()
	indexer, root, repository := newTestIndexer(t, codeparser.New(), nil, false)
	writeSource(t, root, "go.mod", "module example.com/sample\n\ngo 1.23\n")
	writeSource(t, root, "go.sum", strings.Repeat("dependency checksum\n", 600))
	writeSource(t, root, "README.md", strings.Repeat("documented setup line\n", 600))
	writeSource(t, root, ".env.example", "PORT=8080\n")
	writeSource(t, root, ".env", "REAL_SECRET=must-not-be-indexed\n")
	writeSource(t, root, "notes.txt", "not allowlisted\n")

	if err := indexer.Scan(context.Background()); err != nil {
		t.Fatalf("Scan() error = %v", err)
	}
	view := repository.Snapshot()
	defer view.Close() //nolint:errcheck
	files := make(map[string]domain.File)
	for _, file := range view.Files() {
		files[file.Path] = file
	}
	if got := files["go.mod"]; got.Language != "gomod" || !strings.Contains(got.Content, "go 1.23") || got.ContentTruncated {
		t.Fatalf("go.mod = %+v", got)
	}
	if got := files["README.md"]; got.Language != "markdown" || !got.ContentTruncated || len(got.Content) > ProjectFileContentLimit {
		t.Fatalf("README.md bounded preview = language %q truncated=%v bytes=%d", got.Language, got.ContentTruncated, len(got.Content))
	}
	if got, ok := files["go.sum"]; !ok || got.Content != "" || got.ContentTruncated {
		t.Fatalf("go.sum must be metadata-only, got %+v present=%v", got, ok)
	}
	if got := files[".env.example"]; got.Content != "PORT=8080\n" {
		t.Fatalf(".env.example = %+v", got)
	}
	for _, forbidden := range []string{".env", "notes.txt"} {
		if _, ok := files[forbidden]; ok {
			t.Fatalf("%s must never be indexed", forbidden)
		}
	}
}

func assertUnchanged(t *testing.T, indexer *Indexer, repository repository.Store, beforeVersion uint64, beforeFiles int) {
	t.Helper()
	if repository.Version() != beforeVersion {
		t.Fatalf("store version advanced to %d after a failed scan", repository.Version())
	}
	files, _, _, _, _ := repository.Counts()
	if files != beforeFiles {
		t.Fatalf("store file count changed to %d after a failed scan", files)
	}
	if indexer.State() != ScanFailed {
		t.Fatalf("state = %s, want failed", indexer.State())
	}
}

func TestScanWalkErrorIsFatal(t *testing.T) {
	t.Parallel()
	indexer, root, repository := newTestIndexer(t, codeparser.New(), nil, false)
	writeSource(t, root, "a.go", goSource)
	if err := indexer.Scan(context.Background()); err != nil {
		t.Fatalf("setup Scan() error = %v", err)
	}
	version, files := repository.Version(), 1
	if err := os.RemoveAll(root); err != nil {
		t.Fatal(err)
	}
	if err := indexer.Scan(context.Background()); err == nil {
		t.Fatal("Scan succeeded despite a walk error")
	}
	assertUnchanged(t, indexer, repository, version, files)
}

func TestScanReadErrorIsFatal(t *testing.T) {
	t.Parallel()
	indexer, root, repository := newTestIndexer(t, codeparser.New(), nil, false)
	writeSource(t, root, "a.go", goSource)
	if err := indexer.Scan(context.Background()); err != nil {
		t.Fatalf("setup Scan() error = %v", err)
	}
	version := repository.Version()
	writeSource(t, root, "b.go", goSource)
	realReadFile := os.ReadFile
	indexer.readFile = func(name string) ([]byte, error) {
		if filepath.Base(name) == "b.go" {
			return nil, &fs.PathError{Op: "open", Path: name, Err: fs.ErrPermission}
		}
		return realReadFile(name)
	}
	if err := indexer.Scan(context.Background()); err == nil {
		t.Fatal("Scan succeeded despite a controlled read error")
	}
	assertUnchanged(t, indexer, repository, version, 1)
}

func TestScanParseErrorQuarantinesFileAndCommitsOthers(t *testing.T) {
	t.Parallel()
	indexer, root, repository := newTestIndexer(t, codeparser.New(), nil, false)
	writeSource(t, root, "bad.go", "package sample\n\nfunc Bad() int { return 1 }\n")
	writeSource(t, root, "good.go", "package sample\n\nfunc Good() int { return 1 }\n")
	if err := indexer.Scan(context.Background()); err != nil {
		t.Fatalf("setup Scan() error = %v", err)
	}
	baseVersion := repository.Version()
	badHash, _ := repository.FileHash("bad.go")
	goodHash, _ := repository.FileHash("good.go")

	writeSource(t, root, "bad.go", "package sample\n\nfunc Bad() int { return 2 }\n")
	writeSource(t, root, "good.go", "package sample\n\nfunc Good() int { return 2 }\n")
	indexer.parser = selectiveFailParser{delegate: codeparser.New(), badPath: "bad.go"}
	channel, cancel := indexer.Broker().Subscribe()
	defer cancel()

	if err := indexer.Scan(context.Background()); err != nil {
		t.Fatalf("Scan() error = %v", err)
	}
	if repository.Version() != baseVersion+1 {
		t.Fatalf("version = %d, want %d", repository.Version(), baseVersion+1)
	}
	if got, _ := repository.FileHash("bad.go"); got != badHash {
		t.Fatalf("quarantined bad.go hash = %q, want prior %q", got, badHash)
	}
	if got, _ := repository.FileHash("good.go"); got == goodHash {
		t.Fatal("good.go was not updated after another file failed to parse")
	}
	quarantined := waitIndexEventType(t, channel, "index.file.quarantined")
	if quarantined.Path != "bad.go" || quarantined.Error == nil || quarantined.Error.Code != "SOURCE_PARSE_FAILED" {
		t.Fatalf("quarantine event = %#v", quarantined)
	}
}

func TestScanEmbeddingErrorIsFatal(t *testing.T) {
	t.Parallel()
	indexer, root, repository := newTestIndexer(t, codeparser.New(), failingEmbedProvider{}, true)
	writeSource(t, root, "a.go", goSource)
	if err := indexer.Scan(context.Background()); err == nil {
		t.Fatal("Scan succeeded despite an embedding error")
	}
	assertUnchanged(t, indexer, repository, 0, 0)
	if len(repository.Embeddings()) != 0 {
		t.Fatal("failed embedding scan left orphan vectors")
	}
}

func TestScanCancellationIsFatal(t *testing.T) {
	t.Parallel()
	indexer, root, repository := newTestIndexer(t, codeparser.New(), nil, false)
	writeSource(t, root, "a.go", goSource)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if err := indexer.Scan(ctx); err == nil {
		t.Fatal("Scan succeeded despite a cancelled context")
	}
	if repository.Version() != 0 {
		t.Fatalf("cancelled scan advanced version to %d", repository.Version())
	}
}

func TestScanReindexInProgress(t *testing.T) {
	t.Parallel()
	parser := &blockingParser{started: make(chan struct{}), release: make(chan struct{})}
	indexer, root, _ := newTestIndexer(t, parser, nil, false)
	writeSource(t, root, "a.go", goSource)

	done := make(chan error, 1)
	go func() { done <- indexer.Scan(context.Background()) }()
	<-parser.started // the first scan is now holding the run lock

	if err := indexer.Scan(context.Background()); !errors.Is(err, ErrIndexingInProgress) {
		t.Fatalf("concurrent Scan() error = %v, want ErrIndexingInProgress", err)
	}

	close(parser.release)
	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("first Scan() error = %v", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("first Scan did not finish")
	}
}

func TestReconcilePathsReadsFinalStateAndDeletesMissingInOneCommit(t *testing.T) {
	t.Parallel()
	indexer, root, repository := newTestIndexer(t, codeparser.New(), nil, false)
	writeSource(t, root, "a.go", goSource)
	writeSource(t, root, "b.go", goSource)
	if err := indexer.Scan(context.Background()); err != nil {
		t.Fatalf("setup Scan() error = %v", err)
	}
	version := repository.Version()

	writeSource(t, root, "a.go", "package sample\n\nfunc Run() int { return 2 }\n")
	if err := os.Remove(filepath.Join(root, "b.go")); err != nil {
		t.Fatal(err)
	}

	if err := indexer.ReconcilePaths(context.Background(), []string{"b.go", "./a.go", "a.go"}, version); err != nil {
		t.Fatalf("ReconcilePaths() error = %v", err)
	}
	if _, ok := repository.FileHash("b.go"); ok {
		t.Fatal("b.go was not deleted by path reconciliation")
	}
	if _, ok := repository.FileHash("a.go"); !ok {
		t.Fatal("a.go missing after path reconciliation")
	}
	if repository.Version() != version+1 {
		t.Fatalf("version = %d, want one commit to %d", repository.Version(), version+1)
	}
}

func TestReconcileSubtreeDiffsCurrentFilesAgainstIndexedPrefix(t *testing.T) {
	t.Parallel()
	indexer, root, repository := newTestIndexer(t, codeparser.New(), nil, false)
	writeSource(t, root, "pkg/a.go", goSource)
	writeSource(t, root, "pkg/old.go", goSource)
	writeSource(t, root, "other/keep.go", goSource)
	if err := indexer.Scan(context.Background()); err != nil {
		t.Fatalf("setup Scan() error = %v", err)
	}
	version := repository.Version()

	if err := os.Remove(filepath.Join(root, "pkg", "old.go")); err != nil {
		t.Fatal(err)
	}
	writeSource(t, root, "pkg/new.go", goSource)

	if err := indexer.ReconcileSubtree(context.Background(), "pkg", version); err != nil {
		t.Fatalf("ReconcileSubtree() error = %v", err)
	}
	if _, ok := repository.FileHash("pkg/old.go"); ok {
		t.Fatal("pkg/old.go was not deleted by subtree reconciliation")
	}
	if _, ok := repository.FileHash("pkg/new.go"); !ok {
		t.Fatal("pkg/new.go was not added by subtree reconciliation")
	}
	if _, ok := repository.FileHash("other/keep.go"); !ok {
		t.Fatal("other/keep.go was incorrectly removed")
	}
	if repository.Version() != version+1 {
		t.Fatalf("version = %d, want one subtree commit to %d", repository.Version(), version+1)
	}
}

func TestReconcileRejectsStaleExpectedRevision(t *testing.T) {
	t.Parallel()
	indexer, root, repo := newTestIndexer(t, codeparser.New(), nil, false)
	writeSource(t, root, "a.go", goSource)
	if err := indexer.Scan(context.Background()); err != nil {
		t.Fatalf("setup Scan() error = %v", err)
	}
	version := repo.Version()

	if err := indexer.ReconcileFull(context.Background(), version-1); !errors.Is(err, repository.ErrVersionConflict) {
		t.Fatalf("ReconcileFull(stale expected) error = %v, want ErrVersionConflict", err)
	}
	if indexer.State() != ScanComplete {
		t.Fatalf("state = %s, want previous complete state preserved", indexer.State())
	}
	if repo.Version() != version {
		t.Fatalf("stale reconcile advanced version to %d, want %d", repo.Version(), version)
	}
}

func TestReconcileFullAndSubtreeDeleteKnownFilesThatBecomeOversized(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		name      string
		reconcile func(context.Context, *Indexer, uint64) error
	}{
		{"full", func(ctx context.Context, i *Indexer, version uint64) error {
			return i.ReconcileFull(ctx, version)
		}},
		{"subtree", func(ctx context.Context, i *Indexer, version uint64) error {
			return i.ReconcileSubtree(ctx, "pkg", version)
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			indexer, root, repository := newTestIndexer(t, codeparser.New(), nil, false)
			writeSource(t, root, "pkg/a.go", goSource)
			if err := indexer.Scan(context.Background()); err != nil {
				t.Fatalf("setup Scan() error = %v", err)
			}
			version := repository.Version()
			indexer.maxFileBytes = 8
			writeSource(t, root, "pkg/a.go", goSource+"\nfunc Oversized() {}\n")

			if err := tc.reconcile(context.Background(), indexer, version); err != nil {
				t.Fatalf("reconcile error = %v", err)
			}
			if _, ok := repository.FileHash("pkg/a.go"); ok {
				t.Fatal("oversized known file remained indexed")
			}
			if repository.Version() != version+1 {
				t.Fatalf("version = %d, want deletion commit %d", repository.Version(), version+1)
			}
		})
	}
}

func TestReconcileSubtreeDeletesDescendantsWhenDirectoryBecomesFile(t *testing.T) {
	t.Parallel()
	indexer, root, repository := newTestIndexer(t, codeparser.New(), nil, false)
	writeSource(t, root, "pkg/a.go", goSource)
	if err := indexer.Scan(context.Background()); err != nil {
		t.Fatalf("setup Scan() error = %v", err)
	}
	version := repository.Version()
	if err := os.RemoveAll(filepath.Join(root, "pkg")); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "pkg"), []byte("not a directory"), 0o600); err != nil {
		t.Fatal(err)
	}

	if err := indexer.ReconcileSubtree(context.Background(), "pkg", version); err != nil {
		t.Fatalf("ReconcileSubtree() error = %v", err)
	}
	if _, ok := repository.FileHash("pkg/a.go"); ok {
		t.Fatal("descendant entry remained indexed after directory was replaced by a file")
	}
	if repository.Version() != version+1 {
		t.Fatalf("version = %d, want deletion commit %d", repository.Version(), version+1)
	}
}

func TestReconcilePathsConfirmsSelfWriteWithoutSecondCommit(t *testing.T) {
	t.Parallel()
	indexer, root, repository := newTestIndexer(t, codeparser.New(), nil, false)
	writeSource(t, root, "a.go", goSource)
	if err := indexer.Scan(context.Background()); err != nil {
		t.Fatalf("setup Scan() error = %v", err)
	}
	version := repository.Version()
	hash, _ := repository.FileHash("a.go")
	registry := mutation.NewMemoryRegistry(mutation.RegistryConfig{DefaultTTL: time.Minute})
	defer registry.Close()
	indexer.SetMutationRegistry(registry)
	mutationID := domain.InternalMutationID("mut-1")
	if err := registry.Stage(context.Background(), domain.InternalMutation{
		ID:                   mutationID,
		TransactionID:        "tx-1",
		Path:                 "a.go",
		PreviousContentHash:  "sha256:old",
		PublishedContentHash: hash,
	}); err != nil {
		t.Fatalf("Stage() error = %v", err)
	}
	if err := registry.MarkPublished(context.Background(), mutationID, mutation.MutationCommitResult{
		SnapshotID:  repository.SnapshotID(),
		Revision:    repository.Revision(),
		CommittedAt: time.Now().UTC(),
	}); err != nil {
		t.Fatalf("MarkPublished() error = %v", err)
	}

	channel, cancel := indexer.Broker().Subscribe()
	defer cancel()
	if err := indexer.ReconcilePaths(context.Background(), []string{"a.go"}, version); err != nil {
		t.Fatalf("ReconcilePaths() error = %v", err)
	}
	if repository.Version() != version {
		t.Fatalf("self-write confirmation advanced version to %d, want %d", repository.Version(), version)
	}
	waitIndexEventType(t, channel, "watch.self_write_confirmed")
	if snapshot := registry.Snapshot(); snapshot.ObservedCount != 1 || snapshot.SelfWriteConfirmedTotal != 1 {
		t.Fatalf("registry snapshot = %#v, want one observed self-write", snapshot)
	}
}

func TestReconcileFullConfirmsSelfWriteWithoutSecondCommit(t *testing.T) {
	t.Parallel()
	indexer, root, repository := newTestIndexer(t, codeparser.New(), nil, false)
	writeSource(t, root, "a.go", goSource)
	if err := indexer.Scan(context.Background()); err != nil {
		t.Fatalf("setup Scan() error = %v", err)
	}
	version := repository.Version()
	hash, _ := repository.FileHash("a.go")
	registry := mutation.NewMemoryRegistry(mutation.RegistryConfig{DefaultTTL: time.Minute})
	defer registry.Close()
	indexer.SetMutationRegistry(registry)
	mutationID := domain.InternalMutationID("mut-1")
	if err := registry.Stage(context.Background(), domain.InternalMutation{
		ID:                   mutationID,
		TransactionID:        "tx-1",
		Path:                 "a.go",
		PreviousContentHash:  "sha256:old",
		PublishedContentHash: hash,
	}); err != nil {
		t.Fatalf("Stage() error = %v", err)
	}
	if err := registry.MarkPublished(context.Background(), mutationID, mutation.MutationCommitResult{
		SnapshotID:  repository.SnapshotID(),
		Revision:    repository.Revision(),
		CommittedAt: time.Now().UTC(),
	}); err != nil {
		t.Fatalf("MarkPublished() error = %v", err)
	}

	channel, cancel := indexer.Broker().Subscribe()
	defer cancel()
	if err := indexer.ReconcileFull(context.Background(), version); err != nil {
		t.Fatalf("ReconcileFull() error = %v", err)
	}
	if repository.Version() != version {
		t.Fatalf("self-write confirmation advanced version to %d, want %d", repository.Version(), version)
	}
	waitIndexEventType(t, channel, "watch.self_write_confirmed")
	if snapshot := registry.Snapshot(); snapshot.ObservedCount != 1 || snapshot.SelfWriteConfirmedTotal != 1 {
		t.Fatalf("registry snapshot = %#v, want one observed self-write", snapshot)
	}
}

func TestReconcileSubtreeConfirmsSelfWriteWithoutSecondCommit(t *testing.T) {
	t.Parallel()
	indexer, root, repository := newTestIndexer(t, codeparser.New(), nil, false)
	writeSource(t, root, "pkg/a.go", goSource)
	if err := indexer.Scan(context.Background()); err != nil {
		t.Fatalf("setup Scan() error = %v", err)
	}
	version := repository.Version()
	hash, _ := repository.FileHash("pkg/a.go")
	registry := mutation.NewMemoryRegistry(mutation.RegistryConfig{DefaultTTL: time.Minute})
	defer registry.Close()
	indexer.SetMutationRegistry(registry)
	mutationID := domain.InternalMutationID("mut-1")
	if err := registry.Stage(context.Background(), domain.InternalMutation{
		ID:                   mutationID,
		TransactionID:        "tx-1",
		Path:                 "pkg/a.go",
		PreviousContentHash:  "sha256:old",
		PublishedContentHash: hash,
	}); err != nil {
		t.Fatalf("Stage() error = %v", err)
	}
	if err := registry.MarkPublished(context.Background(), mutationID, mutation.MutationCommitResult{
		SnapshotID:  repository.SnapshotID(),
		Revision:    repository.Revision(),
		CommittedAt: time.Now().UTC(),
	}); err != nil {
		t.Fatalf("MarkPublished() error = %v", err)
	}

	channel, cancel := indexer.Broker().Subscribe()
	defer cancel()
	if err := indexer.ReconcileSubtree(context.Background(), "pkg", version); err != nil {
		t.Fatalf("ReconcileSubtree() error = %v", err)
	}
	if repository.Version() != version {
		t.Fatalf("self-write confirmation advanced version to %d, want %d", repository.Version(), version)
	}
	waitIndexEventType(t, channel, "watch.self_write_confirmed")
	if snapshot := registry.Snapshot(); snapshot.ObservedCount != 1 || snapshot.SelfWriteConfirmedTotal != 1 {
		t.Fatalf("registry snapshot = %#v, want one observed self-write", snapshot)
	}
}

func TestCommittedSaveIsConfirmedByRealIndexerHashFormat(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	writeSource(t, root, "a.go", goSource)
	indexPath := filepath.Join(t.TempDir(), "index.json")
	repository, err := repository.OpenJSON(indexPath)
	if err != nil {
		t.Fatalf("OpenJSON() error = %v", err)
	}
	parser := codeparser.New()
	registry := mutation.NewMemoryRegistry(mutation.RegistryConfig{DefaultTTL: time.Minute})
	defer registry.Close()
	idx := New(root, 1_500_000, parser, repository, nil)
	idx.SetMutationRegistry(registry)
	if err := idx.Scan(context.Background()); err != nil {
		t.Fatalf("setup Scan() error = %v", err)
	}
	workspace := service.NewWorkspace(root)
	preparer := service.NewSavePreparer(workspace, repository, parser, nil, 1_500_000)
	committer := service.NewWorkspaceCommitCoordinator(preparer, workspace, repository, filepath.Join(t.TempDir(), "tx"), indexPath)
	committer.SetMutationRegistry(registry)
	current, err := os.ReadFile(filepath.Join(root, "a.go"))
	if err != nil {
		t.Fatal(err)
	}
	prepared, err := committer.Prepare(context.Background(), service.SaveRequest{
		Path:                "a.go",
		Content:             []byte("package sample\n\nfunc Run() int { return 2 }\n"),
		ExpectedContentHash: contenthash.HashContent(current),
	})
	if err != nil {
		t.Fatalf("Prepare() error = %v", err)
	}
	version, err := committer.Commit(prepared, nil)
	if err != nil {
		t.Fatalf("Commit() error = %v", err)
	}

	channel, cancel := idx.Broker().Subscribe()
	defer cancel()
	if err := idx.ReconcilePaths(context.Background(), []string{"a.go"}, version); err != nil {
		t.Fatalf("ReconcilePaths() error = %v", err)
	}
	if repository.Version() != version {
		t.Fatalf("self-write confirmation advanced version to %d, want %d", repository.Version(), version)
	}
	waitIndexEventType(t, channel, "watch.self_write_confirmed")
	snapshot := registry.Snapshot()
	if snapshot.SelfWriteConfirmedTotal != 1 || snapshot.ExternalAfterInternalTotal != 0 {
		t.Fatalf("registry snapshot = %#v, want real save confirmed without external count", snapshot)
	}
}

func TestReconcileFullNormalizesBareHexScannerHashOnce(t *testing.T) {
	t.Parallel()
	indexer, root, repository := newTestIndexer(t, codeparser.New(), nil, false)
	writeSource(t, root, "a.go", goSource)
	bareHash := strings.TrimPrefix(contenthash.HashContent([]byte(goSource)), "sha256:")
	if err := repository.ReplaceFile(domain.ParsedFile{
		File: domain.File{
			Path: "a.go", Language: "go", Hash: bareHash, Size: int64(len(goSource)),
			ModifiedAt: time.Now().UTC(), IndexedAt: time.Now().UTC(),
		},
		Symbols: []domain.Symbol{{
			ID: "a.go:file", Path: "a.go", Name: "a.go", Kind: "file",
			Range: domain.Range{Start: domain.Position{Line: 1, Column: 1}, End: domain.Position{Line: 2, Column: 1}},
		}},
	}); err != nil {
		t.Fatalf("ReplaceFile() error = %v", err)
	}
	version := repository.Version()

	if err := indexer.ReconcileFull(context.Background(), version); err != nil {
		t.Fatalf("normalizing ReconcileFull() error = %v", err)
	}
	if repository.Version() != version+1 {
		t.Fatalf("version = %d, want one normalizing commit %d", repository.Version(), version+1)
	}
	if hash, _ := repository.FileHash("a.go"); !strings.HasPrefix(hash, "sha256:") {
		t.Fatalf("normalized hash = %q, want sha256: prefix", hash)
	}
	version = repository.Version()
	if err := indexer.ReconcileFull(context.Background(), version); err != nil {
		t.Fatalf("second ReconcileFull() error = %v", err)
	}
	if repository.Version() != version {
		t.Fatalf("second reconcile advanced version to %d, want %d", repository.Version(), version)
	}
}

func TestReconcilePathsConfirmsSelfWriteAfterMixedBatchCommit(t *testing.T) {
	t.Parallel()
	indexer, root, repository := newTestIndexer(t, codeparser.New(), nil, false)
	writeSource(t, root, "a.go", goSource)
	writeSource(t, root, "b.go", "package sample\n\nfunc Other() int { return 1 }\n")
	if err := indexer.Scan(context.Background()); err != nil {
		t.Fatalf("setup Scan() error = %v", err)
	}
	version := repository.Version()
	hash, _ := repository.FileHash("a.go")
	registry := mutation.NewMemoryRegistry(mutation.RegistryConfig{DefaultTTL: time.Minute})
	defer registry.Close()
	indexer.SetMutationRegistry(registry)
	mutationID := domain.InternalMutationID("mut-1")
	if err := registry.Stage(context.Background(), domain.InternalMutation{
		ID:                   mutationID,
		TransactionID:        "tx-1",
		Path:                 "a.go",
		PreviousContentHash:  "sha256:old",
		PublishedContentHash: hash,
	}); err != nil {
		t.Fatalf("Stage() error = %v", err)
	}
	if err := registry.MarkPublished(context.Background(), mutationID, mutation.MutationCommitResult{
		SnapshotID:  repository.SnapshotID(),
		Revision:    repository.Revision(),
		CommittedAt: time.Now().UTC(),
	}); err != nil {
		t.Fatalf("MarkPublished() error = %v", err)
	}
	writeSource(t, root, "b.go", "package sample\n\nfunc Other() int { return 2 }\n")

	channel, cancel := indexer.Broker().Subscribe()
	defer cancel()
	if err := indexer.ReconcilePaths(context.Background(), []string{"a.go", "b.go"}, version); err != nil {
		t.Fatalf("ReconcilePaths() error = %v", err)
	}
	if repository.Version() != version+1 {
		t.Fatalf("version = %d, want mixed-batch commit %d", repository.Version(), version+1)
	}
	waitIndexEventType(t, channel, "workspace.files.changed")
	waitIndexEventType(t, channel, "watch.self_write_confirmed")
	if snapshot := registry.Snapshot(); snapshot.ObservedCount != 1 || snapshot.SelfWriteConfirmedTotal != 1 {
		t.Fatalf("registry snapshot = %#v, want mixed batch self-write observed", snapshot)
	}
}

func TestReconcilePathsTreatsDifferentHashAfterInternalSaveAsExternalChange(t *testing.T) {
	t.Parallel()
	indexer, root, repository := newTestIndexer(t, codeparser.New(), nil, false)
	writeSource(t, root, "a.go", goSource)
	if err := indexer.Scan(context.Background()); err != nil {
		t.Fatalf("setup Scan() error = %v", err)
	}
	version := repository.Version()
	hash, _ := repository.FileHash("a.go")
	registry := mutation.NewMemoryRegistry(mutation.RegistryConfig{DefaultTTL: time.Minute})
	defer registry.Close()
	indexer.SetMutationRegistry(registry)
	mutationID := domain.InternalMutationID("mut-1")
	if err := registry.Stage(context.Background(), domain.InternalMutation{
		ID:                   mutationID,
		TransactionID:        "tx-1",
		Path:                 "a.go",
		PreviousContentHash:  "sha256:old",
		PublishedContentHash: hash,
	}); err != nil {
		t.Fatalf("Stage() error = %v", err)
	}
	if err := registry.MarkPublished(context.Background(), mutationID, mutation.MutationCommitResult{
		SnapshotID:  repository.SnapshotID(),
		Revision:    repository.Revision(),
		CommittedAt: time.Now().UTC(),
	}); err != nil {
		t.Fatalf("MarkPublished() error = %v", err)
	}
	writeSource(t, root, "a.go", "package sample\n\nfunc Run() int { return 99 }\n")

	channel, cancel := indexer.Broker().Subscribe()
	defer cancel()
	if err := indexer.ReconcilePaths(context.Background(), []string{"a.go"}, version); err != nil {
		t.Fatalf("ReconcilePaths() error = %v", err)
	}
	if repository.Version() != version+1 {
		t.Fatalf("version = %d, want external change commit %d", repository.Version(), version+1)
	}
	waitIndexEventType(t, channel, "workspace.files.changed")
	if event := drainEvents(t, channel); len(event) != 0 {
		for _, item := range event {
			if item.Type == "watch.self_write_confirmed" {
				t.Fatalf("unexpected self-write confirmation event after external edit: %#v", item)
			}
		}
	}
	snapshot := registry.Snapshot()
	if snapshot.ObservedCount != 0 || snapshot.SelfWriteConfirmedTotal != 0 || snapshot.ExternalAfterInternalTotal != 1 {
		t.Fatalf("registry snapshot = %#v, want one external-after-internal and no self-write observation", snapshot)
	}
}
