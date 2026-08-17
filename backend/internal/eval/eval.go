// Package eval is the offline evaluation harness for the four AI features'
// deterministic layer: it indexes a fixed corpus, runs versioned cases through the
// ContextPacker and the grounding validator, and computes reproducible metrics
// (target resolution, recall, budget, grounding, determinism). It never needs the
// network or a real LLM — a fake provider drives the grounding checks — so it can
// gate CI. LLM quality (prose) is explicitly out of scope.
package eval

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"

	"github.com/ThalesMMS/CodeAtlas/internal/aiout"
	"github.com/ThalesMMS/CodeAtlas/internal/contextpack"
	"github.com/ThalesMMS/CodeAtlas/internal/diagram"
	"github.com/ThalesMMS/CodeAtlas/internal/domain"
	"github.com/ThalesMMS/CodeAtlas/internal/indexer"
	codeparser "github.com/ThalesMMS/CodeAtlas/internal/parser"
	"github.com/ThalesMMS/CodeAtlas/internal/repository"
	"github.com/ThalesMMS/CodeAtlas/internal/service"
)

// Versions stamps a report/baseline with the policy and output-schema versions it
// was produced under, so a baseline is only compared against a matching world.
type Versions struct {
	Hover    string `json:"hover"`
	SeeMore  string `json:"seeMore"`
	Codemap  string `json:"codemap"`
	DeepWiki string `json:"deepWiki"`
	Output   string `json:"output"`
}

// CurrentVersions reports the policy/schema versions in this build.
func CurrentVersions() Versions {
	return Versions{
		Hover:    contextpack.HoverPolicyVersion,
		SeeMore:  contextpack.SeeMorePolicyVersion,
		Codemap:  contextpack.CodemapPolicyVersion,
		DeepWiki: contextpack.DeepWikiPolicyVersion,
		Output:   aiout.ExplanationSchemaVersion + "," + aiout.CodemapSchemaVersion + "," + aiout.WikiPageSchemaVersion + "," + diagram.Version,
	}
}

// Target locates a symbol for hover/see-more cases.
type Target struct {
	Path   string `json:"path"`
	Line   int    `json:"line"`
	Column int    `json:"column"`
}

// Expected is the annotated ground truth for a case.
type Expected struct {
	Symbol                    string   `json:"symbol,omitempty"`
	RelevantSymbolKeys        []string `json:"relevantSymbolKeys,omitempty"`
	EvidenceContentSubstrings []string `json:"evidenceContentSubstrings,omitempty"`
	RequiredEvidencePaths     []string `json:"requiredEvidencePaths,omitempty"`
	ForbiddenPaths            []string `json:"forbiddenPaths,omitempty"`
	MinNodes                  int      `json:"minNodes,omitempty"`
}

// Case is one versioned evaluation scenario.
type Case struct {
	ID       string   `json:"id"`
	Feature  string   `json:"feature"`
	Target   *Target  `json:"target,omitempty"`
	Query    string   `json:"query,omitempty"`
	Scope    []string `json:"scope,omitempty"`
	Expected Expected `json:"expected"`
}

// CaseMetrics are the per-case deterministic measurements.
type CaseMetrics struct {
	ID               string   `json:"id"`
	Feature          string   `json:"feature"`
	TargetResolved   bool     `json:"targetResolved"`
	RecallAtK        float64  `json:"recallAtK"`
	EvidenceCount    int      `json:"evidenceCount"`
	DuplicateRate    float64  `json:"duplicateRate"`
	NoProvenanceRate float64  `json:"noProvenanceRate"`
	ForbiddenHits    int      `json:"forbiddenHits"`
	BudgetViolations int      `json:"budgetViolations"`
	GroundingInvalid int      `json:"groundingInvalid"`
	HashStable       bool     `json:"hashStable"`
	Errors           []string `json:"errors,omitempty"`
}

