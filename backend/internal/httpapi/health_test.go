package httpapi_test

import (
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/ThalesMMS/CodeAtlas/internal/capabilities"
	"github.com/ThalesMMS/CodeAtlas/internal/httpapi"
	"github.com/ThalesMMS/CodeAtlas/internal/readiness"
)

func healthServer(t *testing.T, coord httpapi.ReadinessReader, registry httpapi.CapabilityReader) *httptest.Server {
	t.Helper()
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	handler := httpapi.New(nil, nil, nil, nil, nil, nil, nil, nil, coord, registry, logger).Handler()
	server := httptest.NewServer(handler)
	t.Cleanup(server.Close)
	return server
}

func mustTransition(t *testing.T, err error) {
	t.Helper()
	if err != nil {
		t.Fatalf("coordinator transition error = %v", err)
	}
}

func coordinatorInState(t *testing.T, target readiness.State) *readiness.Coordinator {
	t.Helper()
	c := readiness.NewCoordinator()
	switch target {
	case readiness.StateBooting:
	case readiness.StateProbingCapabilities:
		mustTransition(t, c.Transition(readiness.StateProbingCapabilities, "probe"))
	case readiness.StateIndexing:
		mustTransition(t, c.Transition(readiness.StateProbingCapabilities, "probe"))
		mustTransition(t, c.Transition(readiness.StateIndexing, "index"))
	case readiness.StateReady:
		mustTransition(t, c.Transition(readiness.StateProbingCapabilities, "probe"))
		mustTransition(t, c.Transition(readiness.StateIndexing, "index"))
		mustTransition(t, c.Transition(readiness.StateReady, "ready"))
	case readiness.StateFailed:
		mustTransition(t, c.Transition(readiness.StateProbingCapabilities, "probe"))
		c.SetStep("PROBING_CAPABILITIES")
		mustTransition(t, c.Fail("PROVIDER_UNREACHABLE", "endpoint unreachable"))
	}
	return c
}

func TestHealthLive(t *testing.T) {
	t.Parallel()
	server := healthServer(t, readiness.NewCoordinator(), capabilities.NewRegistry())
	for _, path := range []string{"/api/health/live", "/api/health"} {
		response, err := http.Get(server.URL + path)
		if err != nil {
			t.Fatal(err)
		}
		if response.StatusCode != http.StatusOK {
			t.Fatalf("%s status = %d, want 200", path, response.StatusCode)
		}
		if cache := response.Header.Get("Cache-Control"); cache != "no-store" {
			t.Fatalf("%s Cache-Control = %q, want no-store", path, cache)
		}
		var body map[string]any
		_ = json.NewDecoder(response.Body).Decode(&body)
		response.Body.Close()
		if body["status"] != "alive" {
			t.Fatalf("%s body = %#v, want status alive", path, body)
		}
	}
}

func TestHealthReadyStates(t *testing.T) {
	t.Parallel()
	cases := []struct {
		state      readiness.State
		wantStatus int
		wantBody   string
	}{
		{readiness.StateBooting, http.StatusServiceUnavailable, "not_ready"},
		{readiness.StateProbingCapabilities, http.StatusServiceUnavailable, "not_ready"},
		{readiness.StateIndexing, http.StatusServiceUnavailable, "not_ready"},
		{readiness.StateReady, http.StatusOK, "ready"},
		{readiness.StateFailed, http.StatusServiceUnavailable, "not_ready"},
	}
	for _, tc := range cases {
		t.Run(string(tc.state), func(t *testing.T) {
			t.Parallel()
			server := healthServer(t, coordinatorInState(t, tc.state), capabilities.NewRegistry())
			response, err := http.Get(server.URL + "/api/health/ready")
			if err != nil {
				t.Fatal(err)
			}
			defer response.Body.Close()
			if response.StatusCode != tc.wantStatus {
				t.Fatalf("state %s: status = %d, want %d", tc.state, response.StatusCode, tc.wantStatus)
			}
			if cache := response.Header.Get("Cache-Control"); cache != "no-store" {
				t.Fatalf("Cache-Control = %q, want no-store", cache)
			}
			var body map[string]any
			if err := json.NewDecoder(response.Body).Decode(&body); err != nil {
				t.Fatal(err)
			}
			if body["status"] != tc.wantBody {
				t.Fatalf("state %s: body status = %v, want %q", tc.state, body["status"], tc.wantBody)
			}
			if body["state"] != string(tc.state) {
				t.Fatalf("body state = %v, want %q", body["state"], tc.state)
			}
			if tc.state == readiness.StateFailed {
				errObj, ok := body["error"].(map[string]any)
				if !ok || errObj["code"] != "PROVIDER_UNREACHABLE" {
					t.Fatalf("failed body did not expose sanitized error: %#v", body)
				}
				if body["stage"] != "PROBING_CAPABILITIES" {
					t.Fatalf("failed body stage = %v, want PROBING_CAPABILITIES", body["stage"])
				}
			}
		})
	}
}

func TestCapabilitiesEndpoint(t *testing.T) {
	t.Parallel()
	registry := capabilities.NewRegistry()
	// Insert out of order; a required capability has failed.
	registry.UpdateCapability(capabilities.Result{ID: capabilities.CapabilityWorkspace, Requirement: capabilities.Required, State: capabilities.CapabilityAvailable, Metadata: map[string]string{"symlinked": "false"}})
	registry.UpdateCapability(capabilities.Result{ID: capabilities.CapabilityStore, Requirement: capabilities.Required, State: capabilities.CapabilityUnavailable, ErrorCode: capabilities.ErrCodeStoreCorrupted})

	server := healthServer(t, coordinatorInState(t, readiness.StateFailed), registry)
	response, err := http.Get(server.URL + "/api/capabilities")
	if err != nil {
		t.Fatal(err)
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want 200 even when not ready", response.StatusCode)
	}
	if cache := response.Header.Get("Cache-Control"); cache != "no-store" {
		t.Fatalf("Cache-Control = %q, want no-store", cache)
	}
	var body struct {
		Capabilities []struct {
			ID          string `json:"id"`
			Requirement string `json:"requirement"`
			State       string `json:"state"`
			ErrorCode   string `json:"errorCode"`
		} `json:"capabilities"`
	}
	if err := json.NewDecoder(response.Body).Decode(&body); err != nil {
		t.Fatal(err)
	}
	if len(body.Capabilities) != 2 {
		t.Fatalf("capabilities = %#v, want 2", body.Capabilities)
	}
	// Deterministic order: "store" < "workspace".
	if body.Capabilities[0].ID != string(capabilities.CapabilityStore) || body.Capabilities[1].ID != string(capabilities.CapabilityWorkspace) {
		t.Fatalf("ordering = %s,%s; want store,workspace", body.Capabilities[0].ID, body.Capabilities[1].ID)
	}
	if body.Capabilities[0].ErrorCode != capabilities.ErrCodeStoreCorrupted {
		t.Fatalf("store errorCode = %q", body.Capabilities[0].ErrorCode)
	}
}

func TestCapabilitiesEmpty(t *testing.T) {
	t.Parallel()
	server := healthServer(t, readiness.NewCoordinator(), capabilities.NewRegistry())
	response, err := http.Get(server.URL + "/api/capabilities")
	if err != nil {
		t.Fatal(err)
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want 200", response.StatusCode)
	}
	var body struct {
		Capabilities []any `json:"capabilities"`
	}
	if err := json.NewDecoder(response.Body).Decode(&body); err != nil {
		t.Fatal(err)
	}
	if len(body.Capabilities) != 0 {
		t.Fatalf("capabilities = %#v, want empty array", body.Capabilities)
	}
}
