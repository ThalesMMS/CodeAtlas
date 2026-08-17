package httpapi_test

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	"github.com/ThalesMMS/CodeAtlas/internal/capabilities"
	"github.com/ThalesMMS/CodeAtlas/internal/httpapi"
	"github.com/ThalesMMS/CodeAtlas/internal/readiness"
	"github.com/ThalesMMS/CodeAtlas/internal/settings"
)

const settingsTestToken = "test-settings-token"

type httpSettingsDocumentStore struct {
	mu       sync.Mutex
	document settings.Document
}

func (s *httpSettingsDocumentStore) Load(context.Context) (settings.Document, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	document := s.document
	if document.SchemaVersion == 0 {
		document.SchemaVersion = settings.SettingsSchemaVersion
	}
	if document.Overrides == nil {
		document.Overrides = settings.Overrides{}
	}
	return document, nil
}

func (s *httpSettingsDocumentStore) Save(_ context.Context, document settings.Document) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.document = document
	return nil
}

type httpSettingsCredentialStore struct {
	mu     sync.Mutex
	values map[string]string
	setErr error
}

func (s *httpSettingsCredentialStore) Get(_ context.Context, account string) (string, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	value, ok := s.values[account]
	if !ok {
		return "", settings.ErrCredentialNotFound
	}
	return value, nil
}

func (s *httpSettingsCredentialStore) Set(_ context.Context, account, value string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.setErr != nil {
		return s.setErr
	}
	s.values[account] = value
	return nil
}

func (s *httpSettingsCredentialStore) Delete(_ context.Context, account string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	delete(s.values, account)
	return nil
}

type httpSettingsPreparer struct{}

func (httpSettingsPreparer) Prepare(context.Context, settings.Resolved, settings.ChangeSet) (settings.PreparedRuntime, error) {
	return httpSettingsPrepared{}, nil
}

type httpSettingsPrepared struct{}

func (httpSettingsPrepared) Activate() settings.ActivationResult {
	return settings.ActivationResult{Applied: []settings.Group{settings.GroupLLM}, EmbeddingJobID: "embedding-job-1"}
}
func (httpSettingsPrepared) Abort(context.Context) {}

func newSettingsHTTPServer(t *testing.T, credentials *httpSettingsCredentialStore) (*httptest.Server, *settings.Manager) {
	t.Helper()
	if credentials == nil {
		credentials = &httpSettingsCredentialStore{values: map[string]string{}}
	}
	manager, err := settings.NewManager(context.Background(), settings.Environment{
		settings.FieldLLMBaseURL: "http://127.0.0.1:11434/v1",
		settings.FieldLLMModel:   "test-model",
	}, &httpSettingsDocumentStore{}, credentials, httpSettingsPreparer{})
	if err != nil {
		t.Fatalf("NewManager() error = %v", err)
	}
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	coordinator := coordinatorInState(t, readiness.StateAwaitingConfiguration)
	api := httpapi.New(nil, nil, nil, nil, nil, nil, nil, nil, nil, coordinator, capabilities.NewRegistry(), logger)
	api.SetSettingsManager(manager, settingsTestToken)
	server := httptest.NewServer(api.Handler())
	t.Cleanup(server.Close)
	return server, manager
}

func settingsRequest(t *testing.T, server *httptest.Server, method, path, body string) *http.Request {
	t.Helper()
	request, err := http.NewRequest(method, server.URL+path, strings.NewReader(body))
	if err != nil {
		t.Fatalf("NewRequest() error = %v", err)
	}
	request.Header.Set("X-CodeAtlas-Settings-Token", settingsTestToken)
	if method != http.MethodGet {
		request.Header.Set("Content-Type", "application/json")
		request.Header.Set("Origin", server.URL)
	}
	return request
}

func doSettingsRequest(t *testing.T, request *http.Request) *http.Response {
	t.Helper()
	response, err := http.DefaultClient.Do(request)
	if err != nil {
		t.Fatalf("Do() error = %v", err)
	}
	t.Cleanup(func() { _ = response.Body.Close() })
	return response
}

func responseBody(t *testing.T, response *http.Response) string {
	t.Helper()
	data, err := io.ReadAll(response.Body)
	if err != nil {
		t.Fatalf("ReadAll() error = %v", err)
	}
	return string(data)
}

