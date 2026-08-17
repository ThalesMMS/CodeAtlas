package service

import (
	"strings"
	"testing"
	"time"

	"github.com/ThalesMMS/CodeAtlas/internal/domain"
)

type fakeLookup map[string]domain.Symbol

func (f fakeLookup) GetSymbol(id string) (domain.Symbol, bool) {
	symbol, ok := f[id]
	return symbol, ok
}

func sampleSymbol(id, code string) domain.Symbol {
	return domain.Symbol{ID: id, OccurrenceID: "occ:" + id, Path: "pkg/svc.go", Name: "Submit", QualifiedName: "pkg.Submit", Signature: "func Submit()", Code: code}
}

func TestDependenciesAreSortedDedupedAndOrderIndependent(t *testing.T) {
	t.Parallel()
	a := dependencyForSymbol(sampleSymbol("sym:b", "x"))
	b := dependencyForSymbol(sampleSymbol("sym:a", "y"))
	hash1 := contextPackHash(sortDependencies([]domain.Dependency{a, b, a})) // duplicate a
	hash2 := contextPackHash(sortDependencies([]domain.Dependency{b, a}))    // reverse order
	if hash1 != hash2 {
		t.Fatalf("ContextPackHash depends on order/duplication: %s vs %s", hash1, hash2)
	}
	if got := len(sortDependencies([]domain.Dependency{a, b, a})); got != 2 {
		t.Fatalf("dependencies not deduped: got %d, want 2", got)
	}
}

func TestEvaluateArtifactRuntimeContract(t *testing.T) {
	t.Parallel()
	symbol := sampleSymbol("sym:1", "body")
	metadata := buildArtifactMetadata(ArtifactTypeExplain, "sym:1", PromptVersionExplain, "prov", "model", "sha256:snap", 1,
		[]domain.Dependency{dependencyForSymbol(symbol)}, time.Now())

	status, reasons := evaluateArtifact(metadata, fakeLookup{"sym:1": symbol}, "sha256:other", "prov", "model", PromptVersionExplain)
	if status != domain.ArtifactCurrent || len(reasons) != 0 {
		t.Fatalf("unrelated snapshot change = %s %v, want current", status, reasons)
	}

	cases := []struct {
		name          string
		lookup        fakeLookup
		provider      string
		model         string
		promptVersion string
		wantReason    string
	}{
		{"dependency removed", fakeLookup{}, "prov", "model", PromptVersionExplain, "no longer exists"},
		{"content changed", fakeLookup{"sym:1": sampleSymbol("sym:1", "edited")}, "prov", "model", PromptVersionExplain, "content changed"},
		{"provider changed", fakeLookup{"sym:1": symbol}, "other", "model", PromptVersionExplain, "provider or model changed"},
		{"prompt changed", fakeLookup{"sym:1": symbol}, "prov", "model", "explain.v9", "prompt version changed"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			status, reasons := evaluateArtifact(metadata, tc.lookup, "sha256:snap", tc.provider, tc.model, tc.promptVersion)
			if status != domain.ArtifactStale || !reasonsContain(reasons, tc.wantReason) {
				t.Fatalf("status/reasons = %s %v, want stale containing %q", status, reasons, tc.wantReason)
			}
		})
	}
}

func TestEvaluateGlobalArtifactAgainstSnapshot(t *testing.T) {
	t.Parallel()
	metadata := buildArtifactMetadata(ArtifactTypeDeepWiki, "repo/overview", PromptVersionDeepWiki, "prov", "", "sha256:A", 1,
		[]domain.Dependency{snapshotDependency("sha256:A")}, time.Now())
	status, reasons := evaluateArtifact(metadata, fakeLookup{}, "sha256:B", "prov", "", PromptVersionDeepWiki)
	if status != domain.ArtifactStale || !reasonsContain(reasons, "structural snapshot changed") {
		t.Fatalf("status/reasons = %s %v, want stale snapshot", status, reasons)
	}
}

func reasonsContain(reasons []string, part string) bool {
	for _, reason := range reasons {
		if strings.Contains(reason, part) {
			return true
		}
	}
	return false
}
