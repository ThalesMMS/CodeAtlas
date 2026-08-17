package contextpack

import (
	"crypto/sha256"
	"encoding/hex"
	"strings"

	"github.com/ThalesMMS/CodeAtlas/internal/domain"
	"github.com/ThalesMMS/CodeAtlas/internal/repository"
	"github.com/ThalesMMS/CodeAtlas/internal/textutil"
)

// evidenceFromSymbol projects a flat symbol from the pinned view into Evidence.
// SymbolID carries the stable handle (the v1 SymbolID, or the occurrence handle
// for an occurrence-only symbol), so graph edges and dedup line up.
func evidenceFromSymbol(symbol domain.Symbol, kind string) Evidence {
	content := symbolEvidenceContent(symbol)
	return Evidence{
		Kind:            kind,
		SymbolID:        domain.SymbolID(symbol.ID),
		OccurrenceID:    domain.OccurrenceID(symbol.OccurrenceID),
		Path:            symbol.Path,
		Range:           symbol.Range,
		ContentHash:     shortHash(content),
		Title:           symbolTitle(symbol),
		Content:         content,
		Confidence:      0.7,
		Provenance:      []Provenance{{Source: "ast"}},
		DisplayCode:     symbol.Code,
		DisplayLanguage: symbol.Language,
	}
}

func resolvedTargetCandidate(request ContextRequest) (Candidate, domain.Symbol, bool) {
	if request.ResolvedTarget == nil {
		return Candidate{}, domain.Symbol{}, false
	}
	symbol := request.ResolvedTarget.Symbol
	evidence := request.ResolvedTarget.Evidence
	if evidence.Kind == "" {
		evidence = evidenceFromSymbol(symbol, KindASTObservation)
	}
	if evidence.Path == "" && symbol.Path != "" {
		evidence.Path = symbol.Path
	}
	if evidence.Range.Start.Line < 1 && symbol.Range.Start.Line > 0 {
		evidence.Range = symbol.Range
	}
	if evidence.Title == "" {
		evidence.Title = symbolTitle(symbol)
	}
	if evidence.ContentHash == "" {
		evidence.ContentHash = shortHash(evidence.Content)
	}
	return Candidate{Evidence: evidence, IsTarget: true, Source: "target"}, symbol, true
}

func resolvedTargetIncludesSemanticHover(request ContextRequest) bool {
	return request.ResolvedTarget != nil &&
		request.ResolvedTarget.Symbol.ID == "" &&
		request.ResolvedTarget.Evidence.Kind == KindLSPFact &&
		request.ResolvedTarget.Evidence.Relation == "type"
}

func symbolTitle(symbol domain.Symbol) string {
	base := symbol.Name
	if symbol.QualifiedName != "" {
		base = symbol.QualifiedName
	}
	if doc := firstLine(symbol.DocComment); doc != "" {
		if len(doc) > 240 {
			doc = textutil.TruncateUTF8(doc, 237) + "…"
		}
		return base + " — " + doc
	}
	return base
}

func symbolEvidenceContent(symbol domain.Symbol) string {
	parts := make([]string, 0, 3)
	if doc := strings.TrimSpace(symbol.DocComment); doc != "" {
		parts = append(parts, "Documentation:\n"+doc)
	}
	if code := strings.TrimSpace(symbol.Code); code != "" {
		parts = append(parts, "Definition:\n"+code)
	}
	if summary := strings.TrimSpace(symbol.Summary); summary != "" {
		parts = append(parts, "Structural summary:\n"+summary)
	}
	return strings.Join(parts, "\n\n")
}

func firstLine(value string) string {
	if index := strings.IndexByte(value, '\n'); index >= 0 {
		value = value[:index]
	}
	return strings.TrimSpace(value)
}

func baseEvidenceTitle(title string) string {
	if index := strings.Index(title, " — "); index >= 0 {
		return title[:index]
	}
	return title
}

func shortHash(content string) string {
	digest := sha256.Sum256([]byte(content))
	return hex.EncodeToString(digest[:])[:16]
}

func handleOf(evidence Evidence) string {
	if evidence.SymbolID != "" {
		return string(evidence.SymbolID)
	}
	return string(evidence.OccurrenceID)
}

func edgeAllowSet(types []string) map[string]struct{} {
	if len(types) == 0 {
		return nil
	}
	set := make(map[string]struct{}, len(types))
	for _, edgeType := range types {
		set[edgeType] = struct{}{}
	}
	return set
}

// expandGraph grows the neighborhood of the seeds over the pinned ReadView,
// bounded by depth/nodes/edges and an edge-type allowlist. Cycles are avoided by
// the view's visited set; unresolved relations never become invented symbols.
func expandGraph(view repository.ReadView, seeds []Candidate, policy ExpansionPolicy) ([]Candidate, []domain.Edge) {
	result := append([]Candidate(nil), seeds...)
	if policy.MaxDepth <= 0 || policy.MaxNodes <= 0 {
		return result, nil
	}
	seedIDs := make([]string, 0, len(seeds))
	seen := make(map[string]bool, len(seeds))
	for _, seed := range seeds {
		handle := handleOf(seed.Evidence)
		if handle != "" && !seen[handle] {
			seedIDs = append(seedIDs, handle)
			seen[handle] = true
		}
	}
	if len(seedIDs) == 0 {
		return result, nil
	}

	symbols, edges := view.Graph(seedIDs, policy.MaxDepth, policy.MaxNodes)
	allow := edgeAllowSet(policy.EdgeTypes)
	filtered := make([]domain.Edge, 0, len(edges))
	for _, edge := range edges {
		if allow != nil {
			if _, ok := allow[edge.Type]; !ok {
				continue
			}
		}
		if policy.MaxEdges > 0 && len(filtered) >= policy.MaxEdges {
			break
		}
		filtered = append(filtered, edge)
	}

	// Classify each neighbour's relation to a seed (preserving edge direction) so
	// policies can prioritise parent/outgoing/incoming/imports without re-walking.
	relation := relationToSeeds(filtered, seen)
	for _, symbol := range symbols {
		if seen[symbol.ID] {
			continue
		}
		seen[symbol.ID] = true
		evidence := evidenceFromSymbol(symbol, KindASTObservation)
		evidence.Relation = relation[symbol.ID]
		result = append(result, Candidate{Evidence: evidence, Distance: 1, Source: "graph"})
	}
	return result, filtered
}

// relationToSeeds labels each non-seed handle by its directed relation to a seed:
// parent (contains the seed), child (contained by the seed), calls (seed→handle),
// called_by (handle→seed), or imports.
func relationToSeeds(edges []domain.Edge, seeds map[string]bool) map[string]string {
	relation := make(map[string]string)
	for _, edge := range edges {
		fromSeed := seeds[edge.FromSymbolID]
		toSeed := seeds[edge.ToSymbolID]
		switch {
		case fromSeed && !toSeed:
			switch edge.Type {
			case "calls":
				relation[edge.ToSymbolID] = "calls"
			case "contains":
				relation[edge.ToSymbolID] = "child"
			case "imports":
				relation[edge.ToSymbolID] = "imports"
			}
		case toSeed && !fromSeed:
			switch edge.Type {
			case "calls":
				relation[edge.FromSymbolID] = "called_by"
			case "contains":
				relation[edge.FromSymbolID] = "parent"
			}
		}
	}
	return relation
}
