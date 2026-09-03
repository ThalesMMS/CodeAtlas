package eval

import (
	"context"
	"encoding/json"
	"errors"
	"strings"

	"github.com/ThalesMMS/CodeAtlas/internal/ai"
	"github.com/ThalesMMS/CodeAtlas/internal/aiout"
	"github.com/ThalesMMS/CodeAtlas/internal/contextpack"
)

// qualityProvider is a deterministic, network-free provider used to exercise
// the real surface pipelines in CI. It selects only IDs already present in the
// supplied contracts; prose quality is measured separately by golden fixtures
// and the optional judge.
type qualityProvider struct{}

func (qualityProvider) Name() string    { return "offline-quality-eval" }
func (qualityProvider) Available() bool { return true }
func (qualityProvider) Embed(context.Context, []string) ([][]float64, error) {
	return nil, ai.ErrUnavailable
}

func (qualityProvider) Complete(_ context.Context, systemPrompt, userPrompt string, _ int) (string, error) {
	switch {
	case strings.Contains(systemPrompt, aiout.CodemapSchemaVersion):
		return offlineCodemapResponse(userPrompt)
	case strings.Contains(systemPrompt, aiout.WikiPageSchemaVersion):
		return offlineWikiResponse(userPrompt)
	case strings.Contains(systemPrompt, aiout.WikiPlanSchemaVersion):
		return "", errors.New("offline quality evaluation uses the deterministic wiki manifest")
	case strings.Contains(systemPrompt, aiout.ExplanationSchemaVersion):
		return offlineExplanationResponse(systemPrompt, userPrompt)
	default:
		return "", errors.New("unsupported offline quality prompt")
	}
}

func offlineExplanationResponse(systemPrompt, userPrompt string) (string, error) {
	var pack contextpack.ContextPack
	if err := decodeTaggedJSON(userPrompt, "CODEATLAS_CONTEXT_PACK", &pack); err != nil {
		return "", err
	}
	ids := firstEvidenceIDs(pack.Evidence, 3)
	if len(ids) == 0 {
		return "", errors.New("explanation pack has no evidence")
	}
	output := aiout.Explanation{
		SchemaVersion: aiout.ExplanationSchemaVersion,
		Summary:       "The target is described by indexed repository evidence.",
		Observations: []aiout.Claim{{
			Text: "The target and its role are present in the indexed evidence.", EvidenceIDs: ids[:1],
		}},
		Inferences:    []aiout.Inference{},
		Uncertainties: []aiout.Uncertainty{},
		ChangeImpact:  []aiout.Claim{},
	}
	if strings.Contains(systemPrompt, "See Also") || strings.Contains(systemPrompt, "REQUIRED") {
		output.Summary = "The package exposes grounded domain, service, persistence, and usage evidence."
		output.ChangeImpact = []aiout.Claim{{Text: "Changing the central contract can affect its grounded callers.", EvidenceIDs: ids[:1]}}
		output.CodeEvidenceIDs = selectableCodeEvidence(pack.Evidence, 3)
	}
	return marshalJSON(output)
}

func offlineCodemapResponse(userPrompt string) (string, error) {
	var input struct {
		Nodes []struct {
			ID    string `json:"id"`
			Label string `json:"label"`
		} `json:"nodes"`
		SuggestedFlows []struct {
			Title       string   `json:"title"`
			EntryNodeID string   `json:"entryNodeId"`
			NodeIDs     []string `json:"nodeIds"`
		} `json:"suggestedFlows"`
	}
	if err := decodeTaggedJSON(userPrompt, "CODEATLAS_CODEMAP_STRUCTURE", &input); err != nil {
		return "", err
	}
	labels := make(map[string]string, len(input.Nodes))
	for _, node := range input.Nodes {
		labels[node.ID] = node.Label
	}
	flows := make([]aiout.CodemapFlow, 0, len(input.SuggestedFlows))
	for flowIndex, suggestion := range input.SuggestedFlows {
		flow := aiout.CodemapFlow{Title: suggestion.Title, EntryNodeID: suggestion.EntryNodeID}
		for stepIndex, nodeID := range suggestion.NodeIDs {
			if _, exists := labels[nodeID]; !exists || len(flow.Steps) >= aiout.MaxFlowSteps {
				continue
			}
			flow.Steps = append(flow.Steps, aiout.CodemapFlowStep{
				Label: string(rune('1'+flowIndex)) + string(rune('a'+stepIndex)), NodeID: nodeID,
				Text: "Continue through " + labels[nodeID] + ".",
			})
		}
		if flow.EntryNodeID != "" && len(flow.Steps) > 0 {
			flows = append(flows, flow)
		}
		if len(flows) >= aiout.MaxCodemapFlows {
			break
		}
	}
	if len(flows) == 0 && len(input.Nodes) > 0 {
		flows = []aiout.CodemapFlow{{
			Title: "Main flow", EntryNodeID: input.Nodes[0].ID,
			Steps: []aiout.CodemapFlowStep{{Label: "1a", NodeID: input.Nodes[0].ID, Text: "Start at " + input.Nodes[0].Label + "."}},
		}}
	}
	var trace []string
	for _, flow := range flows {
		for _, step := range flow.Steps {
			if len(trace) < aiout.MaxTraceSteps {
				trace = append(trace, step.NodeID)
			}
		}
	}
	claims := []aiout.Claim{}
	if len(input.Nodes) > 0 {
		claims = append(claims, aiout.Claim{Text: "The flow is grounded in the validated structural graph.", EvidenceIDs: []string{input.Nodes[0].ID}})
	}
	return marshalJSON(aiout.CodemapNarrative{
		SchemaVersion: aiout.CodemapSchemaVersion, Title: "Order handler flow",
		Overview:   strings.Repeat("The handler flow connects dependency wiring, request processing, validation, and persistence using grounded repository evidence. ", 2),
		Motivation: strings.Repeat("The application separates transport, service, and repository responsibilities so each observed layer owns a clear part of order creation. ", 2),
		Details:    strings.Repeat("The generated guide follows backend-suggested entrypoints and validates every step against an indexed node. Source locations and snippets come from backend-owned bytes after the model response passes grounding checks. ", 3), Trace: trace, Flows: flows,
		Claims: claims, Inferences: []aiout.Inference{}, Uncertainties: []aiout.Uncertainty{},
	})
}

