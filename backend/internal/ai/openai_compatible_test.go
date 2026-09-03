package ai

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

// newTestProvider creates an OpenAICompatible provider pointing at the given URL.
func newTestProvider(t *testing.T, baseURL string) *OpenAICompatible {
	t.Helper()
	p := NewOpenAICompatible(Options{
		BaseURL:      baseURL,
		Model:        "test-model",
		ProbeTimeout: time.Second,
	})
	oc, ok := p.(*OpenAICompatible)
	if !ok {
		t.Fatalf("expected *OpenAICompatible, got %T", p)
	}
	return oc
}

// TestNewOpenAICompatibleReturnsDisabledWhenURLEmpty verifies that a missing
// BaseURL gives back a Disabled stub (Available() == false).
func TestNewOpenAICompatibleReturnsDisabledWhenURLEmpty(t *testing.T) {
	t.Parallel()
	p := NewOpenAICompatible(Options{Model: "gpt-4"})
	if p.Available() {
		t.Fatalf("provider should be unavailable when BaseURL is empty")
	}
}

// TestNewOpenAICompatibleReturnsDisabledWhenModelEmpty verifies that a missing
// Model gives back a Disabled stub.
func TestNewOpenAICompatibleReturnsDisabledWhenModelEmpty(t *testing.T) {
	t.Parallel()
	p := NewOpenAICompatible(Options{BaseURL: "http://localhost:1234"})
	if p.Available() {
		t.Fatalf("provider should be unavailable when Model is empty")
	}
}

// TestNewOpenAICompatibleDefaultProbeTimeout verifies the fallback timeout.
func TestNewOpenAICompatibleDefaultProbeTimeout(t *testing.T) {
	t.Parallel()
	p := NewOpenAICompatible(Options{BaseURL: "http://localhost", Model: "m"})
	oc, ok := p.(*OpenAICompatible)
	if !ok {
		t.Fatalf("expected *OpenAICompatible")
	}
	if oc.probeTimeout != defaultProbeTimeout {
		t.Errorf("probeTimeout = %v, want %v", oc.probeTimeout, defaultProbeTimeout)
	}
}

func TestNewOpenAICompatibleRequestTimeout(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name string
		opts Options
		want time.Duration
	}{
		{name: "default", opts: Options{}, want: 10 * time.Minute},
		{name: "custom", opts: Options{RequestTimeout: 17 * time.Minute}, want: 17 * time.Minute},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			provider := NewOpenAICompatible(Options{
				BaseURL: "http://localhost", Model: "m", RequestTimeout: tc.opts.RequestTimeout,
			})
			oc := provider.(*OpenAICompatible)
			if oc.client.Timeout != tc.want {
				t.Fatalf("client timeout = %v, want %v", oc.client.Timeout, tc.want)
			}
		})
	}
}

func TestNewOpenAICompatibleConfiguresProviderConnectionPool(t *testing.T) {
	t.Parallel()
	provider := NewOpenAICompatible(Options{BaseURL: "http://localhost", Model: "m"})
	transport, ok := provider.(*OpenAICompatible).client.Transport.(*http.Transport)
	if !ok {
		t.Fatalf("transport type = %T, want *http.Transport", provider.(*OpenAICompatible).client.Transport)
	}
	if transport.MaxIdleConns != providerMaxIdleConns || transport.MaxIdleConnsPerHost != providerMaxIdlePerHost {
		t.Fatalf("idle pool = %d/%d, want %d/%d", transport.MaxIdleConns, transport.MaxIdleConnsPerHost, providerMaxIdleConns, providerMaxIdlePerHost)
	}
	defaultTransport := http.DefaultTransport.(*http.Transport)
	if transport == defaultTransport || transport.Proxy == nil || transport.DialContext == nil {
		t.Fatal("provider transport must clone the standard transport defaults")
	}
}

