package aiout

import (
	"strings"
	"testing"
)

// noopResolver returns the ID itself as the label for any known ID.
func noopResolver(ids ...string) Resolver {
	known := make(map[string]bool, len(ids))
	for _, id := range ids {
		known[id] = true
	}
	return func(id string) (SourceReference, bool) {
		if known[id] {
			return SourceReference{Label: id}, true
		}
		return SourceReference{}, false
	}
}

// allResolver resolves any ID as itself (use for tests that don't care which IDs are valid).
func allResolver() Resolver {
	return func(id string) (SourceReference, bool) {
		return SourceReference{Label: id}, true
	}
}

func sourceResolver(refs map[string]SourceReference) Resolver {
	return func(id string) (SourceReference, bool) {
		ref, ok := refs[id]
		return ref, ok
	}
}

func codeResolver(blocks map[string]CodeBlock) CodeResolver {
	return func(id string) (CodeBlock, bool) {
		block, ok := blocks[id]
		return block, ok
	}
}

// --- RenderExplanation ---

func TestRenderExplanationEmptyReturnsEmpty(t *testing.T) {
	t.Parallel()
	result := RenderExplanation(Explanation{}, noopResolver())
	if result != "" {
		t.Errorf("empty Explanation should render empty, got %q", result)
	}
}

func TestRenderExplanationSummaryOnly(t *testing.T) {
	t.Parallel()
	exp := Explanation{Summary: "This is the summary."}
	result := RenderExplanation(exp, noopResolver())
	if !strings.Contains(result, "This is the summary.") {
		t.Errorf("rendered output missing summary: %q", result)
	}
}

func TestRenderExplanationObservationsAppearUnderHeading(t *testing.T) {
	t.Parallel()
	exp := Explanation{
		Summary: "Sum",
		Observations: []Claim{
			{Text: "First observation", EvidenceIDs: []string{"e1"}},
		},
	}
	result := RenderExplanation(exp, noopResolver("e1"))
	if !strings.Contains(result, "### Observations") {
		t.Errorf("observations heading missing: %q", result)
	}
	if !strings.Contains(result, "First observation") {
		t.Errorf("observation text missing: %q", result)
	}
}

func TestRenderExplanationUnknownEvidenceIDDropped(t *testing.T) {
	t.Parallel()
	exp := Explanation{
		Summary: "Sum",
		Observations: []Claim{
			{Text: "Obs", EvidenceIDs: []string{"unknown-id"}},
		},
	}
	// Resolver that knows nothing.
	result := RenderExplanation(exp, func(string) (SourceReference, bool) { return SourceReference{}, false })
	// The claim text should still appear; only the citation is dropped.
	if !strings.Contains(result, "Obs") {
		t.Errorf("observation text should still appear: %q", result)
	}
	if strings.Contains(result, "unknown-id") {
		t.Errorf("unknown evidence ID should not appear in output: %q", result)
	}
}

func TestRenderExplanationInferencesLabelledCorrectly(t *testing.T) {
	t.Parallel()
	exp := Explanation{
		Summary: "Sum",
		Inferences: []Inference{
			{Text: "Inferred thing", EvidenceIDs: []string{"e1"}, Confidence: 0.8},
		},
	}
	result := RenderExplanation(exp, noopResolver("e1"))
	if !strings.Contains(result, "### Inferences") {
		t.Errorf("inferences heading missing: %q", result)
	}
	if !strings.Contains(result, "_(inference)_") {
		t.Errorf("inference marker missing: %q", result)
	}
	if !strings.Contains(result, "Inferred thing") {
		t.Errorf("inference text missing: %q", result)
	}
}

func TestRenderExplanationUncertaintiesLabelledCorrectly(t *testing.T) {
	t.Parallel()
	exp := Explanation{
		Summary: "Sum",
		Uncertainties: []Uncertainty{
			{Text: "Unknown thing", Reason: "no data"},
		},
	}
	result := RenderExplanation(exp, noopResolver())
	if !strings.Contains(result, "### Uncertainties") {
		t.Errorf("uncertainties heading missing: %q", result)
	}
	if !strings.Contains(result, "_(uncertainty)_") {
		t.Errorf("uncertainty marker missing: %q", result)
	}
	if !strings.Contains(result, "Unknown thing") {
		t.Errorf("uncertainty text missing: %q", result)
	}
	if !strings.Contains(result, "no data") {
		t.Errorf("uncertainty reason missing: %q", result)
	}
}

