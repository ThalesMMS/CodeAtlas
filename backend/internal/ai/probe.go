package ai

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/ThalesMMS/CodeAtlas/internal/textutil"
)

// ProbeStatus is the coarse outcome of a provider probe.
type ProbeStatus string

const (
	ProbeSuccess ProbeStatus = "success"
	ProbeFailure ProbeStatus = "failure"
)

// Stable, machine-readable error codes for provider probe failures.
const (
	CodeProviderURLInvalid          = "PROVIDER_URL_INVALID"
	CodeProviderTimeout             = "PROVIDER_TIMEOUT"
	CodeProviderUnreachable         = "PROVIDER_UNREACHABLE"
	CodeProviderUnauthorized        = "PROVIDER_UNAUTHORIZED"
	CodeProviderRateLimited         = "PROVIDER_RATE_LIMITED"
	CodeChatModelInvalid            = "CHAT_MODEL_INVALID"
	CodeChatResponseInvalid         = "CHAT_RESPONSE_INVALID"
	CodeStructuredOutputUnsupported = "STRUCTURED_OUTPUT_UNSUPPORTED"
)

// ProviderProbeResult is the structured outcome of one provider probe. Message
// is always sanitized: it never contains the API key, the Authorization header,
// a full remote error body, prompts or workspace content.
type ProviderProbeResult struct {
	Status    ProbeStatus       `json:"status"`
	ErrorCode string            `json:"errorCode,omitempty"`
	Message   string            `json:"message,omitempty"`
	Duration  time.Duration     `json:"duration"`
	Metadata  map[string]string `json:"metadata,omitempty"`
}

// CapabilityProbe is a small diagnostic interface, separate from Provider, used
// to verify that the remote endpoints actually work. Implementations must never
// leak sensitive data (API keys, auth headers, raw remote bodies, prompts or
// workspace content) through ProviderProbeResult.Message.
type CapabilityProbe interface {
	ProbeChat(ctx context.Context) ProviderProbeResult
}

// Compile-time guarantees that the providers implement the probe interface.
var (
	_ CapabilityProbe = (*OpenAICompatible)(nil)
	_ CapabilityProbe = Disabled{}
)

func successResult(start time.Time, metadata map[string]string) ProviderProbeResult {
	return ProviderProbeResult{Status: ProbeSuccess, Duration: time.Since(start), Metadata: metadata}
}

func failureResult(start time.Time, code, message string) ProviderProbeResult {
	return ProviderProbeResult{Status: ProbeFailure, ErrorCode: code, Message: sanitizeText(message), Duration: time.Since(start)}
}

// ProbeChat sends a minimal, deterministic request to <baseURL>/chat/completions
// using the configured model. It succeeds only on a 2xx response with at least
// one decodable choice. The returned text is never persisted or logged.
func (p *OpenAICompatible) ProbeChat(parent context.Context) ProviderProbeResult {
	start := time.Now()
	ctx, cancel := context.WithTimeout(parent, p.probeTimeout)
	defer cancel()

	body := map[string]any{
		"model":       p.model,
		"messages":    []map[string]string{{"role": "user", "content": "ping"}},
		"temperature": 0,
		"max_tokens":  1,
	}
	var response struct {
		Choices []struct {
			Message struct {
				Content string `json:"content"`
			} `json:"message"`
		} `json:"choices"`
	}
	outcome := p.sendProbe(ctx, p.baseURL+"/chat/completions", p.apiKey, body, &response)
	if outcome.transport != nil {
		code, message := classifyTransport(outcome.transport)
		return failureResult(start, code, message)
	}
	if outcome.statusCode < 200 || outcome.statusCode >= 300 {
		return classifyChatStatus(start, outcome.statusCode, outcome.snippet)
	}
	if outcome.decodeErr != nil || len(response.Choices) == 0 {
		return failureResult(start, CodeChatResponseInvalid, "chat response had no decodable choice")
	}
	if len(p.structuredProbe) == 0 {
		return successResult(start, nil)
	}

	structuredBody := map[string]any{
		"model": p.model,
		"messages": []map[string]string{
			{"role": "system", "content": "Return one JSON object matching the supplied schema."},
			{"role": "user", "content": "Return the smallest valid object."},
		},
		"temperature": 0,
		"max_tokens":  64,
		"response_format": map[string]any{
			"type": "json_schema",
			"json_schema": map[string]any{
				"name":   "codeatlas_readiness",
				"schema": json.RawMessage(p.structuredProbe),
				"strict": true,
			},
		},
	}
	response = struct {
		Choices []struct {
			Message struct {
				Content string `json:"content"`
			} `json:"message"`
		} `json:"choices"`
	}{}
	outcome = p.sendProbe(ctx, p.baseURL+"/chat/completions", p.apiKey, structuredBody, &response)
	if outcome.transport != nil {
		code, message := classifyTransport(outcome.transport)
		return failureResult(start, code, message)
	}
	if outcome.statusCode < 200 || outcome.statusCode >= 300 {
		return classifyStructuredStatus(start, outcome.statusCode)
	}
	if outcome.decodeErr != nil || len(response.Choices) == 0 || strings.TrimSpace(response.Choices[0].Message.Content) == "" {
		return failureResult(start, CodeChatResponseInvalid, "structured chat response had no decodable choice")
	}
	return successResult(start, map[string]string{"structuredOutput": "json_schema"})
}

