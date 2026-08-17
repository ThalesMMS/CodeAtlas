package aiout

import (
	"bytes"
	"encoding/json"
	"fmt"
	"math"
	"strings"
	"unicode/utf8"
)

// MaxResponseBytes is the strict ceiling on a single model response. Anything
// larger is rejected outright rather than parsed.
const MaxResponseBytes = 64 * 1024

// ValidationError aggregates every grounding/shape problem so a single controlled
// retry can be given a compact summary (never the raw, possibly-hostile content).
type ValidationError struct {
	Problems []string
}

func (e *ValidationError) Error() string {
	if len(e.Problems) == 0 {
		return "model output invalid"
	}
	return "model output invalid: " + strings.Join(e.Problems, "; ")
}

// Summary returns a compact, bounded list of problems suitable for a correction
// prompt — no raw model content, just the rule violations.
func (e *ValidationError) Summary() string {
	const max = 10
	problems := e.Problems
	if len(problems) > max {
		problems = problems[:max]
	}
	return strings.Join(problems, "\n")
}

// DecodeStrict parses JSON into v rejecting unknown fields, trailing data and
// oversized payloads. Embedded JSON inside Markdown fences is NOT tolerated: the
// body must be exactly one JSON document.
func DecodeStrict(data []byte, v any) error {
	if len(data) > MaxResponseBytes {
		return &ValidationError{Problems: []string{fmt.Sprintf("response exceeds %d byte limit (%d)", MaxResponseBytes, len(data))}}
	}
	if !utf8.Valid(data) {
		return &ValidationError{Problems: []string{"response is not valid UTF-8"}}
	}
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(v); err != nil {
		return &ValidationError{Problems: []string{"malformed JSON: " + err.Error()}}
	}
	if decoder.More() {
		return &ValidationError{Problems: []string{"trailing data after JSON document"}}
	}
	return nil
}

// AllowSet builds a lookup set from evidence/structure IDs.
func AllowSet(ids []string) map[string]struct{} {
	set := make(map[string]struct{}, len(ids))
	for _, id := range ids {
		set[id] = struct{}{}
	}
	return set
}

type checker struct {
	allowed  map[string]struct{}
	problems []string
}

func (c *checker) fail(format string, args ...any) {
	c.problems = append(c.problems, fmt.Sprintf(format, args...))
}

// evidence validates the cited IDs exist and counts them, enforcing per-claim
// limits. It returns how many valid IDs were cited.
func (c *checker) evidence(label string, ids []string) int {
	if len(ids) > MaxEvidencePerClaim {
		c.fail("%s cites %d evidence IDs (max %d)", label, len(ids), MaxEvidencePerClaim)
	}
	valid := 0
	for _, id := range ids {
		if _, ok := c.allowed[id]; !ok {
			c.fail("%s references unknown evidence ID %q", label, id)
			continue
		}
		valid++
	}
	return valid
}

func (c *checker) codeEvidence(label string, ids []string) {
	if len(ids) > MaxCodeEvidence {
		c.fail("%s selects %d code evidence IDs (max %d)", label, len(ids), MaxCodeEvidence)
	}
	if len(ids) > 0 {
		c.evidence(label, ids)
	}
	seen := make(map[string]struct{}, len(ids))
	for _, id := range ids {
		if _, duplicate := seen[id]; duplicate {
			c.fail("%s selects duplicate evidence ID %q", label, id)
		}
		seen[id] = struct{}{}
	}
}

func (c *checker) text(label, value string) {
	if strings.TrimSpace(value) == "" {
		c.fail("%s text is empty", label)
		return
	}
	if utf8.RuneCountInString(value) > MaxClaimRunes {
		c.fail("%s text exceeds %d runes", label, MaxClaimRunes)
	}
}

func (c *checker) observations(label string, claims []Claim) {
	if len(claims) > MaxClaims {
		c.fail("%s has %d entries (max %d)", label, len(claims), MaxClaims)
	}
	for i, claim := range claims {
		field := fmt.Sprintf("%s[%d]", label, i)
		c.text(field, claim.Text)
		// Every factual claim must be grounded in at least one valid evidence ID.
		if c.evidence(field, claim.EvidenceIDs) == 0 {
			c.fail("%s has no supporting evidence", field)
		}
	}
}

