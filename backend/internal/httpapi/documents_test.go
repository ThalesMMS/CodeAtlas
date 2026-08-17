package httpapi_test

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"testing"

	"github.com/ThalesMMS/CodeAtlas/internal/capabilities"
	"github.com/ThalesMMS/CodeAtlas/internal/domain"
	"github.com/ThalesMMS/CodeAtlas/internal/httpapi"
	"github.com/ThalesMMS/CodeAtlas/internal/indexer"
	codeparser "github.com/ThalesMMS/CodeAtlas/internal/parser"
	"github.com/ThalesMMS/CodeAtlas/internal/readiness"
	"github.com/ThalesMMS/CodeAtlas/internal/repository"
	"github.com/ThalesMMS/CodeAtlas/internal/retrieval"
	"github.com/ThalesMMS/CodeAtlas/internal/semantic"
	"github.com/ThalesMMS/CodeAtlas/internal/service"
)

func buildDocServer(t *testing.T) (*httptest.Server, string) {
	return buildDocServerWithIndexer(t, true)
}

func buildDocServerWithIndexer(t *testing.T, attachIndexer bool) (*httptest.Server, string) {
	return buildDocServerWithProvider(t, attachIndexer, nil)
}

func buildDocServerWithProvider(t *testing.T, attachIndexer bool, semanticProvider semantic.SemanticProvider) (*httptest.Server, string) {
	t.Helper()
	root := t.TempDir()
	writeFixture(t, root, "web/checkout.ts", "export function go() { return 1 }\n")

	repository, err := repository.OpenSQLite(context.Background(), repository.SQLiteConfig{WorkspaceRoot: t.TempDir()})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = repository.Close() })
	provider := staticProvider{response: "ok"}
	retriever := retrieval.NewHybrid(repository, provider, false)
	backgroundIndexer := indexer.New(root, 1_500_000, codeparser.New(), repository, retriever)
	if err := backgroundIndexer.Scan(context.Background()); err != nil {
		t.Fatalf("Scan: %v", err)
	}
	var attachedIndexer *indexer.Indexer
	if attachIndexer {
		attachedIndexer = backgroundIndexer
	}
	workspace := service.NewWorkspace(root)
	explainer := service.NewExplainer(repository, workspace, provider)
	codemaps := service.NewCodemapService(repository, retriever, provider)
	deepWiki := service.NewDeepWikiService(repository, provider)
	saver := service.NewSavePreparer(workspace, repository, codeparser.New(), retriever, 1_500_000)
	committer := service.NewWorkspaceCommitCoordinator(saver, workspace, repository, filepath.Join(t.TempDir(), "tx"), "")
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	apiServer := httpapi.New(
		workspace, repository, attachedIndexer, retriever, explainer, codemaps, deepWiki, committer, provider,
		coordinatorInState(t, readiness.StateReady), capabilities.NewRegistry(), logger,
	)
	if semanticProvider != nil {
		apiServer.SetSemanticProvider(semanticProvider)
	}
	server := httptest.NewServer(apiServer.Handler())
	t.Cleanup(server.Close)
	return server, root
}

func doJSON(t *testing.T, method, url string, headers map[string]string, body any) (*http.Response, map[string]any) {
	t.Helper()
	var reader io.Reader
	if body != nil {
		data, _ := json.Marshal(body)
		reader = bytes.NewReader(data)
	}
	request, err := http.NewRequest(method, url, reader)
	if err != nil {
		t.Fatal(err)
	}
	if body != nil {
		request.Header.Set("Content-Type", "application/json")
	}
	for key, value := range headers {
		request.Header.Set(key, value)
	}
	response, err := http.DefaultClient.Do(request)
	if err != nil {
		t.Fatal(err)
	}
	defer response.Body.Close()
	var decoded map[string]any
	raw, _ := io.ReadAll(response.Body)
	if len(raw) > 0 {
		_ = json.Unmarshal(raw, &decoded)
	}
	return response, decoded
}

