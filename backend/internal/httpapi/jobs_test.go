package httpapi_test

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/ThalesMMS/CodeAtlas/internal/capabilities"
	"github.com/ThalesMMS/CodeAtlas/internal/domain"
	"github.com/ThalesMMS/CodeAtlas/internal/httpapi"
	"github.com/ThalesMMS/CodeAtlas/internal/indexer"
	codeparser "github.com/ThalesMMS/CodeAtlas/internal/parser"
	"github.com/ThalesMMS/CodeAtlas/internal/readiness"
	"github.com/ThalesMMS/CodeAtlas/internal/repository"
	"github.com/ThalesMMS/CodeAtlas/internal/service"
)

func TestAsyncFeatureEndpointsReturnJobRefsAndResults(t *testing.T) {
	t.Parallel()
	server := buildJobServer(t)

	var codemapRef struct {
		Job domain.JobSnapshot `json:"job"`
	}
	postJSONStatus(t, server.URL+"/api/codemaps", domain.CodemapRequest{Query: "checkout submit service save", MaxNodes: 12}, http.StatusAccepted, &codemapRef)
	if codemapRef.Job.ID == "" || codemapRef.Job.Type != "codemap.generate" || codemapRef.Job.State == "" {
		t.Fatalf("codemap job ref = %#v", codemapRef.Job)
	}
	codemapJob := waitHTTPJob(t, server.URL, codemapRef.Job.ID, domain.JobSucceeded)
	if codemapJob.ResultArtifactID == "" {
		t.Fatalf("codemap job missing result artifact: %#v", codemapJob)
	}
	var codemap domain.Codemap
	getJSON(t, server.URL+"/api/jobs/"+string(codemapRef.Job.ID)+"/result", &codemap)
	if len(codemap.Nodes) == 0 || codemap.Title == "" || codemap.Artifact.ID == "" {
		t.Fatalf("codemap result = %#v", codemap)
	}

	var wikiRef struct {
		Job domain.JobSnapshot `json:"job"`
	}
	postJSONStatus(t, server.URL+"/api/deepwiki/refresh", map[string]any{}, http.StatusAccepted, &wikiRef)
	if wikiRef.Job.Type != "deepwiki.refresh" {
		t.Fatalf("wiki job ref = %#v", wikiRef.Job)
	}
	waitHTTPJob(t, server.URL, wikiRef.Job.ID, domain.JobSucceeded)
	var collection domain.DeepWikiCollection
	getJSON(t, server.URL+"/api/deepwiki", &collection)
	if collection.Status != domain.DeepWikiReady || len(collection.Pages) == 0 {
		t.Fatalf("wiki collection = %#v", collection)
	}

	var reindexRef struct {
		Job domain.JobSnapshot `json:"job"`
	}
	postJSONStatus(t, server.URL+"/api/reindex", map[string]any{}, http.StatusAccepted, &reindexRef)
	if reindexRef.Job.Type != "repository.reindex" {
		t.Fatalf("reindex job ref = %#v", reindexRef.Job)
	}
	reindexJob := waitHTTPJob(t, server.URL, reindexRef.Job.ID, domain.JobSucceeded)
	if reindexJob.Stage != "no_changes" || reindexJob.Message != "index was already up to date" {
		t.Fatalf("no-op reindex job = %#v, want explicit no_changes outcome", reindexJob)
	}
	if reindexJob.InputSnapshotID != reindexJob.ResultSnapshotID {
		t.Fatalf("no-op snapshots = %q -> %q, want unchanged", reindexJob.InputSnapshotID, reindexJob.ResultSnapshotID)
	}
	var reindexResult struct {
		Committed    bool `json:"committed"`
		FilesChanged int  `json:"filesChanged"`
		FilesRemoved int  `json:"filesRemoved"`
	}
	getJSON(t, server.URL+"/api/jobs/"+string(reindexRef.Job.ID)+"/result", &reindexResult)
	if reindexResult.Committed || reindexResult.FilesChanged != 0 || reindexResult.FilesRemoved != 0 {
		t.Fatalf("no-op reindex result = %#v", reindexResult)
	}

	var ftsRef struct {
		Job domain.JobSnapshot `json:"job"`
	}
	postJSONStatus(t, server.URL+"/api/jobs", map[string]any{"type": "fts.rebuild", "input": map[string]any{}}, http.StatusAccepted, &ftsRef)
	ftsJob := waitHTTPJob(t, server.URL, ftsRef.Job.ID, domain.JobSucceeded)
	if ftsJob.Stage != "rebuilt" || ftsJob.Message != "FTS index rebuilt" {
		t.Fatalf("FTS rebuild job = %#v", ftsJob)
	}
	if ftsJob.InputSnapshotID != ftsJob.ResultSnapshotID {
		t.Fatalf("FTS rebuild changed structural snapshot: %q -> %q", ftsJob.InputSnapshotID, ftsJob.ResultSnapshotID)
	}
	var ftsResult struct {
		Rebuilt bool   `json:"rebuilt"`
		Index   string `json:"index"`
	}
	getJSON(t, server.URL+"/api/jobs/"+string(ftsRef.Job.ID)+"/result", &ftsResult)
	if !ftsResult.Rebuilt || ftsResult.Index != "fts" {
		t.Fatalf("FTS rebuild result = %#v", ftsResult)
	}
}

