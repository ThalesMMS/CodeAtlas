// Package diagram turns factual CodeAtlas graph data into deterministic Mermaid
// text. It never consumes model prose and never invents nodes or relations.
package diagram

import (
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"unicode"

	"github.com/ThalesMMS/CodeAtlas/internal/domain"
	"github.com/ThalesMMS/CodeAtlas/internal/textutil"
)

const (
	Version         = "mermaid/v1"
	DefaultMaxNodes = 20
	DefaultMaxDepth = 5
	maxEdgesFactor  = 3
)

type Node struct {
	ID        string
	Label     string
	Kind      string
	Group     string
	Path      string
	Range     domain.Range
	Relevance float64
	External  bool
}

type Edge struct {
	ID         string
	Source     string
	Target     string
	Type       string
	Confidence float64
}

// Flowchart emits a capped graph TD view. Nodes and edges are selected and
// ordered from their factual properties, so input slice order cannot affect the
// resulting bytes.
func Flowchart(nodes []Node, edges []Edge, maxNodes int) domain.MermaidDiagram {
	selected := selectNodes(nodes, edges, normalizedMaxNodes(maxNodes))
	if len(selected) == 0 {
		return domain.MermaidDiagram{}
	}

	sort.Slice(selected, func(i, j int) bool { return outputNodeKey(selected[i]) < outputNodeKey(selected[j]) })
	aliases := make(map[string]string, len(selected))
	byID := make(map[string]Node, len(selected))
	groups := make(map[string][]Node)
	for i, node := range selected {
		aliases[node.ID] = "n" + strconv.Itoa(i)
		byID[node.ID] = node
		group := normalizedGroup(node)
		groups[group] = append(groups[group], node)
	}

	groupNames := make([]string, 0, len(groups))
	for group := range groups {
		groupNames = append(groupNames, group)
	}
	sort.Slice(groupNames, func(i, j int) bool {
		left, right := strings.ToLower(groupNames[i]), strings.ToLower(groupNames[j])
		if left == right {
			return groupNames[i] < groupNames[j]
		}
		return left < right
	})

	var out strings.Builder
	out.WriteString("graph TD\n")
	for groupIndex, group := range groupNames {
		out.WriteString("  subgraph g")
		out.WriteString(strconv.Itoa(groupIndex))
		out.WriteString("[\"")
		out.WriteString(mermaidText(group, 80))
		out.WriteString("\"]\n")
		out.WriteString("    direction TB\n")
		for _, node := range groups[group] {
			out.WriteString("    ")
			out.WriteString(aliases[node.ID])
			out.WriteString("[\"")
			out.WriteString(mermaidText(nodeDisplayLabel(node), 110))
			out.WriteString("\"]\n")
		}
		out.WriteString("  end\n")
	}

	keptEdges := selectedEdges(edges, aliases, normalizedMaxNodes(maxNodes)*maxEdgesFactor)
	for _, edge := range keptEdges {
		out.WriteString("  ")
		out.WriteString(aliases[edge.Source])
		if byID[edge.Source].External || byID[edge.Target].External {
			out.WriteString(" -. ")
			out.WriteString(mermaidEdgeText(edge.Type))
			out.WriteString(" .-> ")
		} else {
			out.WriteString(" -->|")
			out.WriteString(mermaidEdgeText(edge.Type))
			out.WriteString("| ")
		}
		out.WriteString(aliases[edge.Target])
		out.WriteString("\n")
	}

	return domain.MermaidDiagram{
		Version: Version,
		Kind:    "flowchart",
		Source:  strings.TrimSuffix(out.String(), "\n"),
		Sources: diagramSources(selected),
	}
}