func TestDocumentLifecycle(t *testing.T) {
	t.Parallel()
	server, _ := buildDocServer(t)

	// Open.
	resp, open := doJSON(t, http.MethodPost, server.URL+"/api/documents/open", nil, map[string]any{"path": "web/checkout.ts"})
	if resp.StatusCode != http.StatusCreated {
		t.Fatalf("open status = %d, body=%v", resp.StatusCode, open)
	}
	if resp.Header.Get("Cache-Control") != "no-store" {
		t.Fatal("open response missing Cache-Control: no-store")
	}
	documentID, _ := open["documentId"].(string)
	leaseID, _ := open["leaseId"].(string)
	if documentID == "" || leaseID == "" || open["version"].(float64) != 1 || open["dirty"].(bool) {
		t.Fatalf("unexpected open body: %v", open)
	}

	// Replace -> version 2, dirty.
	resp, replaced := doJSON(t, http.MethodPut, server.URL+"/api/documents/"+documentID+"/content", nil, map[string]any{
		"leaseId": leaseID, "expectedVersion": 1, "newVersion": 2, "content": "export function go() { return 2 }\n",
	})
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("replace status = %d, body=%v", resp.StatusCode, replaced)
	}
	if replaced["version"].(float64) != 2 || !replaced["dirty"].(bool) {
		t.Fatalf("unexpected replace body: %v", replaced)
	}
	if _, hasLease := replaced["leaseId"]; hasLease {
		t.Fatal("replace response must not echo the lease")
	}

	// Get with the lease header.
	resp, got := doJSON(t, http.MethodGet, server.URL+"/api/documents/"+documentID, map[string]string{"X-Document-Lease": leaseID}, nil)
	if resp.StatusCode != http.StatusOK || got["version"].(float64) != 2 {
		t.Fatalf("get status=%d body=%v", resp.StatusCode, got)
	}
	if got["baseContent"] != "export function go() { return 1 }\n" || got["content"] != "export function go() { return 2 }\n" {
		t.Fatalf("reclaim snapshot did not preserve dirty/base content: %v", got)
	}

	// Heartbeat renewal is deliberately metadata-only.
	resp, renewed := doJSON(t, http.MethodPost, server.URL+"/api/documents/"+documentID+"/renew?version=2", map[string]string{"X-Document-Lease": leaseID}, nil)
	if resp.StatusCode != http.StatusOK || renewed["version"].(float64) != 2 {
		t.Fatalf("renew status=%d body=%v", resp.StatusCode, renewed)
	}
	if _, hasContent := renewed["content"]; hasContent {
		t.Fatalf("renew response returned source content: %v", renewed)
	}
	if _, hasBase := renewed["baseContent"]; hasBase {
		t.Fatalf("renew response returned base content: %v", renewed)
	}

	// Save -> commits + clears dirty.
	resp, saved := doJSON(t, http.MethodPost, server.URL+"/api/documents/"+documentID+"/save", nil, map[string]any{"leaseId": leaseID, "version": 2})
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("save status=%d body=%v", resp.StatusCode, saved)
	}
	if saved["dirty"].(bool) {
		t.Fatalf("document still dirty after save: %v", saved)
	}

	// Close.
	resp, _ = doJSON(t, http.MethodDelete, server.URL+"/api/documents/"+documentID, map[string]string{"X-Document-Lease": leaseID}, nil)
	if resp.StatusCode != http.StatusNoContent {
		t.Fatalf("close status = %d", resp.StatusCode)
	}
}