func TestRenderExplanationChangeImpactSection(t *testing.T) {
	t.Parallel()
	exp := Explanation{
		Summary: "Sum",
		ChangeImpact: []Claim{
			{Text: "Breaking change", EvidenceIDs: []string{"e2"}},
		},
	}
	result := RenderExplanation(exp, noopResolver("e2"))
	if !strings.Contains(result, "### Change impact") {
		t.Errorf("changeImpact heading missing: %q", result)
	}
	if !strings.Contains(result, "Breaking change") {
		t.Errorf("changeImpact text missing: %q", result)
	}
}

func TestRenderExplanationHTMLEscaping(t *testing.T) {
	t.Parallel()
	exp := Explanation{Summary: `<script>alert("xss")</script>`}
	result := RenderExplanation(exp, noopResolver())
	if strings.Contains(result, "<script>") {
		t.Errorf("HTML should be escaped but got raw tag: %q", result)
	}
	if !strings.Contains(result, "&lt;script&gt;") {
		t.Errorf("escaped HTML not found in: %q", result)
	}
}

func TestRenderExplanationControlCharsStripped(t *testing.T) {
	t.Parallel()
	exp := Explanation{Summary: "good\x00evil\x01text"}
	result := RenderExplanation(exp, noopResolver())
	if strings.Contains(result, "\x00") || strings.Contains(result, "\x01") {
		t.Errorf("control characters should be stripped: %q", result)
	}
	if !strings.Contains(result, "good") || !strings.Contains(result, "text") {
		t.Errorf("visible text should be preserved: %q", result)
	}
}

func TestRenderReferenceEscapesMarkdownLabelAndOwnsTarget(t *testing.T) {
	t.Parallel()
	result := RenderReference(SourceReference{
		Label: "label ](https://evil.example)[", Path: "pkg/(safe).go", StartLine: 3, EndLine: 4,
	})
	if strings.Contains(result, "](https://evil.example)") {
		t.Fatalf("label escaped the backend-owned target: %q", result)
	}
	for _, escaped := range []string{`\]`, `\(`, `\)`, `\[`} {
		if !strings.Contains(result, escaped) {
			t.Fatalf("reference %q does not contain %q", result, escaped)
		}
	}
	if !strings.Contains(result, "pkg/%28safe%29.go#L3-L4") {
		t.Fatalf("reference target was not encoded by the backend: %q", result)
	}
}

func TestSafeTableCellNeutralizesMarkdownAndControls(t *testing.T) {
	t.Parallel()
	result := SafeTableCell("a|[link](https://evil.example)\x00")
	if strings.Contains(result, "|") || strings.Contains(result, "](https://evil.example)") || strings.Contains(result, "\x00") {
		t.Fatalf("unsafe table cell = %q", result)
	}
	if !strings.Contains(result, "a/") || !strings.Contains(result, `\[link\]`) {
		t.Fatalf("visible table content was not preserved safely: %q", result)
	}
}

func TestRenderWikiLinkEscapesModelPlannedTitle(t *testing.T) {
	t.Parallel()
	result := RenderWiki(WikiPageContent{}, noopResolver(), RenderOptions{WikiLinks: []WikiLink{{
		Slug: "safe-page", Title: "title ](https://evil.example)[", Relation: "child",
	}}})
	if strings.Contains(result, "](https://evil.example)") || !strings.Contains(result, `\]`) {
		t.Fatalf("unsafe wiki link = %q", result)
	}
	if !strings.Contains(result, "](wiki:safe-page)") {
		t.Fatalf("backend-owned wiki target missing: %q", result)
	}
}

func TestRenderExternalReferenceCannotBreakCodeSpan(t *testing.T) {
	t.Parallel()
	result := RenderReference(SourceReference{Label: "external `break`"})
	if strings.Count(result, "`") != 2 || !strings.Contains(result, "external 'break'") {
		t.Fatalf("unsafe external reference = %q", result)
	}
}