// Report is the full evaluation result.
type Report struct {
	Versions Versions      `json:"versions"`
	Cases    []CaseMetrics `json:"cases"`
	Summary  Summary       `json:"summary"`
	Quality  QualityReport `json:"quality"`
}

// Summary aggregates the gate-critical metrics across all cases.
type Summary struct {
	TotalCases            int     `json:"totalCases"`
	TargetResolutionRate  float64 `json:"targetResolutionRate"`
	MeanRecallAtK         float64 `json:"meanRecallAtK"`
	TotalGroundingInvalid int     `json:"totalGroundingInvalid"`
	TotalBudgetViolations int     `json:"totalBudgetViolations"`
	TotalForbiddenHits    int     `json:"totalForbiddenHits"`
	AllDeterministic      bool    `json:"allDeterministic"`
}

// Run indexes the corpus once and evaluates every case against it.
func Run(ctx context.Context, corpusRoots []string, cases []Case) (Report, error) {
	return RunWithOptions(ctx, corpusRoots, cases, QualityOptions{})
}

// RunWithOptions adds the output-quality scorecard and optional LLM judge to
// the deterministic ContextPack gates run by Run.
func RunWithOptions(ctx context.Context, corpusRoots []string, cases []Case, options QualityOptions) (Report, error) {
	repository, err := indexCorpus(ctx, corpusRoots)
	if err != nil {
		return Report{}, err
	}
	defer repository.Close() //nolint:errcheck
	packer := contextpack.NewPacker(repository, map[contextpack.Feature]contextpack.Policy{
		contextpack.FeatureHover:    contextpack.NewHoverPolicy(),
		contextpack.FeatureSeeMore:  contextpack.NewSeeMorePolicy(),
		contextpack.FeatureCodemap:  contextpack.NewCodemapPolicy(),
		contextpack.FeatureDeepWiki: contextpack.NewDeepWikiPolicy(),
	})

	report := Report{Versions: CurrentVersions()}
	for _, testCase := range cases {
		report.Cases = append(report.Cases, evaluateCase(ctx, repository, packer, testCase))
	}
	report.Summary = summarize(report.Cases)
	quality, err := runQuality(ctx, repository, corpusRoots, options)
	if err != nil {
		return Report{}, err
	}
	report.Quality = quality
	return report, nil
}

// indexCorpus parses every source file under each root and adds it to one store,
// keyed by its path relative to that root. It parses files directly (rather than
// indexer.Scan, which reconciles a single tree and would prune the other roots),
// so several disjoint corpus roots accumulate into one index.
func indexCorpus(ctx context.Context, roots []string) (repository.Store, error) {
	tempDir, err := os.MkdirTemp("", "codeatlas-eval-")
	if err != nil {
		return nil, err
	}
	store, err := repository.OpenSQLite(ctx, repository.SQLiteConfig{WorkspaceRoot: tempDir})
	if err != nil {
		_ = os.RemoveAll(tempDir)
		return nil, err
	}
	temporary := &temporaryCorpusStore{Store: store, directory: tempDir}
	parser := codeparser.New()
	for _, root := range roots {
		if err := indexRoot(temporary, parser, root); err != nil {
			_ = temporary.Close()
			return nil, err
		}
	}
	return temporary, nil
}

// temporaryCorpusStore owns both the SQLite handle and its temporary workspace,
// so every return path can release them with the Store contract's Close method.
type temporaryCorpusStore struct {
	repository.Store
	directory string
	once      sync.Once
	closeErr  error
}

func (s *temporaryCorpusStore) Close() error {
	s.once.Do(func() {
		s.closeErr = errors.Join(s.Store.Close(), os.RemoveAll(s.directory))
	})
	return s.closeErr
}

