package app

import (
	"context"
	"encoding/json"
	"log/slog"
	"sync"
	"time"

	"github.com/ThalesMMS/CodeAtlas/internal/ai"
	"github.com/ThalesMMS/CodeAtlas/internal/aiout"
	"github.com/ThalesMMS/CodeAtlas/internal/lspruntime"
	"github.com/ThalesMMS/CodeAtlas/internal/observability"
	"github.com/ThalesMMS/CodeAtlas/internal/settings"
)

type LSPPreparation interface {
	Activate()
	Abort(context.Context)
}

type lspCoordinator interface {
	Prepare(context.Context, settings.Values, settings.ChangeSet) (*lspruntime.Prepared, []settings.FieldError)
}

type SettingsRuntimeOptions struct {
	AIRuntime      *ai.Runtime
	LSPCoordinator lspCoordinator

	BuildAI             func(settings.Values) ai.RuntimeCandidate
	PrepareLSP          func(context.Context, settings.Values, settings.ChangeSet) (LSPPreparation, []settings.FieldError)
	OnProviderActivated func()

	Logger                *slog.Logger
	Metrics               *observability.Metrics
	ProbeTimeout          time.Duration
	StructuredProbeSchema json.RawMessage
}

type SettingsRuntime struct {
	aiRuntime           *ai.Runtime
	buildAI             func(settings.Values) ai.RuntimeCandidate
	prepareLSP          func(context.Context, settings.Values, settings.ChangeSet) (LSPPreparation, []settings.FieldError)
	onProviderActivated func()
}

func NewSettingsRuntime(options SettingsRuntimeOptions) *SettingsRuntime {
	if options.AIRuntime == nil {
		options.AIRuntime = ai.NewRuntime()
	}
	if options.StructuredProbeSchema == nil {
		options.StructuredProbeSchema = aiout.ExplanationSchema()
	}
	if options.BuildAI == nil {
		options.BuildAI = func(values settings.Values) ai.RuntimeCandidate {
			raw := ai.NewOpenAICompatible(ai.Options{
				BaseURL: values.LLMBaseURL, APIKey: values.LLMAPIKey,
				Model: values.LLMModel, ReasoningEffort: values.LLMReasoningEffort,
				StructuredProbeSchema: options.StructuredProbeSchema,
				ProbeTimeout:          options.ProbeTimeout, RequestTimeout: values.LLMTimeout,
			})
			probe, _ := raw.(ai.CapabilityProbe)
			return observability.ObserveRuntimeCandidate(ai.RuntimeCandidate{Provider: raw, Probe: probe}, options.Logger, options.Metrics)
		}
	}
	if options.PrepareLSP == nil && options.LSPCoordinator != nil {
		options.PrepareLSP = func(ctx context.Context, values settings.Values, changes settings.ChangeSet) (LSPPreparation, []settings.FieldError) {
			return options.LSPCoordinator.Prepare(ctx, values, changes)
		}
	}
	return &SettingsRuntime{
		aiRuntime: options.AIRuntime, buildAI: options.BuildAI,
		prepareLSP: options.PrepareLSP, onProviderActivated: options.OnProviderActivated,
	}
}

