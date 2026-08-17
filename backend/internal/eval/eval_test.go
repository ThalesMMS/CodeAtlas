package eval

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/ThalesMMS/CodeAtlas/internal/ai"
	"github.com/ThalesMMS/CodeAtlas/internal/aiout"
	"github.com/ThalesMMS/CodeAtlas/internal/contextpack"
)

// repoRoot walks up from this test's directory to the repository root (which holds
// the eval/ and examples/ corpus).
func repoRoot(t *testing.T) string {
	t.Helper()
	wd, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	// backend/internal/eval -> repo root
	return filepath.Clean(filepath.Join(wd, "..", "..", ".."))
}

func TestIndexCorpusCloseRemovesTemporaryWorkspace(t *testing.T) {
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "main.go"), []byte("package main\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	store, err := indexCorpus(context.Background(), []string{root})
	if err != nil {
		t.Fatal(err)
	}
	temporary, ok := store.(*temporaryCorpusStore)
	if !ok {
		t.Fatalf("indexCorpus store = %T, want *temporaryCorpusStore", store)
	}
	if _, err := os.Stat(temporary.directory); err != nil {
		t.Fatalf("temporary workspace missing before Close: %v", err)
	}
	if err := store.Close(); err != nil {
		t.Fatalf("Close() error = %v", err)
	}
	if _, err := os.Stat(temporary.directory); !os.IsNotExist(err) {
		t.Fatalf("temporary workspace remains after Close: %v", err)
	}
	if err := store.Close(); err != nil {
		t.Fatalf("second Close() error = %v", err)
	}
}

func TestIndexCorpusErrorRemovesTemporaryWorkspace(t *testing.T) {
	tempParent := t.TempDir()
	t.Setenv("TMPDIR", tempParent)
	if _, err := indexCorpus(context.Background(), []string{filepath.Join(tempParent, "missing")}); err == nil {
		t.Fatal("indexCorpus() succeeded for a missing root")
	}
	entries, err := os.ReadDir(tempParent)
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 0 {
		t.Fatalf("temporary workspace leaked after error: %#v", entries)
	}
}

func TestOfflineWikiTableUsesSelectedEvidenceTitle(t *testing.T) {
	t.Parallel()
	packJSON, _ := json.Marshal(contextpack.ContextPack{Evidence: []contextpack.Evidence{
		{Title: "empty id"}, {ID: "ev:selected", Title: "selected title", Kind: contextpack.KindASTObservation, Content: "code"},
	}})
	planJSON, _ := json.Marshal(map[string]any{"title": "Page", "archetype": "module", "allowedRelatedPages": []string{}})
	prompt := "<CODEATLAS_CONTEXT_PACK>\n" + string(packJSON) + "\n</CODEATLAS_CONTEXT_PACK>\n" +
		"<CODEATLAS_WIKI_PAGE_PLAN>\n" + string(planJSON) + "\n</CODEATLAS_WIKI_PAGE_PLAN>"
	raw, err := offlineWikiResponse(prompt)
	if err != nil {
		t.Fatal(err)
	}
	var page aiout.WikiPageContent
	if err := json.Unmarshal([]byte(raw), &page); err != nil {
		t.Fatal(err)
	}
	if got := page.Sections[0].Tables[0].Rows[0][0]; got != "selected title" {
		t.Fatalf("table title = %q, want selected evidence title", got)
	}
}

func corpusRoots(root string) []string {
	return []string{
		filepath.Join(root, "examples", "tinycommerce"),
		filepath.Join(root, "examples", "swiftcommerce"),
		filepath.Join(root, "examples", "pythoncommerce"),
		filepath.Join(root, "examples", "rustcommerce"),
		filepath.Join(root, "eval", "corpus", "ambiguous"),
		filepath.Join(root, "eval", "corpus", "adversarial"),
	}
}

func loadCorpusCases(t *testing.T, root string) []Case {
	t.Helper()
	data, err := os.ReadFile(filepath.Join(root, "eval", "cases.json"))
	if err != nil {
		t.Fatalf("read cases: %v", err)
	}
	var cases []Case
	if err := json.Unmarshal(data, &cases); err != nil {
		t.Fatalf("parse cases: %v", err)
	}
	SortCases(cases)
	return cases
}

