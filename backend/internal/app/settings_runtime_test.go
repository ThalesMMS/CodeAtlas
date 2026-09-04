package app

import (
	"context"
	"errors"
	"reflect"
	"testing"

	"github.com/ThalesMMS/CodeAtlas/internal/ai"
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
func (p *settingsRuntimeProvider) ProbeChat(context.Context) ai.ProviderProbeResult {
	*p.events = append(*p.events, "probe:chat")
	return ai.ProviderProbeResult{Status: ai.ProbeSuccess}
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
	preparer := NewSettingsRuntime(SettingsRuntimeOptions{
		AIRuntime: aiRuntime,
		BuildAI: func(settings.Values) ai.RuntimeCandidate {
			events = append(events, "build:ai")
			return ai.RuntimeCandidate{Provider: newProvider, Probe: newProvider}
		},
		PrepareLSP: func(context.Context, settings.Values, settings.ChangeSet) (LSPPreparation, []settings.FieldError) {
			events = append(events, "prepare:lsp")
			return &settingsRuntimeLSPCandidate{events: &events}, nil
		},
		OnProviderActivated: func() { events = append(events, "signal:provider-retry") },
	})
	resolved := settings.Resolved{Values: settings.Values{
		Workspace: ".", LLMBaseURL: "https://example.test/v1", LLMModel: "new", LLMTimeout: 1,
		GoplsMode: "true", GoplsPath: "gopls",
	}}
	changes := settings.ChangeSet{Fields: []settings.FieldKey{
		settings.FieldWorkspace, settings.FieldLLMModel, settings.FieldGoplsPath,
	}}

	prepared, err := preparer.Prepare(context.Background(), resolved, changes)
	if err != nil {
		t.Fatal(err)
	}
	wantPrepared := []string{"build:ai", "probe:chat", "prepare:lsp"}
	if !reflect.DeepEqual(events, wantPrepared) {
		t.Fatalf("prepare events = %#v, want %#v", events, wantPrepared)
	}
	if aiRuntime.Name() != "old" {
		t.Fatal("AI runtime changed before persistence/activation")
	}
	activation := prepared.Activate()
	if aiRuntime.Name() != "new" {
		t.Fatalf("activation = %#v runtime=%q", activation, aiRuntime.Name())
	}
	wantActivated := append(wantPrepared, "signal:provider-retry", "activate:lsp")
	if !reflect.DeepEqual(events, wantActivated) {
		t.Fatalf("activation events = %#v, want %#v", events, wantActivated)
	}
	for _, group := range []settings.Group{settings.GroupLLM, settings.GroupLanguageServers} {
		if !containsGroup(activation.Applied, group) {
			t.Fatalf("activation groups = %#v, missing %s", activation.Applied, group)
		}
	}
}

// A failing language-server candidate must reject the whole preparation and
// leave the previously activated AI runtime untouched.
func TestSettingsRuntimeLSPFailurePreservesAI(t *testing.T) {
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
		PrepareLSP: func(context.Context, settings.Values, settings.ChangeSet) (LSPPreparation, []settings.FieldError) {
			return nil, []settings.FieldError{{Field: settings.FieldGoplsPath, Code: "LSP_FAILED", Message: "candidate failed"}}
		},
	})
	_, err := preparer.Prepare(context.Background(), settings.Resolved{Values: settings.Values{
		LLMBaseURL: "https://example.test/v1", LLMModel: "new", LLMTimeout: 1,
		GoplsMode: "true", GoplsPath: "broken",
	}}, settings.ChangeSet{Fields: []settings.FieldKey{settings.FieldLLMModel, settings.FieldGoplsPath}})
	if err == nil {
		t.Fatal("LSP failure did not reject the whole preparation")
	}
	if aiRuntime.Name() != "old" || !reflect.DeepEqual(events, []string{"probe:chat"}) {
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

type failingProbeProvider struct {
	settingsRuntimeProvider
}

func (p *failingProbeProvider) ProbeChat(context.Context) ai.ProviderProbeResult {
	*p.events = append(*p.events, "probe:chat")
	return ai.ProviderProbeResult{Status: ai.ProbeFailure}
}

func TestSettingsRuntimeAllowsUnrelatedChangesWhileProviderIsUnconfigured(t *testing.T) {
	events := []string{}
	provider := &failingProbeProvider{settingsRuntimeProvider{id: "candidate", events: &events}}
	aiRuntime := ai.NewRuntime()
	preparer := NewSettingsRuntime(SettingsRuntimeOptions{
		AIRuntime: aiRuntime,
		BuildAI: func(settings.Values) ai.RuntimeCandidate {
			events = append(events, "build:ai")
			return ai.RuntimeCandidate{Provider: provider, Probe: provider}
		},
	})
	values := settings.Values{LLMBaseURL: "https://unreachable.test/v1", LLMModel: "model", LLMTimeout: 1, Workspace: "/repos/demo"}

	// A workspace-only change must be saved even though the provider probe
	// still fails: the app keeps waiting in AWAITING_CONFIGURATION.
	prepared, err := preparer.Prepare(context.Background(), settings.Resolved{Values: values}, settings.ChangeSet{Fields: []settings.FieldKey{settings.FieldWorkspace}})
	if err != nil {
		t.Fatalf("workspace-only prepare with an unreachable provider = %v", err)
	}
	prepared.Activate()
	if aiRuntime.Available() {
		t.Fatal("an unreachable provider candidate was activated")
	}

	// A chat change with the same failing probe is still rejected.
	if _, err := preparer.Prepare(context.Background(), settings.Resolved{Values: values}, settings.ChangeSet{Fields: []settings.FieldKey{settings.FieldLLMModel}}); err == nil {
		t.Fatal("chat change with an unreachable provider was accepted")
	}
}
