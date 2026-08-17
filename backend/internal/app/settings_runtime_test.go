package app

import (
	"context"
	"errors"
	"reflect"
	"testing"

	"github.com/ThalesMMS/CodeAtlas/internal/ai"
	"github.com/ThalesMMS/CodeAtlas/internal/domain"
	"github.com/ThalesMMS/CodeAtlas/internal/retrieval"
	"github.com/ThalesMMS/CodeAtlas/internal/settings"
)

type settingsRuntimeProvider struct {
	id     string
	events *[]string
}

func (p *settingsRuntimeProvider) Name() string    { return p.id }
func (p *settingsRuntimeProvider) Available() bool { return true }
func (p *settingsRuntimeProvider) Complete(context.Context, string, string, int) (string, error) {
	return p.id, nil
}
func (p *settingsRuntimeProvider) Embed(context.Context, []string) ([][]float64, error) {
	return [][]float64{{0.1, 0.2}}, nil
}
func (p *settingsRuntimeProvider) ProbeChat(context.Context) ai.ProviderProbeResult {
	*p.events = append(*p.events, "probe:chat")
	return ai.ProviderProbeResult{Status: ai.ProbeSuccess}
}
func (p *settingsRuntimeProvider) ProbeEmbeddings(context.Context) ai.ProviderProbeResult {
	*p.events = append(*p.events, "probe:embeddings")
	return ai.ProviderProbeResult{Status: ai.ProbeSuccess}
}

type settingsRuntimeEmbeddingCandidate struct {
	events      *[]string
	fingerprint retrieval.EmbeddingFingerprint
}

func (c *settingsRuntimeEmbeddingCandidate) Activate() {
	*c.events = append(*c.events, "activate:embeddings")
}
func (c *settingsRuntimeEmbeddingCandidate) Abort(context.Context) {
	*c.events = append(*c.events, "abort:embeddings")
}
func (c *settingsRuntimeEmbeddingCandidate) NeedsRebuild() bool { return true }
func (c *settingsRuntimeEmbeddingCandidate) Fingerprint() retrieval.EmbeddingFingerprint {
	return c.fingerprint
}

type settingsRuntimeLSPCandidate struct{ events *[]string }

func (c *settingsRuntimeLSPCandidate) Activate() { *c.events = append(*c.events, "activate:lsp") }
func (c *settingsRuntimeLSPCandidate) Abort(context.Context) {
	*c.events = append(*c.events, "abort:lsp")
}

func TestSettingsRuntimePreparesEveryLiveGroupBeforeCommit(t *testing.T) {
	events := []string{}
	oldProvider := &settingsRuntimeProvider{id: "old", events: &events}
	newProvider := &settingsRuntimeProvider{id: "new", events: &events}
	aiRuntime := ai.NewRuntime()
	aiRuntime.Swap(ai.RuntimeCandidate{Provider: oldProvider, Probe: oldProvider})
	embeddingCandidate := &settingsRuntimeEmbeddingCandidate{
		events: &events,
		fingerprint: retrieval.EmbeddingFingerprint{
			Provider: "opaque", Model: "embed", ConfigurationHash: "hash", Dimension: 2,
			TemplateVersion: retrieval.EmbeddingTemplateVersion, Distance: retrieval.EmbeddingDistance,
		},
	}
	preparer := NewSettingsRuntime(SettingsRuntimeOptions{
		AIRuntime: aiRuntime,
		BuildAI: func(settings.Values) ai.RuntimeCandidate {
			events = append(events, "build:ai")
			return ai.RuntimeCandidate{Provider: newProvider, Probe: newProvider}
		},
		PrepareEmbeddings: func(context.Context, retrieval.EmbeddingConfiguration, domain.EmbeddingIndexMetadata, int) (EmbeddingPreparation, error) {
			events = append(events, "prepare:embeddings")
			return embeddingCandidate, nil
		},
		PrepareLSP: func(context.Context, settings.Values, settings.ChangeSet) (LSPPreparation, []settings.FieldError) {
			events = append(events, "prepare:lsp")
			return &settingsRuntimeLSPCandidate{events: &events}, nil
		},
		EmbeddingMetadata: func(context.Context) (domain.EmbeddingIndexMetadata, int, error) {
			return domain.EmbeddingIndexMetadata{}, 0, nil
		},
		ScheduleEmbeddingRebuild: func(context.Context) (domain.JobID, error) {
			events = append(events, "schedule:embeddings")
			return "job-1", nil
		},
		OnProviderActivated: func() { events = append(events, "signal:provider-retry") },
	})
	resolved := settings.Resolved{Values: settings.Values{
		Workspace: ".", LLMBaseURL: "https://example.test/v1", LLMModel: "new", LLMTimeout: 1,
		EnableEmbeddings: true, EmbeddingModel: "embed", EmbeddingBaseURL: "https://embed.test/v1",
		GoplsMode: "true", GoplsPath: "gopls",
	}, Credentials: settings.CredentialReferences{EmbeddingsAPIKeyGeneration: "generation"}}
	changes := settings.ChangeSet{Fields: []settings.FieldKey{
		settings.FieldWorkspace, settings.FieldLLMModel, settings.FieldEnableEmbeddings,
		settings.FieldEmbeddingModel, settings.FieldGoplsPath,
	}}

	prepared, err := preparer.Prepare(context.Background(), resolved, changes)
	if err != nil {
		t.Fatal(err)
	}
	wantPrepared := []string{"build:ai", "probe:chat", "probe:embeddings", "prepare:embeddings", "prepare:lsp"}
	if !reflect.DeepEqual(events, wantPrepared) {
		t.Fatalf("prepare events = %#v, want %#v", events, wantPrepared)
	}
	if aiRuntime.Name() != "old" {
		t.Fatal("AI runtime changed before persistence/activation")
	}
	activation := prepared.Activate()
	if aiRuntime.Name() != "new" || activation.EmbeddingJobID != "job-1" {
		t.Fatalf("activation = %#v runtime=%q", activation, aiRuntime.Name())
	}
	wantActivated := append(wantPrepared, "signal:provider-retry", "activate:embeddings", "activate:lsp", "schedule:embeddings")
	if !reflect.DeepEqual(events, wantActivated) {
		t.Fatalf("activation events = %#v, want %#v", events, wantActivated)
	}
	for _, group := range []settings.Group{settings.GroupLLM, settings.GroupEmbeddings, settings.GroupLanguageServers} {
		if !containsGroup(activation.Applied, group) {
			t.Fatalf("activation groups = %#v, missing %s", activation.Applied, group)
		}
	}
}