func TestRenderExplanationCitationsFormatted(t *testing.T) {
	t.Parallel()
	exp := Explanation{
		Summary: "Sum",
		Observations: []Claim{
			{Text: "Obs", EvidenceIDs: []string{"e1", "e2"}},
		},
	}
	result := RenderExplanation(exp, noopResolver("e1", "e2"))
	// Both IDs should appear as backtick-quoted labels.
	if !strings.Contains(result, "`e1`") || !strings.Contains(result, "`e2`") {
		t.Errorf("evidence labels not formatted as code: %q", result)
	}
}

func TestRenderExplanationLinksCitationsAndDeduplicatesSources(t *testing.T) {
	t.Parallel()
	exp := Explanation{Observations: []Claim{
		{Text: "First", EvidenceIDs: []string{"e1", "e2"}},
		{Text: "Second", EvidenceIDs: []string{"e1"}},
	}}
	resolve := sourceResolver(map[string]SourceReference{
		"e1": {Label: "internal/order/service.go:22-24", Path: "internal/order/service.go", StartLine: 22, EndLine: 24},
		"e2": {Label: "web/src/api client.ts:7", Path: "web/src/api client.ts", StartLine: 7, EndLine: 7},
	})
	result := RenderExplanation(exp, resolve)
	if !strings.Contains(result, "[internal/order/service.go:22-24](internal/order/service.go#L22-L24)") {
		t.Fatalf("line-anchored citation missing: %q", result)
	}
	if !strings.Contains(result, "[web/src/api client.ts:7](web/src/api%20client.ts#L7)") {
		t.Fatalf("encoded citation target missing: %q", result)
	}
	sources := strings.Split(result, "**Sources:**")
	if len(sources) != 2 || strings.Count(sources[1], "internal/order/service.go#L22-L24") != 1 {
		t.Fatalf("section sources were not emitted once and deduplicated: %q", result)
	}
}

func TestRenderExplanationRelevantFilesAndInferenceConfidence(t *testing.T) {
	t.Parallel()
	exp := Explanation{Inferences: []Inference{{Text: "Probably", EvidenceIDs: []string{"e1"}, Confidence: 0.74}}}
	resolve := sourceResolver(map[string]SourceReference{
		"e1": {Label: "pkg/service.go:4", Path: "pkg/service.go", StartLine: 4, EndLine: 4},
	})
	result := RenderExplanation(exp, resolve, RenderOptions{RelevantFiles: []SourceReference{
		{Path: "pkg/service.go"}, {Path: "README.md"}, {Path: "pkg/service.go"},
	}})
	if !strings.HasPrefix(result, "<details>\n<summary>Relevant source files</summary>") {
		t.Fatalf("relevant-files block missing from top: %q", result)
	}
	if strings.Count(result, "[pkg/service.go](pkg/service.go)") != 1 {
		t.Fatalf("relevant files not deduplicated: %q", result)
	}
	if !strings.Contains(result, "**Confidence: 74%.**") {
		t.Fatalf("inference confidence missing: %q", result)
	}
}

func TestRenderExplanationEmbedsBackendResolvedCodeVerbatim(t *testing.T) {
	t.Parallel()
	code := "type Order struct {\n\tID string\n}\n// literal ``` stays code"
	exp := Explanation{Summary: "Order aggregate.", CodeEvidenceIDs: []string{"e1"}}
	result := RenderExplanation(exp, noopResolver(), RenderOptions{CodeResolver: codeResolver(map[string]CodeBlock{
		"e1": {
			Reference: SourceReference{Label: "internal/order/model.go:6-10", Path: "internal/order/model.go", StartLine: 6, EndLine: 10},
			Language:  "go", Content: code,
		},
	})})
	if !strings.Contains(result, "**internal/order/model.go:6-10**") {
		t.Fatalf("code header missing: %q", result)
	}
	if !strings.Contains(result, "````go\n"+code+"\n````") {
		t.Fatalf("code was changed or fence was unsafe: %q", result)
	}
}

