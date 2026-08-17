package httpapi_test

import (
	"net/http"
	"testing"
)

func TestNavigationEndpointReturnsStructuredDefinition(t *testing.T) {
	t.Parallel()
	server := readyErrorServer(t, staticProvider{response: "ok"})

	resp, body := doJSON(t, http.MethodPost, server.URL+"/api/navigation/query", nil, map[string]any{
		"kind":  "definition",
		"path":  "internal/order/service.go",
		"limit": 9999,
		"position": map[string]any{
			"line": 4, "column": 31, "encoding": "utf-16",
		},
	})
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("navigation status = %d body=%v", resp.StatusCode, body)
	}
	if body["kind"] != "definition" || body["snapshotId"] == "" || body["viewHash"] == "" {
		t.Fatalf("navigation metadata missing: %v", body)
	}
	if body["total"].(float64) != 1 || body["truncated"].(bool) {
		t.Fatalf("navigation total/truncated = %v/%v", body["total"], body["truncated"])
	}
	targets := body["targets"].([]any)
	target := targets[0].(map[string]any)
	if target["label"] != "Save" || target["relationship"] != "definition" || target["external"].(bool) {
		t.Fatalf("target = %v, want internal Save definition", target)
	}
	coverage := body["semanticCoverage"].(map[string]any)
	if coverage["coverage"] != "ast_only" || coverage["providerState"] != "disabled" || coverage["llm"] != false {
		t.Fatalf("coverage = %v, want explicit no-LLM AST-only coverage", coverage)
	}
}

func TestNavigationEndpointUsesOverlayDocumentSnapshot(t *testing.T) {
	t.Parallel()
	server, _ := buildDocServer(t)

	resp, open := doJSON(t, http.MethodPost, server.URL+"/api/documents/open", nil, map[string]any{"path": "web/checkout.ts"})
	if resp.StatusCode != http.StatusCreated {
		t.Fatalf("open status=%d body=%v", resp.StatusCode, open)
	}
	documentID := open["documentId"].(string)
	leaseID := open["leaseId"].(string)

	overlayContent := "export function finish() { return 2 }\n"
	resp, replaced := doJSON(t, http.MethodPut, server.URL+"/api/documents/"+documentID+"/content", nil, map[string]any{
		"leaseId": leaseID, "expectedVersion": 1, "newVersion": 2, "content": overlayContent,
	})
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("replace status=%d body=%v", resp.StatusCode, replaced)
	}

	resp, body := doJSON(t, http.MethodPost, server.URL+"/api/navigation/query", nil, map[string]any{
		"kind":            "definition",
		"path":            "web/checkout.ts",
		"documentId":      documentID,
		"documentVersion": 2,
		"position": map[string]any{
			"line": 1, "column": 19, "encoding": "utf-16",
		},
	})
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("overlay navigation status=%d body=%v", resp.StatusCode, body)
	}
	if resp.Header.Get("Cache-Control") != "no-store" {
		t.Fatalf("overlay navigation missing Cache-Control: no-store")
	}
	if body["documentId"] != documentID || body["documentVersion"].(float64) != 2 || body["viewHash"] == "" {
		t.Fatalf("overlay metadata = %v, want document id/version/view hash", body)
	}
	targets := body["targets"].([]any)
	if body["total"].(float64) != 1 || targets[0].(map[string]any)["label"] != "finish" {
		t.Fatalf("overlay targets = %v, want finish from unsaved overlay", body["targets"])
	}
}

func TestNavigationEndpointRejectsInvalidRequests(t *testing.T) {
	t.Parallel()
	server := readyErrorServer(t, staticProvider{response: "ok"})

	resp, body := doJSON(t, http.MethodPost, server.URL+"/api/navigation/query", nil, map[string]any{
		"kind": "rename",
	})
	if resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("unknown kind status = %d body=%v", resp.StatusCode, body)
	}

	resp, body = doJSON(t, http.MethodPost, server.URL+"/api/navigation/query", nil, map[string]any{
		"kind": "definition",
	})
	if resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("missing target status = %d body=%v", resp.StatusCode, body)
	}
}

func TestNavigationEndpointRejectsOverlayVersionAndPathMismatches(t *testing.T) {
	t.Parallel()
	server, _ := buildDocServer(t)

	resp, open := doJSON(t, http.MethodPost, server.URL+"/api/documents/open", nil, map[string]any{"path": "web/checkout.ts"})
	if resp.StatusCode != http.StatusCreated {
		t.Fatalf("open status=%d body=%v", resp.StatusCode, open)
	}
	documentID := open["documentId"].(string)

	resp, body := doJSON(t, http.MethodPost, server.URL+"/api/navigation/query", nil, map[string]any{
		"kind":       "definition",
		"documentId": documentID,
		"path":       "web/checkout.ts",
		"position": map[string]any{
			"line": 1, "column": 19, "encoding": "utf-16",
		},
	})
	if resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("missing documentVersion status=%d body=%v", resp.StatusCode, body)
	}

	resp, body = doJSON(t, http.MethodPost, server.URL+"/api/navigation/query", nil, map[string]any{
		"kind":            "definition",
		"documentId":      documentID,
		"documentVersion": 1,
		"path":            "web/other.ts",
		"position": map[string]any{
			"line": 1, "column": 19, "encoding": "utf-16",
		},
	})
	if resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("path mismatch status=%d body=%v", resp.StatusCode, body)
	}
}
