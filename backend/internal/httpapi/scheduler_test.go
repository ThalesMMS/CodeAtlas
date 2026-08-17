package httpapi_test

import (
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/ThalesMMS/CodeAtlas/internal/capabilities"
	"github.com/ThalesMMS/CodeAtlas/internal/domain"
	"github.com/ThalesMMS/CodeAtlas/internal/httpapi"
	"github.com/ThalesMMS/CodeAtlas/internal/indexer"
	"github.com/ThalesMMS/CodeAtlas/internal/mutation"
	codeparser "github.com/ThalesMMS/CodeAtlas/internal/parser"
	"github.com/ThalesMMS/CodeAtlas/internal/readiness"
	"github.com/ThalesMMS/CodeAtlas/internal/repository"
	"github.com/ThalesMMS/CodeAtlas/internal/retrieval"
	"github.com/ThalesMMS/CodeAtlas/internal/service"
)

type fakeScheduler struct {
	mu      sync.Mutex
	reasons []string
	state   domain.SchedulerState
}

func (f *fakeScheduler) RequestFull(_ context.Context, reason string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.reasons = append(f.reasons, reason)
	return nil
}

func (f *fakeScheduler) State() domain.SchedulerState {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.state
}

func TestReindexUsesSchedulerQueueAndStatsExposeSchedulerState(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	writeFixture(t, root, "a.go", "package main\nfunc A() {}\n")
	repository, err := repository.OpenSQLite(context.Background(), repository.SQLiteConfig{WorkspaceRoot: t.TempDir()})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = repository.Close() })
	provider := staticProvider{response: "ok"}
	retriever := retrieval.NewHybrid(repository, provider, false)
	backgroundIndexer := indexer.New(root, 1_500_000, codeparser.New(), repository, retriever)
	if err := backgroundIndexer.Scan(context.Background()); err != nil {
		t.Fatalf("Scan() error = %v", err)
	}
	workspace := service.NewWorkspace(root)
	saver := service.NewSavePreparer(workspace, repository, codeparser.New(), retriever, 1_500_000)
	committer := service.NewWorkspaceCommitCoordinator(saver, workspace, repository, filepath.Join(t.TempDir(), "tx"), "")
	scheduler := &fakeScheduler{state: domain.SchedulerState{
		Mode: "auto", EffectiveMode: "native", State: "idle", LastPeriodicReconcile: time.Unix(10, 0).UTC(),
	}}
	api := httpapi.New(
		workspace, repository, backgroundIndexer, retriever,
		service.NewExplainer(repository, workspace, provider),
		service.NewCodemapService(repository, retriever, provider),
		service.NewDeepWikiService(repository, provider),
		committer, provider, coordinatorInState(t, readiness.StateReady), capabilities.NewRegistry(),
		slog.New(slog.NewTextHandler(io.Discard, nil)),
	)
	api.SetScheduler(scheduler)
	server := httptest.NewServer(api.Handler())
	defer server.Close()

	response, err := http.Post(server.URL+"/api/reindex", "application/json", nil)
	if err != nil {
		t.Fatal(err)
	}
	_ = response.Body.Close()
	if response.StatusCode != http.StatusAccepted {
		t.Fatalf("POST /api/reindex status = %d, want 202", response.StatusCode)
	}
	waitSchedulerReason(t, scheduler, "manual")

	statsResponse, err := http.Get(server.URL + "/api/stats")
	if err != nil {
		t.Fatal(err)
	}
	defer statsResponse.Body.Close()
	var stats struct {
		Scheduler *domain.SchedulerState `json:"scheduler"`
	}
	if err := json.NewDecoder(statsResponse.Body).Decode(&stats); err != nil {
		t.Fatal(err)
	}
	if stats.Scheduler == nil || stats.Scheduler.EffectiveMode != "native" {
		t.Fatalf("stats scheduler = %#v, want effective native", stats.Scheduler)
	}
}

func waitSchedulerReason(t *testing.T, scheduler *fakeScheduler, want string) {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		scheduler.mu.Lock()
		reasons := append([]string(nil), scheduler.reasons...)
		scheduler.mu.Unlock()
		if len(reasons) > 0 && reasons[0] == want {
			return
		}
		time.Sleep(time.Millisecond)
	}
	scheduler.mu.Lock()
	defer scheduler.mu.Unlock()
	t.Fatalf("scheduler reasons = %v, want %s", scheduler.reasons, want)
}

