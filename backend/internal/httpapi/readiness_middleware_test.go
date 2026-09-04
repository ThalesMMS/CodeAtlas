package httpapi_test

import (
	"encoding/json"
	"io"
	"net/http"
	"regexp"
	"strings"
	"testing"

	"github.com/ThalesMMS/CodeAtlas/internal/capabilities"
	"github.com/ThalesMMS/CodeAtlas/internal/readiness"
)

func TestReadinessGateBlocksFunctionalRoutes(t *testing.T) {
	t.Parallel()
	server := healthServer(t, coordinatorInState(t, readiness.StateIndexing), capabilities.NewRegistry())

	functional := []string{"/api/tree", "/api/file?path=x", "/api/codemaps", "/api/deepwiki", "/api/events"}
	for _, path := range functional {
		response, err := http.Get(server.URL + path)
		if err != nil {
			t.Fatal(err)
		}
		if response.StatusCode != http.StatusServiceUnavailable {
			response.Body.Close()
			t.Fatalf("%s status = %d, want 503", path, response.StatusCode)
		}
		var body struct {
			Error struct {
				Code      string `json:"code"`
				State     string `json:"state"`
				Retryable bool   `json:"retryable"`
			} `json:"error"`
		}
		_ = json.NewDecoder(response.Body).Decode(&body)
		response.Body.Close()
		if body.Error.Code != "APP_NOT_READY" || body.Error.State != "INDEXING" || !body.Error.Retryable {
			t.Fatalf("%s envelope = %#v, want APP_NOT_READY/INDEXING/retryable", path, body.Error)
		}
	}
}

func TestReadinessGateAllowsDiagnosticsAndStatic(t *testing.T) {
	t.Parallel()
	server := healthServer(t, coordinatorInState(t, readiness.StateBooting), capabilities.NewRegistry())

	allowed := []string{"/api/health/live", "/api/health/ready", "/api/capabilities", "/api/stats", "/"}
	for _, path := range allowed {
		response, err := http.Get(server.URL + path)
		if err != nil {
			t.Fatal(err)
		}
		response.Body.Close()
		// /ready returns 503 by contract while not READY, but it must not be gated
		// with the APP_NOT_READY envelope — it answers with its own readiness body.
		if path == "/api/health/ready" {
			if response.StatusCode != http.StatusServiceUnavailable {
				t.Fatalf("%s status = %d, want 503 (its own contract)", path, response.StatusCode)
			}
			continue
		}
		if response.StatusCode != http.StatusOK {
			t.Fatalf("%s status = %d, want 200 (allowed before READY)", path, response.StatusCode)
		}
	}
}

func TestSettingsRoutesAllowedBeforeReady(t *testing.T) {
	t.Parallel()
	server := healthServer(t, coordinatorInState(t, readiness.StateAwaitingConfiguration), capabilities.NewRegistry())
	for _, path := range []string{"/api/settings", "/api/settings/overrides", "/api/settings/restart"} {
		response, err := http.Get(server.URL + path)
		if err != nil {
			t.Fatal(err)
		}
		body, _ := io.ReadAll(response.Body)
		response.Body.Close()
		if response.StatusCode == http.StatusServiceUnavailable && strings.Contains(string(body), "APP_NOT_READY") {
			t.Fatalf("%s was blocked by readiness: %s", path, body)
		}
	}
}

func TestUnknownAPIRouteDoesNotFallThroughToSPA(t *testing.T) {
	t.Parallel()
	server := healthServer(t, coordinatorInState(t, readiness.StateReady), capabilities.NewRegistry())

	response, err := http.Get(server.URL + "/api/nonexistent")
	if err != nil {
		t.Fatal(err)
	}
	defer response.Body.Close()

	if response.StatusCode != http.StatusNotFound {
		t.Fatalf("GET /api/nonexistent status = %d, want 404", response.StatusCode)
	}
	if got := response.Header.Get("Content-Type"); got == "text/html; charset=utf-8" {
		t.Fatalf("GET /api/nonexistent Content-Type = %q, want non-SPA 404", got)
	}
}

func TestStaticFrontendUsesRestrictiveCSP(t *testing.T) {
	t.Parallel()
	server := healthServer(t, coordinatorInState(t, readiness.StateReady), capabilities.NewRegistry())

	response, err := http.Get(server.URL + "/")
	if err != nil {
		t.Fatal(err)
	}
	body, err := io.ReadAll(response.Body)
	response.Body.Close()
	if err != nil {
		t.Fatal(err)
	}

	got := response.Header.Get("Content-Security-Policy")
	noncePattern := regexp.MustCompile(`<meta name="codeatlas-style-nonce" content="([A-Za-z0-9+/]+={0,2})">`)
	nonceMatch := noncePattern.FindSubmatch(body)
	if len(nonceMatch) != 2 {
		t.Fatalf("frontend nonce meta = %q, want one generated nonce", body)
	}
	nonce := string(nonceMatch[1])
	wantParts := []string{
		"default-src 'self'",
		"script-src 'self'",
		"style-src 'self'",
		"style-src-elem 'self' 'nonce-" + nonce + "'",
		"style-src-attr 'unsafe-inline'",
		"connect-src 'self'",
		"worker-src 'self'",
		"object-src 'none'",
		"frame-ancestors 'none'",
	}
	for _, part := range wantParts {
		if !strings.Contains(got, part) {
			t.Fatalf("CSP = %q, missing %q", got, part)
		}
	}
	if strings.Contains(got, "'unsafe-eval'") || strings.Contains(got, "style-src 'self' 'unsafe-inline'") || strings.Contains(got, "style-src-elem 'self' 'unsafe-inline'") {
		t.Fatalf("CSP = %q, want scripts and style elements protected", got)
	}
	if cache := response.Header.Get("Cache-Control"); cache != "no-store" {
		t.Fatalf("frontend Cache-Control = %q, want no-store for nonce-bound HTML", cache)
	}

	second, err := http.Get(server.URL + "/")
	if err != nil {
		t.Fatal(err)
	}
	secondBody, err := io.ReadAll(second.Body)
	second.Body.Close()
	if err != nil {
		t.Fatal(err)
	}
	secondNonce := noncePattern.FindSubmatch(secondBody)
	if len(secondNonce) != 2 || string(secondNonce[1]) == nonce {
		t.Fatalf("second frontend nonce = %q, want a fresh value distinct from %q", secondNonce, nonce)
	}
}
