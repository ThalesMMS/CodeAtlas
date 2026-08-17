package service

import (
	"testing"

	"github.com/ThalesMMS/CodeAtlas/internal/domain"
)

func TestDetectModulesGroupsAndSkipsVendored(t *testing.T) {
	t.Parallel()
	symbols := []domain.Symbol{
		{ID: "1", Path: "cmd/api/main.go", Name: "main", Kind: "function", Language: "go"},
		{ID: "2", Path: "cmd/worker/main.go", Name: "main", Kind: "function", Language: "go"},
		{ID: "3", Path: "internal/store/store.go", Name: "Store", Kind: "type", Language: "go"},
		{ID: "4", Path: "vendor/x/y.go", Name: "Y", Kind: "function", Language: "go"},
		{ID: "5", Path: "node_modules/pkg/index.js", Name: "f", Kind: "function", Language: "javascript"},
	}
	modules := detectModules(symbols)

	names := map[string]domain.WikiModule{}
	for _, module := range modules {
		names[module.Name] = module
	}
	// cmd/* keeps the binary name as a distinct module; internal/* groups by package.
	for _, want := range []string{"cmd/api", "cmd/worker", "internal/store"} {
		if _, ok := names[want]; !ok {
			t.Fatalf("expected module %q, got %v", want, names)
		}
	}
	// Vendored/generated trees never become modules.
	for _, unwanted := range []string{"vendor", "node_modules"} {
		if _, ok := names[unwanted]; ok {
			t.Fatalf("vendored path %q leaked into modules", unwanted)
		}
	}
	if !names["cmd/api"].Entrypoint {
		t.Fatal("cmd/api should be flagged as an entrypoint module")
	}
	if names["internal/store"].Entrypoint {
		t.Fatal("internal/store is not an entrypoint module")
	}
	// Slugs are stable and deterministic.
	if names["cmd/api"].Slug != "module-cmd-api" {
		t.Fatalf("unexpected slug %q", names["cmd/api"].Slug)
	}
}

func TestDetectModulesIsDeterministic(t *testing.T) {
	t.Parallel()
	symbols := []domain.Symbol{
		{ID: "1", Path: "pkg/a/a.go", Kind: "function", Language: "go"},
		{ID: "2", Path: "pkg/b/b.go", Kind: "function", Language: "go"},
	}
	first := detectModules(symbols)
	second := detectModules(symbols)
	if len(first) != len(second) {
		t.Fatalf("length differs: %d vs %d", len(first), len(second))
	}
	for i := range first {
		if first[i].Slug != second[i].Slug {
			t.Fatalf("order differs at %d: %q vs %q", i, first[i].Slug, second[i].Slug)
		}
	}
}