func (c *checker) inferences(claims []Inference) {
	if len(claims) > MaxClaims {
		c.fail("inferences has %d entries (max %d)", len(claims), MaxClaims)
	}
	for i, claim := range claims {
		field := fmt.Sprintf("inferences[%d]", i)
		c.text(field, claim.Text)
		if c.evidence(field, claim.EvidenceIDs) == 0 {
			c.fail("%s has no supporting evidence", field)
		}
		if math.IsNaN(claim.Confidence) || math.IsInf(claim.Confidence, 0) || claim.Confidence < 0 || claim.Confidence > 1 {
			c.fail("%s confidence %v is not finite in [0,1]", field, claim.Confidence)
		}
	}
}

func (c *checker) uncertainties(claims []Uncertainty) {
	if len(claims) > MaxClaims {
		c.fail("uncertainties has %d entries (max %d)", len(claims), MaxClaims)
	}
	for i, claim := range claims {
		field := fmt.Sprintf("uncertainties[%d]", i)
		c.text(field, claim.Text)
		// An uncertainty with no evidence must explain itself.
		if c.evidence(field, claim.EvidenceIDs) == 0 && strings.TrimSpace(claim.Reason) == "" {
			c.fail("%s has neither evidence nor a reason", field)
		}
	}
}

func (c *checker) wikiLimitations(claims []Uncertainty) {
	if len(claims) > MaxClaims {
		c.fail("limitations has %d entries (max %d)", len(claims), MaxClaims)
	}
	for i, limitation := range claims {
		field := fmt.Sprintf("limitations[%d]", i)
		c.text(field, limitation.Text)
		c.text(field+".reason", limitation.Reason)
		c.evidence(field, limitation.EvidenceIDs)
	}
}

func (c *checker) result() error {
	if len(c.problems) == 0 {
		return nil
	}
	return &ValidationError{Problems: c.problems}
}

// ValidateExplanation enforces the grounding rules on a Hover/See More output.
// requireChangeImpact is set for See More.
func ValidateExplanation(allowed map[string]struct{}, exp Explanation, requireChangeImpact bool) error {
	c := &checker{allowed: allowed}
	if exp.SchemaVersion != ExplanationSchemaVersion {
		c.fail("schemaVersion %q != %q", exp.SchemaVersion, ExplanationSchemaVersion)
	}
	c.text("summary", exp.Summary)
	c.codeEvidence("codeEvidenceIds", exp.CodeEvidenceIDs)
	if !requireChangeImpact && len(exp.CodeEvidenceIDs) > 0 {
		c.fail("codeEvidenceIds is not allowed for Hover")
	}
	c.observations("observations", exp.Observations)
	c.inferences(exp.Inferences)
	c.uncertainties(exp.Uncertainties)
	c.observations("changeImpact", exp.ChangeImpact)
	if requireChangeImpact && len(exp.ChangeImpact) == 0 {
		c.fail("changeImpact is required for See More")
	}
	return c.result()
}

// ValidateCodemap enforces grounding on the Codemap narrative: claims cite pack
// evidence; every trace step references an allowed structural ID.
func ValidateCodemap(allowedEvidence, allowedStructure, allowedNodes map[string]struct{}, narr CodemapNarrative) error {
	c := &checker{allowed: allowedEvidence}
	if narr.SchemaVersion != CodemapSchemaVersion {
		c.fail("schemaVersion %q != %q", narr.SchemaVersion, CodemapSchemaVersion)
	}
	c.text("title", narr.Title)
	c.text("overview", narr.Overview)
	c.observations("claims", narr.Claims)
	c.inferences(narr.Inferences)
	c.uncertainties(narr.Uncertainties)
	if len(narr.Trace) > MaxTraceSteps {
		c.fail("trace has %d steps (max %d)", len(narr.Trace), MaxTraceSteps)
	}
	for i, step := range narr.Trace {
		if _, ok := allowedStructure[step]; !ok {
			c.fail("trace[%d] references unknown structural ID %q", i, step)
		}
	}
	if len(narr.Flows) > MaxCodemapFlows {
		c.fail("flows has %d entries (max %d)", len(narr.Flows), MaxCodemapFlows)
	}
	for i, flow := range narr.Flows {
		field := fmt.Sprintf("flows[%d]", i)
		c.text(field+".title", flow.Title)
		if _, ok := allowedNodes[flow.EntryNodeID]; !ok {
			c.fail("%s.entryNodeId references unknown node ID %q", field, flow.EntryNodeID)
		}
		if len(flow.Steps) == 0 {
			c.fail("%s has no steps", field)
		}
		if len(flow.Steps) > MaxFlowSteps {
			c.fail("%s has %d steps (max %d)", field, len(flow.Steps), MaxFlowSteps)
		}
		for j, step := range flow.Steps {
			stepField := fmt.Sprintf("%s.steps[%d]", field, j)
			c.text(stepField+".label", step.Label)
			c.text(stepField+".text", step.Text)
			if _, ok := allowedNodes[step.NodeID]; !ok {
				c.fail("%s.nodeId references unknown node ID %q", stepField, step.NodeID)
			}
		}
	}
	return c.result()
}

