package httpapi_test

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"net/url"
	"path/filepath"
	"strings"
	"testing"

	"github.com/ThalesMMS/CodeAtlas/internal/ai"
	"github.com/ThalesMMS/CodeAtlas/internal/capabilities"
	"github.com/ThalesMMS/CodeAtlas/internal/httpapi"
	"github.com/ThalesMMS/CodeAtlas/internal/indexer"
	"github.com/ThalesMMS/CodeAtlas/internal/observability"
	codeparser "github.com/ThalesMMS/CodeAtlas/internal/parser"
	"github.com/ThalesMMS/CodeAtlas/internal/readiness"
	"github.com/ThalesMMS/CodeAtlas/internal/repository"
	"github.com/ThalesMMS/CodeAtlas/internal/retrieval"
	"github.com/ThalesMMS/CodeAtlas/internal/service"
)

type errorEnvelope struct {
	Error struct {
		Code      string         `json:"code"`
		Message   string         `json:"message"`
		Retryable bool           `json:"retryable"`
		Details   map[string]any `json:"details"`
		RequestID string         `json:"requestId"`
	} `json:"error"`
}

// erroringProvider returns a fixed error from Complete; its message embeds a fake
// secret so tests can assert the secret never reaches the public body.
type erroringProvider struct{ err error }

func (p erroringProvider) Name() string    { return "erroring" }
func (p erroringProvider) Available() bool { return true }
func (p erroringProvider) Complete(context.Context, string, string, int) (string, error) {
	return "", p.err
}
func (p erroringProvider) Embed(context.Context, []string) ([][]float64, error) {
	return nil, p.err
}

type panicProvider struct{}

func (panicProvider) Name() string    { return "panic" }
func (panicProvider) Available() bool { return true }
func (panicProvider) Complete(context.Context, string, string, int) (string, error) {
	panic("provider blew up with secret api_key=sk-PANIC-LEAK")
}
func (panicProvider) Embed(context.Context, []string) ([][]float64, error) {
	return nil, errors.New("no embed")
}

func readyErrorServer(t *testing.T, provider ai.Provider) *httptest.Server {
	return readyErrorServerLogged(t, provider, slog.New(slog.NewTextHandler(io.Discard, nil)), nil)
}

func readyErrorServerLogged(t *testing.T, provider ai.Provider, logger *slog.Logger, metrics *observability.Metrics) *httptest.Server {
	t.Helper()
	root := t.TempDir()
	writeFixture(t, root, "internal/order/service.go", "package order\n\ntype Service struct{}\nfunc (s *Service) Submit() { Save() }\nfunc Save() {}\n")
	repository, err := repository.OpenSQLite(context.Background(), repository.SQLiteConfig{WorkspaceRoot: t.TempDir()})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = repository.Close() })
	retriever := retrieval.NewHybrid(repository, provider, false)
	backgroundIndexer := indexer.New(root, 1_500_000, codeparser.New(), repository, retriever)
	if err := backgroundIndexer.Scan(context.Background()); err != nil {
		t.Fatalf("Scan() error = %v", err)
	}
	workspace := service.NewWorkspace(root)
	explainer := service.NewExplainer(repository, workspace, provider)
	codemaps := service.NewCodemapService(repository, retriever, provider)
	deepWiki := service.NewDeepWikiService(repository, provider)
	saver := service.NewSavePreparer(workspace, repository, codeparser.New(), retriever, 1_500_000)
	committer := service.NewWorkspaceCommitCoordinator(saver, workspace, repository, filepath.Join(t.TempDir(), "tx"), "")
	api := httpapi.New(
		workspace, repository, backgroundIndexer, retriever, explainer, codemaps, deepWiki, committer, provider,
		coordinatorInState(t, readiness.StateReady), capabilities.NewRegistry(), logger,
	)
	api.SetMetrics(metrics)
	server := httptest.NewServer(api.Handler())
	t.Cleanup(server.Close)
	return server
}