// Sequence follows factual calls edges from a deterministic root with bounded
// DFS depth. It returns an empty diagram when no call chain exists.
func Sequence(nodes []Node, edges []Edge, rootID string, maxDepth int) domain.MermaidDiagram {
	byID := uniqueNodes(nodes)
	calls := callEdges(edges, byID)
	if len(calls) == 0 {
		return domain.MermaidDiagram{}
	}
	if maxDepth <= 0 {
		maxDepth = DefaultMaxDepth
	}
	rootID = sequenceRoot(rootID, byID, calls)
	if rootID == "" {
		return domain.MermaidDiagram{}
	}

	outgoing := make(map[string][]Edge)
	for _, edge := range calls {
		outgoing[edge.Source] = append(outgoing[edge.Source], edge)
	}
	for source := range outgoing {
		sort.Slice(outgoing[source], func(i, j int) bool {
			left, right := byID[outgoing[source][i].Target], byID[outgoing[source][j].Target]
			if nodeStableKey(left) == nodeStableKey(right) {
				return edgeStableKey(outgoing[source][i]) < edgeStableKey(outgoing[source][j])
			}
			return nodeStableKey(left) < nodeStableKey(right)
		})
	}

	participants := []string{rootID}
	participantSet := map[string]struct{}{rootID: {}}
	var arrows []Edge
	seenEdges := make(map[string]struct{})
	var walk func(string, int, map[string]bool)
	walk = func(source string, depth int, stack map[string]bool) {
		if depth >= maxDepth {
			return
		}
		for _, edge := range outgoing[source] {
			key := edgeStableKey(edge)
			if _, seen := seenEdges[key]; seen {
				continue
			}
			if _, known := participantSet[edge.Target]; !known {
				if len(participants) >= DefaultMaxNodes {
					continue
				}
				participantSet[edge.Target] = struct{}{}
				participants = append(participants, edge.Target)
			}
			seenEdges[key] = struct{}{}
			arrows = append(arrows, edge)
			if !stack[edge.Target] {
				nextStack := make(map[string]bool, len(stack)+1)
				for id, active := range stack {
					nextStack[id] = active
				}
				nextStack[edge.Target] = true
				walk(edge.Target, depth+1, nextStack)
			}
		}
	}
	walk(rootID, 0, map[string]bool{rootID: true})
	if len(arrows) == 0 {
		return domain.MermaidDiagram{}
	}

	aliases := make(map[string]string, len(participants))
	var out strings.Builder
	out.WriteString("sequenceDiagram\n")
	for i, id := range participants {
		alias := "p" + strconv.Itoa(i)
		aliases[id] = alias
		out.WriteString("  participant ")
		out.WriteString(alias)
		out.WriteString(" as ")
		out.WriteString(sequenceText(nodeDisplayLabel(byID[id]), 110))
		out.WriteString("\n")
	}
	for _, edge := range arrows {
		out.WriteString("  ")
		out.WriteString(aliases[edge.Source])
		out.WriteString("->>")
		out.WriteString(aliases[edge.Target])
		out.WriteString(": calls ")
		out.WriteString(sequenceText(byID[edge.Target].Label, 80))
		out.WriteString("\n")
	}

	selected := make([]Node, 0, len(participants))
	for _, id := range participants {
		selected = append(selected, byID[id])
	}
	return domain.MermaidDiagram{
		Version: Version,
		Kind:    "sequence",
		Source:  strings.TrimSuffix(out.String(), "\n"),
		Sources: diagramSources(selected),
	}
}

func normalizedMaxNodes(value int) int {
	if value <= 0 || value > DefaultMaxNodes {
		return DefaultMaxNodes
	}
	return value
}

func uniqueNodes(nodes []Node) map[string]Node {
	byID := make(map[string]Node, len(nodes))
	for _, node := range nodes {
		if node.ID == "" {
			continue
		}
		current, exists := byID[node.ID]
		if !exists || node.Relevance > current.Relevance || (node.Relevance == current.Relevance && nodeStableKey(node) < nodeStableKey(current)) {
			byID[node.ID] = node
		}
	}
	return byID
}

