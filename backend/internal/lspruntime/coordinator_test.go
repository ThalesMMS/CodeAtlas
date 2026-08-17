package lspruntime

import (
	"context"
	"errors"
	"fmt"
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/ThalesMMS/CodeAtlas/internal/capabilities"
	"github.com/ThalesMMS/CodeAtlas/internal/domain"
	"github.com/ThalesMMS/CodeAtlas/internal/semantic"
	"github.com/ThalesMMS/CodeAtlas/internal/settings"
)

type coordinatorProvider struct{ id semantic.SemanticProviderID }

func (p *coordinatorProvider) ID() semantic.SemanticProviderID { return p.id }
func (p *coordinatorProvider) Capabilities(context.Context) (semantic.SemanticCapabilities, error) {
	return semantic.SemanticCapabilities{Hover: true}, nil
}
func (p *coordinatorProvider) fact() ([]semantic.SemanticFact, error) {
	return []semantic.SemanticFact{{Detail: string(p.id)}}, nil
}
func (p *coordinatorProvider) Hover(context.Context, semantic.SemanticQuery) ([]semantic.SemanticFact, error) {
	return p.fact()
}
func (p *coordinatorProvider) Definitions(context.Context, semantic.SemanticQuery) ([]semantic.SemanticFact, error) {
	return p.fact()
}
func (p *coordinatorProvider) References(context.Context, semantic.SemanticQuery) ([]semantic.SemanticFact, error) {
	return p.fact()
}
func (p *coordinatorProvider) Implementations(context.Context, semantic.SemanticQuery) ([]semantic.SemanticFact, error) {
	return p.fact()
}
func (p *coordinatorProvider) IncomingCalls(context.Context, semantic.SemanticQuery) ([]semantic.SemanticFact, error) {
	return p.fact()
}
func (p *coordinatorProvider) OutgoingCalls(context.Context, semantic.SemanticQuery) ([]semantic.SemanticFact, error) {
	return p.fact()
}
func (p *coordinatorProvider) Diagnostics(context.Context, semantic.SemanticQuery) ([]semantic.SemanticFact, error) {
	return p.fact()
}

type capabilityRecorder struct {
	mu      sync.Mutex
	results []capabilities.Result
}

func (r *capabilityRecorder) UpdateCapability(result capabilities.Result) {
	r.mu.Lock()
	r.results = append(r.results, result)
	r.mu.Unlock()
}

func TestCoordinatorStagesAllChangedFamiliesBeforeActivation(t *testing.T) {
	events := []string{}
	initial := allInitialSlots(&events)
	runtime := semantic.NewRuntime(routerForTestSlots(initial))
	registry := &capabilityRecorder{}
	factories := Factories{
		Go:         testFactory(FamilyGo, &events, nil),
		TypeScript: testFactory(FamilyTypeScript, &events, nil),
		Swift:      testFactory(FamilySwift, &events, nil),
		Python:     testFactory(FamilyPython, &events, nil),
		Rust:       testFactory(FamilyRust, &events, nil),
	}
	coordinator := NewCoordinator(runtime, factories, registry, initial, 50*time.Millisecond)
	changes := settings.ChangeSet{Fields: []settings.FieldKey{settings.FieldGoplsPath, settings.FieldPythonLSPMode}}

	prepared, issues := coordinator.Prepare(context.Background(), settings.Values{GoplsMode: "true", PythonLSPMode: "true"}, changes)
	if len(issues) != 0 || prepared == nil {
		t.Fatalf("Prepare issues = %#v", issues)
	}
	if !reflect.DeepEqual(events, []string{"build:go", "build:python"}) {
		t.Fatalf("staging events = %#v", events)
	}
	if facts, _ := runtime.Hover(context.Background(), semantic.SemanticQuery{Path: "main.go", Position: domain.Position{}}); facts[0].Detail != "old-go" {
		t.Fatalf("runtime swapped during prepare: %#v", facts)
	}

	prepared.Activate()
	prepared.Activate()
	if facts, _ := runtime.Hover(context.Background(), semantic.SemanticQuery{Path: "main.go"}); facts[0].Detail != "new-go" {
		t.Fatalf("Go provider did not swap: %#v", facts)
	}
	if facts, _ := runtime.Hover(context.Background(), semantic.SemanticQuery{Path: "main.py"}); facts[0].Detail != "new-python" {
		t.Fatalf("Python provider did not swap: %#v", facts)
	}
	if facts, _ := runtime.Hover(context.Background(), semantic.SemanticQuery{Path: "main.rs"}); facts[0].Detail != "old-rust" {
		t.Fatalf("unchanged provider was rebuilt: %#v", facts)
	}
	if !reflect.DeepEqual(events, []string{"build:go", "build:python", "shutdown:old-go:bounded", "shutdown:old-python:bounded"}) {
		t.Fatalf("activation events = %#v", events)
	}
	if len(registry.results) != 2 {
		t.Fatalf("capability updates = %#v", registry.results)
	}
	for _, result := range registry.results {
		if _, leaked := result.Metadata["executablePath"]; leaked || result.Message != "" {
			t.Fatalf("capability leaked process detail: %#v", result)
		}
	}
}