func postRaw(t *testing.T, url, body string) (*http.Response, []byte, errorEnvelope) {
	t.Helper()
	response, err := http.Post(url, "application/json", strings.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	defer response.Body.Close()
	raw, _ := io.ReadAll(response.Body)
	var envelope errorEnvelope
	_ = json.Unmarshal(raw, &envelope)
	return response, raw, envelope
}

func getRaw(t *testing.T, url string) (*http.Response, []byte, errorEnvelope) {
	t.Helper()
	response, err := http.Get(url)
	if err != nil {
		t.Fatal(err)
	}
	defer response.Body.Close()
	raw, _ := io.ReadAll(response.Body)
	var envelope errorEnvelope
	_ = json.Unmarshal(raw, &envelope)
	return response, raw, envelope
}

func TestDecodeJSONDifferentiatesFailures(t *testing.T) {
	t.Parallel()
	server := readyErrorServer(t, staticProvider{response: "ok"})

	cases := []struct {
		name   string
		body   string
		status int
		code   string
	}{
		{"invalid syntax", `{"query": `, http.StatusBadRequest, "INVALID_ARGUMENT"},
		{"unknown field", `{"query":"x","bogus":1}`, http.StatusBadRequest, "INVALID_ARGUMENT"},
		{"multiple objects", `{"query":"x"}{"query":"y"}`, http.StatusBadRequest, "INVALID_ARGUMENT"},
		{"empty body", ``, http.StatusBadRequest, "INVALID_ARGUMENT"},
		{"too large", `{"query":"` + strings.Repeat("a", 70_000) + `"}`, http.StatusRequestEntityTooLarge, "REQUEST_TOO_LARGE"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			response, _, envelope := postRaw(t, server.URL+"/api/codemaps", tc.body)
			if response.StatusCode != tc.status {
				t.Fatalf("status = %d, want %d", response.StatusCode, tc.status)
			}
			if envelope.Error.Code != tc.code {
				t.Fatalf("code = %q, want %q", envelope.Error.Code, tc.code)
			}
		})
	}
}

func TestErrorEnvelopeCarriesRequestID(t *testing.T) {
	t.Parallel()
	server := readyErrorServer(t, staticProvider{response: "ok"})
	response, _, envelope := postRaw(t, server.URL+"/api/codemaps", `{"query":""}`)

	if response.StatusCode != http.StatusBadRequest || envelope.Error.Code != "QUERY_REQUIRED" {
		t.Fatalf("status/code = %d/%q, want 400/QUERY_REQUIRED", response.StatusCode, envelope.Error.Code)
	}
	if envelope.Error.RequestID == "" {
		t.Fatal("envelope is missing requestId")
	}
	if header := response.Header.Get("X-Request-ID"); header == "" || header != envelope.Error.RequestID {
		t.Fatalf("X-Request-ID header %q != envelope requestId %q", header, envelope.Error.RequestID)
	}
}

func TestFileNotFoundAndPathOutsideWorkspace(t *testing.T) {
	t.Parallel()
	server := readyErrorServer(t, staticProvider{response: "ok"})

	missing, _, missingEnv := getRaw(t, server.URL+"/api/file?path="+url.QueryEscape("internal/order/ghost.go"))
	if missing.StatusCode != http.StatusNotFound || missingEnv.Error.Code != "FILE_NOT_FOUND" {
		t.Fatalf("missing file = %d/%q, want 404/FILE_NOT_FOUND", missing.StatusCode, missingEnv.Error.Code)
	}

	traversal, _, traversalEnv := getRaw(t, server.URL+"/api/file?path="+url.QueryEscape("../../etc/passwd"))
	if traversal.StatusCode != http.StatusBadRequest || traversalEnv.Error.Code != "PATH_OUTSIDE_WORKSPACE" {
		t.Fatalf("traversal = %d/%q, want 400/PATH_OUTSIDE_WORKSPACE", traversal.StatusCode, traversalEnv.Error.Code)
	}
}

func TestGetFileRejectsOversizedWorkspaceFileBeforeJSONEncoding(t *testing.T) {
	t.Parallel()
	server, root := buildDocServer(t)
	writeFixture(t, root, "large.bin", strings.Repeat("x", 1_500_001))

	response, raw, envelope := getRaw(t, server.URL+"/api/file?path="+url.QueryEscape("large.bin"))
	if response.StatusCode != http.StatusRequestEntityTooLarge || envelope.Error.Code != "REQUEST_TOO_LARGE" {
		t.Fatalf("oversized file = %d/%q body=%s, want 413/REQUEST_TOO_LARGE", response.StatusCode, envelope.Error.Code, raw)
	}
	if envelope.Error.Details["limitBytes"] != float64(1_500_000) {
		t.Fatalf("limit details = %#v", envelope.Error.Details)
	}
}

