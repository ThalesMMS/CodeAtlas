package httpapi_test

import (
	"bytes"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"strings"
	"testing"

	"github.com/ThalesMMS/CodeAtlas/internal/observability"
)

func TestAccessLogHasCorrelationFields(t *testing.T) {
	t.Parallel()
	buffer := &bytes.Buffer{}
	logger := slog.New(slog.NewJSONHandler(buffer, &slog.HandlerOptions{Level: slog.LevelDebug}))
	server := readyErrorServerLogged(t, staticProvider{response: "ok"}, logger, observability.NewMetrics())

	// A functional (non-diagnostic) route logs the access line at Info.
	response, _, _ := postRaw(t, server.URL+"/api/codemaps", `{"query":""}`)
	requestID := response.Header.Get("X-Request-ID")
	if requestID == "" {
		t.Fatal("response missing X-Request-ID header")
	}

	var found map[string]any
	for _, line := range strings.Split(strings.TrimSpace(buffer.String()), "\n") {
		var entry map[string]any
		if json.Unmarshal([]byte(line), &entry) != nil {
			continue
		}
		if entry["msg"] == "http request" && entry["requestId"] == requestID {
			found = entry
			break
		}
	}
	if found == nil {
		t.Fatalf("no access log line for requestId %s:\n%s", requestID, buffer.String())
	}
	for _, field := range []string{"method", "route", "status", "durationMs", "bytes", "requestId"} {
		if _, ok := found[field]; !ok {
			t.Errorf("access log missing field %q: %v", field, found)
		}
	}
	if found["method"] != "POST" || found["route"] != "POST /api/codemaps" {
		t.Fatalf("access log method/route = %v/%v", found["method"], found["route"])
	}
}

func TestRequestIDAcceptedFromClientWhenValid(t *testing.T) {
	t.Parallel()
	server := readyErrorServer(t, staticProvider{response: "ok"})

	// A well-formed client id is echoed back unchanged.
	valid := "client-Req-ID_123"
	request, _ := http.NewRequest(http.MethodGet, server.URL+"/api/stats", nil)
	request.Header.Set("X-Request-ID", valid)
	response, err := http.DefaultClient.Do(request)
	if err != nil {
		t.Fatal(err)
	}
	response.Body.Close()
	if got := response.Header.Get("X-Request-ID"); got != valid {
		t.Fatalf("valid client id not honored: got %q want %q", got, valid)
	}

	// A malformed id (too short, illegal chars) is replaced by a fresh one.
	request2, _ := http.NewRequest(http.MethodGet, server.URL+"/api/stats", nil)
	request2.Header.Set("X-Request-ID", "bad id!")
	response2, err := http.DefaultClient.Do(request2)
	if err != nil {
		t.Fatal(err)
	}
	response2.Body.Close()
	if got := response2.Header.Get("X-Request-ID"); got == "bad id!" || got == "" {
		t.Fatalf("malformed client id was not replaced: %q", got)
	}
}

func TestStatsExposesMetricsSnapshot(t *testing.T) {
	t.Parallel()
	server := readyErrorServerLogged(t, staticProvider{response: "ok"}, slog.New(slog.NewTextHandler(io.Discard, nil)), observability.NewMetrics())

	// Warm-up request so the counter (incremented after each request completes)
	// reflects prior traffic by the time /api/stats reads the snapshot.
	if warmup, err := http.Get(server.URL + "/api/health"); err == nil {
		warmup.Body.Close()
	}

	response, err := http.Get(server.URL + "/api/stats")
	if err != nil {
		t.Fatal(err)
	}
	defer response.Body.Close()
	var payload struct {
		Files   int `json:"files"`
		Metrics struct {
			ReadinessState    string `json:"readinessState"`
			HTTPRequestsTotal int64  `json:"httpRequestsTotal"`
			IndexScansTotal   int64  `json:"indexScansTotal"`
		} `json:"metrics"`
	}
	if err := json.NewDecoder(response.Body).Decode(&payload); err != nil {
		t.Fatal(err)
	}
	if payload.Metrics.ReadinessState != "READY" {
		t.Fatalf("stats metrics readinessState = %q, want READY", payload.Metrics.ReadinessState)
	}
	if payload.Metrics.HTTPRequestsTotal < 1 {
		t.Fatalf("httpRequestsTotal = %d, want >=1", payload.Metrics.HTTPRequestsTotal)
	}
}