// ValidateWiki enforces grounding on a wiki page: every section claim cites pack
// evidence; no free paths or links.
func ValidateWiki(allowed map[string]struct{}, page WikiPageContent, allowedRelated ...map[string]struct{}) error {
	c := &checker{allowed: allowed}
	if page.SchemaVersion != WikiPageSchemaVersion {
		c.fail("schemaVersion %q != %q", page.SchemaVersion, WikiPageSchemaVersion)
	}
	c.text("title", page.Title)
	if len(page.Sections) > MaxSections {
		c.fail("page has %d sections (max %d)", len(page.Sections), MaxSections)
	}
	for i, section := range page.Sections {
		field := fmt.Sprintf("sections[%d]", i)
		c.text(field+".heading", section.Heading)
		c.observations(field+".claims", section.Claims)
		c.codeEvidence(field+".codeEvidenceIds", section.CodeEvidenceIDs)
		if len(section.Tables) > MaxWikiTables {
			c.fail("%s has %d tables (max %d)", field, len(section.Tables), MaxWikiTables)
		}
		for j, table := range section.Tables {
			tableField := fmt.Sprintf("%s.tables[%d]", field, j)
			if table.Kind != "table" {
				c.fail("%s.kind %q != table", tableField, table.Kind)
			}
			if len(table.Columns) == 0 || len(table.Columns) > MaxTableColumns {
				c.fail("%s has %d columns (want 1..%d)", tableField, len(table.Columns), MaxTableColumns)
			}
			for column, value := range table.Columns {
				c.text(fmt.Sprintf("%s.columns[%d]", tableField, column), value)
			}
			if len(table.Rows) > MaxTableRows {
				c.fail("%s has %d rows (max %d)", tableField, len(table.Rows), MaxTableRows)
			}
			for row, values := range table.Rows {
				if len(values) != len(table.Columns) {
					c.fail("%s.rows[%d] has %d cells, want %d", tableField, row, len(values), len(table.Columns))
				}
				for column, value := range values {
					c.text(fmt.Sprintf("%s.rows[%d][%d]", tableField, row, column), value)
				}
			}
			if c.evidence(tableField+".evidenceIds", table.EvidenceIDs) == 0 {
				c.fail("%s has no supporting evidence", tableField)
			}
		}
	}
	if len(page.RelatedPages) > MaxRelatedPages {
		c.fail("relatedPages has %d entries (max %d)", len(page.RelatedPages), MaxRelatedPages)
	}
	var related map[string]struct{}
	if len(allowedRelated) > 0 {
		related = allowedRelated[0]
	}
	seenRelated := make(map[string]struct{}, len(page.RelatedPages))
	for i, slug := range page.RelatedPages {
		if strings.TrimSpace(slug) == "" {
			c.fail("relatedPages[%d] is empty", i)
		}
		if _, duplicate := seenRelated[slug]; duplicate {
			c.fail("relatedPages contains duplicate %q", slug)
		}
		seenRelated[slug] = struct{}{}
		if related == nil {
			c.fail("relatedPages[%d] cannot be validated without a manifest", i)
		} else if _, ok := related[slug]; !ok {
			c.fail("relatedPages[%d] references unknown page %q", i, slug)
		}
	}
	c.inferences(page.Inferences)
	c.wikiLimitations(page.Limitations)
	return c.result()
}