func classifyStructuredStatus(start time.Time, status int) ProviderProbeResult {
	switch status {
	case http.StatusUnauthorized, http.StatusForbidden:
		return failureResult(start, CodeProviderUnauthorized, "provider rejected the credentials")
	case http.StatusTooManyRequests:
		return failureResult(start, CodeProviderRateLimited, "provider rate limited the structured-output probe")
	case http.StatusBadRequest, http.StatusNotFound, http.StatusUnsupportedMediaType,
		http.StatusUnprocessableEntity, http.StatusNotImplemented:
		return failureResult(start, CodeStructuredOutputUnsupported, "provider rejected the runtime structured-output schema")
	default:
		return failureResult(start, CodeChatResponseInvalid, fmt.Sprintf("structured-output probe returned HTTP %d", status))
	}
}

// ProbeChat for the Disabled provider always reports an unconfigured failure.
func (Disabled) ProbeChat(context.Context) ProviderProbeResult {
	return ProviderProbeResult{Status: ProbeFailure, ErrorCode: CodeProviderURLInvalid, Message: "LLM provider is not configured"}
}

// probeOutcome captures the result of a single HTTP probe round-trip.
type probeOutcome struct {
	statusCode int
	transport  error  // non-nil when the HTTP round-trip itself failed
	decodeErr  error  // non-nil when a 2xx body could not be decoded
	snippet    string // sanitized, truncated excerpt of a non-2xx body
}

// sendProbe performs the probe request. The request body is controlled and never
// contains workspace content. On a non-2xx response the body is read with a small
// limit and sanitized for diagnostics; the API key is never included.
func (p *OpenAICompatible) sendProbe(ctx context.Context, endpoint, apiKey string, body, target any) probeOutcome {
	payload, err := json.Marshal(body)
	if err != nil {
		return probeOutcome{transport: err}
	}
	request, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, bytes.NewReader(payload))
	if err != nil {
		return probeOutcome{transport: err}
	}
	request.Header.Set("Content-Type", "application/json")
	if apiKey != "" {
		request.Header.Set("Authorization", "Bearer "+apiKey)
	}
	response, err := p.client.Do(request)
	if err != nil {
		return probeOutcome{transport: err}
	}
	defer response.Body.Close()

	if response.StatusCode < 200 || response.StatusCode >= 300 {
		data, _ := io.ReadAll(io.LimitReader(response.Body, 1<<20))
		return probeOutcome{statusCode: response.StatusCode, snippet: sanitizeSnippet(data, apiKey)}
	}
	if err := decodeLimitedJSON(response.Body, 1<<20, target); err != nil {
		return probeOutcome{statusCode: response.StatusCode, decodeErr: err}
	}
	return probeOutcome{statusCode: response.StatusCode}
}

// classifyTransport maps a transport-level error to a stable code and message.
func classifyTransport(err error) (code, message string) {
	switch {
	case errors.Is(err, context.DeadlineExceeded):
		return CodeProviderTimeout, "provider timed out"
	case errors.Is(err, context.Canceled):
		return CodeProviderTimeout, "provider request cancelled"
	}
	var urlErr *url.Error
	if errors.As(err, &urlErr) && strings.Contains(urlErr.Err.Error(), "unsupported protocol scheme") {
		return CodeProviderURLInvalid, "provider URL scheme is invalid"
	}
	return CodeProviderUnreachable, "provider is unreachable"
}

// classifyHTTPStatus maps a non-2xx status to a probe result. modelInvalidCode is
// used for 404 and 400+model errors; responseInvalidCode is used for unrecognized
// statuses. The label identifies the endpoint in default-case messages (e.g. "chat").
func classifyHTTPStatus(start time.Time, status int, snippet, label, modelInvalidCode, responseInvalidCode string) ProviderProbeResult {
	switch {
	case status == http.StatusUnauthorized || status == http.StatusForbidden:
		return failureResult(start, CodeProviderUnauthorized, "provider rejected the credentials")
	case status == http.StatusTooManyRequests:
		return failureResult(start, CodeProviderRateLimited, "provider rate limited the probe")
	case status == http.StatusNotFound:
		return failureResult(start, modelInvalidCode, label+" model or endpoint not found")
	case status == http.StatusBadRequest && strings.Contains(strings.ToLower(snippet), "model"):
		return failureResult(start, modelInvalidCode, label+" model rejected: "+snippet)
	case status >= 500:
		return failureResult(start, CodeProviderUnreachable, fmt.Sprintf("provider returned HTTP %d", status))
	default:
		return failureResult(start, responseInvalidCode, fmt.Sprintf("unexpected %s HTTP %d: %s", label, status, snippet))
	}
}

func classifyChatStatus(start time.Time, status int, snippet string) ProviderProbeResult {
	return classifyHTTPStatus(start, status, snippet, "chat", CodeChatModelInvalid, CodeChatResponseInvalid)
}

// sanitizeSnippet strips the API key, collapses whitespace and bounds the length
// of a remote body excerpt so it is safe to surface in diagnostics.
func sanitizeSnippet(data []byte, apiKey string) string {
	text := string(data)
	if apiKey != "" {
		text = strings.ReplaceAll(text, apiKey, "***")
	}
	return textutil.CompactMessage(text, 160)
}

// sanitizeText collapses whitespace and bounds the length of a probe message.
func sanitizeText(message string) string {
	return textutil.CompactMessage(message, 300)
}