func TestOpenAICompatibleBoundsConcurrentEndpointRequestsToTwo(t *testing.T) {
	t.Parallel()
	var current atomic.Int32
	var maximum atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		active := current.Add(1)
		defer current.Add(-1)
		for observed := maximum.Load(); active > observed && !maximum.CompareAndSwap(observed, active); observed = maximum.Load() {
		}
		time.Sleep(75 * time.Millisecond)
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"choices":[{"message":{"content":"ok"},"finish_reason":"stop"}]}`))
	}))
	defer server.Close()

	provider := newTestProvider(t, server.URL)
	const calls = 6
	errorsSeen := make(chan error, calls)
	var group sync.WaitGroup
	for range calls {
		group.Add(1)
		go func() {
			defer group.Done()
			_, err := provider.Complete(context.Background(), "system", "user", 100)
			errorsSeen <- err
		}()
	}
	group.Wait()
	close(errorsSeen)
	for err := range errorsSeen {
		if err != nil {
			t.Fatalf("Complete() error = %v", err)
		}
	}
	if got := maximum.Load(); got != 2 {
		t.Fatalf("maximum concurrent endpoint requests = %d, want 2", got)
	}
}

type customRoundTripper struct{}

func (customRoundTripper) RoundTrip(*http.Request) (*http.Response, error) {
	return nil, errors.New("not used")
}

func TestNewOpenAICompatibleHandlesCustomDefaultTransport(t *testing.T) {
	previous := http.DefaultTransport
	http.DefaultTransport = customRoundTripper{}
	t.Cleanup(func() { http.DefaultTransport = previous })

	provider := NewOpenAICompatible(Options{BaseURL: "http://localhost", Model: "m"})
	transport, ok := provider.(*OpenAICompatible).client.Transport.(*http.Transport)
	if !ok || transport.Proxy == nil {
		t.Fatalf("fallback transport = %T, want configured *http.Transport", provider.(*OpenAICompatible).client.Transport)
	}
}

// TestNewOpenAICompatibleStripsTrailingSlash ensures the base URL is normalised.
func TestNewOpenAICompatibleStripsTrailingSlash(t *testing.T) {
	t.Parallel()
	p := NewOpenAICompatible(Options{BaseURL: "http://localhost:8080/v1/", Model: "m"})
	oc, ok := p.(*OpenAICompatible)
	if !ok {
		t.Fatalf("expected *OpenAICompatible")
	}
	if strings.HasSuffix(oc.baseURL, "/") {
		t.Errorf("baseURL %q should not have trailing slash", oc.baseURL)
	}
}

func completionBodyServer(t *testing.T, captured chan<- map[string]any) *httptest.Server {
	t.Helper()
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var body map[string]any
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Errorf("decode request: %v", err)
			w.WriteHeader(http.StatusBadRequest)
			return
		}
		captured <- body
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"model":"test-model","choices":[{"message":{"content":"ok"},"finish_reason":"stop"}]}`))
	}))
}

func TestCompleteSelectsLegacyOrReasoningTokenField(t *testing.T) {
	for _, test := range []struct {
		name       string
		effort     string
		wantField  string
		omitField  string
		wantTokens float64
	}{
		{name: "legacy", wantField: "max_tokens", omitField: "max_completion_tokens", wantTokens: 6144},
		{name: "reasoning", effort: "medium", wantField: "max_completion_tokens", omitField: "max_tokens", wantTokens: 10240},
	} {
		t.Run(test.name, func(t *testing.T) {
			captured := make(chan map[string]any, 1)
			server := completionBodyServer(t, captured)
			defer server.Close()
			provider := NewOpenAICompatible(Options{BaseURL: server.URL, Model: "test-model", ReasoningEffort: test.effort}).(*OpenAICompatible)
			if _, err := provider.Complete(context.Background(), "system", "user", 6144); err != nil {
				t.Fatal(err)
			}
			body := <-captured
			if body[test.wantField] != test.wantTokens {
				t.Fatalf("%s = %#v, want %.0f", test.wantField, body[test.wantField], test.wantTokens)
			}
			if _, exists := body[test.omitField]; exists {
				t.Fatalf("request unexpectedly contains %s: %#v", test.omitField, body)
			}
			got, exists := body["reasoning_effort"]
			if exists != (test.effort != "") || (exists && got != test.effort) {
				t.Fatalf("reasoning_effort = %#v/%v, want %q", got, exists, test.effort)
			}
		})
	}
}

