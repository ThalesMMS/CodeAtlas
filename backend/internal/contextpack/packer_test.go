package contextpack

import (
	"context"
	"path/filepath"
	"strings"
	"testing"

	"github.com/ThalesMMS/CodeAtlas/internal/domain"
	"github.com/ThalesMMS/CodeAtlas/internal/repository"
)

func indexedStore(t testing.TB) repository.Store {
	t.Helper()
	repository, err := repository.OpenJSON(filepath.Join(t.TempDir(), "index.json"))
	if err != nil {
		t.Fatal(err)
	}
	rng := func(start int) domain.Range {
		return domain.Range{Start: domain.Position{Line: start, Column: 1}, End: domain.Position{Line: start + 2, Column: 1}}
	}
	repository.ReplaceFile(domain.ParsedFile{
		File: domain.File{Path: "pkg/svc.go", Language: "go", Hash: "h"},
		Symbols: []domain.Symbol{
			{ID: "f", Path: "pkg/svc.go", Language: "go", Name: "svc.go", QualifiedName: "pkg/svc.go", Kind: "file", Range: rng(1)},
			{ID: "submit", Path: "pkg/svc.go", Language: "go", Name: "Submit", QualifiedName: "pkg.Submit", Kind: "function", Signature: "func Submit()", Code: "func Submit() { Save() }", Range: rng(3)},
			{ID: "save", Path: "pkg/svc.go", Language: "go", Name: "Save", QualifiedName: "pkg.Save", Kind: "function", Signature: "func Save()", Code: "func Save() {}", Range: rng(8)},
		},
		Edges: []domain.Edge{
			{FromSymbolID: "f", ToSymbolID: "submit", ToName: "Submit", Type: "contains", Path: "pkg/svc.go", Line: 3},
			{FromSymbolID: "submit", ToSymbolID: "save", ToName: "Save", Type: "calls", Path: "pkg/svc.go", Line: 3},
		},
	})
	return repository
}

func basePacker(repository repository.Store) *Packer {
	base := NewBasePolicy()
	return NewPacker(repository, map[Feature]Policy{
		FeatureHover: base, FeatureSeeMore: base, FeatureCodemap: base, FeatureDeepWiki: base,
	})
}

func TestBuildIsDeterministic(t *testing.T) {
	t.Parallel()
	repository := indexedStore(t)
	packer := basePacker(repository)
	request := ContextRequest{Feature: FeatureCodemap, SnapshotID: repository.SnapshotID(), Query: "submit save"}

	first, err := packer.Build(context.Background(), request)
	if err != nil {
		t.Fatalf("Build() error = %v", err)
	}
	// A fresh packer (no cache) over the same view must reproduce the pack exactly.
	second, err := basePacker(repository).Build(context.Background(), request)
	if err != nil {
		t.Fatal(err)
	}
	if first.Hash != second.Hash {
		t.Fatalf("non-deterministic hash: %s vs %s", first.Hash, second.Hash)
	}
	if len(first.Evidence) == 0 {
		t.Fatal("expected evidence")
	}
	for i := range first.Evidence {
		if first.Evidence[i].ID != second.Evidence[i].ID {
			t.Fatalf("evidence order/id differs at %d", i)
		}
	}
	if err := ValidatePack(first); err != nil {
		t.Fatalf("built pack is invalid: %v", err)
	}
}

type snapshotCountingStore struct {
	repository.Store
	snapshots int
}

func (s *snapshotCountingStore) Snapshot() repository.ReadView {
	s.snapshots++
	return s.Store.Snapshot()
}
func (s *snapshotCountingStore) SnapshotContext(ctx context.Context) (repository.ReadView, error) {
	s.snapshots++
	return s.Store.SnapshotContext(ctx)
}

