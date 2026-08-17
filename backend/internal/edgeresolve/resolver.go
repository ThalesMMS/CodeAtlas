// Package edgeresolve resolves unresolved graph edges against a flat symbol
// snapshot using derived indexes shared by the JSON and SQLite stores.
package edgeresolve

import (
	"math"
	"path/filepath"
	"strings"
	"unicode"

	"github.com/ThalesMMS/CodeAtlas/internal/domain"
)

// Resolve fills resolvable ToSymbolID values in place. The input symbol map and
// all edge fields other than ToSymbolID/confidence remain unchanged.
func Resolve(symbols map[string]domain.Symbol, edges []domain.Edge) {
	resolve(symbols, edges)
}

type resolverStats struct {
	importCandidates   int
	importLookups      int
	namedCandidateScan int
}

func resolve(symbols map[string]domain.Symbol, edges []domain.Edge) resolverStats {
	byName := make(map[string][]domain.Symbol)
	imports := newImportSuffixIndex()
	stats := resolverStats{}
	for _, symbol := range symbols {
		byName[symbol.Name] = append(byName[symbol.Name], symbol)
		if symbol.Kind == domain.KindFile {
			imports.add(symbol)
			stats.importCandidates++
		}
	}

	type resolutionKey struct {
		name string
		path string
	}
	type resolution struct {
		symbol domain.Symbol
		found  bool
		count  int
	}
	cache := make(map[resolutionKey]resolution)

	for i := range edges {
		edge := &edges[i]
		if edge.ToSymbolID != "" {
			if _, exists := symbols[edge.ToSymbolID]; exists {
				continue
			}
			edge.ToSymbolID = ""
		}
		if edge.Type == "imports" {
			target := strings.Trim(edge.ToName, "./")
			if target == "" {
				continue
			}
			stats.importLookups++
			if id, ok := imports.lookup(target); ok {
				edge.ToSymbolID = id
				edge.Confidence = 0.9
			}
			continue
		}

		key := resolutionKey{name: finalToken(edge.ToName), path: edge.Path}
		resolved, ok := cache[key]
		if !ok {
			candidates := byName[key.name]
			resolved.count = len(candidates)
			stats.namedCandidateScan += len(candidates)
			for _, candidate := range candidates {
				if !resolved.found || betterNamedCandidate(candidate, resolved.symbol, key.path) {
					resolved.symbol = candidate
					resolved.found = true
				}
			}
			cache[key] = resolved
		}
		if !resolved.found {
			continue
		}
		edge.ToSymbolID = resolved.symbol.ID
		if resolved.count == 1 {
			edge.Confidence = 0.95
		} else {
			edge.Confidence = math.Max(edge.Confidence, 0.7)
		}
	}
	return stats
}

func betterNamedCandidate(candidate, current domain.Symbol, currentPath string) bool {
	candidateScore := resolutionScore(candidate, currentPath)
	currentScore := resolutionScore(current, currentPath)
	if candidateScore != currentScore {
		return candidateScore > currentScore
	}
	return candidate.ID < current.ID
}

func resolutionScore(symbol domain.Symbol, currentPath string) int {
	score := 0
	if symbol.Path == currentPath {
		score += 100
	}
	if filepath.Dir(symbol.Path) == filepath.Dir(currentPath) {
		score += 25
	}
	if symbol.Kind != domain.KindFile {
		score += 5
	}
	return score
}

func finalToken(value string) string {
	fields := strings.FieldsFunc(value, func(r rune) bool {
		return !(unicode.IsLetter(r) || unicode.IsDigit(r) || r == '_' || r == '$')
	})
	if len(fields) == 0 {
		return ""
	}
	return fields[len(fields)-1]
}

type importCandidate struct {
	id   string
	path string
}

type importSuffixNode struct {
	children map[byte]*importSuffixNode
	best     importCandidate
	hasBest  bool
}

type importSuffixIndex struct {
	root importSuffixNode
}

func newImportSuffixIndex() *importSuffixIndex { return &importSuffixIndex{} }

func (i *importSuffixIndex) add(symbol domain.Symbol) {
	normalized := filepath.ToSlash(symbol.Path)
	candidate := importCandidate{id: symbol.ID, path: normalized}
	i.insert(normalized, candidate)
	withoutExtension := strings.TrimSuffix(normalized, filepath.Ext(symbol.Path))
	if withoutExtension != normalized {
		i.insert(withoutExtension, candidate)
	}
}

func (i *importSuffixIndex) insert(path string, candidate importCandidate) {
	node := &i.root
	for offset := len(path) - 1; offset >= 0; offset-- {
		if node.children == nil {
			node.children = make(map[byte]*importSuffixNode)
		}
		next := node.children[path[offset]]
		if next == nil {
			next = &importSuffixNode{}
			node.children[path[offset]] = next
		}
		node = next
		if offset == 0 || path[offset-1] == '/' {
			node.consider(candidate)
		}
	}
}

func (n *importSuffixNode) consider(candidate importCandidate) {
	if !n.hasBest || candidate.path < n.best.path || (candidate.path == n.best.path && candidate.id < n.best.id) {
		n.best = candidate
		n.hasBest = true
	}
}

func (i *importSuffixIndex) lookup(target string) (string, bool) {
	node := &i.root
	for offset := len(target) - 1; offset >= 0; offset-- {
		node = node.children[target[offset]]
		if node == nil {
			return "", false
		}
	}
	return node.best.id, node.hasBest
}