func TestStatsExposeInternalMutationSnapshot(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	writeFixture(t, root, "a.go", "package main\nfunc A() {}\n")
	repository, err := repository.OpenSQLite(context.Background(), repository.SQLiteConfig{WorkspaceRoot: t.TempDir()})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = repository.Close() })
	provider := staticProvider{response: "ok"}
	retriever := retrieval.NewHybrid(repository, provider, false)
	backgroundIndexer := indexer.New(root, 1_500_000, codeparser.New(), repository, retriever)
	workspace := service.NewWorkspace(root)
	saver := service.NewSavePreparer(workspace, repository, codeparser.New(), retriever, 1_500_000)
	committer := service.NewWorkspaceCommitCoordinator(saver, workspace, repository, filepath.Join(t.TempDir(), "tx"), "")
	registry := mutation.NewMemoryRegistry(mutation.RegistryConfig{DefaultTTL: time.Minute})
	defer registry.Close()
	if err := registry.Stage(context.Background(), domain.InternalMutation{
		ID:                   "mut-1",
		Path:                 "a.go",
		PublishedContentHash: "sha256:new",
	}); err != nil {
		t.Fatalf("Stage() error = %v", err)
	}
	api := httpapi.New(
		workspace, repository, backgroundIndexer, retriever,
		service.NewExplainer(repository, workspace, provider),
		service.NewCodemapService(repository, retriever, provider),
		service.NewDeepWikiService(repository, provider),
		committer, provider, coordinatorInState(t, readiness.StateReady), capabilities.NewRegistry(),
		slog.New(slog.NewTextHandler(io.Discard, nil)),
	)
	api.SetMutationRegistry(registry)
	server := httptest.NewServer(api.Handler())
	defer server.Close()

	response, err := http.Get(server.URL + "/api/stats")
	if err != nil {
		t.Fatal(err)
	}
	defer response.Body.Close()
	var stats map[string]any
	if err := json.NewDecoder(response.Body).Decode(&stats); err != nil {
		t.Fatal(err)
	}
	internal, ok := stats["internalMutations"].(map[string]any)
	if !ok {
		t.Fatalf("stats internalMutations = %#v, want object", stats["internalMutations"])
	}
	if internal["stagedCount"] != float64(1) {
		t.Fatalf("stats internalMutations = %#v, want stagedCount=1", internal)
	}
	if _, exists := internal["StagedCount"]; exists {
		t.Fatalf("stats internalMutations used PascalCase keys: %#v", internal)
	}
	if _, exists := internal["entries"]; exists {
		t.Fatalf("stats internalMutations exposed entries: %#v", internal)
	}
}

type blockingSchedulerEmbeddingProvider struct {
	calls   atomic.Int32
	entered chan struct{}
	release chan struct{}
}

func (p *blockingSchedulerEmbeddingProvider) Name() string    { return "embedding-test" }
func (p *blockingSchedulerEmbeddingProvider) Available() bool { return true }
func (p *blockingSchedulerEmbeddingProvider) Complete(context.Context, string, string, int) (string, error) {
	return "", nil
}
func (p *blockingSchedulerEmbeddingProvider) Embed(context.Context, []string) ([][]float64, error) {
	call := p.calls.Add(1)
	if call >= 2 {
		select {
		case p.entered <- struct{}{}:
		default:
		}
		<-p.release
	}
	return [][]float64{{0.1, 0.2}}, nil
}

func TestEmbeddingRuntimeRebuildUsesRepositoryDeduplicationKey(t *testing.T) {
	root := t.TempDir()
	store, err := repository.OpenJSON(filepath.Join(t.TempDir(), "index.json"))
	if err != nil {
		t.Fatal(err)
	}
	provider := &blockingSchedulerEmbeddingProvider{entered: make(chan struct{}, 1), release: make(chan struct{})}
	runtime := retrieval.NewEmbeddingRuntime(provider, false)
	prepared, err := runtime.Prepare(context.Background(), retrieval.EmbeddingConfiguration{
		Provider: provider, Enabled: true, Model: "embed", BaseURL: "https://example.test/v1",
	}, domain.EmbeddingIndexMetadata{}, 0)
	if err != nil {
		t.Fatal(err)
	}
	prepared.Activate()
	retriever := retrieval.NewHybridWithRuntime(store, runtime)
	workspace := service.NewWorkspace(root)
	api := httpapi.New(
		workspace, store, nil, retriever, nil, nil, nil, nil, provider,
		coordinatorInState(t, readiness.StateReady), capabilities.NewRegistry(),
		slog.New(slog.NewTextHandler(io.Discard, nil)),
	)
	t.Cleanup(func() {
		close(provider.release)
		_ = api.ShutdownJobs(context.Background())
	})

	first, err := api.ScheduleEmbeddingRebuild(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	select {
	case <-provider.entered:
	case <-time.After(2 * time.Second):
		t.Fatal("embedding rebuild did not start")
	}
	second, err := api.ScheduleEmbeddingRebuild(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if first == "" || first != second {
		t.Fatalf("deduplicated job IDs = %q/%q", first, second)
	}
}
