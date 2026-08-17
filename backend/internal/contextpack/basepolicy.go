package contextpack

import (
	"context"
	"strings"

	"github.com/ThalesMMS/CodeAtlas/internal/domain"
	"github.com/ThalesMMS/CodeAtlas/internal/repository"
)

// BasePolicy is the deterministic default retrieval policy: an explicit target
// (by SymbolID or location) plus lexical seeds, one level of graph expansion over
// calls/contains/imports, scored by target/graph boosts with file/import
// penalties. Feature-specific policies (#29–#32) specialize it.
type BasePolicy struct {
	version   string
	expansion ExpansionPolicy
	budget    BudgetPolicy
}

// NewBasePolicy returns the versioned base policy.
func NewBasePolicy() *BasePolicy {
	return &BasePolicy{
		version: "base.v1",
		expansion: ExpansionPolicy{
			MaxDepth: 1, MaxNodes: 30, MaxEdges: 60,
			EdgeTypes: []string{"calls", "contains", "imports", "inherits", "implements"},
		},
		budget: BudgetPolicy{
			MaxBytes: 16000, MaxBytesPerEvidence: 2000, MaxEvidence: 20,
			MaxNodes: 30, MaxEdges: 60, ReserveBytes: 2000,
		},
	}
}

func (p *BasePolicy) Version() string               { return p.version }
func (p *BasePolicy) Validate(ContextRequest) error { return nil }
func (p *BasePolicy) Expansion() ExpansionPolicy    { return p.expansion }
func (p *BasePolicy) Budget() BudgetPolicy          { return p.budget }

// Seeds collects the explicit target (when present) and lexical hits, recording
// the lexical rank for reciprocal-rank fusion.
func (p *BasePolicy) Seeds(_ context.Context, view repository.ReadView, request ContextRequest) ([]Candidate, error) {
	candidates := make([]Candidate, 0)
	if request.Location != nil {
		if request.Location.SymbolID != "" {
			if symbol, ok := view.GetSymbol(string(request.Location.SymbolID)); ok {
				candidates = append(candidates, Candidate{Evidence: evidenceFromSymbol(symbol, KindASTObservation), IsTarget: true, Source: "target"})
			}
		} else if request.Location.Path != "" {
			if resolved, ok := view.SymbolAt(request.Location.Path, request.Location.Line, request.Location.Column); ok {
				candidates = append(candidates, Candidate{Evidence: evidenceFromSymbol(resolved.ToSymbol(), KindASTObservation), IsTarget: true, Source: "target"})
			}
		}
	}
	if query := strings.TrimSpace(request.Query); query != "" {
		hits, err := view.Search(query, 20)
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
	}
	return candidates, nil
}

// Score applies the documented boosts and penalties. Only the total orders the
// candidates; the breakdown is for debugging.
func (p *BasePolicy) Score(candidate Candidate, _ ContextRequest) ScoreBreakdown {
	breakdown := ScoreBreakdown{}
	if candidate.IsTarget {
		breakdown.TargetBoost = 1.0
	}
	if candidate.Distance > 0 {
		breakdown.GraphBoost = 0.2 / float64(candidate.Distance) // closer neighbors rank higher
	}
	if candidate.Evidence.Kind == KindASTObservation {
		switch kindOf(candidate.Evidence) {
		case domain.KindFile, domain.KindImport:
			breakdown.Penalty += 0.15 // file/import symbols rarely add context on their own
		}
	}
	if candidate.Evidence.Confidence < 0.3 {
		breakdown.Penalty += 0.1
	}
	breakdown.Total = breakdown.TargetBoost + breakdown.GraphBoost - breakdown.Penalty
	return breakdown
}

// kindOf infers a coarse symbol kind from the evidence title for the base
// penalties (the flat evidence does not carry kind directly).
func kindOf(evidence Evidence) string {
	title := baseEvidenceTitle(evidence.Title)
	if strings.HasPrefix(title, "import ") {
		return domain.KindImport
	}
	if strings.HasSuffix(evidence.Path, title) && evidence.SymbolID != "" {
		return domain.KindFile
	}
	return ""
}