func selectNodes(nodes []Node, edges []Edge, limit int) []Node {
	byID := uniqueNodes(nodes)
	degree := make(map[string]int)
	callDegree := make(map[string]int)
	for _, edge := range edges {
		if _, source := byID[edge.Source]; !source {
			continue
		}
		if _, target := byID[edge.Target]; !target {
			continue
		}
		degree[edge.Source]++
		degree[edge.Target]++
		if edge.Type == "calls" {
			callDegree[edge.Source]++
			callDegree[edge.Target]++
		}
	}
	selected := make([]Node, 0, len(byID))
	for _, node := range byID {
		selected = append(selected, node)
	}
	sort.Slice(selected, func(i, j int) bool {
		left := nodeScore(selected[i], degree[selected[i].ID], callDegree[selected[i].ID])
		right := nodeScore(selected[j], degree[selected[j].ID], callDegree[selected[j].ID])
		if left == right {
			return nodeStableKey(selected[i]) < nodeStableKey(selected[j])
		}
		return left > right
	})
	if len(selected) > limit {
		selected = selected[:limit]
	}
	return selected
}

func nodeScore(node Node, degree, callDegree int) float64 {
	score := node.Relevance*1000 + float64(callDegree*80+degree*10)
	if !node.External && node.Path != "" {
		// A readable code map spends its cap on navigable repository symbols
		// before unresolved/external boundaries. External nodes are still shown
		// with dashed edges when room remains.
		score += 1000
	} else {
		score -= 500
	}
	if strings.EqualFold(strings.TrimSpace(node.Label), "main") || strings.EqualFold(node.Kind, "entrypoint") {
		score += 250
	}
	if node.Kind == "file" {
		score -= 20
	}
	return score
}

func nodeStableKey(node Node) string {
	return strings.ToLower(node.Group) + "\x00" + strings.ToLower(node.Label) + "\x00" + node.Path + "\x00" + node.ID
}

func outputNodeKey(node Node) string {
	return strings.ToLower(normalizedGroup(node)) + "\x00" + strings.ToLower(node.Label) + "\x00" + node.Path + "\x00" + node.ID
}

func normalizedGroup(node Node) string {
	if node.External || node.Path == "" {
		return "External"
	}
	if group := strings.TrimSpace(node.Group); group != "" {
		return group
	}
	return "Root"
}

func selectedEdges(edges []Edge, aliases map[string]string, limit int) []Edge {
	seen := make(map[string]struct{})
	selected := make([]Edge, 0, len(edges))
	for _, edge := range edges {
		if _, source := aliases[edge.Source]; !source {
			continue
		}
		if _, target := aliases[edge.Target]; !target || edge.Source == edge.Target {
			continue
		}
		key := edge.Source + "\x00" + edge.Target + "\x00" + edge.Type
		if _, duplicate := seen[key]; duplicate {
			continue
		}
		seen[key] = struct{}{}
		selected = append(selected, edge)
	}
	sort.Slice(selected, func(i, j int) bool {
		left, right := edgePriority(selected[i].Type), edgePriority(selected[j].Type)
		if left != right {
			return left < right
		}
		leftKey := aliases[selected[i].Source] + "\x00" + aliases[selected[i].Target] + "\x00" + edgeStableKey(selected[i])
		rightKey := aliases[selected[j].Source] + "\x00" + aliases[selected[j].Target] + "\x00" + edgeStableKey(selected[j])
		return leftKey < rightKey
	})
	if limit > 0 && len(selected) > limit {
		selected = selected[:limit]
	}
	return selected
}

func callEdges(edges []Edge, nodes map[string]Node) []Edge {
	var calls []Edge
	seen := make(map[string]struct{})
	for _, edge := range edges {
		if edge.Type != "calls" {
			continue
		}
		if _, ok := nodes[edge.Source]; !ok {
			continue
		}
		if _, ok := nodes[edge.Target]; !ok {
			continue
		}
		key := edge.Source + "\x00" + edge.Target
		if _, duplicate := seen[key]; duplicate {
			continue
		}
		seen[key] = struct{}{}
		calls = append(calls, edge)
	}
	return calls
}