func (r *SettingsRuntime) Prepare(ctx context.Context, resolved settings.Resolved, changes settings.ChangeSet) (settings.PreparedRuntime, error) {
	chatChanged := changedAny(changes,
		settings.FieldLLMBaseURL, settings.FieldLLMAPIKey, settings.FieldLLMModel,
		settings.FieldLLMReasoningEffort, settings.FieldLLMTimeout,
	)
	lspChanged := changedAny(changes,
		settings.FieldGoplsMode, settings.FieldGoplsPath,
		settings.FieldTypeScriptLSPMode, settings.FieldTypeScriptLSPPath, settings.FieldTypeScriptSDKPath,
		settings.FieldSwiftLSPMode, settings.FieldSwiftLSPPath,
		settings.FieldPythonLSPMode, settings.FieldPythonLSPPath,
		settings.FieldRustLSPMode, settings.FieldRustLSPPath,
	)

	needsAI := chatChanged || !r.aiRuntime.Available()
	var aiCandidate ai.RuntimeCandidate
	if needsAI {
		aiCandidate = r.buildAI(resolved.Values)
		if aiCandidate.Provider == nil || aiCandidate.Probe == nil {
			return nil, runtimePreparationError(settings.FieldLLMBaseURL, "PROVIDER_UNAVAILABLE", "provider candidate is unavailable")
		}
		if chatChanged || !r.aiRuntime.Available() {
			if result := aiCandidate.Probe.ProbeChat(ctx); result.Status != ai.ProbeSuccess {
				if chatChanged {
					return nil, probePreparationError(result)
				}
				// The provider is still unconfigured and this change does not
				// touch it (for example choosing a workspace before the LLM
				// endpoint). Save the change and keep waiting for a valid
				// provider instead of rejecting unrelated settings.
				needsAI = false
				aiCandidate = ai.RuntimeCandidate{}
			}
		}
	}

	prepared := &preparedSettingsRuntime{owner: r, aiCandidate: aiCandidate, swapAI: needsAI}
	if lspChanged {
		if r.prepareLSP == nil {
			prepared.abortPrepared(ctx)
			return nil, runtimePreparationError(settings.FieldGoplsPath, "LSP_RUNTIME_UNAVAILABLE", "language-server runtime is unavailable")
		}
		candidate, issues := r.prepareLSP(ctx, resolved.Values, changes)
		if len(issues) != 0 {
			prepared.abortPrepared(ctx)
			return nil, &SettingsRuntimeError{Issues: append([]settings.FieldError(nil), issues...)}
		}
		prepared.lsp = candidate
		prepared.applyLSP = true
	}
	return prepared, nil
}

type preparedSettingsRuntime struct {
	owner       *SettingsRuntime
	aiCandidate ai.RuntimeCandidate
	lsp         LSPPreparation
	swapAI      bool
	applyLSP    bool
	finish      sync.Once
	result      settings.ActivationResult
}

func (p *preparedSettingsRuntime) Activate() settings.ActivationResult {
	p.finish.Do(func() {
		if p.swapAI {
			p.owner.aiRuntime.Swap(p.aiCandidate)
			p.result.Applied = append(p.result.Applied, settings.GroupLLM)
			if p.owner.onProviderActivated != nil {
				p.owner.onProviderActivated()
			}
		}
		if p.lsp != nil {
			p.lsp.Activate()
			p.result.Applied = append(p.result.Applied, settings.GroupLanguageServers)
		}
	})
	p.result.Applied = append([]settings.Group(nil), p.result.Applied...)
	return p.result
}

func (p *preparedSettingsRuntime) Abort(ctx context.Context) {
	p.finish.Do(func() { p.abortPrepared(ctx) })
}

func (p *preparedSettingsRuntime) abortPrepared(ctx context.Context) {
	if p.lsp != nil {
		p.lsp.Abort(ctx)
	}
}

type SettingsRuntimeError struct{ Issues []settings.FieldError }

func (e *SettingsRuntimeError) Error() string {
	return "runtime settings candidate could not be prepared"
}
func (e *SettingsRuntimeError) FieldErrors() []settings.FieldError {
	return append([]settings.FieldError(nil), e.Issues...)
}

func runtimePreparationError(field settings.FieldKey, code, message string) error {
	return &SettingsRuntimeError{Issues: []settings.FieldError{{Field: field, Code: code, Message: message}}}
}

func probePreparationError(result ai.ProviderProbeResult) error {
	field := settings.FieldLLMBaseURL
	switch result.ErrorCode {
	case ai.CodeProviderUnauthorized:
		field = settings.FieldLLMAPIKey
	case ai.CodeChatModelInvalid, ai.CodeStructuredOutputUnsupported:
		field = settings.FieldLLMModel
	}
	code := result.ErrorCode
	if code == "" {
		code = "PROVIDER_PROBE_FAILED"
	}
	return runtimePreparationError(field, code, "provider candidate probe failed")
}

func changedAny(changes settings.ChangeSet, fields ...settings.FieldKey) bool {
	for _, field := range fields {
		if changes.Changed(field) {
			return true
		}
	}
	return false
}

var _ settings.RuntimePreparer = (*SettingsRuntime)(nil)
