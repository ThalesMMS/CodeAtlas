package observability

import (
	"bytes"
	"context"
	"errors"
	"log/slog"
	"strings"
	"testing"
)

// recordingProvider is an ai.Provider whose calls capture the prompts/inputs so a
// test can prove they never reach the log.
type recordingProvider struct {
	completeResult string
	completeErr    error
	embedResult    [][]float64
	embedErr       error
}

func (p *recordingProvider) Name() string    { return "openai-compatible:secret-model" }
func (p *recordingProvider) Available() bool { return true }
func (p *recordingProvider) Complete(context.Context, string, string, int) (string, error) {
	return p.completeResult, p.completeErr
}
func (p *recordingProvider) Embed(context.Context, []string) ([][]float64, error) {
	return p.embedResult, p.embedErr
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
	if !strings.Contains(logged, `"operation":"chat"`) || !strings.Contains(logged, `"maxTokens":800`) {
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

func TestObservedEmbedLogsDimension(t *testing.T) {
	t.Parallel()
	logger, buffer := captureLogger()
	metrics := NewMetrics()
	provider := ObserveProvider(&recordingProvider{embedResult: [][]float64{{0.1, 0.2, 0.3, 0.4}}}, logger, metrics)

	if _, err := provider.Embed(context.Background(), []string{"secret code snippet"}); err != nil {
		t.Fatalf("Embed error = %v", err)
	}
	logged := buffer.String()
	if !strings.Contains(logged, `"operation":"embedding"`) || !strings.Contains(logged, `"dimension":4`) || !strings.Contains(logged, `"inputCount":1`) {
		t.Fatalf("embedding log missing metadata: %s", logged)
	}
	if strings.Contains(logged, "secret code snippet") {
		t.Fatalf("embedding log leaked input: %s", logged)
	}
	if metrics.Snapshot().EmbedSuccessTotal != 1 {
		t.Fatal("embed success not counted")
	}
}
