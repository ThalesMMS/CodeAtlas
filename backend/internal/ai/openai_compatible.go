package ai

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"
)

type OpenAICompatible struct {
	baseURL          string
	embeddingBaseURL string
	apiKey           string
	embeddingsAPIKey string
	model            string
	reasoningEffort  string
	embeddingModel   string
	enableEmbeddings bool
	structuredProbe  json.RawMessage
	probeTimeout     time.Duration
	client           *http.Client
}

// Options configures an OpenAI-compatible provider. ProbeTimeout drives the
// diagnostic probes (see probe.go); RequestTimeout bounds business HTTP calls.
type Options struct {
	BaseURL          string
	EmbeddingBaseURL string
	APIKey           string
	EmbeddingsAPIKey string
	Model            string
	ReasoningEffort  string
	EmbeddingModel   string
	EnableEmbeddings bool
	// StructuredProbeSchema, when set, makes ProbeChat verify that the provider
	// can compile the same json_schema dialect used by business requests.
	StructuredProbeSchema json.RawMessage
	ProbeTimeout          time.Duration
	RequestTimeout        time.Duration
}

const (
	defaultProbeTimeout       = 10 * time.Second
	defaultRequestTimeout     = 10 * time.Minute
	maxProviderResponseBytes  = int64(16 << 20)
	maxProviderErrorBodyBytes = int64(16 << 20)
	providerMaxIdleConns      = 20
	providerMaxIdlePerHost    = 10
)

func NewOpenAICompatible(opts Options) Provider {
	if strings.TrimSpace(opts.BaseURL) == "" || strings.TrimSpace(opts.Model) == "" {
		return Disabled{}
	}
	probeTimeout := opts.ProbeTimeout
	if probeTimeout <= 0 {
		probeTimeout = defaultProbeTimeout
	}
	requestTimeout := opts.RequestTimeout
	if requestTimeout <= 0 {
		requestTimeout = defaultRequestTimeout
	}
	embeddingBaseURL := strings.TrimSpace(opts.EmbeddingBaseURL)
	if embeddingBaseURL == "" {
		embeddingBaseURL = opts.BaseURL
	}
	embeddingsAPIKey := opts.EmbeddingsAPIKey
	if embeddingsAPIKey == "" {
		embeddingsAPIKey = opts.APIKey
	}
	transport := providerTransport()
	transport.MaxIdleConns = providerMaxIdleConns
	transport.MaxIdleConnsPerHost = providerMaxIdlePerHost
	return &OpenAICompatible{
		baseURL:          strings.TrimRight(opts.BaseURL, "/"),
		embeddingBaseURL: strings.TrimRight(embeddingBaseURL, "/"),
		apiKey:           opts.APIKey,
		embeddingsAPIKey: embeddingsAPIKey,
		model:            opts.Model,
		reasoningEffort:  strings.ToLower(strings.TrimSpace(opts.ReasoningEffort)),
		embeddingModel:   opts.EmbeddingModel,
		enableEmbeddings: opts.EnableEmbeddings,
		structuredProbe:  append(json.RawMessage(nil), opts.StructuredProbeSchema...),
		probeTimeout:     probeTimeout,
		client: &http.Client{
			Timeout:   requestTimeout,
			Transport: transport,
		},
	}
}

func providerTransport() *http.Transport {
	if transport, ok := http.DefaultTransport.(*http.Transport); ok {
		return transport.Clone()
	}
	return (&http.Transport{Proxy: http.ProxyFromEnvironment}).Clone()
}

func (p *OpenAICompatible) Name() string    { return "openai-compatible:" + p.model }
func (p *OpenAICompatible) Available() bool { return true }

func (p *OpenAICompatible) Complete(ctx context.Context, systemPrompt, userPrompt string, maxTokens int) (string, error) {
	if maxTokens <= 0 {
		maxTokens = 800
	}
	requestBody := map[string]any{
		"model": p.model,
		"messages": []map[string]string{
			{"role": "system", "content": systemPrompt},
			{"role": "user", "content": userPrompt},
		},
		"temperature": 0.1,
	}
	p.applyCompletionControls(requestBody, maxTokens, "")
	var response struct {
		Choices []struct {
			Message struct {
				Content string `json:"content"`
			} `json:"message"`
		} `json:"choices"`
		Error any `json:"error"`
	}
	if err := p.postJSON(ctx, p.baseURL+"/chat/completions", p.apiKey, requestBody, &response); err != nil {
		return "", err
	}
	if len(response.Choices) == 0 {
		return "", fmt.Errorf("LLM returned no choices")
	}
	content := strings.TrimSpace(response.Choices[0].Message.Content)
	if content == "" {
		return "", fmt.Errorf("LLM returned an empty response")
	}
	return content, nil
}

