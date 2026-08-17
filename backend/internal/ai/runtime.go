package ai

import (
	"context"
	"sync/atomic"
)

type RuntimeCandidate struct {
	Provider Provider
	Probe    CapabilityProbe
}

type runtimeSnapshot struct {
	provider Provider
	probe    CapabilityProbe
}

// Runtime is a stable Provider reference whose immutable concrete candidate can
// be replaced atomically. Each delegated operation loads exactly one snapshot,
// so an in-flight call cannot cross from an old provider to a new one.
type Runtime struct {
	current atomic.Pointer[runtimeSnapshot]
}

func NewRuntime() *Runtime {
	runtime := &Runtime{}
	runtime.Swap(RuntimeCandidate{})
	return runtime
}

func (r *Runtime) Swap(candidate RuntimeCandidate) {
	provider := candidate.Provider
	if provider == nil {
		provider = Disabled{}
	}
	probe := candidate.Probe
	if probe == nil {
		if providerProbe, ok := provider.(CapabilityProbe); ok {
			probe = providerProbe
		} else {
			probe = Disabled{}
		}
	}
	r.current.Store(&runtimeSnapshot{provider: provider, probe: probe})
}

func (r *Runtime) load() *runtimeSnapshot {
	if snapshot := r.current.Load(); snapshot != nil {
		return snapshot
	}
	disabled := Disabled{}
	return &runtimeSnapshot{provider: disabled, probe: disabled}
}

func (r *Runtime) Name() string {
	snapshot := r.load()
	return snapshot.provider.Name()
}

func (r *Runtime) Available() bool {
	snapshot := r.load()
	return snapshot.provider.Available()
}

func (r *Runtime) Complete(ctx context.Context, systemPrompt, userPrompt string, maxTokens int) (string, error) {
	snapshot := r.load()
	return snapshot.provider.Complete(ctx, systemPrompt, userPrompt, maxTokens)
}

func (r *Runtime) CompleteStructured(ctx context.Context, request GenerationRequest) (GenerationResult, error) {
	snapshot := r.load()
	if structured, ok := snapshot.provider.(StructuredCompleter); ok {
		return structured.CompleteStructured(ctx, request)
	}
	return Generate(ctx, snapshot.provider, request)
}

func (r *Runtime) Embed(ctx context.Context, texts []string) ([][]float64, error) {
	snapshot := r.load()
	return snapshot.provider.Embed(ctx, texts)
}

func (r *Runtime) ProbeChat(ctx context.Context) ProviderProbeResult {
	snapshot := r.load()
	return snapshot.probe.ProbeChat(ctx)
}

func (r *Runtime) ProbeEmbeddings(ctx context.Context) ProviderProbeResult {
	snapshot := r.load()
	return snapshot.probe.ProbeEmbeddings(ctx)
}

var (
	_ Provider            = (*Runtime)(nil)
	_ StructuredCompleter = (*Runtime)(nil)
	_ CapabilityProbe     = (*Runtime)(nil)
)