func TestReasoningTokenReserveMatchesGatewayDefaults(t *testing.T) {
	for effort, want := range map[string]int{
		"": 0, "none": 0, "minimal": 256, "low": 1024, "medium": 4096,
		"high": 8192, "xhigh": 16384, "max": 0,
	} {
		if got := reasoningTokenReserve(effort); got != want {
			t.Errorf("reasoningTokenReserve(%q) = %d, want %d", effort, got, want)
		}
	}
}

func TestCompleteStructuredUsesRequestReasoningOverride(t *testing.T) {
	captured := make(chan map[string]any, 1)
	server := completionBodyServer(t, captured)
	defer server.Close()

	provider := NewOpenAICompatible(Options{
		BaseURL: server.URL, Model: "test-model", ReasoningEffort: "medium",
	}).(*OpenAICompatible)
	_, err := provider.CompleteStructured(context.Background(), GenerationRequest{
		ReasoningEffort: "none",
		UserPrompt:      "hover",
		MaxOutputTokens: 1400,
		OutputSchema:    json.RawMessage(`{"type":"object"}`),
	})
	if err != nil {
		t.Fatal(err)
	}
	body := <-captured
	if body["reasoning_effort"] != "none" {
		t.Fatalf("reasoning_effort = %#v, want none", body["reasoning_effort"])
	}
	if body["max_completion_tokens"] != float64(1400) {
		t.Fatalf("max_completion_tokens = %#v, want 1400", body["max_completion_tokens"])
	}
	if _, exists := body["max_tokens"]; exists {
		t.Fatalf("request override unexpectedly used max_tokens: %#v", body)
	}
}

func TestCompleteStructuredPreservesReasoningControlsOnSchemaFallback(t *testing.T) {
	var bodies []map[string]any
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var body map[string]any
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Fatal(err)
		}
		bodies = append(bodies, body)
		if _, hasSchema := body["response_format"]; hasSchema {
			w.WriteHeader(http.StatusUnprocessableEntity)
			_, _ = w.Write([]byte(`{"error":{"message":"unsupported","type":"invalid_request_error","param":"response_format","code":"unsupported_value"}}`))
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"model":"test-model","choices":[{"message":{"content":"{\"ok\":true}"},"finish_reason":"stop"}]}`))
	}))
	defer server.Close()

	provider := NewOpenAICompatible(Options{BaseURL: server.URL, Model: "test-model", ReasoningEffort: "medium"}).(*OpenAICompatible)
	_, err := provider.CompleteStructured(context.Background(), GenerationRequest{
		UserPrompt: "user", MaxOutputTokens: 4096, OutputSchema: json.RawMessage(`{"type":"object"}`),
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(bodies) != 2 {
		t.Fatalf("request count = %d, want 2", len(bodies))
	}
	for index, body := range bodies {
		if body["reasoning_effort"] != "medium" || body["max_completion_tokens"] != float64(8192) {
			t.Fatalf("request %d controls = %#v", index, body)
		}
		if _, exists := body["max_tokens"]; exists {
			t.Fatalf("request %d unexpectedly includes max_tokens: %#v", index, body)
		}
	}
	if _, exists := bodies[0]["response_format"]; !exists {
		t.Fatal("schema request omitted response_format")
	}
	if _, exists := bodies[1]["response_format"]; exists {
		t.Fatal("compatibility retry retained response_format")
	}
}

// TestCompleteStructuredSuccess verifies the happy path with a native schema response.
func TestCompleteStructuredSuccess(t *testing.T) {
	t.Parallel()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{
			"model":"test-model",
			"choices":[{"message":{"content":"{\"ok\":true}"},"finish_reason":"stop"}]
		}`))
	}))
	defer server.Close()

	p := newTestProvider(t, server.URL)
	result, err := p.CompleteStructured(context.Background(), GenerationRequest{
		SystemPrompt: "sys",
		UserPrompt:   "user",
		OutputSchema: json.RawMessage(`{"type":"object"}`),
	})
	if err != nil {
		t.Fatalf("CompleteStructured = %v, want no error", err)
	}
	if string(result.RawJSON) != `{"ok":true}` {
		t.Errorf("RawJSON = %q", result.RawJSON)
	}
	if result.FinishReason != "stop" {
		t.Errorf("FinishReason = %q, want stop", result.FinishReason)
	}
}