func TestSettingsAPIGetPutConflictAndReset(t *testing.T) {
	server, _ := newSettingsHTTPServer(t, nil)

	get := doSettingsRequest(t, settingsRequest(t, server, http.MethodGet, "/api/settings", ""))
	if get.StatusCode != http.StatusOK {
		t.Fatalf("GET status = %d body=%s", get.StatusCode, responseBody(t, get))
	}
	if got := get.Header.Get("Cache-Control"); got != "no-store" {
		t.Fatalf("GET Cache-Control = %q, want no-store", got)
	}
	var initial settings.SanitizedSnapshot
	if err := json.NewDecoder(get.Body).Decode(&initial); err != nil {
		t.Fatalf("decode GET = %v", err)
	}
	fieldCount := 0
	for _, fields := range initial.Groups {
		fieldCount += len(fields)
		for _, field := range fields {
			if field.Key == settings.FieldLLMAPIKey || field.Key == settings.FieldEmbeddingsAPIKey {
				if field.Value != nil || field.RunningValue != nil || field.Configured == nil {
					t.Fatalf("secret field leaked or omitted status: %+v", field)
				}
			}
		}
	}
	if fieldCount != len(settings.DocumentedFields()) {
		t.Fatalf("GET field count = %d, want %d", fieldCount, len(settings.DocumentedFields()))
	}

	secret := "request-secret-must-never-be-returned"
	putBody := `{"revision":0,"overrides":{"CODEATLAS_LLM_MODEL":{"operation":"replace","value":"next-model"},"CODEATLAS_WORKSPACE":{"operation":"replace","value":"next-workspace"}},"secrets":{"CODEATLAS_LLM_API_KEY":{"operation":"replace","value":"` + secret + `"}}}`
	put := doSettingsRequest(t, settingsRequest(t, server, http.MethodPut, "/api/settings", putBody))
	putResponse := responseBody(t, put)
	if put.StatusCode != http.StatusOK {
		t.Fatalf("PUT status = %d body=%s", put.StatusCode, putResponse)
	}
	if strings.Contains(putResponse, secret) {
		t.Fatal("PUT response contains submitted secret")
	}
	var updated settings.UpdateResult
	if err := json.Unmarshal([]byte(putResponse), &updated); err != nil {
		t.Fatalf("decode PUT = %v", err)
	}
	if updated.Snapshot.Revision != 1 || updated.EmbeddingJobID != "embedding-job-1" {
		t.Fatalf("PUT result = %+v", updated)
	}
	if len(updated.RestartRequired) != 1 || updated.RestartRequired[0] != settings.FieldWorkspace {
		t.Fatalf("restartRequired = %v, want workspace", updated.RestartRequired)
	}

	conflict := doSettingsRequest(t, settingsRequest(t, server, http.MethodPut, "/api/settings", `{"revision":0}`))
	conflictBody := responseBody(t, conflict)
	if conflict.StatusCode != http.StatusConflict || !strings.Contains(conflictBody, settings.SettingsRevisionConflict) || !strings.Contains(conflictBody, `"revision":1`) {
		t.Fatalf("conflict = %d %s", conflict.StatusCode, conflictBody)
	}

	reset := doSettingsRequest(t, settingsRequest(t, server, http.MethodDelete, "/api/settings/overrides", `{"revision":1}`))
	resetBody := responseBody(t, reset)
	if reset.StatusCode != http.StatusOK || !strings.Contains(resetBody, `"revision":2`) {
		t.Fatalf("DELETE = %d %s", reset.StatusCode, resetBody)
	}
}

