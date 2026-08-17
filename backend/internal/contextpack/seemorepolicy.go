package contextpack

import (
	"context"
	"fmt"
	"path"
	"sort"
	"strings"

	"github.com/ThalesMMS/CodeAtlas/internal/domain"
	"github.com/ThalesMMS/CodeAtlas/internal/repository"
)

// SeeMorePolicyVersion is bumped on any incompatible change to the See More
// retrieval shape or defaults.
const SeeMorePolicyVersion = "see_more.v3"

// maxSemanticCandidates caps how many structurally-unrelated (semantic) seeds may
// enter the budget, so retrieval never drowns observed relations.
const maxSemanticCandidates = 4

// Target + usage consume two of the 21 evidence slots, leaving room to reserve
// at least one defining symbol from up to 19 package files.
const maxDefinitionSeeds = 19

// SeeMorePolicy is the deeper, flow-and-change-impact policy. Unlike Hover it
// expands two levels, reserves categories for callers/downstream/tests/configs,
// and may add a bounded number of semantic candidates (tests/configs found by an
// auxiliary lexical query derived deterministically from the target), clearly
// marked and lower-confidence. Sources not yet implemented (Git diff, LSP
// diagnostics) are declared as source_unavailable omissions, never invented.
type SeeMorePolicy struct{}

func NewSeeMorePolicy() *SeeMorePolicy { return &SeeMorePolicy{} }

func (SeeMorePolicy) Version() string { return SeeMorePolicyVersion }

func (SeeMorePolicy) Validate(request ContextRequest) error {
	if request.Feature != FeatureSeeMore {
		return fmt.Errorf("see-more policy received feature %q", request.Feature)
	}
	if request.Location == nil || (request.Location.Path == "" && request.Location.SymbolID == "") {
		return fmt.Errorf("see-more requires a location (path+line+column or symbolId)")
	}
	return nil
}

func (SeeMorePolicy) Expansion() ExpansionPolicy {
	return ExpansionPolicy{MaxDepth: 2, MaxNodes: 24, MaxEdges: 40, EdgeTypes: []string{"calls", "references", "implements", "inherits", "tests", "configures", "imports"}}
}

func (SeeMorePolicy) Budget() BudgetPolicy {
	// Substantially larger than Hover, reserving room for a structured answer.
	return BudgetPolicy{MaxBytes: 28000, MaxBytesPerEvidence: 3200, MaxEvidence: 21, MaxNodes: 24, MaxEdges: 40, ReserveBytes: 6000}
}

// Seeds resolves the target, then adds a bounded set of semantic candidates from
// an auxiliary lexical query derived deterministically from the target (never
// from LLM prose). Semantic candidates are marked and lower-confidence.
func (SeeMorePolicy) Seeds(_ context.Context, view repository.ReadView, request ContextRequest) ([]Candidate, error) {
	var target Candidate
	var targetSymbol domain.Symbol
	if resolved, resolvedSymbol, ok := resolvedTargetCandidate(request); ok {
		target = resolved
		targetSymbol = resolvedSymbol
	} else if request.Location.SymbolID != "" {
		symbol, ok := view.GetSymbol(string(request.Location.SymbolID))
		if !ok {
			return nil, fmt.Errorf("symbol not found")
		}
		targetSymbol = symbol
		target = Candidate{Evidence: evidenceFromSymbol(symbol, KindASTObservation), IsTarget: true, Source: "target"}
	} else {
		resolved, ok := view.SymbolAt(request.Location.Path, request.Location.Line, request.Location.Column)
		if !ok {
			return nil, fmt.Errorf("no symbol at position")
		}
		targetSymbol = resolved.ToSymbol()
		target = Candidate{Evidence: evidenceFromSymbol(targetSymbol, KindASTObservation), IsTarget: true, Source: "target"}
	}

	candidates := []Candidate{target}
	if usage, ok := hoverUsageEvidence(view, request.Location, targetSymbol); ok {
		candidates = append(candidates, Candidate{Evidence: usage, Source: RelationUsageSite})
	}
	candidates = append(candidates, definitionFirstSeeds(view, targetSymbol)...)
	query := auxiliaryQuery(target.Evidence)
	if query == "" {
		return candidates, nil
	}
	added := 0
	hits, err := view.Search(query, 12)
	if err != nil {
		return nil, err
	}
	for _, hit := range hits {
		if added >= maxSemanticCandidates {
			break
		}
		if string(hit.Symbol.ID) == string(target.Evidence.SymbolID) {
			continue
		}
		evidence := evidenceFromSymbol(hit.Symbol, KindRetrievedInference)
		evidence.Confidence = 0.35 // semantic candidates carry lower confidence
		evidence.Provenance = []Provenance{{Source: "retrieval", Detail: "auxiliary lexical"}}
		candidates = append(candidates, Candidate{Evidence: evidence, LexicalRank: added + 1, Source: "semantic_candidate"})
		added++
	}
	return candidates, nil
}