func TestDocumentReclaimTransfersLeaseAtomically(t *testing.T) {
	t.Parallel()
	server, _ := buildDocServer(t)
	resp, opened := doJSON(t, http.MethodPost, server.URL+"/api/documents/open", nil, map[string]any{"path": "web/checkout.ts"})
	if resp.StatusCode != http.StatusCreated {
		t.Fatalf("open status=%d body=%v", resp.StatusCode, opened)
	}
	documentID := opened["documentId"].(string)
	oldLease := opened["leaseId"].(string)

	resp, reclaimed := doJSON(t, http.MethodPost, server.URL+"/api/documents/"+documentID+"/reclaim", map[string]string{"X-Document-Lease": oldLease}, nil)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("reclaim status=%d body=%v", resp.StatusCode, reclaimed)
	}
	newLease, _ := reclaimed["leaseId"].(string)
	if newLease == "" || newLease == oldLease || reclaimed["baseContent"] == nil {
		t.Fatalf("reclaim did not rotate lease/preserve base: %v", reclaimed)
	}

	resp, staleClose := doJSON(t, http.MethodDelete, server.URL+"/api/documents/"+documentID, map[string]string{"X-Document-Lease": oldLease}, nil)
	if resp.StatusCode != http.StatusNotFound {
		t.Fatalf("stale page close status=%d body=%v, want 404", resp.StatusCode, staleClose)
	}
	resp, _ = doJSON(t, http.MethodDelete, server.URL+"/api/documents/"+documentID, map[string]string{"X-Document-Lease": newLease}, nil)
	if resp.StatusCode != http.StatusNoContent {
		t.Fatalf("reclaimed close status=%d", resp.StatusCode)
	}
	resp, reopened := doJSON(t, http.MethodPost, server.URL+"/api/documents/open", nil, map[string]any{"path": "web/checkout.ts"})
	if resp.StatusCode != http.StatusCreated {
		t.Fatalf("reopen status=%d body=%v", resp.StatusCode, reopened)
	}
}

func TestNilIndexerFileAndDocumentSavesDoNotPanic(t *testing.T) {
	t.Parallel()
	server, _ := buildDocServerWithIndexer(t, false)

	resp, fileBody := doJSON(t, http.MethodPut, server.URL+"/api/file", nil, map[string]any{
		"path": "web/checkout.ts", "content": "export function go() { return 2 }\n", "expectedContentHash": "sha256:stale",
	})
	if resp.StatusCode != http.StatusConflict {
		t.Fatalf("stale file save status=%d body=%v, want 409 stable error", resp.StatusCode, fileBody)
	}
	envelope, _ := fileBody["error"].(map[string]any)
	if envelope["code"] != service.CodeFileChangedOnDisk {
		t.Fatalf("stale file save error=%v, want %s", fileBody, service.CodeFileChangedOnDisk)
	}

	resp, currentFile := doJSON(t, http.MethodGet, server.URL+"/api/file?path="+url.QueryEscape("web/checkout.ts"), nil, nil)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("read file status=%d body=%v", resp.StatusCode, currentFile)
	}
	resp, fileBody = doJSON(t, http.MethodPut, server.URL+"/api/file", nil, map[string]any{
		"path":                "web/checkout.ts",
		"content":             "export function go() { return 2 }\n",
		"expectedContentHash": currentFile["contentHash"],
	})
	if resp.StatusCode != http.StatusOK || fileBody["saved"] != true {
		t.Fatalf("file save with nil indexer status=%d body=%v, want saved", resp.StatusCode, fileBody)
	}

	resp, open := doJSON(t, http.MethodPost, server.URL+"/api/documents/open", nil, map[string]any{"path": "web/checkout.ts"})
	if resp.StatusCode != http.StatusCreated {
		t.Fatalf("open status=%d body=%v", resp.StatusCode, open)
	}
	documentID := open["documentId"].(string)
	leaseID := open["leaseId"].(string)
	resp, replaced := doJSON(t, http.MethodPut, server.URL+"/api/documents/"+documentID+"/content", nil, map[string]any{
		"leaseId": leaseID, "expectedVersion": 1, "newVersion": 2, "content": "export function go() { return 3 }\n",
	})
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("replace status=%d body=%v", resp.StatusCode, replaced)
	}
	resp, saved := doJSON(t, http.MethodPost, server.URL+"/api/documents/"+documentID+"/save", nil, map[string]any{
		"leaseId": leaseID, "version": 2,
	})
	if resp.StatusCode != http.StatusOK || saved["dirty"].(bool) {
		t.Fatalf("document save with nil indexer status=%d body=%v, want clean save", resp.StatusCode, saved)
	}
}