func TestCoordinatorFailureAbortsEveryCandidateAndPreservesRouter(t *testing.T) {
	events := []string{}
	initial := allInitialSlots(&events)
	runtime := semantic.NewRuntime(routerForTestSlots(initial))
	factories := Factories{
		Go:         testFactory(FamilyGo, &events, nil),
		TypeScript: testFactory(FamilyTypeScript, &events, errors.New("process output with C:/secret/server.exe")),
	}
	coordinator := NewCoordinator(runtime, factories, &capabilityRecorder{}, initial, 50*time.Millisecond)
	changes := settings.ChangeSet{Fields: []settings.FieldKey{settings.FieldGoplsPath, settings.FieldTypeScriptLSPPath}}

	prepared, issues := coordinator.Prepare(context.Background(), settings.Values{GoplsMode: "true", TypeScriptLSPMode: "true"}, changes)
	if prepared != nil || len(issues) != 1 || issues[0].Field != settings.FieldTypeScriptLSPPath {
		t.Fatalf("Prepare = %#v/%#v", prepared, issues)
	}
	if !reflect.DeepEqual(events, []string{"build:go", "build:typescript", "shutdown:new-go:bounded"}) {
		t.Fatalf("abort events = %#v", events)
	}
	if facts, _ := runtime.Hover(context.Background(), semantic.SemanticQuery{Path: "main.go"}); facts[0].Detail != "old-go" {
		t.Fatalf("old router was not preserved: %#v", facts)
	}
	if fmt.Sprint(issues) == "" || containsAny(fmt.Sprint(issues), "secret", ".exe", "process output") {
		t.Fatalf("issue leaked factory details: %#v", issues)
	}
}

func TestCoordinatorModeFalseInstallsASTOnlyBeforeOldShutdown(t *testing.T) {
	events := []string{}
	initial := allInitialSlots(&events)
	runtime := semantic.NewRuntime(routerForTestSlots(initial))
	coordinator := NewCoordinator(runtime, Factories{Go: testFactory(FamilyGo, &events, nil)}, &capabilityRecorder{}, initial, 50*time.Millisecond)
	prepared, issues := coordinator.Prepare(context.Background(), settings.Values{GoplsMode: "false"}, settings.ChangeSet{Fields: []settings.FieldKey{settings.FieldGoplsMode}})
	if len(issues) != 0 {
		t.Fatal(issues)
	}
	if len(events) != 0 {
		t.Fatalf("disabled family constructed a process: %#v", events)
	}
	prepared.Activate()
	if _, err := runtime.Hover(context.Background(), semantic.SemanticQuery{Path: "main.go"}); !errors.Is(err, semantic.ErrCapabilityUnsupported) {
		t.Fatalf("disabled route remained active: %v", err)
	}
	if !reflect.DeepEqual(events, []string{"shutdown:old-go:bounded"}) {
		t.Fatalf("shutdown order = %#v", events)
	}
}

func allInitialSlots(events *[]string) map[Family]Slot {
	result := map[Family]Slot{}
	for _, family := range []Family{FamilyGo, FamilyTypeScript, FamilySwift, FamilyPython, FamilyRust} {
		name := "old-" + string(family)
		result[family] = Slot{
			Provider:   &coordinatorProvider{id: semantic.SemanticProviderID(name)},
			Capability: capabilities.Result{ID: capabilityID(family), State: capabilities.CapabilityAvailable},
			Shutdown: func(name string) func(context.Context) error {
				return func(ctx context.Context) error {
					bounded := "unbounded"
					if _, ok := ctx.Deadline(); ok {
						bounded = "bounded"
					}
					*events = append(*events, "shutdown:"+name+":"+bounded)
					return nil
				}
			}(name),
		}
	}
	return result
}

func testFactory(family Family, events *[]string, buildErr error) Factory {
	return func(context.Context, settings.Values) (Slot, error) {
		*events = append(*events, "build:"+string(family))
		if buildErr != nil {
			return Slot{}, buildErr
		}
		name := "new-" + string(family)
		return Slot{
			Provider: &coordinatorProvider{id: semantic.SemanticProviderID(name)},
			Capability: capabilities.Result{
				ID: capabilityID(family), State: capabilities.CapabilityAvailable,
				Message: "raw process output", Metadata: map[string]string{"languageFamily": string(family), "executablePath": "C:/private/server.exe"},
			},
			Shutdown: func(ctx context.Context) error {
				bounded := "unbounded"
				if _, ok := ctx.Deadline(); ok {
					bounded = "bounded"
				}
				*events = append(*events, "shutdown:"+name+":"+bounded)
				return nil
			},
		}, nil
	}
}

func routerForTestSlots(slots map[Family]Slot) *semantic.PathRouter {
	routes := map[string]semantic.SemanticProvider{}
	for family, slot := range slots {
		for _, extension := range familyExtensions(family) {
			routes[extension] = slot.Provider
		}
	}
	return semantic.NewPathRouter(routes)
}

func containsAny(value string, needles ...string) bool {
	for _, needle := range needles {
		if strings.Contains(value, needle) {
			return true
		}
	}
	return false
}