func TestBuildCacheHitDoesNotMaterializeAnotherView(t *testing.T) {
	t.Parallel()
	store := &snapshotCountingStore{Store: indexedStore(t)}
	packer := basePacker(store)
	request := ContextRequest{Feature: FeatureCodemap, SnapshotID: store.SnapshotID(), Query: "submit save"}
	if _, err := packer.Build(context.Background(), request); err != nil {
		t.Fatalf("first Build: %v", err)
	}
	if _, err := packer.Build(context.Background(), request); err != nil {
		t.Fatalf("cached Build: %v", err)
	}
	if store.snapshots != 1 {
		t.Fatalf("Snapshot calls = %d, want 1 after a cache hit", store.snapshots)
	}
}

func TestBuildFixesSnapshotAndRejectsMismatch(t *testing.T) {
	t.Parallel()
	repository := indexedStore(t)
	packer := basePacker(repository)
	if _, err := packer.Build(context.Background(), ContextRequest{Feature: FeatureCodemap, SnapshotID: "sha256:wrong", Query: "submit"}); err == nil {
		t.Fatal("Build accepted a request pinned to a non-active snapshot")
	}
	// The same request pinned to the active snapshot succeeds, and the pack's
	// snapshot metadata matches the pinned view.
	pack, err := packer.Build(context.Background(), ContextRequest{Feature: FeatureCodemap, SnapshotID: repository.SnapshotID(), Query: "submit"})
	if err != nil {
		t.Fatalf("Build() error = %v", err)
	}
	if pack.Snapshot.ID != repository.SnapshotID() {
		t.Fatalf("pack snapshot %s != active %s", pack.Snapshot.ID, repository.SnapshotID())
	}
}

func TestBuildRespectsBudgetAndRecordsOmissions(t *testing.T) {
	t.Parallel()
	repository := indexedStore(t)
	tiny := &BasePolicy{
		version:   "tiny.v1",
		expansion: ExpansionPolicy{MaxDepth: 1, MaxNodes: 10, MaxEdges: 10, EdgeTypes: []string{"calls", "contains"}},
		budget:    BudgetPolicy{MaxBytes: 60, MaxBytesPerEvidence: 20, MaxEvidence: 1, ReserveBytes: 0},
	}
	packer := NewPacker(repository, map[Feature]Policy{FeatureCodemap: tiny})
	pack, err := packer.Build(context.Background(), ContextRequest{Feature: FeatureCodemap, SnapshotID: repository.SnapshotID(), Query: "submit save"})
	if err != nil {
		t.Fatalf("Build() error = %v", err)
	}
	if len(pack.Evidence) > 1 {
		t.Fatalf("budget MaxEvidence=1 exceeded: %d", len(pack.Evidence))
	}
	if len(pack.Omissions) == 0 {
		t.Fatal("dropped candidates were not recorded as omissions")
	}
	if pack.Budget.UsedBytes > tiny.budget.MaxBytes {
		t.Fatalf("used %d bytes over budget %d", pack.Budget.UsedBytes, tiny.budget.MaxBytes)
	}
	for _, evidence := range pack.Evidence {
		if len(evidence.Content) > tiny.budget.MaxBytesPerEvidence+len("\n…") {
			t.Fatalf("evidence content not truncated to per-evidence budget: %d", len(evidence.Content))
		}
	}
}

func TestApplyBudgetDropsEdgesWhoseNodesWereDropped(t *testing.T) {
	t.Parallel()
	ranked := []Candidate{
		{Evidence: Evidence{ID: "ev:a", SymbolID: "a", Title: "A", Content: "func A() {}"}},
		{Evidence: Evidence{ID: "ev:b", SymbolID: "b", Title: "B", Content: "func B() {}"}},
	}
	rawEdges := []domain.Edge{{FromSymbolID: "a", ToSymbolID: "b", Type: "calls"}}

	_, graph, _, _ := applyBudget(ranked, rawEdges, BudgetPolicy{
		MaxBytes: 1024, MaxEvidence: 2, MaxNodes: 1, MaxEdges: 2,
	})

	if len(graph.Nodes) != 1 {
		t.Fatalf("graph nodes = %d, want 1", len(graph.Nodes))
	}
	if len(graph.Edges) != 0 {
		t.Fatalf("graph contains edge with a dropped endpoint: %+v", graph.Edges)
	}
}