func TestDocumentApiContracts(t *testing.T) {
	t.Parallel()
	server, _ := buildDocServer(t)
	resp, open := doJSON(t, http.MethodPost, server.URL+"/api/documents/open", nil, map[string]any{"path": "web/checkout.ts"})
	if resp.StatusCode != http.StatusCreated {
		t.Fatalf("open: %d", resp.StatusCode)
	}
	documentID := open["documentId"].(string)
	leaseID := open["leaseId"].(string)

	// Second open of the same path is rejected.
	if resp, _ := doJSON(t, http.MethodPost, server.URL+"/api/documents/open", nil, map[string]any{"path": "web/checkout.ts"}); resp.StatusCode != http.StatusConflict {
		t.Fatalf("second open = %d, want 409", resp.StatusCode)
	}
	// Stale expectedVersion conflicts.
	if resp, _ := doJSON(t, http.MethodPut, server.URL+"/api/documents/"+documentID+"/content", nil, map[string]any{
		"leaseId": leaseID, "expectedVersion": 99, "newVersion": 100, "content": "x",
	}); resp.StatusCode != http.StatusConflict {
		t.Fatalf("stale replace = %d, want 409", resp.StatusCode)
	}
	// GET without a lease header is rejected.
	if resp, _ := doJSON(t, http.MethodGet, server.URL+"/api/documents/"+documentID, nil, nil); resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("get without lease = %d, want 400", resp.StatusCode)
	}
	// Missing Content-Type on a write endpoint is rejected.
	request, _ := http.NewRequest(http.MethodPost, server.URL+"/api/documents/open", bytes.NewReader([]byte(`{"path":"web/checkout.ts"}`)))
	response, err := http.DefaultClient.Do(request)
	if err != nil {
		t.Fatal(err)
	}
	response.Body.Close()
	if response.StatusCode != http.StatusBadRequest {
		t.Fatalf("missing content-type = %d, want 400", response.StatusCode)
	}
}

func TestDocumentCleanExternalChangeReloadsAndResolves(t *testing.T) {
	t.Parallel()
	server, root := buildDocServer(t)
	resp, open := doJSON(t, http.MethodPost, server.URL+"/api/documents/open", nil, map[string]any{"path": "web/checkout.ts"})
	if resp.StatusCode != http.StatusCreated {
		t.Fatalf("open status=%d body=%v", resp.StatusCode, open)
	}
	documentID := open["documentId"].(string)
	leaseID := open["leaseId"].(string)

	writeFixture(t, root, "web/checkout.ts", "export function go() { return 9 }\n")
	resp, reindex := doJSON(t, http.MethodPost, server.URL+"/api/reindex", nil, nil)
	if resp.StatusCode != http.StatusAccepted {
		t.Fatalf("reindex status=%d", resp.StatusCode)
	}
	waitHTTPJob(t, server.URL, jobIDFromBody(t, reindex), domain.JobSucceeded)

	resp, got := doJSON(t, http.MethodGet, server.URL+"/api/documents/"+documentID, map[string]string{"X-Document-Lease": leaseID}, nil)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("get status=%d body=%v", resp.StatusCode, got)
	}
	if got["version"].(float64) != 2 || got["dirty"].(bool) || got["state"] != "external_changed_clean" {
		t.Fatalf("document after external clean change = %v", got)
	}
	if got["content"] != "export function go() { return 9 }\n" {
		t.Fatalf("content = %q, want external content", got["content"])
	}
	conflict, ok := got["conflict"].(map[string]any)
	if !ok || conflict["reasonCode"] != "EXTERNAL_MODIFICATION" || conflict["externalContentHash"] == "" {
		t.Fatalf("conflict = %#v, want external modification metadata", got["conflict"])
	}

	resp, resolved := doJSON(t, http.MethodPost, server.URL+"/api/documents/"+documentID+"/resolve-conflict", nil, map[string]any{
		"leaseId": leaseID, "documentVersion": 2, "externalContentHash": conflict["externalContentHash"], "strategy": "reload_external",
	})
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("resolve reload status=%d body=%v", resp.StatusCode, resolved)
	}
	if resolved["version"].(float64) != 3 || resolved["dirty"].(bool) || resolved["state"] != "clean" {
		t.Fatalf("resolved reload body = %v", resolved)
	}
	if _, exists := resolved["conflict"]; exists {
		t.Fatalf("resolved reload still exposes conflict: %v", resolved)
	}
}

