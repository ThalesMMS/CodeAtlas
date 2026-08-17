package app

import (
	"context"
	"encoding/json"
	"log/slog"
	"strings"
	"sync"
	"time"

	"github.com/ThalesMMS/CodeAtlas/internal/ai"
	"github.com/ThalesMMS/CodeAtlas/internal/aiout"
	"github.com/ThalesMMS/CodeAtlas/internal/domain"
	"github.com/ThalesMMS/CodeAtlas/internal/lspruntime"
	"github.com/ThalesMMS/CodeAtlas/internal/observability"
	"github.com/ThalesMMS/CodeAtlas/internal/retrieval"
	"github.com/ThalesMMS/CodeAtlas/internal/settings"
)

type EmbeddingPreparation interface {
	Activate()
	Abort(context.Context)
	NeedsRebuild() bool
	Fingerprint() retrieval.EmbeddingFingerprint
}

type LSPPreparation interface {
	Activate()
	Abort(context.Context)
}

type embeddingMetadataStore interface {
	EmbeddingMetadataContext(context.Context) (domain.EmbeddingIndexMetadata, error)
	EmbeddingCount() int
}

type lspCoordinator interface {
	Prepare(context.Context, settings.Values, settings.ChangeSet) (*lspruntime.Prepared, []settings.FieldError)
}

type SettingsRuntimeOptions struct {
	AIRuntime        *ai.Runtime
	EmbeddingRuntime *retrieval.EmbeddingRuntime
	EmbeddingStore   embeddingMetadataStore
	LSPCoordinator   lspCoordinator

	BuildAI                  func(settings.Values) ai.RuntimeCandidate
	PrepareEmbeddings        func(context.Context, retrieval.EmbeddingConfiguration, domain.EmbeddingIndexMetadata, int) (EmbeddingPreparation, error)
	PrepareLSP               func(context.Context, settings.Values, settings.ChangeSet) (LSPPreparation, []settings.FieldError)
	EmbeddingMetadata        func(context.Context) (domain.EmbeddingIndexMetadata, int, error)
	ScheduleEmbeddingRebuild func(context.Context) (domain.JobID, error)
	OnProviderActivated      func()

	Logger                *slog.Logger
	Metrics               *observability.Metrics
	ProbeTimeout          time.Duration
	StructuredProbeSchema json.RawMessage
}

type SettingsRuntime struct {
	aiRuntime                *ai.Runtime
	buildAI                  func(settings.Values) ai.RuntimeCandidate
	prepareEmbeddings        func(context.Context, retrieval.EmbeddingConfiguration, domain.EmbeddingIndexMetadata, int) (EmbeddingPreparation, error)
	prepareLSP               func(context.Context, settings.Values, settings.ChangeSet) (LSPPreparation, []settings.FieldError)
	embeddingMetadata        func(context.Context) (domain.EmbeddingIndexMetadata, int, error)
	scheduleEmbeddingRebuild func(context.Context) (domain.JobID, error)
	onProviderActivated      func()
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
				BaseURL: values.LLMBaseURL, EmbeddingBaseURL: values.EmbeddingBaseURL,
				APIKey: values.LLMAPIKey, EmbeddingsAPIKey: values.EmbeddingsAPIKey,
				Model: values.LLMModel, ReasoningEffort: values.LLMReasoningEffort,
				EmbeddingModel: values.EmbeddingModel, EnableEmbeddings: values.EnableEmbeddings,
				StructuredProbeSchema: options.StructuredProbeSchema,
				ProbeTimeout:          options.ProbeTimeout, RequestTimeout: values.LLMTimeout,
			})
			probe, _ := raw.(ai.CapabilityProbe)
			return observability.ObserveRuntimeCandidate(ai.RuntimeCandidate{Provider: raw, Probe: probe}, options.Logger, options.Metrics)
		}
	}
	if options.PrepareEmbeddings == nil && options.EmbeddingRuntime != nil {
		options.PrepareEmbeddings = func(ctx context.Context, configuration retrieval.EmbeddingConfiguration, metadata domain.EmbeddingIndexMetadata, count int) (EmbeddingPreparation, error) {
			return options.EmbeddingRuntime.Prepare(ctx, configuration, metadata, count)
		}
	}
	if options.PrepareLSP == nil && options.LSPCoordinator != nil {
		options.PrepareLSP = func(ctx context.Context, values settings.Values, changes settings.ChangeSet) (LSPPreparation, []settings.FieldError) {
			return options.LSPCoordinator.Prepare(ctx, values, changes)
		}
	}
	if options.EmbeddingMetadata == nil && options.EmbeddingStore != nil {
		options.EmbeddingMetadata = func(ctx context.Context) (domain.EmbeddingIndexMetadata, int, error) {
			metadata, err := options.EmbeddingStore.EmbeddingMetadataContext(ctx)
			return metadata, options.EmbeddingStore.EmbeddingCount(), err
		}
	}
	return &SettingsRuntime{
		aiRuntime: options.AIRuntime, buildAI: options.BuildAI,
		prepareEmbeddings: options.PrepareEmbeddings, prepareLSP: options.PrepareLSP,
		embeddingMetadata: options.EmbeddingMetadata, scheduleEmbeddingRebuild: options.ScheduleEmbeddingRebuild,
		onProviderActivated: options.OnProviderActivated,
	}
}

