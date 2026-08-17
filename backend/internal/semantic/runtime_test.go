package semantic

import (
	"context"
	"errors"
	"fmt"
	"reflect"
	"testing"
)

type runtimeReplayProvider struct {
	id       SemanticProviderID
	opens    []string
	changes  []string
	closes   []string
	failOpen DocumentID
}

func (p *runtimeReplayProvider) ID() SemanticProviderID { return p.id }
func (p *runtimeReplayProvider) Capabilities(context.Context) (SemanticCapabilities, error) {
	return SemanticCapabilities{Hover: true, Diagnostics: true, SemanticTokensFull: true}, nil
}
func (p *runtimeReplayProvider) facts(method string) ([]SemanticFact, error) {
	return []SemanticFact{{Detail: fmt.Sprintf("%s:%s", p.id, method)}}, nil
}
func (p *runtimeReplayProvider) Hover(context.Context, SemanticQuery) ([]SemanticFact, error) {
	return p.facts("hover")
}
func (p *runtimeReplayProvider) Definitions(context.Context, SemanticQuery) ([]SemanticFact, error) {
	return p.facts("definitions")
}
func (p *runtimeReplayProvider) References(context.Context, SemanticQuery) ([]SemanticFact, error) {
	return p.facts("references")
}
func (p *runtimeReplayProvider) Implementations(context.Context, SemanticQuery) ([]SemanticFact, error) {
	return p.facts("implementations")
}
func (p *runtimeReplayProvider) IncomingCalls(context.Context, SemanticQuery) ([]SemanticFact, error) {
	return p.facts("incoming")
}
func (p *runtimeReplayProvider) OutgoingCalls(context.Context, SemanticQuery) ([]SemanticFact, error) {
	return p.facts("outgoing")
}
func (p *runtimeReplayProvider) Diagnostics(context.Context, SemanticQuery) ([]SemanticFact, error) {
	return p.facts("diagnostics")
}
func (p *runtimeReplayProvider) SemanticTokens(context.Context, SemanticQuery) (SemanticTokenSet, error) {
	return SemanticTokenSet{ProviderSession: string(p.id)}, nil
}
func (p *runtimeReplayProvider) ProviderState() ProviderState {
	return ProviderState{State: ProviderStateAvailable, SessionID: string(p.id)}
}
func (p *runtimeReplayProvider) OpenDocument(_ context.Context, id DocumentID, version DocumentVersion, path, language string, content []byte) error {
	p.opens = append(p.opens, fmt.Sprintf("%s:%d:%s:%s:%s", id, version, path, language, content))
	if id == p.failOpen {
		return errors.New("replay failed")
	}
	return nil
}
func (p *runtimeReplayProvider) ChangeDocument(_ context.Context, id DocumentID, version DocumentVersion, path string, content []byte) error {
	p.changes = append(p.changes, fmt.Sprintf("%s:%d:%s:%s", id, version, path, content))
	return nil
}
func (p *runtimeReplayProvider) CloseDocument(_ context.Context, id DocumentID, path string) error {
	p.closes = append(p.closes, fmt.Sprintf("%s:%s", id, path))
	return nil
}

func TestRuntimeReplaysOpenDocumentsBeforeSwap(t *testing.T) {
	oldProvider := &runtimeReplayProvider{id: "old"}
	newProvider := &runtimeReplayProvider{id: "new"}
	runtime := NewRuntime(NewPathRouter(map[string]SemanticProvider{".go": oldProvider}))

	ctx := context.Background()
	if err := runtime.OpenDocument(ctx, "b", 1, "b.go", "go", []byte("b-v1")); err != nil {
		t.Fatal(err)
	}
	if err := runtime.OpenDocument(ctx, "a", 1, "a.go", "go", []byte("a-v1")); err != nil {
		t.Fatal(err)
	}
	if err := runtime.ChangeDocument(ctx, "a", 2, "a.go", []byte("a-v2")); err != nil {
		t.Fatal(err)
	}

	activate, abort, err := runtime.Prepare(ctx, NewPathRouter(map[string]SemanticProvider{".go": newProvider}))
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(newProvider.opens, []string{"a:2:a.go:go:a-v2", "b:1:b.go:go:b-v1"}) {
		t.Fatalf("candidate replay = %#v", newProvider.opens)
	}
	if facts, _ := runtime.Hover(ctx, SemanticQuery{Path: "a.go"}); facts[0].Detail != "old:hover" {
		t.Fatalf("query swapped before activation: %#v", facts)
	}

	activate()
	activate()
	if facts, _ := runtime.Definitions(ctx, SemanticQuery{Path: "a.go"}); facts[0].Detail != "new:definitions" {
		t.Fatalf("definition did not swap: %#v", facts)
	}
	if facts, _ := runtime.Diagnostics(ctx, SemanticQuery{Path: "a.go"}); facts[0].Detail != "new:diagnostics" {
		t.Fatalf("diagnostics did not swap: %#v", facts)
	}
	if tokens, _ := runtime.SemanticTokens(ctx, SemanticQuery{Path: "a.go"}); tokens.ProviderSession != "new" {
		t.Fatalf("tokens did not swap: %#v", tokens)
	}
	if id, ok := runtime.ProviderIDForPath("a.go"); !ok || id != "new" {
		t.Fatalf("provider ID = %q/%v", id, ok)
	}
	if state := runtime.ProviderStateForPath("a.go"); state.SessionID != "new" {
		t.Fatalf("provider state = %#v", state)
	}
	if err := runtime.ChangeDocument(ctx, "a", 3, "a.go", []byte("a-v3")); err != nil {
		t.Fatal(err)
	}
	if len(newProvider.changes) != 1 || len(oldProvider.changes) != 1 {
		t.Fatalf("post-swap changes old/new = %#v/%#v", oldProvider.changes, newProvider.changes)
	}
	abort(ctx)
}

func TestRuntimeReplayFailureClosesCandidateAndPreservesOldRouter(t *testing.T) {
	oldProvider := &runtimeReplayProvider{id: "old"}
	brokenProvider := &runtimeReplayProvider{id: "broken", failOpen: "b"}
	runtime := NewRuntime(NewPathRouter(map[string]SemanticProvider{".go": oldProvider}))
	ctx := context.Background()
	_ = runtime.OpenDocument(ctx, "a", 1, "a.go", "go", []byte("a"))
	_ = runtime.OpenDocument(ctx, "b", 1, "b.go", "go", []byte("b"))

	activate, abort, err := runtime.Prepare(ctx, NewPathRouter(map[string]SemanticProvider{".go": brokenProvider}))
	if err == nil || activate != nil || abort != nil {
		t.Fatalf("Prepare returned activate=%t abort=%t error=%v", activate != nil, abort != nil, err)
	}
	if !reflect.DeepEqual(brokenProvider.closes, []string{"a:a.go"}) {
		t.Fatalf("candidate cleanup = %#v", brokenProvider.closes)
	}
	if err := runtime.ChangeDocument(ctx, "a", 2, "a.go", []byte("old-still-live")); err != nil {
		t.Fatal(err)
	}
	if len(oldProvider.changes) != 1 || len(brokenProvider.changes) != 0 {
		t.Fatalf("changes old/broken = %#v/%#v", oldProvider.changes, brokenProvider.changes)
	}
}

var (
	_ SemanticProvider      = (*Runtime)(nil)
	_ SemanticTokenProvider = (*Runtime)(nil)
	_ DocumentSemanticSync  = (*Runtime)(nil)
)