// TestCompleteStructuredRejectsLengthTruncation verifies that finish_reason=length is an error.
func TestCompleteStructuredRejectsLengthTruncation(t *testing.T) {
	t.Parallel()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{
			"choices":[{"message":{"content":"{\"partial\":true}"},"finish_reason":"length"}]
		}`))
	}))
	defer server.Close()

	p := newTestProvider(t, server.URL)
	_, err := p.CompleteStructured(context.Background(), GenerationRequest{
		UserPrompt:   "user",
		OutputSchema: json.RawMessage(`{"type":"object"}`),
	})
	if err == nil || !strings.Contains(err.Error(), "truncated") {
		t.Fatalf("expected truncation error, got %v", err)
	}
}

// TestCompleteStructuredRetriesToPlainTextOnUnsupportedSchema checks that when
// the server returns HTTP 422, the provider retries without json_schema.
func TestCompleteStructuredRetriesToPlainTextOnUnsupportedSchema(t *testing.T) {
	t.Parallel()
	attempts := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		attempts++
		var body map[string]any
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			w.WriteHeader(http.StatusBadRequest)
			return
		}
		_, hasFormat := body["response_format"]
		if hasFormat {
			// First attempt with json_schema — reject it.
			w.WriteHeader(http.StatusUnprocessableEntity) // 422
			_, _ = w.Write([]byte(`{"error":{"message":"response format not supported","type":"invalid_request_error","param":"response_format","code":"unsupported_value"}}`))
			return
		}
		// Second attempt without schema — succeed.
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{
			"choices":[{"message":{"content":"{\"fallback\":true}"},"finish_reason":"stop"}]
		}`))
	}))
	defer server.Close()

	p := newTestProvider(t, server.URL)
	result, err := p.CompleteStructured(context.Background(), GenerationRequest{
		UserPrompt:   "user",
		OutputSchema: json.RawMessage(`{"type":"object"}`),
	})
	if err != nil {
		t.Fatalf("CompleteStructured = %v, want no error on retry", err)
	}
	if attempts != 2 {
		t.Errorf("server was called %d times, want 2", attempts)
	}
	if string(result.RawJSON) != `{"fallback":true}` {
		t.Errorf("RawJSON = %q", result.RawJSON)
	}
}

func TestCompleteStructuredRetriesWithoutSchemaWhenProviderCannotProduceSchemaValidOutput(t *testing.T) {
	t.Parallel()
	attempts := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		attempts++
		var body map[string]any
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			w.WriteHeader(http.StatusBadRequest)
			return
		}
		if _, hasFormat := body["response_format"]; hasFormat {
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusBadGateway)
			_, _ = w.Write([]byte(`{"error":{"message":"DiffusionGemma failed to produce a schema-valid structured response after 3 attempt(s)"}}`))
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"choices":[{"message":{"content":"{\"fallback\":true}"},"finish_reason":"stop"}]}`))
	}))
	defer server.Close()

	p := newTestProvider(t, server.URL)
	result, err := p.CompleteStructured(context.Background(), GenerationRequest{
		UserPrompt:   "user",
		OutputSchema: json.RawMessage(`{"type":"object"}`),
	})
	if err != nil {
		t.Fatalf("CompleteStructured = %v, want prompt-enforced JSON fallback", err)
	}
	if attempts != 2 {
		t.Fatalf("attempts = %d, want schema request plus one fallback", attempts)
	}
	if string(result.RawJSON) != `{"fallback":true}` {
		t.Fatalf("RawJSON = %q", result.RawJSON)
	}
}

func TestSchemaGenerationFailureClassificationIsNarrow(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name string
		err  error
		want bool
	}{
		{
			name: "gateway schema generation failure",
			err: &providerHTTPError{StatusCode: http.StatusBadGateway,
				Message: "DiffusionGemma failed to produce a schema-valid structured response after 3 attempt(s)"},
			want: true,
		},
		{name: "ordinary bad gateway", err: &providerHTTPError{StatusCode: http.StatusBadGateway, Message: "upstream unavailable"}, want: false},
		{name: "plain error impersonation", err: errors.New("schema-valid structured response"), want: false},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got := isSchemaGenerationFailure(tc.err); got != tc.want {
				t.Fatalf("isSchemaGenerationFailure(%#v) = %v, want %v", tc.err, got, tc.want)
			}
		})
	}
}

// TestCompleteStructuredNoSchemaSkipsResponseFormat verifies that when no schema
// is provided the request body does not include response_format.
func TestCompleteStructuredNoSchemaSkipsResponseFormat(t *testing.T) {
	t.Parallel()
	var captured map[string]any
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewDecoder(r.Body).Decode(&captured)
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{
			"choices":[{"message":{"content":"hello"},"finish_reason":"stop"}]
		}`))
	}))
	defer server.Close()

	p := newTestProvider(t, server.URL)
	_, err := p.CompleteStructured(context.Background(), GenerationRequest{
		UserPrompt:   "user",
		OutputSchema: nil, // empty
	})
	if err != nil {
		t.Fatalf("CompleteStructured = %v", err)
	}
	if _, hasFormat := captured["response_format"]; hasFormat {
		t.Error("request should not include response_format when OutputSchema is nil")
	}
}