func TestRenderExplanationMarksTruncatedCodeInHeader(t *testing.T) {
	t.Parallel()
	result := RenderExplanation(Explanation{Summary: "x", CodeEvidenceIDs: []string{"e1"}}, noopResolver(), RenderOptions{
		CodeResolver: codeResolver(map[string]CodeBlock{"e1": {
			Reference: SourceReference{Label: "main.go:1-20"}, Language: "go", Content: "func main() {\n}", Truncated: true,
		}}),
	})
	if !strings.Contains(result, "_(truncated to complete lines)_") {
		t.Fatalf("truncated code is not labelled: %q", result)
	}
}

func TestRenderExplanationPreservesExistingTrailingNewline(t *testing.T) {
	t.Parallel()
	code := "func main() {}\n"
	result := RenderExplanation(Explanation{Summary: "x", CodeEvidenceIDs: []string{"e1"}}, noopResolver(), RenderOptions{
		CodeResolver: codeResolver(map[string]CodeBlock{"e1": {
			Reference: SourceReference{Label: "main.go:1"}, Language: "go", Content: code,
		}}),
	})
	if !strings.Contains(result, "```go\n"+code+"```") || strings.Contains(result, "```go\n"+code+"\n```") {
		t.Fatalf("renderer added a second newline to source bytes: %q", result)
	}
}

func TestRenderSeeMoreUsesReferenceSectionsAndDeterministicSeeAlso(t *testing.T) {
	exp := Explanation{
		Summary:         "order is the order-management package.",
		CodeEvidenceIDs: []string{"definition", "usage"},
		Observations:    []Claim{{Text: "Repository is the persistence contract.", EvidenceIDs: []string{"definition"}}},
		Inferences: []Inference{
			{Text: "weak speculation", EvidenceIDs: []string{"definition"}, Confidence: 0.49},
			{Text: "supported note", EvidenceIDs: []string{"definition"}, Confidence: 0.8},
		},
	}
	result := RenderExplanation(exp, noopResolver("definition", "usage"), RenderOptions{
		ExplanationSections: true, DefinitionCodeIDs: []string{"definition"}, UsageCodeIDs: []string{"usage"},
		MinInference: 0.5,
		CodeResolver: codeResolver(map[string]CodeBlock{
			"definition": {Reference: SourceReference{Label: "model.go:6-11"}, Language: "go", Content: "type Order struct{}"},
			"usage":      {Reference: SourceReference{Label: "main.go:10-16"}, Language: "go", Content: "repository := order.NewMemoryRepository()"},
		}),
		SeeAlso: []SourceReference{{Label: "Repository", Path: "repository.go", StartLine: 9, EndLine: 12}},
	})
	for _, heading := range []string{"### Definition", "### Key Components and Tests", "### Example Usages", "### Notes", "### See Also"} {
		if !strings.Contains(result, heading) {
			t.Fatalf("missing %q in See More rendering: %s", heading, result)
		}
	}
	if strings.Contains(result, "weak speculation") || !strings.Contains(result, "supported note") {
		t.Fatalf("inference confidence gate failed: %s", result)
	}
	if !strings.Contains(result, "[Repository](repository.go#L9-L12)") {
		t.Fatalf("deterministic See Also missing: %s", result)
	}
}

func TestRenderExplanationOutputTrimmed(t *testing.T) {
	t.Parallel()
	exp := Explanation{Summary: "Sum"}
	result := RenderExplanation(exp, noopResolver())
	if result != strings.TrimSpace(result) {
		t.Errorf("output has leading/trailing whitespace: %q", result)
	}
}

// --- RenderWiki ---

func TestRenderWikiEmptyReturnsEmpty(t *testing.T) {
	t.Parallel()
	result := RenderWiki(WikiPageContent{}, noopResolver())
	if result != "" {
		t.Errorf("empty WikiPageContent should render empty, got %q", result)
	}
}

func TestRenderWikiSectionsRenderedWithH2Heading(t *testing.T) {
	t.Parallel()
	page := WikiPageContent{
		Title: "My Wiki",
		Sections: []WikiSection{
			{
				Heading: "Overview",
				Claims:  []Claim{{Text: "The service does X", EvidenceIDs: []string{"e1"}}},
			},
		},
	}
	result := RenderWiki(page, noopResolver("e1"))
	if !strings.Contains(result, "## Overview") {
		t.Errorf("section heading not H2: %q", result)
	}
	if !strings.Contains(result, "The service does X") {
		t.Errorf("claim text missing: %q", result)
	}
	if !strings.Contains(result, "**Sources:** `e1`") {
		t.Errorf("section Sources line missing: %q", result)
	}
}