func TestSettingsAPISecurityBoundary(t *testing.T) {
	server, _ := newSettingsHTTPServer(t, nil)
	tests := []struct {
		name       string
		mutate     func(*http.Request)
		wantStatus int
	}{
		{name: "missing token", mutate: func(r *http.Request) { r.Header.Del("X-CodeAtlas-Settings-Token") }, wantStatus: http.StatusForbidden},
		{name: "wrong token", mutate: func(r *http.Request) { r.Header.Set("X-CodeAtlas-Settings-Token", "wrong") }, wantStatus: http.StatusForbidden},
		{name: "non-loopback host", mutate: func(r *http.Request) { r.Host = "example.test" }, wantStatus: http.StatusForbidden},
		{name: "mismatched origin", mutate: func(r *http.Request) { r.Header.Set("Origin", "http://localhost:9") }, wantStatus: http.StatusForbidden},
		{name: "missing mutation origin", mutate: func(r *http.Request) { r.Header.Del("Origin") }, wantStatus: http.StatusForbidden},
		{name: "wrong media type", mutate: func(r *http.Request) { r.Header.Set("Content-Type", "text/plain") }, wantStatus: http.StatusUnsupportedMediaType},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			request := settingsRequest(t, server, http.MethodPut, "/api/settings", `{"revision":0}`)
			tc.mutate(request)
			response := doSettingsRequest(t, request)
			if response.StatusCode != tc.wantStatus {
				t.Fatalf("status = %d body=%s, want %d", response.StatusCode, responseBody(t, response), tc.wantStatus)
			}
		})
	}

	oversized := settingsRequest(t, server, http.MethodPut, "/api/settings", `{"revision":0,"padding":"`+strings.Repeat("x", 65<<10)+`"}`)
	response := doSettingsRequest(t, oversized)
	if response.StatusCode != http.StatusRequestEntityTooLarge {
		t.Fatalf("oversized status = %d body=%s", response.StatusCode, responseBody(t, response))
	}

	api := httpapi.New(nil, nil, nil, nil, nil, nil, nil, nil, nil, coordinatorInState(t, readiness.StateReady), capabilities.NewRegistry(), slog.New(slog.NewTextHandler(io.Discard, nil)))
	_, manager := newSettingsHTTPServer(t, nil)
	api.SetSettingsManager(manager, settingsTestToken)
	handler := api.Handler()
	direct := httptest.NewRequest(http.MethodGet, "http://localhost/api/settings", nil)
	direct.RemoteAddr = "198.51.100.20:4321"
	direct.Header.Set("X-CodeAtlas-Settings-Token", settingsTestToken)
	recorder := httptest.NewRecorder()
	handler.ServeHTTP(recorder, direct)
	if recorder.Code != http.StatusForbidden {
		t.Fatalf("non-loopback peer status = %d body=%s", recorder.Code, recorder.Body.String())
	}
}

func TestSettingsAPIRejectsUnknownJSONAndDoesNotLeakVaultFailure(t *testing.T) {
	credentials := &httpSettingsCredentialStore{values: map[string]string{}, setErr: errors.New("vault backend request-secret failure")}
	server, _ := newSettingsHTTPServer(t, credentials)

	unknown := doSettingsRequest(t, settingsRequest(t, server, http.MethodPut, "/api/settings", `{"revision":0,"unknown":true}`))
	if unknown.StatusCode != http.StatusBadRequest {
		t.Fatalf("unknown JSON status = %d body=%s", unknown.StatusCode, responseBody(t, unknown))
	}

	secret := "request-secret-failure"
	body := `{"revision":0,"secrets":{"CODEATLAS_LLM_API_KEY":{"operation":"replace","value":"` + secret + `"}}}`
	failed := doSettingsRequest(t, settingsRequest(t, server, http.MethodPut, "/api/settings", body))
	failedBody := responseBody(t, failed)
	if failed.StatusCode != http.StatusServiceUnavailable || !strings.Contains(failedBody, settings.SettingsVaultFailed) {
		t.Fatalf("vault failure = %d %s", failed.StatusCode, failedBody)
	}
	if strings.Contains(failedBody, secret) || strings.Contains(failedBody, "backend") {
		t.Fatalf("vault response leaks internal or submitted value: %s", failedBody)
	}
}

func TestSettingsAPIMutationRequiresASingleJSONObject(t *testing.T) {
	server, _ := newSettingsHTTPServer(t, nil)
	request := settingsRequest(t, server, http.MethodPut, "/api/settings", "{} {}")
	response := doSettingsRequest(t, request)
	if response.StatusCode != http.StatusBadRequest {
		t.Fatalf("trailing JSON status = %d body=%s", response.StatusCode, responseBody(t, response))
	}
}