// CompleteStructured requests a JSON-schema-constrained response. If the endpoint
// explicitly rejects response_format/json_schema, it retries once without it,
// appending an explicit JSON-only instruction so the caller's local validator
// still applies. A truncated response (finish_reason=length) is an error.
func (p *OpenAICompatible) CompleteStructured(ctx context.Context, req GenerationRequest) (GenerationResult, error) {
	maxTokens := req.MaxOutputTokens
	if maxTokens <= 0 {
		maxTokens = 800
	}
	build := func(withSchema bool) map[string]any {
		userPrompt := req.UserPrompt
		body := map[string]any{
			"model":       p.model,
			"temperature": 0.1,
		}
		p.applyCompletionControls(body, maxTokens, req.ReasoningEffort)
		if withSchema {
			body["response_format"] = map[string]any{
				"type": "json_schema",
				"json_schema": map[string]any{
					"name":   "codeatlas_output",
					"schema": json.RawMessage(req.OutputSchema),
					"strict": true,
				},
			}
		} else {
			userPrompt += jsonOnlyInstruction
		}
		body["messages"] = []map[string]string{
			{"role": "system", "content": req.SystemPrompt},
			{"role": "user", "content": userPrompt},
		}
		return body
	}

	useSchema := len(req.OutputSchema) > 0
	result, err := p.chatJSON(ctx, build(useSchema))
	if err != nil && useSchema && isUnsupportedRequest(err) {
		// The endpoint does not support json_schema; degrade to prompt-enforced JSON.
		result, err = p.chatJSON(ctx, build(false))
	}
	return result, err
}

func (p *OpenAICompatible) applyCompletionControls(body map[string]any, maxTokens int, requestEffort string) {
	effort := p.reasoningEffort
	if override := strings.ToLower(strings.TrimSpace(requestEffort)); override != "" {
		effort = override
	}
	if effort == "" {
		body["max_tokens"] = maxTokens
		return
	}
	body["reasoning_effort"] = effort
	body["max_completion_tokens"] = maxTokens + reasoningTokenReserve(effort)
}

// reasoningTokenReserve mirrors the gateway's default thinking budgets. The
// service-level MaxOutputTokens values describe the validated final response,
// while max_completion_tokens also counts hidden reasoning tokens. Adding the
// reserve prevents reasoning from consuming the entire response allowance.
// "max" has no separate bounded reserve, so the caller's explicit limit stays
// authoritative for that mode.
func reasoningTokenReserve(effort string) int {
	switch effort {
	case "minimal":
		return 256
	case "low":
		return 1024
	case "medium":
		return 4096
	case "high":
		return 8192
	case "xhigh":
		return 16384
	default:
		return 0
	}
}

// chatJSON posts a chat completion and returns the raw content as a structured
// result, rejecting empty or truncated responses.
func (p *OpenAICompatible) chatJSON(ctx context.Context, body map[string]any) (GenerationResult, error) {
	var response struct {
		Model   string `json:"model"`
		Choices []struct {
			Message struct {
				Content string `json:"content"`
			} `json:"message"`
			FinishReason string `json:"finish_reason"`
		} `json:"choices"`
	}
	if err := p.postJSON(ctx, p.baseURL+"/chat/completions", p.apiKey, body, &response); err != nil {
		return GenerationResult{}, err
	}
	if len(response.Choices) == 0 {
		return GenerationResult{}, fmt.Errorf("LLM returned no choices")
	}
	choice := response.Choices[0]
	if choice.FinishReason == "length" {
		return GenerationResult{}, fmt.Errorf("LLM response truncated (finish_reason=length)")
	}
	content := strings.TrimSpace(choice.Message.Content)
	if content == "" {
		return GenerationResult{}, fmt.Errorf("LLM returned an empty response")
	}
	return GenerationResult{RawJSON: []byte(content), Provider: p.Name(), Model: response.Model, FinishReason: choice.FinishReason}, nil
}

type providerHTTPError struct {
	StatusCode int
	Code       string
	Param      string
	Type       string
	Message    string
	Body       string
}

func (e *providerHTTPError) Error() string {
	detail := strings.TrimSpace(e.Message)
	if detail == "" {
		detail = strings.TrimSpace(e.Body)
	}
	if detail == "" {
		detail = http.StatusText(e.StatusCode)
	}
	return fmt.Sprintf("LLM HTTP %d: %s", e.StatusCode, detail)
}

// isUnsupportedRequest only accepts typed provider metadata that points at
// response_format/json_schema. Other 4xx failures (context length, auth, model,
// malformed prompts) are deterministic and must not be replayed.
func isUnsupportedRequest(err error) bool {
	var httpError *providerHTTPError
	if !errors.As(err, &httpError) {
		return false
	}
	switch httpError.StatusCode {
	case http.StatusBadRequest, http.StatusNotFound, http.StatusUnsupportedMediaType,
		http.StatusUnprocessableEntity, http.StatusNotImplemented:
	default:
		return false
	}
	param := strings.ToLower(strings.TrimSpace(httpError.Param))
	if param == "response_format" || strings.HasPrefix(param, "response_format.") || strings.HasPrefix(param, "response_format[") {
		return true
	}
	for _, value := range []string{httpError.Code, httpError.Type} {
		switch strings.ToLower(strings.TrimSpace(value)) {
		case "json_schema_not_supported", "unsupported_json_schema", "response_format_not_supported", "unsupported_response_format":
			return true
		}
	}
	return false
}