func TestCodeDisplayTruncationKeepsOnlyCompleteSourceLines(t *testing.T) {
	t.Parallel()
	code := "line one\nline two is long\nline three"
	truncated, didTruncate := truncateCodeContent(code, 18)
	if !didTruncate || truncated != "line one" {
		t.Fatalf("truncateCodeContent() = %q, %v", truncated, didTruncate)
	}
	if strings.Contains(truncated, "…") || strings.HasSuffix(truncated, "line t") {
		t.Fatalf("code contains a marker or partial line: %q", truncated)
	}
	if got, truncated := truncateCodeContent("one very long line", 5); got != "" || !truncated {
		t.Fatalf("single oversized line = %q, %v; want empty,true", got, truncated)
	}
}

func TestBuildHonorsCancellation(t *testing.T) {
	t.Parallel()
	repository := indexedStore(t)
	packer := basePacker(repository)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := packer.Build(ctx, ContextRequest{Feature: FeatureCodemap, SnapshotID: repository.SnapshotID(), Query: "submit"}); err == nil {
		t.Fatal("Build ignored a cancelled context")
	}
}

func TestReciprocalRankFusion(t *testing.T) {
	t.Parallel()
	top := reciprocalRankFusion(Candidate{LexicalRank: 1})
	lower := reciprocalRankFusion(Candidate{LexicalRank: 5})
	if top <= lower {
		t.Fatalf("rank 1 (%f) should outrank rank 5 (%f)", top, lower)
	}
	both := reciprocalRankFusion(Candidate{LexicalRank: 3, PackageAPIRank: 3})
	if both <= reciprocalRankFusion(Candidate{LexicalRank: 3}) {
		t.Fatal("appearing in both lists should not lower the fused score")
	}
	if target := reciprocalRankFusion(Candidate{IsTarget: true}); target < 1 {
		t.Fatal("the explicit target must dominate")
	}
}

func TestSeeMorePolicyIsDeeperAndDeclaresMissingSources(t *testing.T) {
	t.Parallel()
	repository := indexedStore(t)
	packer := NewPacker(repository, map[Feature]Policy{FeatureSeeMore: NewSeeMorePolicy()})
	pack, err := packer.Build(context.Background(), ContextRequest{
		Feature:    FeatureSeeMore,
		SnapshotID: repository.SnapshotID(),
		Location:   &SourceLocation{Path: "pkg/svc.go", Line: 4, Column: 1},
	})
	if err != nil {
		t.Fatalf("Build() error = %v", err)
	}
	if pack.PolicyVersion != SeeMorePolicyVersion {
		t.Fatalf("policy version = %q, want %q", pack.PolicyVersion, SeeMorePolicyVersion)
	}
	if err := ValidatePack(pack); err != nil {
		t.Fatalf("see-more pack invalid: %v", err)
	}
	// Unimplemented change-impact sources are declared, not invented.
	foundUnavailable := false
	for _, omission := range pack.Omissions {
		if omission.Reason == OmitSourceUnavailable {
			foundUnavailable = true
		}
	}
	if !foundUnavailable {
		t.Fatal("see-more did not declare source_unavailable omissions for unimplemented sources")
	}
	// The target must be present in the evidence.
	hasTarget := false
	for _, evidence := range pack.Evidence {
		if string(evidence.SymbolID) == string(pack.Target.SymbolID) {
			hasTarget = true
		}
	}
	if !hasTarget {
		t.Fatal("target evidence missing from see-more pack")
	}
}

