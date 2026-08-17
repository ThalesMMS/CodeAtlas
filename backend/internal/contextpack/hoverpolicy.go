package contextpack

import (
	"context"
	"fmt"
	"path"
	"sort"
	"strings"
	"unicode"
	"unicode/utf8"

	"github.com/ThalesMMS/CodeAtlas/internal/domain"
	"github.com/ThalesMMS/CodeAtlas/internal/repository"
)

// HoverPolicyVersion is bumped whenever the hover retrieval shape or defaults
// change (which invalidates cached packs and requires fixture updates).
const HoverPolicyVersion = "hover.v3"

const maxHoverPackageAPI = 5

// HoverPolicy is the local, frequent, target-driven policy. It never runs a broad
// BM25/dense search to guess the symbol under the cursor: the target is resolved
// from the position, then a single level of graph neighbours (parent, outgoing
// and incoming calls, direct imports) is added, ranked by a fixed relation
// priority and bounded by a tight budget that reserves room for a short answer.
type HoverPolicy struct{}

func NewHoverPolicy() *HoverPolicy { return &HoverPolicy{} }

func (HoverPolicy) Version() string { return HoverPolicyVersion }

func (HoverPolicy) Validate(request ContextRequest) error {
	if request.Feature != FeatureHover {
		return fmt.Errorf("hover policy received feature %q", request.Feature)
	}
	if request.Location == nil || (request.Location.Path == "" && request.Location.SymbolID == "") {
		return fmt.Errorf("hover requires a location (path+line+column or symbolId)")
	}
	return nil
}

func (HoverPolicy) Expansion() ExpansionPolicy {
	return ExpansionPolicy{MaxDepth: 1, MaxNodes: 8, MaxEdges: 12, EdgeTypes: []string{"calls", "contains", "imports", "inherits", "implements"}}
}

func (HoverPolicy) Budget() BudgetPolicy {
	// ~6KB of evidence with a generous reserve for the short hover answer.
	return BudgetPolicy{MaxBytes: 6000, MaxBytesPerEvidence: 1400, MaxEvidence: 9, MaxNodes: 8, MaxEdges: 12, ReserveBytes: 2000}
}

// Seeds resolves only the target occurrence at the position (or the supplied
// SymbolID); it does not search the repository.
func (HoverPolicy) Seeds(_ context.Context, view repository.ReadView, request ContextRequest) ([]Candidate, error) {
	location := request.Location
	var symbol domain.Symbol
	var target Candidate
	if resolved, resolvedSymbol, ok := resolvedTargetCandidate(request); ok {
		target = resolved
		symbol = resolvedSymbol
	} else if location.SymbolID != "" {
		resolved, ok := view.GetSymbol(string(location.SymbolID))
		if !ok {
			return nil, fmt.Errorf("symbol not found")
		}
		symbol = resolved
		target = Candidate{Evidence: evidenceFromSymbol(symbol, KindASTObservation), IsTarget: true, Source: "target"}
	} else {
		resolved, ok := view.SymbolAt(location.Path, location.Line, location.Column)
		if !ok {
			return nil, fmt.Errorf("no symbol at position")
		}
		symbol = resolved.ToSymbol()
		target = Candidate{Evidence: evidenceFromSymbol(symbol, KindASTObservation), IsTarget: true, Source: "target"}
	}

	candidates := []Candidate{target}
	if usage, ok := hoverUsageEvidence(view, location, symbol); ok {
		candidates = append(candidates, Candidate{Evidence: usage, Source: RelationUsageSite})
	}
	if symbol.Kind == domain.KindImport || symbol.Kind == domain.KindPackage {
		candidates = append(candidates, hoverPackageAPI(view, symbol)...)
	}
	return candidates, nil
}

func hoverUsageEvidence(view repository.ReadView, location *SourceLocation, target domain.Symbol) (Evidence, bool) {
	if location == nil || location.Path == "" || location.Line < 1 {
		return Evidence{}, false
	}
	var file domain.Symbol
	for _, symbol := range view.SymbolsByPath(location.Path) {
		if symbol.Path == location.Path && symbol.Kind == domain.KindFile {
			file = symbol
			break
		}
	}
	if file.Code == "" {
		return Evidence{}, false
	}
	lines := strings.Split(strings.ReplaceAll(file.Code, "\r\n", "\n"), "\n")
	if location.Line > len(lines) {
		return Evidence{}, false
	}
	start, end := location.Line-2, location.Line+2
	if start < 1 {
		start = 1
	}
	if end > len(lines) {
		end = len(lines)
	}
	content := strings.TrimSpace(strings.Join(lines[start-1:end], "\n"))
	if content == "" {
		return Evidence{}, false
	}
	return Evidence{
		Kind: KindASTObservation, Path: location.Path,
		Range: domain.Range{
			Start: domain.Position{Line: start, Column: 1},
			End:   domain.Position{Line: end, Column: 1},
		},
		ContentHash: shortHash(content), Title: "Usage site for " + target.Name,
		Content: "Usage site:\n" + content, Relation: RelationUsageSite, Confidence: 1,
		Provenance:  []Provenance{{Source: "ast", Detail: RelationUsageSite}},
		DisplayCode: content, DisplayLanguage: file.Language,
	}, true
}

