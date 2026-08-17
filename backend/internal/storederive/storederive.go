// Package storederive owns deterministic derivations shared by every store
// adapter. Keeping these rules here prevents backend-dependent read behavior.
package storederive

import (
	"path/filepath"
	"sort"
	"strings"
	"unicode"
	"unicode/utf8"

	"github.com/ThalesMMS/CodeAtlas/internal/domain"
	"github.com/ThalesMMS/CodeAtlas/internal/symbols"
)

const (
	DefaultGraphDepth    = 2
	DefaultGraphMaxNodes = 40
)

func SymbolHandle(resolved domain.ResolvedSymbol) string { return symbols.Handle(resolved) }

func ResolveParsedFile(parsed domain.ParsedFile) ([]domain.ResolvedSymbol, map[string]string) {
	if symbols.IsCanonicalParserBatch(parsed.Symbols) {
		resolved, handles, err := symbols.ResolveCanonicalBatch(parsed.Symbols, parsed.Edges, parsed.File.Hash)
		if err != nil {
			panic(err)
		}
		return resolved, handles
	}
	byInput := make(map[string]domain.Symbol, len(parsed.Symbols))
	for _, symbol := range parsed.Symbols {
		byInput[symbol.ID] = symbol
	}
	parentOf := make(map[string]string)
	for _, edge := range parsed.Edges {
		if edge.Type != "contains" || edge.ToSymbolID == "" {
			continue
		}
		if parent, ok := byInput[edge.FromSymbolID]; ok && parent.Kind != domain.KindFile {
			parentOf[edge.ToSymbolID] = edge.FromSymbolID
		}
	}
	resolved := make([]domain.ResolvedSymbol, 0, len(parsed.Symbols))
	handles := make(map[string]string, len(parsed.Symbols))
	stableIDs := make(map[string]domain.SymbolID, len(parsed.Symbols))
	visiting := make(map[string]bool)
	var resolveOne func(string)
	resolveOne = func(inputID string) {
		if _, done := handles[inputID]; done || visiting[inputID] {
			return
		}
		visiting[inputID] = true
		var parentID domain.SymbolID
		if parentInput := parentOf[inputID]; parentInput != "" {
			if _, exists := byInput[parentInput]; exists {
				resolveOne(parentInput)
				parentID = stableIDs[parentInput]
			}
		}
		item := symbols.Resolve(byInput[inputID], parentID, parsed.File.Hash)
		resolved = append(resolved, item)
		stableIDs[inputID] = item.Occurrence.SymbolID
		handles[inputID] = SymbolHandle(item)
	}
	for _, symbol := range parsed.Symbols {
		resolveOne(symbol.ID)
	}
	return resolved, handles
}

func RemapEdge(edge domain.Edge, handles map[string]string) domain.Edge {
	if mapped, ok := handles[edge.FromSymbolID]; ok {
		edge.FromSymbolID = mapped
	}
	if mapped, ok := handles[edge.ToSymbolID]; ok {
		edge.ToSymbolID = mapped
	}
	return edge
}

func Snippet(symbol domain.Symbol, tokens []string) string {
	content := symbol.DocComment
	if content == "" {
		content = symbol.Summary
	}
	if content == "" {
		content = symbol.Code
	}
	lower, position := strings.ToLower(content), -1
	for _, token := range tokens {
		if index := strings.Index(lower, token); index >= 0 && (position < 0 || index < position) {
			position = index
		}
	}
	if position < 0 {
		position = 0
	}
	start := position - 90
	if start < 0 {
		start = 0
	}
	end := start + 360
	if end > len(content) {
		end = len(content)
	}
	for start < len(content) && !utf8.RuneStart(content[start]) {
		start++
	}
	for end > start && end < len(content) && !utf8.RuneStart(content[end]) {
		end--
	}
	value := strings.TrimSpace(content[start:end])
	if start > 0 {
		value = "…" + value
	}
	if end < len(content) {
		value += "…"
	}
	return value
}

func SymbolLookupNames(symbol domain.Symbol) []string {
	names := make([]string, 0, 2)
	if symbol.Name != "" {
		names = append(names, symbol.Name)
	}
	qualified := strings.TrimSpace(symbol.QualifiedName)
	for _, separator := range []string{"::", ".", "/"} {
		if index := strings.LastIndex(qualified, separator); index >= 0 {
			qualified = qualified[index+len(separator):]
		}
	}
	if qualified != "" && qualified != symbol.Name {
		names = append(names, qualified)
	}
	return names
}

func ResolveSymbol(all []domain.Symbol, name, currentPath string) (domain.Symbol, bool) {
	name = finalToken(name)
	if name == "" {
		return domain.Symbol{}, false
	}
	candidates := make([]domain.Symbol, 0)
	for _, symbol := range all {
		if symbol.Name == name || strings.HasSuffix(symbol.QualifiedName, "."+name) || strings.HasSuffix(symbol.QualifiedName, "::"+name) {
			candidates = append(candidates, symbol)
		}
	}
	if len(candidates) == 0 {
		return domain.Symbol{}, false
	}
	sort.SliceStable(candidates, func(i, j int) bool {
		left, right := resolutionScore(candidates[i], currentPath), resolutionScore(candidates[j], currentPath)
		if left != right {
			return left > right
		}
		return candidates[i].ID < candidates[j].ID
	})
	return candidates[0], true
}

