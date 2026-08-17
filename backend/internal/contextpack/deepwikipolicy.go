package contextpack

import (
	"context"
	"fmt"
	"sort"
	"strings"

	"github.com/ThalesMMS/CodeAtlas/internal/domain"
	"github.com/ThalesMMS/CodeAtlas/internal/repository"
)

// DeepWikiPolicyVersion is bumped on any incompatible change to the DeepWiki
// retrieval/scoping shape.
const DeepWikiPolicyVersion = "deepwiki.v3"

// DeepWikiPolicy builds a scoped Context Pack for one DeepWiki page. With a module
// scope (Scope.Paths set) it limits evidence to the module's files plus boundary
// dependencies; with repository scope it seeds entrypoints and exported API for
// the overview. Pages are budgeted per page, never as one repo-wide pack.
type DeepWikiPolicy struct{}

func NewDeepWikiPolicy() *DeepWikiPolicy { return &DeepWikiPolicy{} }

func (DeepWikiPolicy) Version() string { return DeepWikiPolicyVersion }

func (DeepWikiPolicy) Validate(request ContextRequest) error {
	if request.Feature != FeatureDeepWiki {
		return fmt.Errorf("deepwiki policy received feature %q", request.Feature)
	}
	if request.Options.Scope == ScopeModule && (request.Scope == nil || len(request.Scope.Paths) == 0) {
		return fmt.Errorf("deepwiki module scope requires scope paths")
	}
	return nil
}

func (DeepWikiPolicy) Expansion() ExpansionPolicy {
	// Depth 1: the module is already the scope; expansion only reaches boundary
	// dependencies one hop out.
	return ExpansionPolicy{MaxDepth: 1, MaxNodes: 40, MaxEdges: 60, EdgeTypes: []string{"calls", "references", "implements", "inherits", "imports", "contains"}}
}

func (DeepWikiPolicy) Budget() BudgetPolicy {
	// Per-page budget reserving room for a useful Markdown answer.
	return BudgetPolicy{MaxBytes: 36000, MaxBytesPerEvidence: 2000, MaxEvidence: 32, MaxNodes: 40, MaxEdges: 60, ReserveBytes: 8000}
}

// Seeds enumerates the pinned view and selects the symbols in scope: a module's
// files/public symbols, or repository entrypoints and exported API for the
// overview. Summaries-first ordering is achieved by Score; the packer budgets.
func (DeepWikiPolicy) Seeds(_ context.Context, view repository.ReadView, request ContextRequest) ([]Candidate, error) {
	all := view.AllSymbols()
	candidates := projectFileSeeds(view.Files(), request)
	if request.Options.Scope == ScopeModule {
		inScope := pathSet(request.Scope.Paths)
		for _, symbol := range all {
			if _, ok := inScope[symbol.Path]; !ok {
				continue
			}
			candidates = append(candidates, Candidate{Evidence: evidenceFromSymbol(symbol, KindASTObservation), Source: "module"})
		}
		return candidates, nil
	}
	// Repository scope (overview): entrypoints + exported, capped for budget.
	for _, symbol := range all {
		if isOverviewSeed(symbol) {
			candidates = append(candidates, Candidate{Evidence: evidenceFromSymbol(symbol, KindASTObservation), Source: "overview"})
		}
	}
	sort.SliceStable(candidates, func(i, j int) bool {
		return overviewRank(candidates[i]) < overviewRank(candidates[j])
	})
	if len(candidates) > 40 {
		candidates = candidates[:40]
	}
	return candidates, nil
}

func pathSet(paths []string) map[string]struct{} {
	set := make(map[string]struct{}, len(paths))
	for _, path := range paths {
		set[path] = struct{}{}
	}
	return set
}

func isOverviewSeed(symbol domain.Symbol) bool {
	name := strings.ToLower(symbol.Name)
	if name == "main" || strings.HasPrefix(name, "new") || strings.HasSuffix(name, "handler") || name == "app" || name == "server" {
		return true
	}
	if symbol.Kind == "file" {
		return true
	}
	// Exported (capitalized) top-level functions/types are public API signal.
	if (symbol.Kind == "function" || symbol.Kind == "type" || symbol.Kind == "class" || symbol.Kind == "interface") && symbol.Name != "" {
		first := symbol.Name[0]
		return first >= 'A' && first <= 'Z'
	}
	return false
}

func overviewRank(candidate Candidate) int {
	name := strings.ToLower(candidate.Evidence.Title)
	switch {
	case strings.HasSuffix(name, "main"), strings.Contains(name, "main"):
		return 0
	case strings.Contains(name, "server"), strings.Contains(name, "app"):
		return 1
	default:
		return 2
	}
}

// Score keeps the target/public API and observed flow ahead of files and imports
// so summaries-then-flow ordering survives the budget.
func (DeepWikiPolicy) Score(candidate Candidate, _ ContextRequest) ScoreBreakdown {
	breakdown := ScoreBreakdown{}
	switch candidate.Evidence.Kind {
	case KindASTObservation:
		breakdown.TargetBoost = 1.0
	case KindConfig:
		breakdown.TargetBoost = 1.4
	}
	switch candidate.Evidence.Relation {
	case "calls", "called_by", "implements", "references":
		breakdown.RelationBoost = 0.5
	case "imports":
		breakdown.RelationBoost = 0.1
	}
	if candidate.Distance > 0 {
		breakdown.GraphBoost = 0.2 / float64(candidate.Distance)
		breakdown.Penalty += 0.3 // boundary deps rank below in-scope symbols
	}
	breakdown.Total = breakdown.TargetBoost + breakdown.RelationBoost + breakdown.GraphBoost - breakdown.Penalty
	return breakdown
}

// SemanticMethods requests definitions/implementations (API/contracts) and a
// little call hierarchy (entrypoints/flows) per module. Optional; coverage is
// recorded as an omission when unavailable.
func (DeepWikiPolicy) SemanticMethods(ContextRequest) (methods []string, mandatory bool) {
	return []string{"definition", "implementation", "incomingCalls"}, false
}