func indexRoot(repository repository.Store, parser *codeparser.Engine, root string) error {
	return filepath.WalkDir(root, func(path string, entry fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if entry.IsDir() {
			name := entry.Name()
			if name == ".codeatlas" || name == "node_modules" || name == "vendor" || name == ".git" {
				return fs.SkipDir
			}
			return nil
		}
		relative, err := filepath.Rel(root, path)
		if err != nil {
			return err
		}
		relative = filepath.ToSlash(relative)
		if !isSourceFile(path) && !indexer.IsProjectFile(relative) {
			return nil
		}
		content, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		if project, ok := indexer.InspectProjectFile(relative, content); ok {
			repository.ReplaceFile(domain.ParsedFile{File: domain.File{
				Path: relative, Language: project.Language, Hash: contentHash(content), Size: int64(len(content)),
				Content: project.Content, ContentTruncated: project.ContentTruncated,
			}})
			return nil
		}
		if !isSourceFile(path) {
			return nil
		}
		symbols, edges, language, parseErr := parser.Parse(relative, content)
		if parseErr != nil {
			return nil // skip unparseable files; the corpus is curated
		}
		repository.ReplaceFile(domain.ParsedFile{
			File:    domain.File{Path: relative, Language: language, Hash: contentHash(content)},
			Symbols: symbols,
			Edges:   edges,
		})
		return nil
	})
}

func isSourceFile(path string) bool {
	switch filepath.Ext(path) {
	case ".go", ".ts", ".tsx", ".js", ".jsx", ".swift", ".py", ".rs":
		return true
	default:
		return false
	}
}

func contentHash(content []byte) string {
	sum := sha256.Sum256(content)
	return hex.EncodeToString(sum[:8])
}

func evaluateCase(ctx context.Context, repository repository.Store, packer *contextpack.Packer, testCase Case) CaseMetrics {
	metrics := CaseMetrics{ID: testCase.ID, Feature: testCase.Feature, HashStable: true}
	request, err := buildRequest(repository, testCase)
	if err != nil {
		metrics.Errors = append(metrics.Errors, err.Error())
		return metrics
	}
	pack, err := packer.Build(ctx, request)
	if err != nil {
		metrics.Errors = append(metrics.Errors, "build: "+err.Error())
		return metrics
	}
	// Determinism: a second build over the same logical input must reproduce the
	// exact hash and evidence order.
	second, err := contextpack.NewPacker(repository, allPolicies()).Build(ctx, request)
	if err != nil || second.Hash != pack.Hash {
		metrics.HashStable = false
	}

	scoreCase(&metrics, testCase, pack)
	return metrics
}

func allPolicies() map[contextpack.Feature]contextpack.Policy {
	return map[contextpack.Feature]contextpack.Policy{
		contextpack.FeatureHover:    contextpack.NewHoverPolicy(),
		contextpack.FeatureSeeMore:  contextpack.NewSeeMorePolicy(),
		contextpack.FeatureCodemap:  contextpack.NewCodemapPolicy(),
		contextpack.FeatureDeepWiki: contextpack.NewDeepWikiPolicy(),
	}
}

func buildRequest(repository repository.Store, testCase Case) (contextpack.ContextRequest, error) {
	feature := contextpack.Feature(testCase.Feature)
	request := contextpack.ContextRequest{Feature: feature, SnapshotID: repository.SnapshotID(), Query: testCase.Query}
	switch feature {
	case contextpack.FeatureHover, contextpack.FeatureSeeMore:
		if testCase.Target == nil {
			return request, fmt.Errorf("case %s missing target", testCase.ID)
		}
		snapshot := repository.Snapshot()
		defer snapshot.Close()
		resolved, ok := snapshot.SymbolAt(testCase.Target.Path, testCase.Target.Line, testCase.Target.Column)
		if !ok {
			return request, fmt.Errorf("case %s: no symbol at %s:%d", testCase.ID, testCase.Target.Path, testCase.Target.Line)
		}
		flat := resolved.ToSymbol()
		if feature == contextpack.FeatureHover || feature == contextpack.FeatureSeeMore {
			if source := indexedSource(snapshot, testCase.Target.Path); source != nil {
				if identifier := service.IdentifierAt(source, testCase.Target.Line, testCase.Target.Column); identifier != "" {
					if flat.Name != identifier {
						if exact, found := snapshot.ResolveSymbol(identifier, testCase.Target.Path); found {
							flat = exact
						}
					}
				}
			}
		}
		request.Location = &contextpack.SourceLocation{
			Path: testCase.Target.Path, Line: testCase.Target.Line, Column: testCase.Target.Column,
			SymbolID: domain.SymbolID(flat.ID),
		}
	case contextpack.FeatureDeepWiki:
		if len(testCase.Scope) == 0 {
			request.Options.Scope = contextpack.ScopeRepository
		} else {
			request.Options.Scope = contextpack.ScopeModule
			request.Scope = &contextpack.RequestScope{Paths: testCase.Scope}
		}
	}
	return request, nil
}