// TraverseGraph returns the handles reachable from the seeds in at most depth
// relation hops. Non-positive limits use the repository contract defaults.
func TraverseGraph(
	seedIDs []string,
	depth int,
	maxNodes int,
	exists func(string) bool,
	adjacency map[string][]int,
	edges []domain.Edge,
) (map[string]struct{}, int) {
	if depth <= 0 {
		depth = DefaultGraphDepth
	}
	if maxNodes <= 0 {
		maxNodes = DefaultGraphMaxNodes
	}
	visited := make(map[string]struct{}, min(len(seedIDs), maxNodes))
	frontier := make([]string, 0, min(len(seedIDs), maxNodes))
	for _, seed := range seedIDs {
		if len(visited) >= maxNodes {
			break
		}
		if _, seen := visited[seed]; seen || !exists(seed) {
			continue
		}
		visited[seed] = struct{}{}
		frontier = append(frontier, seed)
	}

	edgeVisits := 0
	for level := 0; level < depth && len(frontier) > 0 && len(visited) < maxNodes; level++ {
		next := make([]string, 0)
		for _, handle := range frontier {
			for _, edgeIndex := range adjacency[handle] {
				edgeVisits++
				edge := edges[edgeIndex]
				neighbor := edge.ToSymbolID
				if neighbor == handle {
					neighbor = edge.FromSymbolID
				}
				if neighbor == "" || !exists(neighbor) {
					continue
				}
				if _, seen := visited[neighbor]; seen {
					continue
				}
				if len(visited) >= maxNodes {
					break
				}
				visited[neighbor] = struct{}{}
				next = append(next, neighbor)
			}
		}
		frontier = next
	}
	return visited, edgeVisits
}

// GraphEdges returns the induced edges for the surviving node set. Unresolved
// outgoing relations are retained because their empty ToSymbolID does not point
// at a missing internal node and consumers use ToName to model external nodes.
func GraphEdges(edges []domain.Edge, adjacency map[string][]int, nodeIDs map[string]struct{}) []domain.Edge {
	indexes := make(map[int]struct{})
	for handle := range nodeIDs {
		for _, edgeIndex := range adjacency[handle] {
			edge := edges[edgeIndex]
			if _, fromIncluded := nodeIDs[edge.FromSymbolID]; !fromIncluded {
				continue
			}
			if edge.ToSymbolID != "" {
				if _, toIncluded := nodeIDs[edge.ToSymbolID]; !toIncluded {
					continue
				}
			}
			indexes[edgeIndex] = struct{}{}
		}
	}
	ordered := make([]int, 0, len(indexes))
	for edgeIndex := range indexes {
		ordered = append(ordered, edgeIndex)
	}
	sort.Ints(ordered)
	result := make([]domain.Edge, 0, len(ordered))
	for _, edgeIndex := range ordered {
		result = append(result, edges[edgeIndex])
	}
	return result
}

func finalToken(value string) string {
	fields := strings.FieldsFunc(value, func(r rune) bool { return !(unicode.IsLetter(r) || unicode.IsDigit(r) || r == '_' || r == '$') })
	if len(fields) == 0 {
		return ""
	}
	return fields[len(fields)-1]
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

func FileTree(files []domain.File) domain.FileTreeNode {
	root := &treeBuilder{name: "workspace", dirs: make(map[string]*treeBuilder)}
	for _, file := range files {
		parts, current := strings.Split(filepath.ToSlash(file.Path), "/"), root
		for index, part := range parts {
			if index == len(parts)-1 {
				current.files = append(current.files, domain.FileTreeNode{Name: part, Path: file.Path, Type: "file", Language: file.Language})
				continue
			}
			next := current.dirs[part]
			if next == nil {
				next = &treeBuilder{name: part, path: strings.Join(parts[:index+1], "/"), dirs: make(map[string]*treeBuilder)}
				current.dirs[part] = next
			}
			current = next
		}
	}
	return root.node()
}

type treeBuilder struct {
	name, path string
	dirs       map[string]*treeBuilder
	files      []domain.FileTreeNode
}

func (b *treeBuilder) node() domain.FileTreeNode {
	node := domain.FileTreeNode{Name: b.name, Path: b.path, Type: "directory"}
	names := make([]string, 0, len(b.dirs))
	for name := range b.dirs {
		names = append(names, name)
	}
	sort.Strings(names)
	for _, name := range names {
		node.Children = append(node.Children, b.dirs[name].node())
	}
	sort.Slice(b.files, func(i, j int) bool { return b.files[i].Name < b.files[j].Name })
	node.Children = append(node.Children, b.files...)
	return node
}