func offlineWikiResponse(userPrompt string) (string, error) {
	var pack contextpack.ContextPack
	if err := decodeTaggedJSON(userPrompt, "CODEATLAS_CONTEXT_PACK", &pack); err != nil {
		return "", err
	}
	var plan struct {
		Title               string   `json:"title"`
		Archetype           string   `json:"archetype"`
		AllowedRelatedPages []string `json:"allowedRelatedPages"`
	}
	if err := decodeTaggedJSON(userPrompt, "CODEATLAS_WIKI_PAGE_PLAN", &plan); err != nil {
		return "", err
	}
	ids := firstEvidenceIDs(pack.Evidence, 1)
	if len(ids) == 0 {
		return "", errors.New("wiki pack has no evidence")
	}
	selectedTitle := ""
	for _, evidence := range pack.Evidence {
		if string(evidence.ID) == ids[0] {
			selectedTitle = evidence.Title
			break
		}
	}
	related := plan.AllowedRelatedPages
	if len(related) > 2 {
		related = related[:2]
	}
	return marshalJSON(aiout.WikiPageContent{
		SchemaVersion: aiout.WikiPageSchemaVersion, Title: plan.Title,
		Sections: []aiout.WikiSection{{
			Heading:         "Responsibilities and flow",
			Claims:          []aiout.Claim{{Text: "This page is grounded in its scoped repository evidence.", EvidenceIDs: ids}},
			CodeEvidenceIDs: selectableCodeEvidence(pack.Evidence, 1),
			Tables: []aiout.WikiTable{{
				Kind: "table", Columns: []string{"Element", "Archetype"},
				Rows: [][]string{{selectedTitle, plan.Archetype}}, EvidenceIDs: ids,
			}},
		}},
		RelatedPages: related, Inferences: []aiout.Inference{}, Limitations: []aiout.Uncertainty{},
	})
}

func firstEvidenceIDs(evidence []contextpack.Evidence, limit int) []string {
	ids := make([]string, 0, limit)
	for _, item := range evidence {
		if item.ID == "" {
			continue
		}
		ids = append(ids, string(item.ID))
		if len(ids) >= limit {
			break
		}
	}
	return ids
}

func selectableCodeEvidence(evidence []contextpack.Evidence, limit int) []string {
	var ids []string
	seen := make(map[string]struct{})
	for _, relation := range []string{"definition", "usage_site", ""} {
		for _, item := range evidence {
			if item.Kind != contextpack.KindASTObservation || item.Relation != relation || item.Content == "" {
				continue
			}
			ids = append(ids, string(item.ID))
			seen[string(item.ID)] = struct{}{}
			if len(ids) >= limit {
				return ids
			}
			break
		}
	}
	for _, item := range evidence {
		if item.Kind != contextpack.KindASTObservation || item.Content == "" {
			continue
		}
		if _, duplicate := seen[string(item.ID)]; duplicate {
			continue
		}
		ids = append(ids, string(item.ID))
		if len(ids) >= limit {
			break
		}
	}
	return ids
}

func decodeTaggedJSON(prompt, tag string, target any) error {
	start := "<" + tag + ">\n"
	end := "\n</" + tag + ">"
	startIndex := strings.Index(prompt, start)
	if startIndex < 0 {
		return errors.New("missing " + tag + " block")
	}
	startIndex += len(start)
	endIndex := strings.Index(prompt[startIndex:], end)
	if endIndex < 0 {
		return errors.New("unterminated " + tag + " block")
	}
	return json.Unmarshal([]byte(prompt[startIndex:startIndex+endIndex]), target)
}

func marshalJSON(value any) (string, error) {
	encoded, err := json.Marshal(value)
	return string(encoded), err
}
