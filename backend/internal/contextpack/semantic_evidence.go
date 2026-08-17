package contextpack

import (
	"strings"

	"github.com/ThalesMMS/CodeAtlas/internal/domain"
	"github.com/ThalesMMS/CodeAtlas/internal/semantic"
)

// SemanticEvidenceFromFact converts a provider fact into bounded ContextPack
// evidence. External URIs are represented as textual boundaries with an empty
// path, so the frontend can never turn them into workspace navigation links.
func SemanticEvidenceFromFact(fact semantic.SemanticFact, method string) Evidence {
	path := fact.Location.Path
	rng := fact.Location.Range
	content := fact.Detail
	title := semanticRelationTitle(fact)
	if strings.Contains(path, "://") {
		path = ""
		rng = domain.Range{}
		if fact.Subject.Name != "" {
			title = fact.Subject.Name + " (external boundary)"
		} else {
			title = "external definition boundary"
		}
		if strings.TrimSpace(content) == "" {
			content = "Definition is outside the indexed workspace."
		}
	}
	return Evidence{
		Kind:        KindLSPFact,
		SymbolID:    fact.Subject.SymbolID,
		Path:        path,
		Range:       rng,
		Title:       title,
		Content:     content,
		ContentHash: shortHash(content),
		Relation:    semanticRelationOf(fact.Kind),
		Relevance:   fact.Confidence,
		Confidence:  fact.Confidence,
		Provenance:  []Provenance{{Source: fact.Provenance.Source, Detail: method}},
	}
}

func semanticRelationTitle(fact semantic.SemanticFact) string {
	if fact.Object != nil && fact.Object.Name != "" {
		return fact.Object.Name
	}
	if fact.Subject.Name != "" {
		return fact.Subject.Name
	}
	return fact.Kind
}

func semanticRelationOf(kind string) string {
	switch kind {
	case semantic.KindDefinition:
		return "definition"
	case semantic.KindReference:
		return "reference"
	case semantic.KindImplementation:
		return "implements"
	case semantic.KindCallIncoming:
		return "called_by"
	case semantic.KindCallOutgoing:
		return "calls"
	case semantic.KindDiagnostic:
		return "diagnostic"
	case semantic.KindHoverType:
		return "type"
	default:
		return ""
	}
}