func TestDeepWikiFailedJobNamesFailedPageAndLogsSanitizedValidation(t *testing.T) {
	root := t.TempDir()
	writeFixture(t, root, "internal/order/service.go", "package order\nfunc Submit() {}\n")
	writeFixture(t, root, "internal/order/service_test.go", "package order\nfunc TestSubmit() {}\n")
	repo, err := repository.OpenJSON(filepath.Join(t.TempDir(), "index.json"))
	if err != nil {
		t.Fatal(err)
	}
	provider := failingDeepWikiProvider{}
	backgroundIndexer := indexer.New(root, 1_500_000, codeparser.New(), repo)
	if err := backgroundIndexer.Scan(context.Background()); err != nil {
		t.Fatal(err)
	}
	workspace := service.NewWorkspace(root)
	saver := service.NewSavePreparer(workspace, repo, codeparser.New(), 1_500_000)
	deepWiki := service.NewDeepWikiService(repo, provider)
	deepWiki.SetPlannerEnabled(false)
	var logs bytes.Buffer
	api := httpapi.New(
		workspace, repo, backgroundIndexer,
		service.NewExplainer(repo, workspace, provider), service.NewCodemapService(repo, provider), deepWiki,
		service.NewWorkspaceCommitCoordinator(saver, workspace, repo, filepath.Join(t.TempDir(), "tx"), ""),
		provider, coordinatorInState(t, readiness.StateReady), capabilities.NewRegistry(),
		slog.New(slog.NewTextHandler(&logs, nil)),
	)
	server := httptest.NewServer(api.Handler())
	t.Cleanup(server.Close)

	var created struct {
		Job domain.JobSnapshot `json:"job"`
	}
	postJSONStatus(t, server.URL+"/api/deepwiki/refresh", map[string]any{}, http.StatusAccepted, &created)
	failed := waitHTTPJob(t, server.URL, created.Job.ID, domain.JobFailed)
	if !strings.HasPrefix(failed.Message, "failed to generate page ") || !strings.Contains(failed.Message, "testing") {
		t.Fatalf("failed job message = %q, want actual testing page", failed.Message)
	}
	logged := logs.String()
	if !strings.Contains(logged, "pageSlug=") || !strings.Contains(logged, "testing") || !strings.Contains(logged, "reason") {
		t.Fatalf("DeepWiki failure log omitted page/reason: %s", logged)
	}
	if strings.Contains(logged, "RAW_MODEL_MARKER") {
		t.Fatalf("DeepWiki failure log leaked model content: %s", logged)
	}
}

func TestJobsAPIListsCancelsAndStreamsStructuredEvents(t *testing.T) {
	t.Parallel()
	server := buildJobServer(t)

	streamReq, err := http.NewRequest(http.MethodGet, server.URL+"/api/events", nil)
	if err != nil {
		t.Fatal(err)
	}
	streamCtx, cancelStream := context.WithCancel(context.Background())
	defer cancelStream()
	streamReq = streamReq.WithContext(streamCtx)
	streamResp, err := http.DefaultClient.Do(streamReq)
	if err != nil {
		t.Fatal(err)
	}
	defer streamResp.Body.Close()
	if streamResp.StatusCode != http.StatusOK {
		t.Fatalf("events status = %d", streamResp.StatusCode)
	}
	eventLines := make(chan string, 32)
	go func() {
		scanner := bufio.NewScanner(streamResp.Body)
		for scanner.Scan() {
			eventLines <- scanner.Text()
		}
		close(eventLines)
	}()

	var created struct {
		Job domain.JobSnapshot `json:"job"`
	}
	postJSONStatus(t, server.URL+"/api/jobs", map[string]any{"type": "repository.reindex", "input": map[string]any{}}, http.StatusAccepted, &created)
	seen := waitSSEDataType(t, eventLines, "job.queued")
	if seen.Job.ID != created.Job.ID || seen.Job.Type != "repository.reindex" {
		t.Fatalf("streamed event = %#v, created=%#v", seen, created.Job)
	}

	var listed struct {
		Jobs []domain.JobSnapshot `json:"jobs"`
	}
	getJSON(t, server.URL+"/api/jobs?type=repository.reindex&limit=10", &listed)
	if len(listed.Jobs) == 0 {
		t.Fatal("jobs list did not include created job")
	}

	var canceled struct {
		Job domain.JobSnapshot `json:"job"`
	}
	postJSONStatus(t, server.URL+"/api/jobs", map[string]any{"type": "unknown.job", "input": map[string]any{}}, http.StatusBadRequest, nil)
	response := doRawJSON(t, http.MethodDelete, server.URL+"/api/jobs/"+string(created.Job.ID), nil)
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK && response.StatusCode != http.StatusConflict {
		raw, _ := io.ReadAll(response.Body)
		t.Fatalf("cancel status=%d body=%s", response.StatusCode, raw)
	}
	_ = json.NewDecoder(response.Body).Decode(&canceled)
}

