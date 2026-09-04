package ai

import (
	"context"
	"encoding/json"
	"fmt"
	"sync"
	"testing"
)

type blockingRuntimeProvider struct {
	id      string
	entered chan string
	release <-chan struct{}
}

func (p *blockingRuntimeProvider) wait(operation string) {
	if p.entered != nil {
		p.entered <- operation
	}
	if p.release != nil {
		<-p.release
	}
}

func (p *blockingRuntimeProvider) Name() string    { return p.id }
func (p *blockingRuntimeProvider) Available() bool { return p.id != "disabled" }
func (p *blockingRuntimeProvider) Complete(context.Context, string, string, int) (string, error) {
	p.wait("complete")
	return p.id + ":complete", nil
}
func (p *blockingRuntimeProvider) CompleteStructured(context.Context, GenerationRequest) (GenerationResult, error) {
	p.wait("structured")
	return GenerationResult{RawJSON: []byte(fmt.Sprintf(`{"provider":%q}`, p.id)), Provider: p.id}, nil
}
func (p *blockingRuntimeProvider) ProbeChat(context.Context) ProviderProbeResult {
	p.wait("probe-chat")
	return ProviderProbeResult{Status: ProbeSuccess, Metadata: map[string]string{"provider": p.id}}
}

func TestRuntimeProviderInFlightCallsKeepOneImmutableSnapshot(t *testing.T) {
	release := make(chan struct{})
	old := &blockingRuntimeProvider{id: "old", entered: make(chan string, 3), release: release}
	newProvider := &blockingRuntimeProvider{id: "new"}
	runtime := NewRuntime()
	runtime.Swap(RuntimeCandidate{Provider: old, Probe: old})

	results := make(chan string, 3)
	go func() {
		value, _ := runtime.Complete(context.Background(), "system", "user", 1)
		results <- value
	}()
	go func() {
		value, _ := runtime.CompleteStructured(context.Background(), GenerationRequest{OutputSchema: json.RawMessage(`{"type":"object"}`)})
		results <- value.Provider + ":structured"
	}()
	go func() { results <- runtime.ProbeChat(context.Background()).Metadata["provider"] + ":probe-chat" }()

	entered := map[string]bool{}
	for len(entered) < 3 {
		entered[<-old.entered] = true
	}
	runtime.Swap(RuntimeCandidate{Provider: newProvider, Probe: newProvider})
	close(release)

	got := map[string]bool{}
	for index := 0; index < 3; index++ {
		got[<-results] = true
	}
	for _, want := range []string{"old:complete", "old:structured", "old:probe-chat"} {
		if !got[want] {
			t.Fatalf("in-flight results = %#v, missing %q", got, want)
		}
	}
	if runtime.Name() != "new" || !runtime.Available() {
		t.Fatalf("runtime metadata = %q/%v", runtime.Name(), runtime.Available())
	}
	if value, _ := runtime.Complete(context.Background(), "", "", 0); value != "new:complete" {
		t.Fatalf("subsequent Complete = %q", value)
	}
	if value, _ := runtime.CompleteStructured(context.Background(), GenerationRequest{OutputSchema: json.RawMessage(`{}`)}); value.Provider != "new" {
		t.Fatalf("subsequent CompleteStructured = %#v", value)
	}
	if runtime.ProbeChat(context.Background()).Metadata["provider"] != "new" {
		t.Fatal("subsequent probe did not use the new snapshot")
	}
}

func TestRuntimeProviderConcurrentReadersAndSwaps(t *testing.T) {
	runtime := NewRuntime()
	providers := []*blockingRuntimeProvider{{id: "one"}, {id: "two"}}
	runtime.Swap(RuntimeCandidate{Provider: providers[0], Probe: providers[0]})

	var wait sync.WaitGroup
	for index := 0; index < 100; index++ {
		wait.Add(2)
		go func(index int) {
			defer wait.Done()
			candidate := providers[index%len(providers)]
			for iteration := 0; iteration < 100; iteration++ {
				runtime.Swap(RuntimeCandidate{Provider: candidate, Probe: candidate})
			}
		}(index)
		go func() {
			defer wait.Done()
			for iteration := 0; iteration < 100; iteration++ {
				name := runtime.Name()
				if name != "one" && name != "two" {
					t.Errorf("torn provider name %q", name)
					return
				}
				if !runtime.Available() {
					t.Error("torn provider availability")
					return
				}
			}
		}()
	}
	wait.Wait()
}

func TestRuntimeProviderDefaultsToDisabledAndNormalizesMissingRoles(t *testing.T) {
	runtime := NewRuntime()
	if runtime.Available() || runtime.Name() != "unconfigured-llm" {
		t.Fatalf("default runtime = %q/%v", runtime.Name(), runtime.Available())
	}
	if result := runtime.ProbeChat(context.Background()); result.Status != ProbeFailure {
		t.Fatalf("default probe = %#v", result)
	}
	runtime.Swap(RuntimeCandidate{})
	if runtime.Available() {
		t.Fatal("nil candidate did not normalize to Disabled")
	}
}