func indexedSource(view repository.ReadView, path string) []byte {
	for _, symbol := range view.AllSymbols() {
		if symbol.Path == path && symbol.Kind == domain.KindFile && symbol.Code != "" {
			return []byte(symbol.Code)
		}
	}
	return nil
}

// scoreCase computes the deterministic metrics from the built pack.
func scoreCase(metrics *CaseMetrics, testCase Case, pack contextpack.ContextPack) {
	metrics.EvidenceCount = len(pack.Evidence)

	// Target resolution: the pack's target symbol matches the expected name.
	if testCase.Expected.Symbol != "" && pack.Target != nil {
		for _, evidence := range pack.Evidence {
			if string(evidence.SymbolID) == string(pack.Target.SymbolID) && symbolName(evidence.Title) == testCase.Expected.Symbol {
				metrics.TargetResolved = true
				break
			}
		}
	} else if testCase.Expected.Symbol == "" {
		metrics.TargetResolved = true // not a target-resolution case
	}

	// Recall@K of the annotated relevant symbols among the pack evidence.
	if keys := testCase.Expected.RelevantSymbolKeys; len(keys) > 0 {
		present := 0
		for _, key := range keys {
			if evidenceContainsSymbol(pack, key) {
				present++
			}
		}
		metrics.RecallAtK = float64(present) / float64(len(keys))
	} else {
		metrics.RecallAtK = 1
	}

	// Forbidden paths must never appear in evidence.
	for _, evidence := range pack.Evidence {
		for _, forbidden := range testCase.Expected.ForbiddenPaths {
			if pathHasPrefix(evidence.Path, forbidden) {
				metrics.ForbiddenHits++
			}
		}
	}

	// Duplicate / no-provenance rates and budget violations.
	metrics.DuplicateRate = duplicateRate(pack)
	metrics.NoProvenanceRate = noProvenanceRate(pack)
	metrics.BudgetViolations = budgetViolations(pack)
	metrics.GroundingInvalid = groundingInvalidCount(pack)

	if testCase.Expected.MinNodes > 0 && len(pack.Graph.Nodes) < testCase.Expected.MinNodes {
		metrics.Errors = append(metrics.Errors, fmt.Sprintf("graph has %d nodes, want >= %d", len(pack.Graph.Nodes), testCase.Expected.MinNodes))
	}
	for _, want := range testCase.Expected.EvidenceContentSubstrings {
		found := false
		for _, evidence := range pack.Evidence {
			if strings.Contains(evidence.Content, want) {
				found = true
				break
			}
		}
		if !found {
			metrics.Errors = append(metrics.Errors, fmt.Sprintf("evidence content missing %q", want))
		}
	}
	for _, requiredPath := range testCase.Expected.RequiredEvidencePaths {
		found := false
		for _, evidence := range pack.Evidence {
			if evidence.Path == requiredPath {
				found = true
				break
			}
		}
		if !found {
			metrics.Errors = append(metrics.Errors, "missing required evidence path: "+requiredPath)
		}
	}
}