func definitionFirstSeeds(view repository.ReadView, target domain.Symbol) []Candidate {
	stored, ok := view.GetSymbol(target.ID)
	if ok {
		target = stored
	}
	switch target.Kind {
	case domain.KindImport, domain.KindPackage:
		return packageDefinitionSeeds(view, target)
	case domain.KindType, domain.KindClass, domain.KindInterface:
		return typeDefinitionSeeds(view, target)
	default:
		return nil
	}
}

type definitionSeed struct {
	priority int
	exported bool
	symbol   domain.Symbol
}

func packageDefinitionSeeds(view repository.ReadView, target domain.Symbol) []Candidate {
	importPath := importPathOf(target)
	if target.Kind == domain.KindPackage && target.QualifiedName != "" {
		importPath = target.QualifiedName
	}
	packageDir := workspacePackageDir(view.Files(), importPath)
	if packageDir == "" {
		return nil
	}
	grouped := make(map[string][]definitionSeed)
	for _, symbol := range view.AllSymbols() {
		if path.Dir(path.Clean(symbol.Path)) != packageDir || !seeMoreDefinitionSymbol(symbol) {
			continue
		}
		exported := symbol.Kind == domain.KindTest || hoverPublicSymbol(symbol)
		priority := seeMoreDefinitionPriority(symbol)
		if !exported {
			priority += 10
		}
		grouped[symbol.Path] = append(grouped[symbol.Path], definitionSeed{priority: priority, exported: exported, symbol: symbol})
	}
	paths := make([]string, 0, len(grouped))
	for symbolPath := range grouped {
		paths = append(paths, symbolPath)
		sortDefinitionSeeds(grouped[symbolPath])
	}
	sort.Strings(paths)

	// First pass guarantees one concrete declaration from every package file.
	ordered := make([]definitionSeed, 0, maxDefinitionSeeds)
	for _, symbolPath := range paths {
		ordered = append(ordered, grouped[symbolPath][0])
	}
	// Then add the remaining key components in semantic priority order.
	remaining := make([]definitionSeed, 0)
	for _, symbolPath := range paths {
		for _, seed := range grouped[symbolPath][1:] {
			if seed.exported {
				remaining = append(remaining, seed)
			}
		}
	}
	sortDefinitionSeeds(remaining)
	ordered = append(ordered, remaining...)
	if len(ordered) > maxDefinitionSeeds {
		ordered = ordered[:maxDefinitionSeeds]
	}
	return definitionCandidates(ordered)
}

func typeDefinitionSeeds(view repository.ReadView, target domain.Symbol) []Candidate {
	prefixes := []string{target.QualifiedName + ".", target.Name + ".", "::" + target.Name + "."}
	seeds := make([]definitionSeed, 0)
	for _, symbol := range view.AllSymbols() {
		if symbol.ID == target.ID || path.Dir(path.Clean(symbol.Path)) != path.Dir(path.Clean(target.Path)) {
			continue
		}
		for _, prefix := range prefixes {
			if prefix != "." && strings.Contains(symbol.QualifiedName, prefix) {
				exported := hoverPublicSymbol(symbol)
				priority := seeMoreDefinitionPriority(symbol)
				if !exported {
					priority += 10
				}
				seeds = append(seeds, definitionSeed{priority: priority, exported: exported, symbol: symbol})
				break
			}
		}
	}
	sortDefinitionSeeds(seeds)
	if len(seeds) > maxDefinitionSeeds {
		seeds = seeds[:maxDefinitionSeeds]
	}
	return definitionCandidates(seeds)
}

func definitionCandidates(seeds []definitionSeed) []Candidate {
	result := make([]Candidate, 0, len(seeds))
	for _, seed := range seeds {
		kind, relation := KindASTObservation, "definition"
		if seed.symbol.Kind == domain.KindTest {
			kind, relation = KindTest, "tests"
		}
		evidence := evidenceFromSymbol(seed.symbol, kind)
		evidence.Relation = relation
		evidence.Confidence = 1
		evidence.Provenance = []Provenance{{Source: "ast", Detail: "definition_first"}}
		result = append(result, Candidate{Evidence: evidence, Source: relation})
	}
	return result
}