// TestCompleteStructuredSetsJSONInstructionOnFallback verifies that the JSON-only
// instruction is appended to user prompt when degrading to plain text mode.
func TestCompleteStructuredSetsJSONInstructionOnFallback(t *testing.T) {
	t.Parallel()
	var capturedMessages []map[string]string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var body map[string]any
		_ = json.NewDecoder(r.Body).Decode(&body)
		_, hasFormat := body["response_format"]
		if hasFormat {
			w.WriteHeader(http.StatusBadRequest)
			_, _ = w.Write([]byte(`{"error":{"message":"json_schema unsupported","type":"invalid_request_error","param":"response_format.type","code":"unsupported_value"}}`))
			return
		}
		// Capture messages on the fallback attempt.
		if msgs, ok := body["messages"].([]any); ok {
			for _, m := range msgs {
				if mm, ok := m.(map[string]any); ok {
					capturedMessages = append(capturedMessages, map[string]string{
						"role":    mm["role"].(string),
						"content": mm["content"].(string),
					})
				}
			}
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"choices":[{"message":{"content":"{}"},"finish_reason":"stop"}]}`))
	}))
	defer server.Close()

	p := newTestProvider(t, server.URL)
	_, _ = p.CompleteStructured(context.Background(), GenerationRequest{
		UserPrompt:   "my query",
		OutputSchema: json.RawMessage(`{"type":"object"}`),
	})
	// Find the user message.
	var userContent string
	for _, m := range capturedMessages {
		if m["role"] == "user" {
			userContent = m["content"]
		}
	}
	if !strings.HasSuffix(userContent, jsonOnlyInstruction) {
		t.Errorf("fallback user prompt does not end with JSON-only instruction: %q", userContent)
	}
	if !strings.Contains(userContent, "<CODEATLAS_OUTPUT_SCHEMA>") ||
		!strings.Contains(userContent, `{"type":"object"}`) ||
		!strings.Contains(userContent, "</CODEATLAS_OUTPUT_SCHEMA>") {
		t.Errorf("fallback user prompt does not carry the exact output schema: %q", userContent)
	}
}

func TestCompleteStructuredDoesNotRetryUnrelatedBadRequest(t *testing.T) {
	t.Parallel()
	attempts := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		attempts++
		w.WriteHeader(http.StatusBadRequest)
		_, _ = w.Write([]byte(`{"error":{"message":"maximum context length exceeded","type":"invalid_request_error","param":"messages","code":"context_length_exceeded"}}`))
	}))
	defer server.Close()

	p := newTestProvider(t, server.URL)
	_, err := p.CompleteStructured(context.Background(), GenerationRequest{
		UserPrompt:   strings.Repeat("large prompt ", 100),
		OutputSchema: json.RawMessage(`{"type":"object"}`),
	})
	if err == nil {
		t.Fatal("CompleteStructured() succeeded on context length error")
	}
	if attempts != 1 {
		t.Fatalf("attempts = %d, want one deterministic request", attempts)
	}
	var httpError *providerHTTPError
	if !errors.As(err, &httpError) || httpError.Code != "context_length_exceeded" || httpError.Param != "messages" {
		t.Fatalf("error = %#v, want typed provider metadata", err)
	}
}

func TestIsUnsupportedRequestRequiresTypedSchemaMetadata(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name string
		err  error
		want bool
	}{
		{name: "response format param", err: &providerHTTPError{StatusCode: 400, Param: "response_format"}, want: true},
		{name: "nested response format param", err: &providerHTTPError{StatusCode: 422, Param: "response_format.json_schema"}, want: true},
		{name: "explicit code", err: &providerHTTPError{StatusCode: 400, Code: "json_schema_not_supported"}, want: true},
		{name: "context length", err: &providerHTTPError{StatusCode: 400, Param: "messages", Code: "context_length_exceeded"}, want: false},
		{name: "server error", err: &providerHTTPError{StatusCode: 500, Param: "response_format"}, want: false},
		{name: "plain text impersonation", err: errors.New("LLM HTTP 400: response_format unsupported"), want: false},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got := isUnsupportedRequest(tc.err); got != tc.want {
				t.Fatalf("isUnsupportedRequest(%#v) = %v, want %v", tc.err, got, tc.want)
			}
		})
	}
}

// TestCompleteReturnsErrorOnEmptyChoices validates the empty-choices guard.
func TestCompleteReturnsErrorOnEmptyChoices(t *testing.T) {
	t.Parallel()
	server := jsonServer(http.StatusOK, `{"choices":[]}`)
	defer server.Close()
	p := newTestProvider(t, server.URL)
	_, err := p.Complete(context.Background(), "sys", "user", 10)
	if err == nil {
		t.Fatal("Complete should error on empty choices")
	}
}

func TestCompleteRejectsProviderResponseOverLimit(t *testing.T) {
	t.Parallel()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, `{"choices":[{"message":{"content":"`)
		chunk := strings.Repeat("x", 32<<10)
		for written := int64(0); written <= maxProviderResponseBytes; written += int64(len(chunk)) {
			if _, err := io.WriteString(w, chunk); err != nil {
				return
			}
		}
		_, _ = io.WriteString(w, `"}}]}`)
	}))
	defer server.Close()

	p := newTestProvider(t, server.URL)
	_, err := p.Complete(context.Background(), "sys", "user", 10)
	if err == nil || !strings.Contains(err.Error(), "response exceeds") {
		t.Fatalf("Complete oversized response error = %v", err)
	}
}

func TestCompleteRejectsTrailingJSONValue(t *testing.T) {
	t.Parallel()
	server := jsonServer(http.StatusOK, `{"choices":[{"message":{"content":"ok"}}]} {"extra":true}`)
	defer server.Close()
	p := newTestProvider(t, server.URL)
	_, err := p.Complete(context.Background(), "sys", "user", 10)
	if err == nil || !strings.Contains(err.Error(), "multiple JSON values") {
		t.Fatalf("Complete trailing response error = %v", err)
	}
}

func TestCompleteStreamingDecodeRespectsCancellation(t *testing.T) {
	t.Parallel()
	started := make(chan struct{})
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, `{"choices":[`)
		if flusher, ok := w.(http.Flusher); ok {
			flusher.Flush()
		}
		close(started)
		<-r.Context().Done()
	}))
	defer server.Close()

	ctx, cancel := context.WithCancel(context.Background())
	go func() {
		<-started
		cancel()
	}()
	p := newTestProvider(t, server.URL)
	_, err := p.Complete(ctx, "sys", "user", 10)
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("Complete cancellation error = %v, want context.Canceled", err)
	}
}