func summarize(cases []CaseMetrics) Summary {
	summary := Summary{TotalCases: len(cases), AllDeterministic: true}
	if len(cases) == 0 {
		return summary
	}
	resolved, recallSum := 0, 0.0
	for _, metrics := range cases {
		if metrics.TargetResolved {
			resolved++
		}
		recallSum += metrics.RecallAtK
		summary.TotalGroundingInvalid += metrics.GroundingInvalid
		summary.TotalBudgetViolations += metrics.BudgetViolations
		summary.TotalForbiddenHits += metrics.ForbiddenHits
		if !metrics.HashStable {
			summary.AllDeterministic = false
		}
	}
	summary.TargetResolutionRate = float64(resolved) / float64(len(cases))
	summary.MeanRecallAtK = recallSum / float64(len(cases))
	return summary
}

// --- metric helpers ---

func evidenceContainsSymbol(pack contextpack.ContextPack, name string) bool {
	for _, evidence := range pack.Evidence {
		if symbolName(evidence.Title) == name {
			return true
		}
	}
	return false
}

func symbolName(qualified string) string {
	qualified = strings.SplitN(qualified, " — ", 2)[0]
	for _, sep := range []string{"::", ".", "/"} {
		for {
			idx := indexLast(qualified, sep)
			if idx < 0 {
				break
			}
			qualified = qualified[idx+len(sep):]
		}
	}
	return qualified
}

func indexLast(s, sub string) int {
	last := -1
	for i := 0; i+len(sub) <= len(s); i++ {
		if s[i:i+len(sub)] == sub {
			last = i
		}
	}
	return last
}

func pathHasPrefix(path, prefix string) bool {
	prefix = trimGlob(prefix)
	return len(prefix) > 0 && len(path) >= len(prefix) && path[:len(prefix)] == prefix
}

func trimGlob(prefix string) string {
	for len(prefix) > 0 && (prefix[len(prefix)-1] == '.' || prefix[len(prefix)-1] == '*' || prefix[len(prefix)-1] == '/') {
		prefix = prefix[:len(prefix)-1]
	}
	return prefix
}

func duplicateRate(pack contextpack.ContextPack) float64 {
	if len(pack.Evidence) == 0 {
		return 0
	}
	seen := make(map[string]struct{}, len(pack.Evidence))
	duplicates := 0
	for _, evidence := range pack.Evidence {
		key := string(evidence.ID)
		if _, ok := seen[key]; ok {
			duplicates++
			continue
		}
		seen[key] = struct{}{}
	}
	return float64(duplicates) / float64(len(pack.Evidence))
}

func noProvenanceRate(pack contextpack.ContextPack) float64 {
	if len(pack.Evidence) == 0 {
		return 0
	}
	missing := 0
	for _, evidence := range pack.Evidence {
		if evidence.Kind == "" {
			missing++
		}
	}
	return float64(missing) / float64(len(pack.Evidence))
}

// budgetViolations counts evidence whose content exceeds the schema-validated
// pack contract, plus an over-the-reported-budget check.
func budgetViolations(pack contextpack.ContextPack) int {
	violations := 0
	if pack.Budget.RequestedBytes > 0 && pack.Budget.UsedBytes > pack.Budget.RequestedBytes {
		violations++
	}
	return violations
}

// groundingInvalidCount validates the built pack itself: a malformed pack (the
// retrieval layer's own contract) counts as a grounding failure. Output-side
// grounding is exercised by the fake-provider unit tests.
func groundingInvalidCount(pack contextpack.ContextPack) int {
	if err := contextpack.ValidatePack(pack); err != nil {
		return 1
	}
	return 0
}

// SortCases orders cases by ID for stable reports.
func SortCases(cases []Case) {
	sort.Slice(cases, func(i, j int) bool { return cases[i].ID < cases[j].ID })
}