func (r *SettingsRuntime) SetEmbeddingScheduler(schedule func(context.Context) (domain.JobID, error)) {
	r.scheduleEmbeddingRebuild = schedule
}

func (r *SettingsRuntime) Prepare(ctx context.Context, resolved settings.Resolved, changes settings.ChangeSet) (settings.PreparedRuntime, error) {
	chatChanged := changedAny(changes,
		settings.FieldLLMBaseURL, settings.FieldLLMAPIKey, settings.FieldLLMModel,
		settings.FieldLLMReasoningEffort, settings.FieldLLMTimeout,
	)
	embeddingsChanged := changedAny(changes,
		settings.FieldEnableEmbeddings, settings.FieldEmbeddingModel,
		settings.FieldEmbeddingBaseURL, settings.FieldEmbeddingsAPIKey,
	)
	lspChanged := changedAny(changes,
		settings.FieldGoplsMode, settings.FieldGoplsPath,
		settings.FieldTypeScriptLSPMode, settings.FieldTypeScriptLSPPath, settings.FieldTypeScriptSDKPath,
		settings.FieldSwiftLSPMode, settings.FieldSwiftLSPPath,
		settings.FieldPythonLSPMode, settings.FieldPythonLSPPath,
		settings.FieldRustLSPMode, settings.FieldRustLSPPath,
	)

	needsAI := chatChanged || embeddingsChanged || !r.aiRuntime.Available()
	var aiCandidate ai.RuntimeCandidate
	if needsAI {
		aiCandidate = r.buildAI(resolved.Values)
		if aiCandidate.Provider == nil || aiCandidate.Probe == nil {
			return nil, runtimePreparationError(settings.FieldLLMBaseURL, "PROVIDER_UNAVAILABLE", "provider candidate is unavailable")
		}
		if chatChanged || !r.aiRuntime.Available() {
			if result := aiCandidate.Probe.ProbeChat(ctx); result.Status != ai.ProbeSuccess {
				return nil, probePreparationError(result, false)
			}
		}
		if resolved.Values.EnableEmbeddings && embeddingsChanged {
			if result := aiCandidate.Probe.ProbeEmbeddings(ctx); result.Status != ai.ProbeSuccess {
				return nil, probePreparationError(result, true)
			}
		}
	}

	prepared := &preparedSettingsRuntime{owner: r, aiCandidate: aiCandidate, swapAI: needsAI}
	if embeddingsChanged {
		if r.prepareEmbeddings == nil {
			return nil, runtimePreparationError(settings.FieldEnableEmbeddings, "EMBEDDING_RUNTIME_UNAVAILABLE", "embedding runtime is unavailable")
		}
		metadata := domain.EmbeddingIndexMetadata{}
		count := 0
		if resolved.Values.EnableEmbeddings {
			if r.embeddingMetadata == nil {
				return nil, runtimePreparationError(settings.FieldEnableEmbeddings, "EMBEDDING_STORE_UNAVAILABLE", "embedding metadata is unavailable")
			}
			var err error
			metadata, count, err = r.embeddingMetadata(ctx)
			if err != nil {
				return nil, runtimePreparationError(settings.FieldEnableEmbeddings, "EMBEDDING_STORE_UNAVAILABLE", "embedding metadata is unavailable")
			}
		}
		embeddingProvider := ai.Provider(r.aiRuntime)
		if needsAI {
			embeddingProvider = aiCandidate.Provider
		}
		baseURL := resolved.Values.EmbeddingBaseURL
		if strings.TrimSpace(baseURL) == "" {
			baseURL = resolved.Values.LLMBaseURL
		}
		candidate, err := r.prepareEmbeddings(ctx, retrieval.EmbeddingConfiguration{
			Provider: embeddingProvider, Enabled: resolved.Values.EnableEmbeddings,
			Model: resolved.Values.EmbeddingModel, BaseURL: baseURL,
			CredentialGeneration: embeddingCredentialGeneration(resolved),
		}, metadata, count)
		if err != nil {
			return nil, runtimePreparationError(settings.FieldEmbeddingModel, "EMBEDDING_CANDIDATE_FAILED", "embedding candidate could not be prepared")
		}
		prepared.embedding = candidate
		prepared.applyEmbeddings = true
	}

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
	owner           *SettingsRuntime
	aiCandidate     ai.RuntimeCandidate
	embedding       EmbeddingPreparation
	lsp             LSPPreparation
	swapAI          bool
	applyEmbeddings bool
	applyLSP        bool
	finish          sync.Once
	result          settings.ActivationResult
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
		if p.embedding != nil {
			p.embedding.Activate()
			p.result.Applied = append(p.result.Applied, settings.GroupEmbeddings)
		}
		if p.lsp != nil {
			p.lsp.Activate()
			p.result.Applied = append(p.result.Applied, settings.GroupLanguageServers)
		}
		if p.embedding != nil && p.embedding.NeedsRebuild() && p.owner.scheduleEmbeddingRebuild != nil {
			if jobID, err := p.owner.scheduleEmbeddingRebuild(context.Background()); err == nil {
				p.result.EmbeddingJobID = string(jobID)
			}
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
	if p.embedding != nil {
		p.embedding.Abort(ctx)
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

func probePreparationError(result ai.ProviderProbeResult, embeddings bool) error {
	field := settings.FieldLLMBaseURL
	if embeddings {
		field = settings.FieldEmbeddingBaseURL
	}
	switch result.ErrorCode {
	case ai.CodeProviderUnauthorized:
		if embeddings {
			field = settings.FieldEmbeddingsAPIKey
		} else {
			field = settings.FieldLLMAPIKey
		}
	case ai.CodeChatModelInvalid, ai.CodeStructuredOutputUnsupported:
		field = settings.FieldLLMModel
	case ai.CodeEmbeddingModelMissing, ai.CodeEmbeddingModelInvalid, ai.CodeEmbeddingResponseInvalid:
		field = settings.FieldEmbeddingModel
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

func embeddingCredentialGeneration(resolved settings.Resolved) string {
	if resolved.Credentials.EmbeddingsAPIKeyGeneration != "" {
		return resolved.Credentials.EmbeddingsAPIKeyGeneration
	}
	return resolved.Credentials.LLMAPIKeyGeneration
}

var _ settings.RuntimePreparer = (*SettingsRuntime)(nil)