// TestNameIncludesModel verifies the Name() method encodes the model identifier.
func TestNameIncludesModel(t *testing.T) {
	t.Parallel()
	p := NewOpenAICompatible(Options{BaseURL: "http://x", Model: "my-model"})
	if !strings.Contains(p.Name(), "my-model") {
		t.Errorf("Name() = %q should contain model name", p.Name())
	}
}

func TestEmbedUsesSeparateEmbeddingBaseURL(t *testing.T) {
	t.Parallel()
	var requestPath string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requestPath = r.URL.Path
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"data":[{"index":0,"embedding":[0.1,0.2,0.3]},{"index":1,"embedding":[0.4,0.5,0.6]}]}`))
	}))
	defer server.Close()

	p := NewOpenAICompatible(Options{
		BaseURL:          "http://chat.invalid/v1",
		EmbeddingBaseURL: server.URL + "/v1",
		Model:            "chat-model",
		EmbeddingModel:   "embed-model",
	})
	oc, ok := p.(*OpenAICompatible)
	if !ok {
		t.Fatalf("expected *OpenAICompatible, got %T", p)
	}
	vectors, err := oc.Embed(context.Background(), []string{"a", "b"})
	if err != nil {
		t.Fatalf("Embed = %v, want no error", err)
	}
	if requestPath != "/v1/embeddings" {
		t.Fatalf("request path = %q, want /v1/embeddings", requestPath)
	}
	if len(vectors) != 2 || len(vectors[0]) != 3 || len(vectors[1]) != 3 {
		t.Fatalf("vectors = %v, want 2 vectors of dimension 3", vectors)
	}
}

func TestEmbedUsesEmbeddingsAPIKey(t *testing.T) {
	t.Parallel()
	var authorization string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		authorization = r.Header.Get("Authorization")
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"data":[{"index":0,"embedding":[0.1,0.2]}]}`))
	}))
	defer server.Close()

	p := NewOpenAICompatible(Options{
		BaseURL:          "http://chat.invalid/v1",
		EmbeddingBaseURL: server.URL,
		APIKey:           "chat-key",
		EmbeddingsAPIKey: "embeddings-key",
		Model:            "chat-model",
		EmbeddingModel:   "embed-model",
	})
	if _, err := p.Embed(context.Background(), []string{"x"}); err != nil {
		t.Fatalf("Embed() error = %v", err)
	}
	if authorization != "Bearer embeddings-key" {
		t.Fatalf("Authorization = %q, want embeddings API key", authorization)
	}
}

