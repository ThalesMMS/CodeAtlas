package contextpack

import (
	"context"
	"fmt"
	"strings"

	"github.com/ThalesMMS/CodeAtlas/internal/repository"
)

// CodemapPolicyVersion is bumped on any incompatible change to the codemap
// retrieval/subgraph shape.
const CodemapPolicyVersion = "codemap.v2"

// CodemapPolicy builds the task-specific subgraph for a Codemap: lexical seeds
// from the query (plus an explicit selected symbol / current-file boost passed in
// the request), expanded over flow edges. The model later narrates only over this
// validated structure — it never invents nodes or edges.
type CodemapPolicy struct{}

func NewCodemapPolicy() *CodemapPolicy { return &CodemapPolicy{} }

func (CodemapPolicy) Version() string { return CodemapPolicyVersion }

func (CodemapPolicy) Validate(request ContextRequest) error {
	if request.Feature != FeatureCodemap {
		return fmt.Errorf("codemap policy received feature %q", request.Feature)
	}
	if strings.TrimSpace(request.Query) == "" {
		return fmt.Errorf("codemap requires a non-empty query")
	}
	return nil
}

func (CodemapPolicy) Expansion() ExpansionPolicy {
	// Flow edges drive the map; imports/contains are support/location only.
	return ExpansionPolicy{MaxDepth: 2, MaxNodes: 40, MaxEdges: 80, EdgeTypes: []string{"calls", "references", "implements", "inherits", "tests", "configures", "imports", "contains"}}
}

func (CodemapPolicy) Budget() BudgetPolicy {
	return BudgetPolicy{MaxBytes: 40000, MaxBytesPerEvidence: 1600, MaxEvidence: 40, MaxNodes: 40, MaxEdges: 80, ReserveBytes: 6000}
}

// Seeds collects lexical hits for the query and boosts an explicitly selected
// symbol (passed via Location.SymbolID). It preserves each seed's lexical rank
// for reciprocal-rank fusion.
func (CodemapPolicy) Seeds(_ context.Context, view repository.ReadView, request ContextRequest) ([]Candidate, error) {
	candidates := make([]Candidate, 0)
	if request.Location != nil && request.Location.SymbolID != "" {
		if symbol, ok := view.GetSymbol(string(request.Location.SymbolID)); ok {
			candidates = append(candidates, Candidate{Evidence: evidenceFromSymbol(symbol, KindASTObservation), IsTarget: true, Source: "selected"})
		}
	}
	hits, err := view.Search(request.Query, 12)
	if err != nil {
		return nil, err
	}
	for i, hit := range hits {
		candidates = append(candidates, Candidate{
			Evidence:    evidenceFromSymbol(hit.Symbol, KindASTObservation),
			LexicalRank: i + 1,
			Source:      "lexical",
		})
	}
	return candidates, nil
}

// Score boosts selected seeds and flow relations; imports/contains are support
// only, file/import symbols are penalized as standalone nodes.
func (CodemapPolicy) Score(candidate Candidate, _ ContextRequest) ScoreBreakdown {
	breakdown := ScoreBreakdown{}
	if candidate.IsTarget {
		breakdown.TargetBoost = 1.5
	}
	switch candidate.Evidence.Relation {
	case "calls", "called_by":
		breakdown.RelationBoost = 0.8
	case "implements", "references":
		breakdown.RelationBoost = 0.6
	case "parent", "child":
		breakdown.RelationBoost = 0.3
	case "imports":
		breakdown.RelationBoost = 0.1 // support/location only
	}
	if candidate.Distance > 0 {
		breakdown.GraphBoost = 0.2 / float64(candidate.Distance)
	}
	breakdown.Total = breakdown.TargetBoost + breakdown.RelationBoost + breakdown.GraphBoost - breakdown.Penalty
	return breakdown
}

// SemanticMethods requests resolved call hierarchy and implementations to enrich
// the factual subgraph (gopls calls outrank heuristic AST calls via Fusion;
// references never become call edges). Optional.
func (CodemapPolicy) SemanticMethods(ContextRequest) (methods []string, mandatory bool) {
	return []string{"incomingCalls", "outgoingCalls", "implementation"}, false
}