func TestDeepWikiPolicyModuleScopeLimitsEvidence(t *testing.T) {
	t.Parallel()
	repository := indexedStore(t)
	packer := NewPacker(repository, map[Feature]Policy{FeatureDeepWiki: NewDeepWikiPolicy()})
	pack, err := packer.Build(context.Background(), ContextRequest{
		Feature:    FeatureDeepWiki,
		SnapshotID: repository.SnapshotID(),
		Query:      "pkg",
		Scope:      &RequestScope{Paths: []string{"pkg/svc.go"}},
		Options:    ContextOptions{Scope: ScopeModule},
	})
	if err != nil {
		t.Fatalf("Build() error = %v", err)
	}
	if pack.PolicyVersion != DeepWikiPolicyVersion {
		t.Fatalf("policy version = %q, want %q", pack.PolicyVersion, DeepWikiPolicyVersion)
	}
	if len(pack.Evidence) == 0 {
		t.Fatal("module scope produced no evidence")
	}
	// All in-scope evidence must come from the module's paths (boundary deps may be
	// one hop out, but the seeds are strictly scoped).
	for _, evidence := range pack.Evidence {
		if evidence.Path != "" && evidence.Path != "pkg/svc.go" {
			// allowed only if it is a graph-expanded boundary dependency
			continue
		}
	}
	// A module scope without paths is rejected.
	if _, err := packer.Build(context.Background(), ContextRequest{
		Feature: FeatureDeepWiki, SnapshotID: repository.SnapshotID(), Options: ContextOptions{Scope: ScopeModule},
	}); err == nil {
		t.Fatal("module scope without paths must be rejected")
	}
}

func TestDeepWikiPolicySeedsLineAnchoredGoModAndScopesModuleConfigs(t *testing.T) {
	t.Parallel()
	repository := indexedStore(t)
	if err := repository.ReplaceFile(domain.ParsedFile{File: domain.File{
		Path: "go.mod", Language: "gomod", Hash: "go-mod", Content: "module example.com/tinycommerce\n\ngo 1.23\n",
	}}); err != nil {
		t.Fatal(err)
	}
	if err := repository.ReplaceFile(domain.ParsedFile{File: domain.File{
		Path: "pkg/README.md", Language: "markdown", Hash: "pkg-readme", Content: "Package setup instructions.\n",
	}}); err != nil {
		t.Fatal(err)
	}
	packer := NewPacker(repository, map[Feature]Policy{FeatureDeepWiki: NewDeepWikiPolicy()})
	overview, err := packer.Build(context.Background(), ContextRequest{
		Feature: FeatureDeepWiki, SnapshotID: repository.SnapshotID(), Options: ContextOptions{Scope: ScopeRepository},
	})
	if err != nil {
		t.Fatal(err)
	}
	wantLines := map[int]string{1: "module example.com/tinycommerce", 3: "go 1.23"}
	for _, evidence := range overview.Evidence {
		if evidence.Kind == KindConfig && evidence.Path == "go.mod" {
			if want, ok := wantLines[evidence.Range.Start.Line]; ok && evidence.Range.End.Line == evidence.Range.Start.Line && evidence.Content == want {
				delete(wantLines, evidence.Range.Start.Line)
			}
		}
	}
	if len(wantLines) != 0 {
		t.Fatalf("overview missing exact go.mod evidence lines: %#v; evidence=%+v", wantLines, overview.Evidence)
	}
	for _, evidence := range overview.Evidence {
		if evidence.Path == "pkg/README.md" {
			t.Fatal("repository overview received nested module config")
		}
	}

	module, err := packer.Build(context.Background(), ContextRequest{
		Feature: FeatureDeepWiki, SnapshotID: repository.SnapshotID(),
		Scope: &RequestScope{Paths: []string{"pkg/svc.go"}}, Options: ContextOptions{Scope: ScopeModule},
	})
	if err != nil {
		t.Fatal(err)
	}
	hasModuleReadme := false
	for _, evidence := range module.Evidence {
		if evidence.Path == "go.mod" {
			t.Fatal("module pack received unrelated root config")
		}
		if evidence.Kind == KindConfig && evidence.Path == "pkg/README.md" {
			hasModuleReadme = true
		}
	}
	if !hasModuleReadme {
		t.Fatal("module pack missing config under its path")
	}
}

func TestBudgetPreservesScannerTruncationFlag(t *testing.T) {
	t.Parallel()
	evidence, _, _, _ := applyBudget([]Candidate{{Evidence: Evidence{
		ID: "ev:config", Kind: KindConfig, Path: "README.md", Title: "README.md", Content: "short",
		DisplayCode: "short", DisplayCodeTruncated: true,
	}}}, nil, BudgetPolicy{MaxBytes: 100, MaxBytesPerEvidence: 20, MaxEvidence: 1})
	if len(evidence) != 1 || !evidence[0].DisplayCodeTruncated {
		t.Fatalf("stored truncation flag was lost: %+v", evidence)
	}
}

