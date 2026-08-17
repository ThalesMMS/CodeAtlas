package service

import (
	"strings"

	"github.com/ThalesMMS/CodeAtlas/internal/aiout"
	"github.com/ThalesMMS/CodeAtlas/internal/contextpack"
	"github.com/ThalesMMS/CodeAtlas/internal/diagram"
	"github.com/ThalesMMS/CodeAtlas/internal/domain"
)

func codemapDiagram(nodes []domain.CodemapNode, edges []domain.CodemapEdge) *domain.MermaidDiagram {
	diagramNodes := make([]diagram.Node, 0, len(nodes))
	for _, node := range nodes {
		diagramNodes = append(diagramNodes, diagram.Node{
			ID: node.ID, Label: node.Label, Kind: node.Kind, Group: node.Group,
			Path: node.Path, Range: node.Range, Relevance: node.Relevance,
			External: node.Kind == "external" || node.Kind == "unresolved" || node.Path == "",
		})
	}
	diagramEdges := make([]diagram.Edge, 0, len(edges))
	for _, edge := range edges {
		diagramEdges = append(diagramEdges, diagram.Edge{
			ID: edge.ID, Source: edge.Source, Target: edge.Target, Type: edge.Type, Confidence: edge.Confidence,
		})
	}
	result := diagram.Flowchart(diagramNodes, diagramEdges, diagram.DefaultMaxNodes)
	if result.Source == "" {
		return nil
	}
	return &result
}

func packDiagram(symbolsByID map[string]domain.Symbol, pack contextpack.ContextPack) *domain.MermaidDiagram {
	nodes := make([]diagram.Node, 0, len(pack.Evidence))
	for _, evidence := range pack.Evidence {
		if evidence.ID == "" || evidence.Path == "" {
			continue
		}
		label := evidence.Title
		kind := evidence.Kind
		path := evidence.Path
		rangeValue := evidence.Range
		if symbol, ok := symbolsByID[string(evidence.SymbolID)]; ok {
			label = symbol.Name
			kind = symbol.Kind
			path = symbol.Path
			rangeValue = symbol.Range
		}
		if separator := strings.Index(label, " — "); separator >= 0 {
			label = label[:separator]
		}
		nodes = append(nodes, diagram.Node{
			ID: string(evidence.ID), Label: label, Kind: kind, Group: sourceGroup(path, kind),
			Path: path, Range: rangeValue, Relevance: evidence.Relevance,
		})
	}
	edges := make([]diagram.Edge, 0, len(pack.Graph.Edges))
	for _, edge := range pack.Graph.Edges {
		edges = append(edges, diagram.Edge{
			ID:     edge.Type + ":" + string(edge.From) + ":" + string(edge.To),
			Source: string(edge.From), Target: string(edge.To), Type: edge.Type,
			Confidence: 1,
		})
	}
	result := diagram.Sequence(nodes, edges, "", diagram.DefaultMaxDepth)
	if result.Source == "" {
		result = diagram.Flowchart(nodes, edges, diagram.DefaultMaxNodes)
	}
	if result.Source == "" {
		return nil
	}
	return &result
}

func sourceGroup(path, kind string) string {
	if kind == "test" || isTestSourcePath(path) {
		return "Tests"
	}
	path = strings.ReplaceAll(path, `\`, "/")
	parts := strings.Split(strings.Trim(path, "/"), "/")
	if len(parts) > 1 && parts[0] != "" {
		return parts[0]
	}
	if kind == "file" {
		return "Files"
	}
	return "Root"
}

func isTestSourcePath(filePath string) bool {
	normalized := strings.ToLower(strings.ReplaceAll(filePath, `\`, "/"))
	parts := strings.Split(strings.Trim(normalized, "/"), "/")
	for _, component := range parts[:max(0, len(parts)-1)] {
		if component == "test" || component == "tests" || component == "__tests__" {
			return true
		}
	}
	base := parts[len(parts)-1]
	return strings.HasSuffix(base, "_test.go") || strings.Contains(base, ".test.") || strings.Contains(base, ".spec.")
}

func diagramMermaidBlock(value *domain.MermaidDiagram) *aiout.MermaidBlock {
	if value == nil || value.Source == "" {
		return nil
	}
	refs := make([]aiout.SourceReference, 0, len(value.Sources))
	for _, source := range value.Sources {
		refs = append(refs, aiout.SourceReference{
			Label: sourceLocationLabel(source.Path, source.Range.Start.Line, source.Range.End.Line),
			Path:  source.Path, StartLine: source.Range.Start.Line, EndLine: source.Range.End.Line,
		})
	}
	return &aiout.MermaidBlock{Title: "Architecture diagram", Source: value.Source, Sources: refs}
}