func (p *OpenAICompatible) Embed(ctx context.Context, texts []string) ([][]float64, error) {
	if p.embeddingModel == "" {
		return nil, fmt.Errorf("embedding model is not configured")
	}
	requestBody := map[string]any{
		"model": p.embeddingModel,
		"input": texts,
	}
	var response struct {
		Data []struct {
			Index     int       `json:"index"`
			Embedding []float64 `json:"embedding"`
		} `json:"data"`
	}
	if err := p.postJSON(ctx, p.embeddingBaseURL+"/embeddings", p.embeddingsAPIKey, requestBody, &response); err != nil {
		return nil, err
	}
	vectors := make([][]float64, len(texts))
	for _, item := range response.Data {
		if item.Index >= 0 && item.Index < len(vectors) {
			vectors[item.Index] = item.Embedding
		}
	}
	for i, vector := range vectors {
		if len(vector) == 0 {
			return nil, fmt.Errorf("embedding response missing index %d", i)
		}
	}
	return vectors, nil
}

func (p *OpenAICompatible) postJSON(ctx context.Context, endpoint, apiKey string, body any, target any) error {
	payload, err := json.Marshal(body)
	if err != nil {
		return err
	}
	request, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, bytes.NewReader(payload))
	if err != nil {
		return err
	}
	request.Header.Set("Content-Type", "application/json")
	if apiKey != "" {
		request.Header.Set("Authorization", "Bearer "+apiKey)
	}
	response, err := p.client.Do(request)
	if err != nil {
		return fmt.Errorf("LLM request: %w", err)
	}
	defer response.Body.Close()
	if response.StatusCode < 200 || response.StatusCode >= 300 {
		data, err := io.ReadAll(io.LimitReader(response.Body, maxProviderErrorBodyBytes))
		if err != nil {
			return err
		}
		return parseProviderHTTPError(response.StatusCode, data)
	}
	if err := decodeLimitedJSON(response.Body, maxProviderResponseBytes, target); err != nil {
		return fmt.Errorf("decode LLM response: %w", err)
	}
	return nil
}

func parseProviderHTTPError(statusCode int, data []byte) *providerHTTPError {
	result := &providerHTTPError{StatusCode: statusCode, Body: strings.TrimSpace(string(data))}
	var envelope map[string]json.RawMessage
	if json.Unmarshal(data, &envelope) != nil {
		return result
	}
	rawError := envelope["error"]
	if len(rawError) == 0 {
		return result
	}
	var message string
	if json.Unmarshal(rawError, &message) == nil {
		result.Message = message
		return result
	}
	var details struct {
		Message string          `json:"message"`
		Code    json.RawMessage `json:"code"`
		Param   json.RawMessage `json:"param"`
		Type    string          `json:"type"`
	}
	if json.Unmarshal(rawError, &details) != nil {
		return result
	}
	result.Message = details.Message
	result.Code = providerErrorScalar(details.Code)
	result.Param = providerErrorScalar(details.Param)
	result.Type = details.Type
	return result
}

func providerErrorScalar(raw json.RawMessage) string {
	if len(raw) == 0 || string(raw) == "null" {
		return ""
	}
	var value string
	if json.Unmarshal(raw, &value) == nil {
		return value
	}
	var number json.Number
	if json.Unmarshal(raw, &number) == nil {
		return number.String()
	}
	return ""
}

// decodeLimitedJSON decodes exactly one JSON value directly from the response
// stream. It reads at most limit+1 bytes so an oversized response is distinct
// from malformed/truncated JSON, and rejects trailing values or garbage just as
// json.Unmarshal did before the streaming path.
func decodeLimitedJSON(reader io.Reader, limit int64, target any) error {
	limited := &io.LimitedReader{R: reader, N: limit + 1}
	decoder := json.NewDecoder(limited)
	if err := decoder.Decode(target); err != nil {
		if limited.N <= 0 {
			return fmt.Errorf("response exceeds %d bytes", limit)
		}
		return err
	}
	var trailing json.RawMessage
	err := decoder.Decode(&trailing)
	if limited.N <= 0 {
		return fmt.Errorf("response exceeds %d bytes", limit)
	}
	if errors.Is(err, io.EOF) {
		return nil
	}
	if err != nil {
		return fmt.Errorf("trailing data: %w", err)
	}
	return errors.New("response contained multiple JSON values")
}