func TestEmbedFallsBackToBaseURL(t *testing.T) {
	t.Parallel()
	var requestPath string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requestPath = r.URL.Path
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"data":[{"index":0,"embedding":[0.1,0.2]}]}`))
	}))
	defer server.Close()

	p := NewOpenAICompatible(Options{
		BaseURL:        server.URL + "/v1",
		Model:          "default",
		EmbeddingModel: "embed",
	})
	oc, ok := p.(*OpenAICompatible)
	if !ok {
		t.Fatalf("expected *OpenAICompatible, got %T", p)
	}
	if _, err := oc.Embed(context.Background(), []string{"x"}); err != nil {
		t.Fatalf("Embed = %v, want no error", err)
	}
	if requestPath != "/v1/embeddings" {
		t.Fatalf("request path = %q, want /v1/embeddings", requestPath)
	}
}

func TestEmbedRequiresModel(t *testing.T) {
	t.Parallel()
	p := NewOpenAICompatible(Options{
		BaseURL:        "http://localhost:1234",
		Model:          "default",
		EmbeddingModel: "",
	})
	oc, ok := p.(*OpenAICompatible)
	if !ok {
		t.Fatalf("expected *OpenAICompatible, got %T", p)
	}
	if _, err := oc.Embed(context.Background(), []string{"x"}); err == nil || !strings.Contains(err.Error(), "embedding model is not configured") {
		t.Fatalf("Embed = %v, want missing model error", err)
	}
}

func TestNewOpenAICompatibleStripsTrailingSlashFromEmbeddingBaseURL(t *testing.T) {
	t.Parallel()
	p := NewOpenAICompatible(Options{
		BaseURL:          "http://localhost:8080/v1/",
		EmbeddingBaseURL: "http://localhost:11434/v1/",
		Model:            "m",
	})
	oc, ok := p.(*OpenAICompatible)
	if !ok {
		t.Fatalf("expected *OpenAICompatible")
	}
	if strings.HasSuffix(oc.embeddingBaseURL, "/") {
		t.Errorf("embeddingBaseURL %q should not have trailing slash", oc.embeddingBaseURL)
	}
	if strings.HasSuffix(oc.baseURL, "/") {
		t.Errorf("baseURL %q should not have trailing slash", oc.baseURL)
	}
}
