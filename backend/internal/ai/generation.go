package ai

import (
	"context"
	"encoding/json"
	"strings"
)

// GenerationRequest asks a provider for a structured (JSON) response constrained
// by an output schema, versioned independently of the ContextPack.
type GenerationRequest struct {
	Operation       string
	SystemPrompt    string
	UserPrompt      string
	MaxOutputTokens int
	// ReasoningEffort overrides the provider default for this request. Empty
	// inherits the provider configuration; "none" explicitly disables thinking.
	ReasoningEffort string
	OutputSchema    json.RawMessage
	SchemaVersion   string
}

// GenerationResult is the raw JSON the model returned plus provenance. The caller
// decodes it strictly and validates grounding; this layer does no parsing.
type GenerationResult struct {
	RawJSON      []byte
	Provider     string
	Model        string
	FinishReason string
}

// StructuredCompleter is the optional capability of a provider that can constrain
// the response to a JSON schema natively (response_format/json_schema).
type StructuredCompleter interface {
	CompleteStructured(ctx context.Context, req GenerationRequest) (GenerationResult, error)
}

const jsonOnlyInstruction = "\n\nRespond ONLY with a single valid JSON document that conforms to the requested schema. No Markdown, no code fences, and no text outside the JSON."

// Generate returns a structured result for req. A provider implementing
// StructuredCompleter uses native json_schema; otherwise Generate falls back to
// Complete with an explicit JSON-only instruction. Either way the caller validates
// the JSON locally, so an endpoint without native support is still safe.
func Generate(ctx context.Context, provider Provider, req GenerationRequest) (GenerationResult, error) {
	if provider == nil || !provider.Available() {
		return GenerationResult{}, ErrUnavailable
	}
	if structured, ok := provider.(StructuredCompleter); ok && len(req.OutputSchema) > 0 {
		return structured.CompleteStructured(ctx, req)
	}
	userPrompt := req.UserPrompt
	if len(req.OutputSchema) > 0 {
		userPrompt += jsonOnlyInstruction
	}
	raw, err := provider.Complete(ctx, req.SystemPrompt, userPrompt, req.MaxOutputTokens)
	if err != nil {
		return GenerationResult{}, err
	}
	return GenerationResult{RawJSON: []byte(strings.TrimSpace(raw)), Provider: provider.Name()}, nil
}
