package ai

import (
	"context"
	"encoding/json"
	"errors"
	"testing"
)

// mockProvider is a test double for Provider that records calls and returns
// configured responses. It does NOT implement StructuredCompleter.
type mockProvider struct {
	available        bool
	completeFn       func(ctx context.Context, sys, user string, maxTokens int) (string, error)
	lastSystemPrompt string
	lastUserPrompt   string
}

func (m *mockProvider) Name() string    { return "mock" }
func (m *mockProvider) Available() bool { return m.available }
func (m *mockProvider) Complete(ctx context.Context, sys, user string, maxTokens int) (string, error) {
	m.lastSystemPrompt = sys
	m.lastUserPrompt = user
	if m.completeFn != nil {
		return m.completeFn(ctx, sys, user, maxTokens)
	}
	return `{"ok":true}`, nil
}

// mockStructuredProvider implements both Provider and StructuredCompleter.
type mockStructuredProvider struct {
	mockProvider
	structuredFn func(ctx context.Context, req GenerationRequest) (GenerationResult, error)
}

func (m *mockStructuredProvider) CompleteStructured(ctx context.Context, req GenerationRequest) (GenerationResult, error) {
	if m.structuredFn != nil {
		return m.structuredFn(ctx, req)
	}
	return GenerationResult{RawJSON: []byte(`{"structured":true}`), Provider: "mock-structured"}, nil
}

func TestGenerateUnavailableProvider(t *testing.T) {
	t.Parallel()
	_, err := Generate(context.Background(), &mockProvider{available: false}, GenerationRequest{})
	if !errors.Is(err, ErrUnavailable) {
		t.Fatalf("Generate(disabled) = %v, want ErrUnavailable", err)
	}
}

func TestGenerateNilProvider(t *testing.T) {
	t.Parallel()
	_, err := Generate(context.Background(), nil, GenerationRequest{})
	if !errors.Is(err, ErrUnavailable) {
		t.Fatalf("Generate(nil) = %v, want ErrUnavailable", err)
	}
}

func TestGenerateFallsBackToCompleteWhenNoStructuredCompleter(t *testing.T) {
	t.Parallel()
	provider := &mockProvider{available: true}
	req := GenerationRequest{
		SystemPrompt: "sys",
		UserPrompt:   "user",
		OutputSchema: json.RawMessage(`{"type":"object"}`),
	}
	result, err := Generate(context.Background(), provider, req)
	if err != nil {
		t.Fatalf("Generate = %v, want no error", err)
	}
	// When falling back via Complete, the JSON-only instruction must be appended.
	if !containsJSONInstruction(provider.lastUserPrompt) {
		t.Errorf("user prompt should contain JSON-only instruction, got %q", provider.lastUserPrompt)
	}
	if string(result.RawJSON) == "" {
		t.Errorf("RawJSON should not be empty")
	}
	if result.Provider != "mock" {
		t.Errorf("Provider = %q, want mock", result.Provider)
	}
}

func TestGenerateNoSchemaNoJSONInstruction(t *testing.T) {
	t.Parallel()
	provider := &mockProvider{available: true}
	req := GenerationRequest{
		SystemPrompt: "sys",
		UserPrompt:   "user",
		// No OutputSchema.
	}
	_, err := Generate(context.Background(), provider, req)
	if err != nil {
		t.Fatalf("Generate = %v, want no error", err)
	}
	// Without a schema the JSON-only instruction must NOT be added.
	if containsJSONInstruction(provider.lastUserPrompt) {
		t.Errorf("user prompt should not contain JSON-only instruction when no schema given")
	}
}

func TestGenerateUsesStructuredCompleterWhenAvailable(t *testing.T) {
	t.Parallel()
	structuredCalled := false
	provider := &mockStructuredProvider{
		mockProvider: mockProvider{available: true},
		structuredFn: func(_ context.Context, req GenerationRequest) (GenerationResult, error) {
			structuredCalled = true
			return GenerationResult{RawJSON: []byte(`{"via":"structured"}`), Provider: "structured"}, nil
		},
	}
	req := GenerationRequest{
		UserPrompt:   "user",
		OutputSchema: json.RawMessage(`{"type":"object"}`),
	}
	result, err := Generate(context.Background(), provider, req)
	if err != nil {
		t.Fatalf("Generate = %v, want no error", err)
	}
	if !structuredCalled {
		t.Error("CompleteStructured should have been called but was not")
	}
	if string(result.RawJSON) != `{"via":"structured"}` {
		t.Errorf("RawJSON = %q, want structured result", result.RawJSON)
	}
}

func TestGenerateStructuredCompleterSkippedWhenNoSchema(t *testing.T) {
	t.Parallel()
	structuredCalled := false
	provider := &mockStructuredProvider{
		mockProvider: mockProvider{available: true},
		structuredFn: func(_ context.Context, _ GenerationRequest) (GenerationResult, error) {
			structuredCalled = true
			return GenerationResult{}, nil
		},
	}
	req := GenerationRequest{
		UserPrompt:   "user",
		OutputSchema: nil, // no schema → must NOT use StructuredCompleter
	}
	_, err := Generate(context.Background(), provider, req)
	if err != nil {
		t.Fatalf("Generate = %v, want no error", err)
	}
	if structuredCalled {
		t.Error("CompleteStructured should not be called when OutputSchema is empty")
	}
}

func TestGenerateCompleteErrorPropagates(t *testing.T) {
	t.Parallel()
	want := errors.New("provider error")
	provider := &mockProvider{
		available: true,
		completeFn: func(_ context.Context, _, _ string, _ int) (string, error) {
			return "", want
		},
	}
	_, err := Generate(context.Background(), provider, GenerationRequest{})
	if !errors.Is(err, want) {
		t.Fatalf("Generate error = %v, want %v", err, want)
	}
}

func TestGenerateTrimsWhitespaceFromRawJSON(t *testing.T) {
	t.Parallel()
	provider := &mockProvider{
		available: true,
		completeFn: func(_ context.Context, _, _ string, _ int) (string, error) {
			return "  {\"trimmed\":true}  \n", nil
		},
	}
	result, err := Generate(context.Background(), provider, GenerationRequest{})
	if err != nil {
		t.Fatalf("Generate = %v", err)
	}
	if string(result.RawJSON) != `{"trimmed":true}` {
		t.Errorf("RawJSON = %q, want trimmed JSON", result.RawJSON)
	}
}

// containsJSONInstruction checks that the JSON-only instruction was injected.
func containsJSONInstruction(prompt string) bool {
	return len(prompt) > 0 && len(jsonOnlyInstruction) > 0 &&
		len(prompt) >= len(jsonOnlyInstruction) &&
		prompt[len(prompt)-len(jsonOnlyInstruction):] == jsonOnlyInstruction
}
