package ai

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func probeProvider(t *testing.T, opts Options) CapabilityProbe {
	t.Helper()
	if opts.ProbeTimeout == 0 {
		opts.ProbeTimeout = time.Second
	}
	provider := NewOpenAICompatible(opts)
	probe, ok := provider.(CapabilityProbe)
	if !ok {
		t.Fatalf("provider %T does not implement CapabilityProbe", provider)
	}
	return probe
}

func jsonServer(status int, body string) *httptest.Server {
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(status)
		_, _ = w.Write([]byte(body))
	}))
}

func TestProbeChatSuccess(t *testing.T) {
	t.Parallel()
	server := jsonServer(http.StatusOK, `{"choices":[{"message":{"content":"pong"}}]}`)
	defer server.Close()
	result := probeProvider(t, Options{BaseURL: server.URL, Model: "default"}).ProbeChat(context.Background())
	if result.Status != ProbeSuccess {
		t.Fatalf("ProbeChat = %#v, want success", result)
	}
}

func TestProbeChatRejectsUnsupportedStructuredSchema(t *testing.T) {
	t.Parallel()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var request map[string]any
		if err := json.NewDecoder(r.Body).Decode(&request); err != nil {
			t.Fatalf("decode request: %v", err)
		}
		w.Header().Set("Content-Type", "application/json")
		if request["response_format"] != nil {
			w.WriteHeader(http.StatusBadRequest)
			_, _ = w.Write([]byte(`{"error":{"message":"Grammar error: Unimplemented keys","type":"BadRequestError"}}`))
			return
		}
		_, _ = w.Write([]byte(`{"choices":[{"message":{"content":"pong"}}]}`))
	}))
	defer server.Close()

	result := probeProvider(t, Options{
		BaseURL:               server.URL,
		Model:                 "default",
		StructuredProbeSchema: json.RawMessage(`{"type":"object","properties":{"ids":{"type":"array","uniqueItems":true}}}`),
	}).ProbeChat(context.Background())
	if result.Status != ProbeFailure || result.ErrorCode != CodeStructuredOutputUnsupported {
		t.Fatalf("ProbeChat = %#v, want failure/%s", result, CodeStructuredOutputUnsupported)
	}
}

func TestProbeChatFailureClassification(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name     string
		status   int
		body     string
		wantCode string
	}{
		{"unauthorized", http.StatusUnauthorized, `{"error":"nope"}`, CodeProviderUnauthorized},
		{"model-not-found", http.StatusNotFound, `{"error":"missing"}`, CodeChatModelInvalid},
		{"rate-limited", http.StatusTooManyRequests, `{"error":"slow down"}`, CodeProviderRateLimited},
		{"invalid-json", http.StatusOK, `not json at all`, CodeChatResponseInvalid},
		{"empty-choices", http.StatusOK, `{"choices":[]}`, CodeChatResponseInvalid},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			server := jsonServer(tc.status, tc.body)
			defer server.Close()
			result := probeProvider(t, Options{BaseURL: server.URL, Model: "default"}).ProbeChat(context.Background())
			if result.Status != ProbeFailure || result.ErrorCode != tc.wantCode {
				t.Fatalf("ProbeChat = %#v, want failure/%s", result, tc.wantCode)
			}
		})
	}
}

func TestProbeChatTimeout(t *testing.T) {
	t.Parallel()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		select {
		case <-time.After(2 * time.Second):
			w.WriteHeader(http.StatusOK)
		case <-r.Context().Done():
		}
	}))
	defer server.Close()
	result := probeProvider(t, Options{BaseURL: server.URL, Model: "default", ProbeTimeout: 20 * time.Millisecond}).ProbeChat(context.Background())
	if result.Status != ProbeFailure || result.ErrorCode != CodeProviderTimeout {
		t.Fatalf("ProbeChat = %#v, want failure/%s", result, CodeProviderTimeout)
	}
}

func TestProbeChatNeverLeaksAPIKey(t *testing.T) {
	t.Parallel()
	const secret = "sk-supersecret-key"
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// Echo the Authorization header to ensure the probe strips it from diagnostics.
		w.WriteHeader(http.StatusBadRequest)
		_, _ = w.Write([]byte(`{"error":"the model was not found; received auth ` + r.Header.Get("Authorization") + `"}`))
	}))
	defer server.Close()
	result := probeProvider(t, Options{BaseURL: server.URL, APIKey: secret, Model: "default"}).ProbeChat(context.Background())
	if result.ErrorCode != CodeChatModelInvalid {
		t.Fatalf("ErrorCode = %q, want %s", result.ErrorCode, CodeChatModelInvalid)
	}
	if strings.Contains(result.Message, secret) {
		t.Fatalf("probe message leaked the API key: %q", result.Message)
	}
}

func TestProbeChatRespectsCancellation(t *testing.T) {
	t.Parallel()
	server := jsonServer(http.StatusOK, `{"choices":[{"message":{"content":"pong"}}]}`)
	defer server.Close()
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	result := probeProvider(t, Options{BaseURL: server.URL, Model: "default"}).ProbeChat(ctx)
	if result.Status != ProbeFailure {
		t.Fatalf("ProbeChat(cancelled) = %#v, want failure", result)
	}
}

func TestDisabledProviderProbeChatReportsUnconfiguredFailure(t *testing.T) {
	t.Parallel()
	disabled := Disabled{}
	got := disabled.ProbeChat(context.Background())
	if got.Status != ProbeFailure {
		t.Fatalf("Disabled.ProbeChat = %#v, want failure", got)
	}
	if got.ErrorCode != CodeProviderURLInvalid {
		t.Fatalf("Disabled.ProbeChat ErrorCode = %q, want %s", got.ErrorCode, CodeProviderURLInvalid)
	}
}