func sortDefinitionSeeds(seeds []definitionSeed) {
	sort.Slice(seeds, func(i, j int) bool {
		if seeds[i].priority != seeds[j].priority {
			return seeds[i].priority < seeds[j].priority
		}
		if seeds[i].symbol.Path != seeds[j].symbol.Path {
			return seeds[i].symbol.Path < seeds[j].symbol.Path
		}
		if seeds[i].symbol.Name != seeds[j].symbol.Name {
			return seeds[i].symbol.Name < seeds[j].symbol.Name
		}
		return seeds[i].symbol.ID < seeds[j].symbol.ID
	})
}

func seeMoreDefinitionSymbol(symbol domain.Symbol) bool {
	return symbol.Kind != domain.KindFile && symbol.Kind != domain.KindImport && symbol.Name != ""
}

func seeMoreDefinitionPriority(symbol domain.Symbol) int {
	if symbol.Kind == domain.KindTest {
		return 0
	}
	if symbol.Kind == domain.KindInterface {
		return 0
	}
	if symbol.Kind == domain.KindType || symbol.Kind == domain.KindClass {
		return 1
	}
	if symbol.Kind == domain.KindFunction && strings.HasPrefix(symbol.Name, "New") {
		return 2
	}
	if symbol.Kind == domain.KindMethod {
		return 3
	}
	return 4
}

// auxiliaryQuery builds a deterministic lexical query from the target's name and
// qualified name (tests/configs that reference the symbol). No LLM prose.
func auxiliaryQuery(evidence Evidence) string {
	title := baseEvidenceTitle(evidence.Title)
	parts := []string{}
	if name := lastSegment(title); name != "" {
		parts = append(parts, name)
	}
	if title != "" && title != lastSegment(title) {
		parts = append(parts, title)
	}
	return strings.TrimSpace(strings.Join(parts, " "))
}

func lastSegment(qualified string) string {
	qualified = strings.TrimSpace(qualified)
	for _, sep := range []string{"::", ".", "/"} {
		if idx := strings.LastIndex(qualified, sep); idx >= 0 {
			qualified = qualified[idx+len(sep):]
		}
	}
	return qualified
}

// Score prioritizes the target, then observed relations by direction, and
// penalizes structurally-unrelated semantic candidates and low confidence.
func (SeeMorePolicy) Score(candidate Candidate, _ ContextRequest) ScoreBreakdown {
	breakdown := ScoreBreakdown{}
	if candidate.IsTarget {
		breakdown.TargetBoost = 3.0
	}
	switch candidate.Evidence.Relation {
	case "definition":
		breakdown.RelationBoost = 1.4
	case RelationUsageSite:
		breakdown.RelationBoost = 1.2
	case "tests":
		breakdown.RelationBoost = 1.6
	case "parent":
		breakdown.RelationBoost = 1.0
	case "calls":
		breakdown.RelationBoost = 0.9
	case "called_by":
		breakdown.RelationBoost = 0.8
	case "implements", "references":
		breakdown.RelationBoost = 0.6
	case "imports":
		breakdown.RelationBoost = 0.4
	case "child":
		breakdown.RelationBoost = 0.3
	}
	if candidate.Distance > 0 {
		breakdown.GraphBoost = 0.3 / float64(candidate.Distance) // depth-2 nodes rank below depth-1
	}
	if candidate.Source == "semantic_candidate" && candidate.Evidence.Relation == "" {
		breakdown.Penalty += 0.4 // unrelated to any observed edge
	}
	if candidate.Evidence.Confidence < 0.3 {
		breakdown.Penalty += 0.2
	}
	breakdown.Total = breakdown.TargetBoost + breakdown.RelationBoost + breakdown.GraphBoost - breakdown.Penalty
	return breakdown
}

// StaticOmissions declares the change-impact sources See More cannot yet supply,
// so the contract is explicit instead of silently incomplete.
func (SeeMorePolicy) StaticOmissions(ContextRequest) []Omission {
	return []Omission{
		{Reason: OmitSourceUnavailable, Ref: "git_diff", Detail: "Git change context not yet implemented"},
		{Reason: OmitSourceUnavailable, Ref: "lsp_diagnostics", Detail: "static diagnostics not yet implemented"},
	}
}

// SemanticMethods requests the deeper semantic set for See More: hover,
// definitions, references, implementations, one-hop call hierarchy and
// diagnostics. Optional: a category timeout yields omissions, not a failed pack.
func (SeeMorePolicy) SemanticMethods(request ContextRequest) (methods []string, mandatory bool) {
	if resolvedTargetIncludesSemanticHover(request) {
		return []string{"definition", "references", "implementation", "incomingCalls", "outgoingCalls", "diagnostics"}, false
	}
	return []string{"hover", "definition", "references", "implementation", "incomingCalls", "outgoingCalls", "diagnostics"}, false
}
