package observability

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"strings"
	"testing"

	"github.com/ThalesMMS/CodeAtlas/internal/ai"
)

// recordingProvider is an ai.Provider whose calls capture the prompts/inputs so a
// test can prove they never reach the log.
type recordingProvider struct {
	completeResult string
	completeErr    error
}

func (p *recordingProvider) Name() string    { return "openai-compatible:secret-model" }
func (p *recordingProvider) Available() bool { return true }
func (p *recordingProvider) Complete(context.Context, string, string, int) (string, error) {
	return p.completeResult, p.completeErr
}

func captureLogger() (*slog.Logger, *bytes.Buffer) {
	buffer := &bytes.Buffer{}
	return slog.New(slog.NewJSONHandler(buffer, &slog.HandlerOptions{Level: slog.LevelDebug})), buffer
}

func TestObservedCompleteLogsMetadataNotContent(t *testing.T) {
	t.Parallel()
	logger, buffer := captureLogger()
	metrics := NewMetrics()
	const secretPrompt = "SYSTEM-PROMPT-WITH-SECRET-INSTRUCTIONS"
	const secretReply = "ASSISTANT-REPLY-WITH-PRIVATE-DATA"
	provider := ObserveProvider(&recordingProvider{completeResult: secretReply}, logger, metrics)

	result, err := provider.Complete(context.Background(), secretPrompt, "user "+secretPrompt, 800)
	if err != nil || result != secretReply {
		t.Fatalf("Complete passthrough broken: %v / %q", err, result)
	}
	logged := buffer.String()
	if !strings.Contains(logged, `"operation":"chat"`) || !strings.Contains(logged, `"maxTokens":800`) || !strings.Contains(logged, `"inputCount":1`) {
		t.Fatalf("ai log missing metadata: %s", logged)
	}
	if strings.Contains(logged, secretPrompt) || strings.Contains(logged, secretReply) {
		t.Fatalf("ai log leaked prompt/response: %s", logged)
	}
	if metrics.Snapshot().LLMSuccessTotal != 1 {
		t.Fatal("LLM success not counted")
	}
}

func TestObservedCompleteErrorLogsRedactedCause(t *testing.T) {
	t.Parallel()
	logger, buffer := captureLogger()
	metrics := NewMetrics()
	provider := ObserveProvider(&recordingProvider{completeErr: errors.New("LLM HTTP 401: api_key=sk-LEAKED-SECRET-123456")}, logger, metrics)

	if _, err := provider.Complete(context.Background(), "p", "u", 0); err == nil {
		t.Fatal("expected error passthrough")
	}
	logged := buffer.String()
	if !strings.Contains(logged, `"status":"error"`) || !strings.Contains(logged, `"errorCode"`) {
		t.Fatalf("error log missing status/errorCode: %s", logged)
	}
	if strings.Contains(logged, "sk-LEAKED-SECRET-123456") {
		t.Fatalf("error log leaked the api key: %s", logged)
	}
	if metrics.Snapshot().LLMFailureTotal != 1 {
		t.Fatal("LLM failure not counted")
	}
}

type nativeStructuredProvider struct {
	structuredCalls int
	probeCalls      int
}

func (p *nativeStructuredProvider) Name() string    { return "native" }
func (p *nativeStructuredProvider) Available() bool { return true }
func (p *nativeStructuredProvider) Complete(context.Context, string, string, int) (string, error) {
	return `{"fallback":true}`, nil
}
func (p *nativeStructuredProvider) CompleteStructured(context.Context, ai.GenerationRequest) (ai.GenerationResult, error) {
	p.structuredCalls++
	return ai.GenerationResult{RawJSON: []byte(`{"native":true}`), Provider: "native"}, nil
}
func (p *nativeStructuredProvider) ProbeChat(context.Context) ai.ProviderProbeResult {
	p.probeCalls++
	return ai.ProviderProbeResult{Status: ai.ProbeSuccess}
}

func TestObservedRuntimeCandidatePreservesStructuredCompletionAndProbe(t *testing.T) {
	inner := &nativeStructuredProvider{}
	logger, buffer := captureLogger()
	candidate := ObserveRuntimeCandidate(ai.RuntimeCandidate{Provider: inner}, logger, NewMetrics())
	runtime := ai.NewRuntime()
	runtime.Swap(candidate)

	result, err := runtime.CompleteStructured(context.Background(), ai.GenerationRequest{
		Operation:    "runtime-test",
		OutputSchema: json.RawMessage(`{"type":"object"}`),
	})
	if err != nil {
		t.Fatal(err)
	}
	if string(result.RawJSON) != `{"native":true}` || inner.structuredCalls != 1 {
		t.Fatalf("native structured result/calls = %s/%d", result.RawJSON, inner.structuredCalls)
	}
	if runtime.ProbeChat(context.Background()).Status != ai.ProbeSuccess || inner.probeCalls != 1 {
		t.Fatal("candidate probe was not preserved")
	}
	if !strings.Contains(buffer.String(), `"operation":"chat:runtime-test"`) {
		t.Fatalf("structured call was not observed: %s", buffer.String())
	}
}