func hoverPackageAPI(view repository.ReadView, imported domain.Symbol) []Candidate {
	packageDir := workspacePackageDir(view.Files(), importPathOf(imported))
	if packageDir == "" {
		return nil
	}
	type ranked struct {
		priority int
		symbol   domain.Symbol
	}
	var matches []ranked
	for _, file := range view.Files() {
		if path.Dir(path.Clean(file.Path)) != packageDir {
			continue
		}
		for _, symbol := range view.SymbolsByPath(file.Path) {
			if !hoverPublicSymbol(symbol) {
				continue
			}
			priority := 2
			if symbol.Kind == domain.KindFunction && strings.HasPrefix(symbol.Name, "New") {
				priority = 0
			} else if symbol.Kind == domain.KindType || symbol.Kind == domain.KindInterface || symbol.Kind == domain.KindClass {
				priority = 1
			}
			matches = append(matches, ranked{priority: priority, symbol: symbol})
		}
	}
	sort.Slice(matches, func(i, j int) bool {
		if matches[i].priority != matches[j].priority {
			return matches[i].priority < matches[j].priority
		}
		if matches[i].symbol.Name != matches[j].symbol.Name {
			return matches[i].symbol.Name < matches[j].symbol.Name
		}
		return matches[i].symbol.ID < matches[j].symbol.ID
	})
	if len(matches) > maxHoverPackageAPI {
		matches = matches[:maxHoverPackageAPI]
	}
	result := make([]Candidate, 0, len(matches))
	for index, match := range matches {
		evidence := evidenceFromSymbol(match.symbol, KindASTObservation)
		evidence.Relation = "package_api"
		evidence.Confidence = 1
		evidence.Provenance = []Provenance{{Source: "ast", Detail: "package_export"}}
		result = append(result, Candidate{Evidence: evidence, Source: "package_api", PackageAPIRank: index + 1})
	}
	return result
}

func workspacePackageDir(files []domain.File, importPath string) string {
	if importPath == "" {
		return ""
	}
	for _, file := range files {
		if path.Base(file.Path) != "go.mod" || file.Content == "" {
			continue
		}
		for _, line := range strings.Split(strings.ReplaceAll(file.Content, "\r\n", "\n"), "\n") {
			fields := strings.Fields(line)
			if len(fields) != 2 || fields[0] != "module" {
				continue
			}
			module := strings.TrimSuffix(fields[1], "/")
			if importPath == module {
				return path.Dir(file.Path)
			}
			if strings.HasPrefix(importPath, module+"/") {
				return path.Clean(path.Join(path.Dir(file.Path), strings.TrimPrefix(importPath, module+"/")))
			}
		}
	}
	return ""
}

func importPathOf(symbol domain.Symbol) string {
	if symbol.Kind == domain.KindPackage && strings.TrimSpace(symbol.QualifiedName) != "" {
		return strings.TrimSpace(symbol.QualifiedName)
	}
	value := strings.TrimSpace(strings.TrimPrefix(strings.TrimSpace(symbol.Signature), "import"))
	return strings.Trim(value, "\"`")
}

func hoverPublicSymbol(symbol domain.Symbol) bool {
	if symbol.Name == "" || symbol.Kind == domain.KindFile || symbol.Kind == domain.KindImport || symbol.Kind == domain.KindTest {
		return false
	}
	first, _ := utf8.DecodeRuneInString(symbol.Name)
	return unicode.IsUpper(first)
}

// Score realizes the hover budget-reservation order: target first, then parent,
// outgoing calls, incoming calls, imports; file/import-only and low-confidence
// evidence is penalized.
func (HoverPolicy) Score(candidate Candidate, _ ContextRequest) ScoreBreakdown {
	breakdown := ScoreBreakdown{}
	if candidate.IsTarget {
		breakdown.TargetBoost = 2.0
	}
	switch candidate.Evidence.Relation {
	case RelationUsageSite:
		breakdown.RelationBoost = 1.2
	case "package_api":
		breakdown.RelationBoost = 0.8
	case "parent":
		breakdown.RelationBoost = 0.9
	case "calls":
		breakdown.RelationBoost = 0.7
	case "called_by":
		breakdown.RelationBoost = 0.5
	case "imports":
		breakdown.RelationBoost = 0.3
	case "child":
		breakdown.RelationBoost = 0.2
	}
	if candidate.Distance > 0 {
		breakdown.GraphBoost = 0.1 / float64(candidate.Distance)
	}
	if candidate.Evidence.Confidence < 0.3 {
		breakdown.Penalty += 0.2
	}
	breakdown.Total = breakdown.TargetBoost + breakdown.RelationBoost + breakdown.GraphBoost - breakdown.Penalty
	return breakdown
}

// SemanticMethods requests primary hover text, the exact definition and
// intersecting diagnostics. The Explainer preloads hover+definition while
// resolving the exact target; the collector reuses both request-local results
// and adds diagnostics, including for synced open-document versions.
func (HoverPolicy) SemanticMethods(request ContextRequest) (methods []string, mandatory bool) {
	if resolvedTargetIncludesSemanticHover(request) {
		return []string{"definition", "diagnostics"}, false
	}
	return []string{"hover", "definition", "diagnostics"}, false
}

// SemanticEvidenceFirst makes gopls hover/doc-comment text the primary evidence
// whenever it is available. The optional collector records an AST-only omission
// when it is not.
func (HoverPolicy) SemanticEvidenceFirst() bool { return true }