func TestSettingsRuntimeLSPFailureAbortsPreparedEmbeddingAndPreservesAI(t *testing.T) {
	events := []string{}
	oldProvider := &settingsRuntimeProvider{id: "old", events: &events}
	newProvider := &settingsRuntimeProvider{id: "new", events: &events}
	aiRuntime := ai.NewRuntime()
	aiRuntime.Swap(ai.RuntimeCandidate{Provider: oldProvider, Probe: oldProvider})
	preparer := NewSettingsRuntime(SettingsRuntimeOptions{
		AIRuntime: aiRuntime,
		BuildAI: func(settings.Values) ai.RuntimeCandidate {
			return ai.RuntimeCandidate{Provider: newProvider, Probe: newProvider}
		},
		PrepareEmbeddings: func(context.Context, retrieval.EmbeddingConfiguration, domain.EmbeddingIndexMetadata, int) (EmbeddingPreparation, error) {
			return &settingsRuntimeEmbeddingCandidate{events: &events}, nil
		},
		PrepareLSP: func(context.Context, settings.Values, settings.ChangeSet) (LSPPreparation, []settings.FieldError) {
			return nil, []settings.FieldError{{Field: settings.FieldGoplsPath, Code: "LSP_FAILED", Message: "candidate failed"}}
		},
		EmbeddingMetadata: func(context.Context) (domain.EmbeddingIndexMetadata, int, error) {
			return domain.EmbeddingIndexMetadata{}, 0, nil
		},
	})
	_, err := preparer.Prepare(context.Background(), settings.Resolved{Values: settings.Values{
		LLMBaseURL: "https://example.test/v1", LLMModel: "new", LLMTimeout: 1,
		EnableEmbeddings: true, EmbeddingModel: "embed", GoplsMode: "true", GoplsPath: "broken",
	}}, settings.ChangeSet{Fields: []settings.FieldKey{settings.FieldLLMModel, settings.FieldEmbeddingModel, settings.FieldGoplsPath}})
	if err == nil {
		t.Fatal("LSP failure did not reject the whole preparation")
	}
	if aiRuntime.Name() != "old" || !reflect.DeepEqual(events, []string{"probe:chat", "probe:embeddings", "abort:embeddings"}) {
		t.Fatalf("rollback runtime/events = %q/%#v", aiRuntime.Name(), events)
	}
	var fieldErrors interface{ FieldErrors() []settings.FieldError }
	if !errors.As(err, &fieldErrors) || len(fieldErrors.FieldErrors()) != 1 {
		t.Fatalf("field error = %v", err)
	}
}

func containsGroup(groups []settings.Group, group settings.Group) bool {
	for _, candidate := range groups {
		if candidate == group {
			return true
		}
	}
	return false
}