// TestEvalGatesPassAndAreDeterministic is the offline, network-free gate that runs
// in CI: every case resolves its target with grounded, budget-respecting,
// duplicate-free evidence, and two independent runs produce byte-identical
// reports (deterministic ContextPackHash and ordering).
func TestEvalGatesPassAndAreDeterministic(t *testing.T) {
	root := repoRoot(t)
	cases := loadCorpusCases(t, root)
	roots := corpusRoots(root)

	first, err := Run(context.Background(), roots, cases)
	if err != nil {
		t.Fatalf("Run() error = %v", err)
	}
	// Gate against a baseline derived from this run's contract: the absolute gates
	// (target=100%, grounding/budget/forbidden=0, determinism) must hold.
	if violations := Gate(first, NewBaseline(first)); len(violations) != 0 {
		t.Fatalf("gate violations on a fresh baseline: %v", violations)
	}
	if first.Summary.TargetResolutionRate != 1.0 {
		t.Fatalf("target resolution = %.3f, want 1.0", first.Summary.TargetResolutionRate)
	}
	if first.Summary.TotalGroundingInvalid != 0 || first.Summary.TotalBudgetViolations != 0 || first.Summary.TotalForbiddenHits != 0 {
		t.Fatalf("expected zero grounding/budget/forbidden, got %+v", first.Summary)
	}
	if !first.Summary.AllDeterministic {
		t.Fatal("a single run already reported non-deterministic hashes")
	}
	if len(first.Quality.Surfaces) != 4 || len(first.Quality.Goldens) != 4 {
		t.Fatalf("quality scorecard = %d surfaces/%d goldens, want 4/4", len(first.Quality.Surfaces), len(first.Quality.Goldens))
	}
	if first.Quality.Judge.Status != "skipped" {
		t.Fatalf("offline judge status = %q, want skipped", first.Quality.Judge.Status)
	}
	for _, surface := range first.Quality.Surfaces {
		if surface.Score <= 0 || surface.StructureCoverage <= 0 {
			t.Errorf("surface %q has an empty deterministic rubric: %#v", surface.Surface, surface)
		}
	}
	knownSpeculationFound := false
	for _, golden := range first.Quality.Goldens {
		if golden.Surface == "see_more" && slices.Contains(golden.Current.SpeculationHits, "probably") {
			knownSpeculationFound = true
		}
	}
	if !knownSpeculationFound {
		t.Fatal("historical See More fixture did not flag the known probably speculation")
	}

	// A second independent run (fresh store, fresh packers) must match exactly.
	second, err := Run(context.Background(), roots, cases)
	if err != nil {
		t.Fatalf("second Run() error = %v", err)
	}
	firstJSON, _ := json.Marshal(first)
	secondJSON, _ := json.Marshal(second)
	if string(firstJSON) != string(secondJSON) {
		t.Fatal("two eval runs produced different reports; the deterministic layer is not reproducible")
	}
}

// TestEvalBaselineMatchesCurrentVersions guards that the committed baseline is for
// the current policy/schema versions — a stale baseline must fail loudly.
func TestEvalBaselineMatchesCurrentVersions(t *testing.T) {
	root := repoRoot(t)
	data, err := os.ReadFile(filepath.Join(root, "eval", "baseline.json"))
	if err != nil {
		t.Fatalf("read baseline: %v", err)
	}
	var baseline Baseline
	if err := json.Unmarshal(data, &baseline); err != nil {
		t.Fatalf("parse baseline: %v", err)
	}
	if baseline.Versions != CurrentVersions() {
		t.Fatalf("baseline versions %+v != current %+v — run `make eval-update` and note it in the PR", baseline.Versions, CurrentVersions())
	}
}

func TestQualityBaselineGatesRegressions(t *testing.T) {
	report := Report{
		Versions: CurrentVersions(),
		Summary: Summary{
			TargetResolutionRate: 1,
			MeanRecallAtK:        1,
			AllDeterministic:     true,
		},
		Quality: QualityReport{Surfaces: []SurfaceScore{{
			Surface: "codemap", Score: 90, ExternalNodeRatio: 0.10,
		}}},
	}
	baseline := NewBaseline(report)
	report.Quality.Surfaces[0].Score = 80
	report.Quality.Surfaces[0].SpeculationHits = []string{"probably"}
	report.Quality.Surfaces[0].ExternalNodeRatio = 0.40

	violations := strings.Join(Gate(report, baseline), "\n")
	for _, wanted := range []string{"quality score codemap", "speculation hits codemap", "external-node ratio codemap"} {
		if !strings.Contains(violations, wanted) {
			t.Errorf("Gate() violations = %q, want %q", violations, wanted)
		}
	}
}

func TestRequestedUnavailableJudgeSkipsGracefully(t *testing.T) {
	report := runJudge(context.Background(), QualityOptions{
		JudgeRequested: true,
		Judge:          ai.Disabled{},
	}, nil, nil)
	if report.Status != "skipped" || !strings.Contains(report.Reason, "not available") {
		t.Fatalf("runJudge() = %#v, want unavailable skip", report)
	}
}
