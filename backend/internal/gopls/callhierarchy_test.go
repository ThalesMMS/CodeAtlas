package gopls

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/ThalesMMS/CodeAtlas/internal/domain"
	"github.com/ThalesMMS/CodeAtlas/internal/lspconv"
	"github.com/ThalesMMS/CodeAtlas/internal/semantic"
)

func callItemJSON(root, relative, name string, line int) callHierarchyItem {
	uri := lspconv.PathToURI(root + "/" + relative)
	return callHierarchyItem{
		Name: name, Kind: 12, URI: uri,
		Range:          lspRange{Start: lspPosition{Line: line}, End: lspPosition{Line: line, Character: 10}},
		SelectionRange: lspRange{Start: lspPosition{Line: line, Character: 5}, End: lspPosition{Line: line, Character: 5 + len(name)}},
		Data:           json.RawMessage(`{"opaque":"server-state"}`),
	}
}

func TestIncomingCallsProduceDirectionalFacts(t *testing.T) {
	t.Parallel()
	files := map[string][]byte{
		"a.go": []byte("package a\nfunc Target() {}\nfunc Caller() { Target() }\n"),
	}
	provider, fake, root := providerWithFake(t, files)
	target := callItemJSON(root, "a.go", "Target", 1)
	prepared, _ := json.Marshal([]callHierarchyItem{target})
	fake.respond("textDocument/prepareCallHierarchy", prepared)
	caller := callItemJSON(root, "a.go", "Caller", 2)
	incoming, _ := json.Marshal([]incomingCall{{From: caller, FromRanges: []lspRange{{Start: lspPosition{Line: 2, Character: 15}, End: lspPosition{Line: 2, Character: 21}}}}})
	fake.respond("callHierarchy/incomingCalls", incoming)

	facts, err := provider.IncomingCalls(context.Background(), semantic.SemanticQuery{SnapshotID: "s1", Path: "a.go", Position: domain.Position{Line: 2, Column: 6}})
	if err != nil {
		t.Fatalf("IncomingCalls: %v", err)
	}
	if len(facts) != 1 {
		t.Fatalf("expected one incoming-call fact, got %d", len(facts))
	}
	fact := facts[0]
	if fact.Kind != semantic.KindCallIncoming {
		t.Fatalf("kind = %q, want call_incoming", fact.Kind)
	}
	if fact.Object == nil || fact.Object.Name != "Caller" {
		t.Fatalf("incoming caller object = %+v, want Caller", fact.Object)
	}
	if len(fact.Related) == 0 {
		t.Fatal("expected fromRanges as related evidence")
	}
}

func TestOutgoingCallRangesUseCallerURI(t *testing.T) {
	t.Parallel()
	files := map[string][]byte{
		"caller.go": []byte("package p\nfunc Caller() { Callee() }\n"),
		"callee.go": []byte("package p\nfunc Callee() {}\n"),
	}
	provider, fake, root := providerWithFake(t, files)
	caller := callItemJSON(root, "caller.go", "Caller", 1)
	prepared, _ := json.Marshal([]callHierarchyItem{caller})
	fake.respond("textDocument/prepareCallHierarchy", prepared)
	callee := callItemJSON(root, "callee.go", "Callee", 1)
	outgoing, _ := json.Marshal([]outgoingCall{{To: callee, FromRanges: []lspRange{{Start: lspPosition{Line: 1, Character: 16}, End: lspPosition{Line: 1, Character: 22}}}}})
	fake.respond("callHierarchy/outgoingCalls", outgoing)

	facts, err := provider.OutgoingCalls(context.Background(), semantic.SemanticQuery{SnapshotID: "s1", Path: "caller.go", Position: domain.Position{Line: 2, Column: 6}})
	if err != nil {
		t.Fatalf("OutgoingCalls: %v", err)
	}
	if len(facts) != 1 || facts[0].Location.Path != "callee.go" {
		t.Fatalf("outgoing call = %+v, want callee primary location", facts)
	}
	if len(facts[0].Related) != 1 || facts[0].Related[0].Path != "caller.go" {
		t.Fatalf("outgoing related locations = %+v, want caller.go", facts[0].Related)
	}
}

func TestCallHierarchyAmbiguous(t *testing.T) {
	t.Parallel()
	provider, fake, root := providerWithFake(t, map[string][]byte{"a.go": []byte("package a\nfunc A() {}\n")})
	itemA := callItemJSON(root, "a.go", "A", 1)
	itemB := callItemJSON(root, "b.go", "B", 1)
	prepared, _ := json.Marshal([]callHierarchyItem{itemA, itemB})
	fake.respond("textDocument/prepareCallHierarchy", prepared)
	if _, err := provider.IncomingCalls(context.Background(), semantic.SemanticQuery{Path: "a.go", Position: domain.Position{Line: 2, Column: 6}}); err == nil {
		t.Fatal("two distinct call-hierarchy items should be ambiguous")
	}
}

func TestDiagnosticsIngestionAndVersioning(t *testing.T) {
	t.Parallel()
	files := map[string][]byte{"a.go": []byte("package a\nfunc A() int { return }\n")}
	provider, fake, root := providerWithFake(t, files)
	uri := lspconv.PathToURI(root + "/a.go")

	// A versioned publishDiagnostics for document version 3.
	params, _ := json.Marshal(publishDiagnosticsParams{
		URI: uri, Version: int64Ptr(3),
		Diagnostics: []lspDiagnostic{{
			Range:    lspRange{Start: lspPosition{Line: 1, Character: 14}, End: lspPosition{Line: 1, Character: 21}},
			Severity: 1, Source: "compiler", Message: "missing return value",
		}},
	})
	fake.fire("textDocument/publishDiagnostics", params)

	query := semantic.SemanticQuery{Path: "a.go", DocumentID: "doc1", DocumentVersion: 3}
	facts, err := provider.Diagnostics(context.Background(), query)
	if err != nil {
		t.Fatalf("Diagnostics: %v", err)
	}
	if len(facts) != 1 {
		t.Fatalf("expected one diagnostic fact, got %d", len(facts))
	}
	if facts[0].Kind != semantic.KindDiagnostic || facts[0].Confidence != semantic.ConfidenceDiagnostic {
		t.Fatalf("bad diagnostic fact: %+v", facts[0])
	}
	if !facts[0].VersionKnown || facts[0].DocumentVersion != 3 {
		t.Fatalf("versioned diagnostic should be VersionKnown at v3: %+v", facts[0])
	}

	// A query for a different version gets the stale set discarded.
	if got, _ := provider.Diagnostics(context.Background(), semantic.SemanticQuery{Path: "a.go", DocumentID: "doc1", DocumentVersion: 4}); len(got) != 0 {
		t.Fatalf("stale-version diagnostics should be empty, got %d", len(got))
	}

	// An empty publishDiagnostics clears the set.
	clear, _ := json.Marshal(publishDiagnosticsParams{URI: uri, Version: int64Ptr(3), Diagnostics: []lspDiagnostic{}})
	fake.fire("textDocument/publishDiagnostics", clear)
	if got, _ := provider.Diagnostics(context.Background(), query); len(got) != 0 {
		t.Fatalf("cleared diagnostics should be empty, got %d", len(got))
	}
}

func int64Ptr(v int64) *int64 { return &v }