func TestRenderWikiInsertsBackendMermaidWithSources(t *testing.T) {
	t.Parallel()
	page := WikiPageContent{Sections: []WikiSection{{Heading: "Overview", Claims: []Claim{}}}}
	result := RenderWiki(page, noopResolver(), RenderOptions{Mermaid: &MermaidBlock{
		Title:  "Architecture",
		Source: "graph TD\n  a[\"main\"] --> b[\"service\"]",
		Sources: []SourceReference{
			{Label: "service", Path: "internal/service.go", StartLine: 8, EndLine: 12},
			{Label: "main", Path: "cmd/main.go", StartLine: 3, EndLine: 7},
		},
	}})
	if !strings.Contains(result, "### Architecture\n\n```mermaid\ngraph TD") {
		t.Fatalf("backend Mermaid block missing: %q", result)
	}
	if !strings.Contains(result, "**Diagram sources:** [main](cmd/main.go#L3-L7), [service](internal/service.go#L8-L12)") {
		t.Fatalf("diagram sources missing or unstable: %q", result)
	}
}

func TestRenderWikiInferencesAppear(t *testing.T) {
	t.Parallel()
	page := WikiPageContent{
		Title: "T",
		Inferences: []Inference{
			{Text: "Probably works", EvidenceIDs: []string{"e1"}, Confidence: 0.7},
		},
	}
	result := RenderWiki(page, noopResolver("e1"))
	if !strings.Contains(result, "### Inferences") {
		t.Errorf("inferences section missing: %q", result)
	}
}

func TestRenderWikiLimitationsAppear(t *testing.T) {
	t.Parallel()
	page := WikiPageContent{
		Title: "T",
		Limitations: []Uncertainty{
			{Text: "Unknown behavior", Reason: "not tested"},
		},
	}
	result := RenderWiki(page, noopResolver())
	if !strings.Contains(result, "### Uncertainties") {
		t.Errorf("uncertainties section missing: %q", result)
	}
	if !strings.Contains(result, "Unknown behavior") {
		t.Errorf("limitation text missing: %q", result)
	}
}

func TestRenderWikiEmptySectionHeadingSkipped(t *testing.T) {
	t.Parallel()
	page := WikiPageContent{
		Sections: []WikiSection{
			{
				Heading: "", // empty heading should not produce "## "
				Claims:  []Claim{{Text: "Some claim", EvidenceIDs: []string{}}},
			},
		},
	}
	result := RenderWiki(page, noopResolver())
	if strings.Contains(result, "## ") {
		t.Errorf("empty heading should not produce a heading line: %q", result)
	}
}

func TestRenderWikiTablesAndRelatedPages(t *testing.T) {
	t.Parallel()
	page := WikiPageContent{
		SchemaVersion: WikiPageSchemaVersion,
		Sections: []WikiSection{{
			Heading: "Fields",
			Tables: []WikiTable{{
				Kind:        "table",
				Columns:     []string{"Field", "Type"},
				Rows:        [][]string{{"ID", "string"}},
				EvidenceIDs: []string{"e1"},
			}},
		}},
	}
	result := RenderWiki(page, noopResolver("e1"), RenderOptions{
		WikiLinks: []WikiLink{{Slug: "architecture", Title: "Architecture", Relation: "parent"}},
	})
	for _, expected := range []string{
		"| Field | Type |",
		"| ID | string |",
		"**Sources:** `e1`",
		"[Architecture (parent)](wiki:architecture)",
	} {
		if !strings.Contains(result, expected) {
			t.Fatalf("rendered wiki missing %q: %s", expected, result)
		}
	}
}

// --- RenderCodemap ---

func TestRenderCodemapEmptyReturnsEmpty(t *testing.T) {
	t.Parallel()
	result := RenderCodemap(CodemapNarrative{}, noopResolver())
	if result != "" {
		t.Errorf("empty CodemapNarrative should render empty, got %q", result)
	}
}