func TestSymbolNotFoundForBadPosition(t *testing.T) {
	t.Parallel()
	server := readyErrorServer(t, staticProvider{response: "ok"})
	response, _, envelope := postRaw(t, server.URL+"/api/explain", `{"path":"internal/order/service.go","line":999,"column":1,"depth":"hover"}`)
	if response.StatusCode != http.StatusNotFound || envelope.Error.Code != "SYMBOL_NOT_FOUND" {
		t.Fatalf("status/code = %d/%q, want 404/SYMBOL_NOT_FOUND", response.StatusCode, envelope.Error.Code)
	}
}

func TestExplainRejectsUnknownFeature(t *testing.T) {
	t.Parallel()
	server := readyErrorServer(t, staticProvider{response: "ok"})
	response, _, envelope := postRaw(t, server.URL+"/api/explain", `{"feature":"expand","path":"internal/order/service.go","position":{"line":5,"column":6,"encoding":"utf-16"}}`)
	if response.StatusCode != http.StatusBadRequest || envelope.Error.Code != "INVALID_ARGUMENT" {
		t.Fatalf("status/code = %d/%q, want 400/INVALID_ARGUMENT", response.StatusCode, envelope.Error.Code)
	}
}

func TestProviderErrorDoesNotLeakSecret(t *testing.T) {
	t.Parallel()
	secret := "sk-SUPER-SECRET-TOKEN-123"
	server := readyErrorServer(t, erroringProvider{err: errors.New("dial tcp failed for api_key=" + secret)})
	response, raw, envelope := postRaw(t, server.URL+"/api/explain", `{"path":"internal/order/service.go","line":5,"column":6,"depth":"hover"}`)

	if response.StatusCode != http.StatusServiceUnavailable || envelope.Error.Code != "PROVIDER_UNAVAILABLE" {
		t.Fatalf("status/code = %d/%q, want 503/PROVIDER_UNAVAILABLE", response.StatusCode, envelope.Error.Code)
	}
	if strings.Contains(string(raw), secret) {
		t.Fatalf("response body leaked the provider secret: %s", raw)
	}
	if envelope.Error.Message != "The AI provider is unavailable." {
		t.Fatalf("message = %q, want the sanitized public text", envelope.Error.Message)
	}
}

func TestProviderContextErrorsKeepTheirPublicClassification(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name       string
		cause      error
		wantStatus int
		wantCode   string
	}{
		{"cancelled", context.Canceled, 499, "REQUEST_CANCELLED"},
		{"deadline", context.DeadlineExceeded, http.StatusGatewayTimeout, "PROVIDER_TIMEOUT"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			server := readyErrorServer(t, erroringProvider{err: tc.cause})
			response, _, envelope := postRaw(t, server.URL+"/api/explain", `{"path":"internal/order/service.go","line":5,"column":6,"depth":"hover"}`)
			if response.StatusCode != tc.wantStatus || envelope.Error.Code != tc.wantCode {
				t.Fatalf("status/code = %d/%q, want %d/%s", response.StatusCode, envelope.Error.Code, tc.wantStatus, tc.wantCode)
			}
		})
	}
}

func TestPanicRecoveryReturnsInternalError(t *testing.T) {
	t.Parallel()
	server := readyErrorServer(t, panicProvider{})
	response, raw, envelope := postRaw(t, server.URL+"/api/explain", `{"path":"internal/order/service.go","line":5,"column":6,"depth":"hover"}`)

	if response.StatusCode != http.StatusInternalServerError || envelope.Error.Code != "INTERNAL_ERROR" {
		t.Fatalf("status/code = %d/%q, want 500/INTERNAL_ERROR", response.StatusCode, envelope.Error.Code)
	}
	if envelope.Error.Message != "Internal error." {
		t.Fatalf("message = %q, want generic internal message", envelope.Error.Message)
	}
	if strings.Contains(string(raw), "sk-PANIC-LEAK") || strings.Contains(string(raw), "blew up") {
		t.Fatalf("panic detail leaked into the body: %s", raw)
	}
}
