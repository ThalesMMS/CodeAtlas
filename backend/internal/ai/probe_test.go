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

func TestProbeEmbeddingsDisabled(t *testing.T) {
	t.Parallel()
	result := probeProvider(t, Options{BaseURL: "http://example.invalid", Model: "default"}).ProbeEmbeddings(context.Background())
	if result.Status != ProbeDisabled {
		t.Fatalf("ProbeEmbeddings = %#v, want disabled", result)
	}
}

func TestProbeEmbeddingsModelMissing(t *testing.T) {
	t.Parallel()
	result := probeProvider(t, Options{BaseURL: "http://example.invalid", Model: "default", EnableEmbeddings: true}).ProbeEmbeddings(context.Background())
	if result.Status != ProbeFailure || result.ErrorCode != CodeEmbeddingModelMissing {
		t.Fatalf("ProbeEmbeddings = %#v, want failure/%s", result, CodeEmbeddingModelMissing)
	}
}

func TestProbeEmbeddingsSuccess(t *testing.T) {
	t.Parallel()
	server := jsonServer(http.StatusOK, `{"data":[{"embedding":[0.1,0.2,0.3,0.4]}]}`)
	defer server.Close()
	result := probeProvider(t, Options{BaseURL: server.URL, Model: "default", EmbeddingModel: "embed", EnableEmbeddings: true}).ProbeEmbeddings(context.Background())
	if result.Status != ProbeSuccess {
		t.Fatalf("ProbeEmbeddings = %#v, want success", result)
	}
	if result.Metadata["dimension"] != "4" {
		t.Fatalf("dimension metadata = %q, want 4", result.Metadata["dimension"])
	}
}

func TestProbeEmbeddingsResponseValidation(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name string
		body string
	}{
		{"no-vectors", `{"data":[]}`},
		{"too-many-vectors", `{"data":[{"embedding":[0.1]},{"embedding":[0.2]}]}`},
		{"empty-vector", `{"data":[{"embedding":[]}]}`},
		{"non-finite", `{"data":[{"embedding":[1e999]}]}`},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			server := jsonServer(http.StatusOK, tc.body)
			defer server.Close()
			result := probeProvider(t, Options{BaseURL: server.URL, Model: "default", EmbeddingModel: "embed", EnableEmbeddings: true}).ProbeEmbeddings(context.Background())
			if result.Status != ProbeFailure || result.ErrorCode != CodeEmbeddingResponseInvalid {
				t.Fatalf("ProbeEmbeddings(%s) = %#v, want failure/%s", tc.name, result, CodeEmbeddingResponseInvalid)
			}
		})
	}
}

func TestProbeEmbeddingsUsesSeparateEmbeddingBaseURL(t *testing.T) {
	t.Parallel()
	var requestPath string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requestPath = r.URL.Path
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"data":[{"embedding":[0.1,0.2,0.3]}]}`))
	}))
	defer server.Close()

	result := probeProvider(t, Options{
		BaseURL:          "http://chat.invalid/v1",
		EmbeddingBaseURL: server.URL + "/v1",
		Model:            "chat-model",
		EmbeddingModel:   "embed",
		EnableEmbeddings: true,
	}).ProbeEmbeddings(context.Background())
	if result.Status != ProbeSuccess {
		t.Fatalf("ProbeEmbeddings = %#v, want success", result)
	}
	if requestPath != "/v1/embeddings" {
		t.Fatalf("request path = %q, want /v1/embeddings", requestPath)
	}
}

func TestProbeEmbeddingsUsesEmbeddingsAPIKey(t *testing.T) {
	t.Parallel()
	var authorization string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		authorization = r.Header.Get("Authorization")
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"data":[{"embedding":[0.1,0.2,0.3]}]}`))
	}))
	defer server.Close()

	result := probeProvider(t, Options{
		BaseURL:          "http://chat.invalid/v1",
		EmbeddingBaseURL: server.URL,
		APIKey:           "chat-key",
		EmbeddingsAPIKey: "embeddings-key",
		Model:            "chat-model",
		EmbeddingModel:   "embed",
		EnableEmbeddings: true,
	}).ProbeEmbeddings(context.Background())
	if result.Status != ProbeSuccess {
		t.Fatalf("ProbeEmbeddings = %#v, want success", result)
	}
	if authorization != "Bearer embeddings-key" {
		t.Fatalf("Authorization = %q, want embeddings API key", authorization)
	}
}

func TestProbeEmbeddingsModelInvalid(t *testing.T) {
	t.Parallel()
	server := jsonServer(http.StatusNotFound, `{"error":"unknown model"}`)
	defer server.Close()
	result := probeProvider(t, Options{BaseURL: server.URL, Model: "default", EmbeddingModel: "embed", EnableEmbeddings: true}).ProbeEmbeddings(context.Background())
	if result.Status != ProbeFailure || result.ErrorCode != CodeEmbeddingModelInvalid {
		t.Fatalf("ProbeEmbeddings = %#v, want failure/%s", result, CodeEmbeddingModelInvalid)
	}
}

func TestDisabledProviderProbes(t *testing.T) {
	t.Parallel()
	disabled := Disabled{}
	if got := disabled.ProbeChat(context.Background()); got.Status != ProbeFailure {
		t.Fatalf("Disabled.ProbeChat = %#v, want failure", got)
	}
	if got := disabled.ProbeEmbeddings(context.Background()); got.Status != ProbeDisabled {
		t.Fatalf("Disabled.ProbeEmbeddings = %#v, want disabled", got)
	}
}
