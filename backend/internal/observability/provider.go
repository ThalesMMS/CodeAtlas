package observability

import (
	"context"
	"log/slog"

	"github.com/ThalesMMS/CodeAtlas/internal/ai"
	"github.com/ThalesMMS/CodeAtlas/internal/apperror"
)

// observingProvider decorates an ai.Provider with structured logging and metrics.
// It logs only sanitized metadata — provider/model name, operation, duration,
// status, error code and input count — and never the
// prompt, the response, source content or the Authorization header.
type observingProvider struct {
	inner   ai.Provider
	logger  *slog.Logger
	metrics *Metrics
	clock   Clock
}

// ObserveProvider wraps a provider for observability. A nil logger falls back to
// slog.Default; a nil metrics collector is a no-op.
func ObserveProvider(inner ai.Provider, logger *slog.Logger, metrics *Metrics) ai.Provider {
	if logger == nil {
		logger = slog.Default()
	}
	return &observingProvider{inner: inner, logger: logger, metrics: metrics, clock: RealClock}
}

// ObserveRuntimeCandidate decorates the concrete provider before it is swapped
// into an ai.Runtime. The original probe is retained because observability wraps
// calls rather than capability discovery, and the wrapper preserves native
// structured completion through CompleteStructured.
func ObserveRuntimeCandidate(candidate ai.RuntimeCandidate, logger *slog.Logger, metrics *Metrics) ai.RuntimeCandidate {
	provider := candidate.Provider
	if provider == nil {
		provider = ai.Disabled{}
	}
	probe := candidate.Probe
	if probe == nil {
		if providerProbe, ok := provider.(ai.CapabilityProbe); ok {
			probe = providerProbe
		} else {
			probe = ai.Disabled{}
		}
	}
	return ai.RuntimeCandidate{Provider: ObserveProvider(provider, logger, metrics), Probe: probe}
}

func (p *observingProvider) Name() string    { return p.inner.Name() }
func (p *observingProvider) Available() bool { return p.inner.Available() }

func (p *observingProvider) Complete(ctx context.Context, systemPrompt, userPrompt string, maxTokens int) (string, error) {
	start := p.clock.Now()
	result, err := p.inner.Complete(ctx, systemPrompt, userPrompt, maxTokens)
	p.logCall(ctx, "chat", p.clock.Now().Sub(start).Milliseconds(), 1, maxTokens, err)
	p.metrics.LLMCall(err == nil)
	return result, err
}

// CompleteStructured forwards a structured generation, preserving the inner
// provider's native json_schema support (via ai.Generate) while recording the
// same sanitized metrics. The schema version is logged; content never is.
func (p *observingProvider) CompleteStructured(ctx context.Context, req ai.GenerationRequest) (ai.GenerationResult, error) {
	start := p.clock.Now()
	result, err := ai.Generate(ctx, p.inner, req)
	operation := "chat:structured"
	if req.Operation != "" {
		operation = "chat:" + req.Operation
	}
	p.logCall(ctx, operation, p.clock.Now().Sub(start).Milliseconds(), 1, req.MaxOutputTokens, err)
	p.metrics.LLMCall(err == nil)
	return result, err
}

// logCall emits one structured line per AI call. Content is never included.
func (p *observingProvider) logCall(ctx context.Context, operation string, durationMs int64, inputCount, maxTokens int, err error) {
	attrs := []any{
		"provider", p.inner.Name(),
		"operation", operation,
		"durationMs", durationMs,
		"inputCount", inputCount,
	}
	if maxTokens > 0 {
		attrs = append(attrs, "maxTokens", maxTokens)
	}
	if err != nil {
		attrs = append(attrs, "status", "error", "errorCode", aiErrorCode(err), "cause", RedactString(err.Error()))
		p.logger.WarnContext(ctx, "ai call failed", attrs...)
		return
	}
	attrs = append(attrs, "status", "ok")
	p.logger.InfoContext(ctx, "ai call", attrs...)
}

func aiErrorCode(err error) string {
	if appErr, ok := apperror.As(err); ok {
		return string(appErr.Code)
	}
	return string(apperror.CodeProviderUnavailable)
}