func TestDocumentDirtyExternalConflictBlocksSaveAndCanKeepLocal(t *testing.T) {
	t.Parallel()
	server, root := buildDocServer(t)
	resp, open := doJSON(t, http.MethodPost, server.URL+"/api/documents/open", nil, map[string]any{"path": "web/checkout.ts"})
	if resp.StatusCode != http.StatusCreated {
		t.Fatalf("open status=%d body=%v", resp.StatusCode, open)
	}
	documentID := open["documentId"].(string)
	leaseID := open["leaseId"].(string)
	localContent := "export function go() { return 2 }\n"
	resp, replaced := doJSON(t, http.MethodPut, server.URL+"/api/documents/"+documentID+"/content", nil, map[string]any{
		"leaseId": leaseID, "expectedVersion": 1, "newVersion": 2, "content": localContent,
	})
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("replace status=%d body=%v", resp.StatusCode, replaced)
	}

	writeFixture(t, root, "web/checkout.ts", "export function go() { return 7 }\n")
	resp, reindex := doJSON(t, http.MethodPost, server.URL+"/api/reindex", nil, nil)
	if resp.StatusCode != http.StatusAccepted {
		t.Fatalf("reindex status=%d", resp.StatusCode)
	}
	waitHTTPJob(t, server.URL, jobIDFromBody(t, reindex), domain.JobSucceeded)

	resp, got := doJSON(t, http.MethodGet, server.URL+"/api/documents/"+documentID, map[string]string{"X-Document-Lease": leaseID}, nil)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("get status=%d body=%v", resp.StatusCode, got)
	}
	if got["version"].(float64) != 2 || got["state"] != "conflicted_dirty" || got["content"] != localContent {
		t.Fatalf("document after dirty external change = %v", got)
	}
	conflict := got["conflict"].(map[string]any)
	externalHash := conflict["externalContentHash"].(string)
	if externalHash == "" {
		t.Fatalf("conflict = %#v, want external hash", conflict)
	}

	resp, saveError := doJSON(t, http.MethodPost, server.URL+"/api/documents/"+documentID+"/save", nil, map[string]any{"leaseId": leaseID, "version": 2})
	if resp.StatusCode != http.StatusConflict {
		t.Fatalf("conflicted save status=%d body=%v", resp.StatusCode, saveError)
	}
	if saveError["error"].(map[string]any)["code"] != "DOCUMENT_CONFLICT_CHANGED" {
		t.Fatalf("save error = %v, want DOCUMENT_CONFLICT_CHANGED", saveError)
	}

	resp, resolved := doJSON(t, http.MethodPost, server.URL+"/api/documents/"+documentID+"/resolve-conflict", nil, map[string]any{
		"leaseId": leaseID, "documentVersion": 2, "externalContentHash": externalHash, "strategy": "keep_local_rebase",
	})
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("resolve keep-local status=%d body=%v", resp.StatusCode, resolved)
	}
	if resolved["version"].(float64) != 3 || resolved["state"] != "local_dirty" || !resolved["dirty"].(bool) || resolved["content"] != localContent || resolved["willOverwriteExternalOnSave"] != true {
		t.Fatalf("resolved keep-local body = %v", resolved)
	}

	resp, saved := doJSON(t, http.MethodPost, server.URL+"/api/documents/"+documentID+"/save", nil, map[string]any{"leaseId": leaseID, "version": 3})
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("save after keep-local status=%d body=%v", resp.StatusCode, saved)
	}
	onDisk, err := os.ReadFile(filepath.Join(root, "web", "checkout.ts"))
	if err != nil {
		t.Fatal(err)
	}
	if string(onDisk) != localContent {
		t.Fatalf("saved content = %q, want local content", onDisk)
	}
}

func jobIDFromBody(t *testing.T, body map[string]any) domain.JobID {
	t.Helper()
	jobBody, ok := body["job"].(map[string]any)
	if !ok {
		t.Fatalf("job response = %#v, want job object", body)
	}
	id, ok := jobBody["id"].(string)
	if !ok || id == "" {
		t.Fatalf("job response = %#v, want id", body)
	}
	return domain.JobID(id)
}