type failingDeepWikiProvider struct{}

func (failingDeepWikiProvider) Name() string    { return "failing-deepwiki" }
func (failingDeepWikiProvider) Available() bool { return true }
func (failingDeepWikiProvider) Complete(_ context.Context, systemPrompt, userPrompt string, _ int) (string, error) {
	if strings.Contains(systemPrompt, "wiki-page/v4") && strings.Contains(userPrompt, `"archetype":"testing"`) {
		return `{"schemaVersion":"wiki-page/v4","title":"Testing","sections":[],"relatedPages":[],"inferences":[],"limitations":[{"text":"RAW_MODEL_MARKER"}]}`, nil
	}
	if strings.Contains(systemPrompt, "wiki-page/v4") {
		return `{"schemaVersion":"wiki-page/v4","title":"Page","sections":[],"relatedPages":[],"inferences":[],"limitations":[]}`, nil
	}
	return "{}", nil
}
func buildJobServer(t *testing.T) *httptest.Server {
	t.Helper()
	root := t.TempDir()
	writeFixture(t, root, "web/checkout.ts", `export class Checkout {
  completeCheckout() { submitOrder() }
}
function submitOrder() {}
`)
	writeFixture(t, root, "internal/order/service.go", `package order

type Service struct{}
func (s *Service) Submit() { Save() }
func Save() {}
`)
	repo, err := repository.OpenSQLite(context.Background(), repository.SQLiteConfig{WorkspaceRoot: t.TempDir()})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = repo.Close() })
	provider := staticProvider{response: "ok"}
	backgroundIndexer := indexer.New(root, 1_500_000, codeparser.New(), repo)
	if err := backgroundIndexer.Scan(context.Background()); err != nil {
		t.Fatalf("Scan() error = %v", err)
	}
	workspace := service.NewWorkspace(root)
	explainer := service.NewExplainer(repo, workspace, provider)
	codemaps := service.NewCodemapService(repo, provider)
	deepWiki := service.NewDeepWikiService(repo, provider)
	saver := service.NewSavePreparer(workspace, repo, codeparser.New(), 1_500_000)
	committer := service.NewWorkspaceCommitCoordinator(saver, workspace, repo, filepath.Join(t.TempDir(), "tx"), "")
	api := httpapi.New(
		workspace, repo, backgroundIndexer, explainer, codemaps, deepWiki, committer, provider,
		coordinatorInState(t, readiness.StateReady), capabilities.NewRegistry(), slog.New(slog.NewTextHandler(io.Discard, nil)),
	)
	server := httptest.NewServer(api.Handler())
	t.Cleanup(server.Close)
	return server
}

func postJSONStatus(t *testing.T, url string, payload any, want int, target any) {
	t.Helper()
	body, err := json.Marshal(payload)
	if err != nil {
		t.Fatal(err)
	}
	response, err := http.Post(url, "application/json", bytes.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	defer response.Body.Close()
	if response.StatusCode != want {
		raw, _ := io.ReadAll(response.Body)
		t.Fatalf("POST %s status=%d want=%d body=%s", url, response.StatusCode, want, raw)
	}
	if target != nil {
		if err := json.NewDecoder(response.Body).Decode(target); err != nil {
			t.Fatal(err)
		}
	}
}

func doRawJSON(t *testing.T, method, url string, payload any) *http.Response {
	t.Helper()
	var body io.Reader
	if payload != nil {
		data, _ := json.Marshal(payload)
		body = bytes.NewReader(data)
	}
	request, err := http.NewRequest(method, url, body)
	if err != nil {
		t.Fatal(err)
	}
	if payload != nil {
		request.Header.Set("Content-Type", "application/json")
	}
	response, err := http.DefaultClient.Do(request)
	if err != nil {
		t.Fatal(err)
	}
	return response
}

func waitHTTPJob(t *testing.T, baseURL string, id domain.JobID, want domain.JobState) domain.JobSnapshot {
	t.Helper()
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		var response struct {
			Job domain.JobSnapshot `json:"job"`
		}
		getJSON(t, baseURL+"/api/jobs/"+string(id), &response)
		if response.Job.State == want {
			return response.Job
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("job %s did not reach %s", id, want)
	return domain.JobSnapshot{}
}

func waitSSEDataType(t *testing.T, lines <-chan string, want string) domain.JobEvent {
	t.Helper()
	deadline := time.After(3 * time.Second)
	for {
		select {
		case line := <-lines:
			if !strings.HasPrefix(line, "data: ") {
				continue
			}
			var event domain.JobEvent
			if err := json.Unmarshal([]byte(strings.TrimPrefix(line, "data: ")), &event); err != nil {
				continue
			}
			if event.Type == want {
				return event
			}
		case <-deadline:
			t.Fatalf("timed out waiting for SSE data type %s", want)
		}
	}
}