func sequenceRoot(requested string, nodes map[string]Node, calls []Edge) string {
	if _, ok := nodes[requested]; requested != "" && ok {
		return requested
	}
	incoming := make(map[string]int)
	sources := make(map[string]struct{})
	for _, edge := range calls {
		sources[edge.Source] = struct{}{}
		incoming[edge.Target]++
	}
	candidates := make([]Node, 0, len(sources))
	for id := range sources {
		candidates = append(candidates, nodes[id])
	}
	sort.Slice(candidates, func(i, j int) bool {
		leftMain := strings.EqualFold(candidates[i].Label, "main")
		rightMain := strings.EqualFold(candidates[j].Label, "main")
		if leftMain != rightMain {
			return leftMain
		}
		leftRoot, rightRoot := incoming[candidates[i].ID] == 0, incoming[candidates[j].ID] == 0
		if leftRoot != rightRoot {
			return leftRoot
		}
		if candidates[i].Relevance != candidates[j].Relevance {
			return candidates[i].Relevance > candidates[j].Relevance
		}
		return nodeStableKey(candidates[i]) < nodeStableKey(candidates[j])
	})
	if len(candidates) == 0 {
		return ""
	}
	return candidates[0].ID
}

func edgePriority(kind string) int {
	switch kind {
	case "calls":
		return 0
	case "implements", "extends":
		return 1
	case "contains":
		return 2
	case "imports":
		return 3
	default:
		return 4
	}
}

func edgeStableKey(edge Edge) string {
	return edge.Source + "\x00" + edge.Target + "\x00" + edge.Type + "\x00" + edge.ID
}

func nodeDisplayLabel(node Node) string {
	label := strings.TrimSpace(node.Label)
	if label == "" {
		label = node.ID
	}
	if node.Path != "" {
		label += " · " + filepath.Base(filepath.ToSlash(node.Path))
	}
	return label
}

func diagramSources(nodes []Node) []domain.DiagramSource {
	seen := make(map[string]struct{})
	result := make([]domain.DiagramSource, 0, len(nodes))
	for _, node := range nodes {
		if node.Path == "" || node.External {
			continue
		}
		key := node.ID + "\x00" + node.Path + "\x00" + strconv.Itoa(node.Range.Start.Line)
		if _, duplicate := seen[key]; duplicate {
			continue
		}
		seen[key] = struct{}{}
		result = append(result, domain.DiagramSource{NodeID: node.ID, Label: node.Label, Path: node.Path, Range: node.Range})
	}
	sort.Slice(result, func(i, j int) bool {
		if result[i].Path != result[j].Path {
			return result[i].Path < result[j].Path
		}
		if result[i].Range.Start.Line != result[j].Range.Start.Line {
			return result[i].Range.Start.Line < result[j].Range.Start.Line
		}
		return result[i].NodeID < result[j].NodeID
	})
	return result
}

func mermaidText(value string, maxRunes int) string {
	value = strings.Join(strings.Fields(value), " ")
	value = textutil.TruncateRunes(value, maxRunes)
	replacer := strings.NewReplacer("&", "&amp;", "<", "&lt;", ">", "&gt;", "\"", "&quot;")
	return replacer.Replace(value)
}

func mermaidEdgeText(value string) string {
	value = sequenceText(value, 40)
	value = strings.ReplaceAll(value, "|", " ")
	if value == "" {
		return "relates"
	}
	return value
}

func sequenceText(value string, maxRunes int) string {
	value = strings.Join(strings.Fields(value), " ")
	var out strings.Builder
	for _, r := range textutil.TruncateRunes(value, maxRunes) {
		if unicode.IsLetter(r) || unicode.IsDigit(r) || unicode.IsSpace(r) || strings.ContainsRune("._/()#-·", r) {
			out.WriteRune(r)
		} else {
			out.WriteByte(' ')
		}
	}
	return strings.Join(strings.Fields(out.String()), " ")
}