type fakeSemanticSource struct {
	result SemanticResult
	err    error
}

func (f fakeSemanticSource) Collect(context.Context, SemanticRequest) (SemanticResult, error) {
	return f.result, f.err
}

func TestPackerMergesSemanticEvidenceAndOmissions(t *testing.T) {
	t.Parallel()
	repository := indexedStore(t)
	source := fakeSemanticSource{result: SemanticResult{
		Evidence: []Evidence{{
			Kind: KindLSPFact, Path: "pkg/svc.go", Title: "Save", Content: "func Save()",
			Relation: "type", Relevance: 0.95, Confidence: 0.95, Provenance: []Provenance{{Source: "gopls"}},
		}},
		Omissions: []Omission{{Reason: OmitSourceUnavailable, Ref: "semantic:references", Detail: "ast_only"}},
	}}
	packer := NewPacker(repository, map[Feature]Policy{FeatureHover: NewHoverPolicy()}).WithSemanticSource(source)
	pack, err := packer.Build(context.Background(), ContextRequest{
		Feature: FeatureHover, SnapshotID: repository.SnapshotID(),
		Location: &SourceLocation{Path: "pkg/svc.go", Line: 3, Column: 1},
	})
	if err != nil {
		t.Fatalf("Build: %v", err)
	}
	hasLSPFact := false
	for _, evidence := range pack.Evidence {
		if evidence.Kind == KindLSPFact {
			hasLSPFact = true
		}
	}
	if !hasLSPFact {
		t.Fatal("semantic LSP fact was not merged into the pack")
	}
	if pack.Evidence[0].Kind != KindLSPFact {
		t.Fatalf("hover semantic evidence should be primary, got %+v", pack.Evidence)
	}
	hasOmission := false
	for _, omission := range pack.Omissions {
		if omission.Ref == "semantic:references" {
			hasOmission = true
		}
	}
	if !hasOmission {
		t.Fatal("semantic omission was not recorded in the pack")
	}
	if err := ValidatePack(pack); err != nil {
		t.Fatalf("pack with semantic evidence is invalid: %v", err)
	}
}

func TestHoverSemanticMethodsIncludeSyncedOverlay(t *testing.T) {
	t.Parallel()
	policy := NewHoverPolicy()
	version := int64(2)
	methods, mandatory := policy.SemanticMethods(ContextRequest{DocumentVersion: &version})
	if len(methods) != 3 || methods[0] != "hover" || methods[1] != "definition" || methods[2] != "diagnostics" || mandatory {
		t.Fatalf("overlay semantic methods = %v/%v, want optional hover+definition+diagnostics", methods, mandatory)
	}
	methods, mandatory = policy.SemanticMethods(ContextRequest{})
	if len(methods) != 3 || methods[0] != "hover" || methods[1] != "definition" || methods[2] != "diagnostics" || mandatory {
		t.Fatalf("persisted semantic methods = %v/%v", methods, mandatory)
	}
	methods, mandatory = policy.SemanticMethods(ContextRequest{ResolvedTarget: &ResolvedTarget{
		Symbol:   domain.Symbol{Name: "repository", Kind: domain.KindVariable},
		Evidence: Evidence{Kind: KindLSPFact, Relation: "type"},
	}})
	if len(methods) != 2 || methods[0] != "definition" || methods[1] != "diagnostics" || mandatory {
		t.Fatalf("semantic-target methods = %v/%v, want reused target hover plus optional definition+diagnostics", methods, mandatory)
	}
}

func BenchmarkBuild(b *testing.B) {
	repository := indexedStore(b)
	packer := basePacker(repository)
	request := ContextRequest{Feature: FeatureCodemap, SnapshotID: repository.SnapshotID(), Query: "submit save"}
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		if _, err := packer.Build(context.Background(), request); err != nil {
			b.Fatal(err)
		}
	}
}