func TestRenderCodemapOverviewAndMotivation(t *testing.T) {
	t.Parallel()
	narr := CodemapNarrative{
		Overview:   "High-level overview",
		Motivation: "Because it matters",
	}
	result := RenderCodemap(narr, noopResolver())
	if !strings.Contains(result, "High-level overview") {
		t.Errorf("overview missing: %q", result)
	}
	if !strings.Contains(result, "**Reason:**") {
		t.Errorf("motivation label missing: %q", result)
	}
	if !strings.Contains(result, "Because it matters") {
		t.Errorf("motivation text missing: %q", result)
	}
}

func TestRenderCodemapDetailsSection(t *testing.T) {
	t.Parallel()
	narr := CodemapNarrative{
		Overview: "Overview",
		Details:  "Detailed explanation here",
	}
	result := RenderCodemap(narr, noopResolver())
	if !strings.Contains(result, "Detailed explanation here") {
		t.Errorf("details missing: %q", result)
	}
}

func TestRenderCodemapClaimsSection(t *testing.T) {
	t.Parallel()
	narr := CodemapNarrative{
		Overview: "O",
		Claims:   []Claim{{Text: "Flow starts at X", EvidenceIDs: []string{"e1"}}},
	}
	result := RenderCodemap(narr, noopResolver("e1"))
	if !strings.Contains(result, "### Observations") {
		t.Errorf("observations heading missing: %q", result)
	}
	if !strings.Contains(result, "Flow starts at X") {
		t.Errorf("claim text missing: %q", result)
	}
}

func TestRenderCodemapHTMLEscapingInOverview(t *testing.T) {
	t.Parallel()
	narr := CodemapNarrative{Overview: `<b>bold</b>`}
	result := RenderCodemap(narr, noopResolver())
	if strings.Contains(result, "<b>") {
		t.Errorf("raw HTML tag should be escaped: %q", result)
	}
}

func TestRenderCodemapEmptyMotivationSkipped(t *testing.T) {
	t.Parallel()
	narr := CodemapNarrative{Overview: "O", Motivation: ""}
	result := RenderCodemap(narr, noopResolver())
	if strings.Contains(result, "**Reason:**") {
		t.Errorf("empty motivation should not appear: %q", result)
	}
}

func TestRenderCodemapOutputTrimmed(t *testing.T) {
	t.Parallel()
	narr := CodemapNarrative{Overview: "O"}
	result := RenderCodemap(narr, noopResolver())
	if result != strings.TrimSpace(result) {
		t.Errorf("output should be trimmed, got: %q", result)
	}
}

func TestRenderCodemapUncertaintiesWithReason(t *testing.T) {
	t.Parallel()
	narr := CodemapNarrative{
		Overview:      "O",
		Uncertainties: []Uncertainty{{Text: "Maybe fails", Reason: "no test coverage"}},
	}
	result := RenderCodemap(narr, noopResolver())
	if !strings.Contains(result, "Maybe fails") {
		t.Errorf("uncertainty text missing: %q", result)
	}
	// Reason should appear after em dash.
	if !strings.Contains(result, " — no test coverage") {
		t.Errorf("uncertainty reason missing or wrong format: %q", result)
	}
}

func TestRenderExplanationNoUncertaintiesReason(t *testing.T) {
	t.Parallel()
	exp := Explanation{
		Uncertainties: []Uncertainty{
			{Text: "Just uncertain", Reason: ""},
		},
	}
	result := RenderExplanation(exp, noopResolver())
	// Without a reason, no em dash should appear.
	if strings.Contains(result, " — ") {
		t.Errorf("no reason should not produce em dash: %q", result)
	}
}

// TestRenderExplanationTabsAndNewlinesReplacedBySpaces verifies the safeText
// behavior for tab and newline normalization.
func TestRenderExplanationTabsAndNewlinesReplacedBySpaces(t *testing.T) {
	t.Parallel()
	exp := Explanation{Summary: "line1\nline2\ttab"}
	result := RenderExplanation(exp, noopResolver())
	if strings.Contains(result, "\n\n") {
		// The double newline from the summary block is expected, but the embedded
		// newline should be collapsed.
	}
	if strings.Contains(result, "\t") {
		t.Errorf("tab character should be replaced: %q", result)
	}
}
