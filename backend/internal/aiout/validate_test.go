package aiout

import (
	"strings"
	"testing"
)

func allowed() map[string]struct{} { return AllowSet([]string{"ev:1", "ev:2"}) }

func validExplanation() Explanation {
	return Explanation{
		SchemaVersion: ExplanationSchemaVersion,
		Summary:       "Symbol summary.",
		Observations:  []Claim{{Text: "Faz X.", EvidenceIDs: []string{"ev:1"}}},
		Inferences:    []Inference{{Text: "Probably Y.", EvidenceIDs: []string{"ev:2"}, Confidence: 0.6}},
		Uncertainties: []Uncertainty{{Text: "Z cannot be determined.", Reason: "no runtime evidence"}},
		ChangeImpact:  []Claim{{Text: "Mudar afeta W.", EvidenceIDs: []string{"ev:1"}}},
	}
}

func TestValidateExplanationAcceptsGrounded(t *testing.T) {
	t.Parallel()
	if err := ValidateExplanation(allowed(), validExplanation(), true); err != nil {
		t.Fatalf("valid explanation rejected: %v", err)
	}
}

func TestValidateExplanationRejectsUngrounded(t *testing.T) {
	t.Parallel()
	cases := map[string]func(*Explanation){
		"unknown evidence ID":     func(e *Explanation) { e.Observations[0].EvidenceIDs = []string{"ev:999"} },
		"observation no evidence": func(e *Explanation) { e.Observations[0].EvidenceIDs = nil },
		"confidence out of range": func(e *Explanation) { e.Inferences[0].Confidence = 1.5 },
		"uncertainty no reason":   func(e *Explanation) { e.Uncertainties[0] = Uncertainty{Text: "x"} },
		"wrong schema version":    func(e *Explanation) { e.SchemaVersion = "explanation/v9" },
	}
	for name, mutate := range cases {
		t.Run(name, func(t *testing.T) {
			exp := validExplanation()
			mutate(&exp)
			if err := ValidateExplanation(allowed(), exp, true); err == nil {
				t.Fatalf("%s: expected rejection", name)
			}
		})
	}
}

func TestValidateExplanationRequiresChangeImpactForSeeMore(t *testing.T) {
	t.Parallel()
	exp := validExplanation()
	exp.ChangeImpact = nil
	if err := ValidateExplanation(allowed(), exp, true); err == nil {
		t.Fatal("See More without changeImpact must be rejected")
	}
	// Hover (requireChangeImpact=false) tolerates an empty changeImpact.
	if err := ValidateExplanation(allowed(), exp, false); err != nil {
		t.Fatalf("Hover without changeImpact rejected: %v", err)
	}
}

func TestValidateExplanationCodeEvidenceIsGroundedAndHoverFree(t *testing.T) {
	t.Parallel()
	exp := validExplanation()
	exp.CodeEvidenceIDs = []string{"ev:1"}
	if err := ValidateExplanation(allowed(), exp, true); err != nil {
		t.Fatalf("grounded See More code selection rejected: %v", err)
	}
	if err := ValidateExplanation(allowed(), exp, false); err == nil || !strings.Contains(err.Error(), "not allowed for Hover") {
		t.Fatalf("Hover code selection error = %v", err)
	}
	exp.CodeEvidenceIDs = []string{"ev:missing"}
	if err := ValidateExplanation(allowed(), exp, true); err == nil || !strings.Contains(err.Error(), "unknown evidence ID") {
		t.Fatalf("unknown code evidence error = %v", err)
	}
}

func TestValidateExplanationRejectsDuplicateCodeEvidenceLocally(t *testing.T) {
	t.Parallel()
	exp := validExplanation()
	exp.CodeEvidenceIDs = []string{"ev:1", "ev:1"}
	if err := ValidateExplanation(allowed(), exp, true); err == nil || !strings.Contains(err.Error(), "duplicate evidence ID") {
		t.Fatalf("duplicate code evidence error = %v", err)
	}
}

func TestValidateWikiCodeEvidenceIsGrounded(t *testing.T) {
	t.Parallel()
	page := validWikiPage()
	page.Sections[0].CodeEvidenceIDs = []string{"ev:UNKNOWN"}
	if err := ValidateWiki(allowed(), page); err == nil || !strings.Contains(err.Error(), "unknown evidence ID") {
		t.Fatalf("unknown wiki code evidence error = %v", err)
	}
}

func TestValidateWikiTablesAndRelatedPages(t *testing.T) {
	t.Parallel()
	page := validWikiPage()
	page.RelatedPages = []string{"architecture"}
	page.Sections[0].Tables = []WikiTable{{
		Kind: "table", Columns: []string{"Field", "Type"},
		Rows: [][]string{{"ID", "string"}}, EvidenceIDs: []string{"ev:1"},
	}}
	if err := ValidateWiki(allowed(), page, AllowSet([]string{"architecture"})); err != nil {
		t.Fatalf("grounded table/related page rejected: %v", err)
	}
	page.Sections[0].Tables[0].Rows[0] = []string{"missing second cell"}
	if err := ValidateWiki(allowed(), page, AllowSet([]string{"architecture"})); err == nil {
		t.Fatal("table row with the wrong column count was accepted")
	}
	page = validWikiPage()
	page.RelatedPages = []string{"invented"}
	if err := ValidateWiki(allowed(), page, AllowSet([]string{"architecture"})); err == nil {
		t.Fatal("invented related page was accepted")
	}
}

func TestValidateWikiRejectsDuplicateSelectionsLocally(t *testing.T) {
	t.Parallel()
	page := validWikiPage()
	page.Sections[0].CodeEvidenceIDs = []string{"ev:1", "ev:1"}
	page.RelatedPages = []string{"architecture", "architecture"}
	err := ValidateWiki(allowed(), page, AllowSet([]string{"architecture"}))
	if err == nil || !strings.Contains(err.Error(), "duplicate evidence ID") || !strings.Contains(err.Error(), "relatedPages contains duplicate") {
		t.Fatalf("duplicate wiki selection error = %v", err)
	}
}

func TestDecodeStrictRejectsUnknownFieldsAndFences(t *testing.T) {
	t.Parallel()
	var exp Explanation
	// Unknown field.
	if err := DecodeStrict([]byte(`{"schemaVersion":"explanation/v2","summary":"x","rogue":1}`), &exp); err == nil {
		t.Fatal("unknown field accepted")
	}
	// Markdown fence is not permissively unwrapped.
	if err := DecodeStrict([]byte("```json\n{\"summary\":\"x\"}\n```"), &exp); err == nil {
		t.Fatal("markdown-fenced JSON accepted")
	}
	// Trailing data.
	if err := DecodeStrict([]byte(`{"summary":"x"}{"more":1}`), &exp); err == nil {
		t.Fatal("trailing data accepted")
	}
	// Oversized.
	big := `{"summary":"` + strings.Repeat("a", MaxResponseBytes) + `"}`
	if err := DecodeStrict([]byte(big), &exp); err == nil {
		t.Fatal("oversized response accepted")
	}
}

func TestRenderExplanationEscapesHTMLAndResolvesCitations(t *testing.T) {
	t.Parallel()
	exp := Explanation{
		SchemaVersion: ExplanationSchemaVersion,
		Summary:       "<script>alert(1)</script>",
		Observations:  []Claim{{Text: "faz X", EvidenceIDs: []string{"ev:1"}}},
	}
	resolve := func(id string) (SourceReference, bool) {
		if id == "ev:1" {
			return SourceReference{Label: "pkg/svc.go:4-6", Path: "pkg/svc.go", StartLine: 4, EndLine: 6}, true
		}
		return SourceReference{}, false
	}
	out := RenderExplanation(exp, resolve)
	if strings.Contains(out, "<script>") {
		t.Fatalf("raw script survived rendering: %q", out)
	}
	if !strings.Contains(out, "&lt;script&gt;") {
		t.Fatalf("script was not escaped: %q", out)
	}
	// The backend-resolved citation appears; the model never wrote the path.
	if !strings.Contains(out, "pkg/svc.go:4-6") {
		t.Fatalf("resolved citation missing: %q", out)
	}
	if !strings.Contains(out, "(pkg/svc.go#L4-L6)") {
		t.Fatalf("resolved citation is not line-anchored: %q", out)
	}
}

func TestValidateCodemapTraceMustUseStructuralIDs(t *testing.T) {
	t.Parallel()
	allowedEvidence := AllowSet([]string{"ev:1"})
	allowedStructure := AllowSet([]string{"node-1", "edge-1"})
	narr := CodemapNarrative{
		SchemaVersion: CodemapSchemaVersion,
		Title:         "Map",
		Overview:      strings.Repeat("Grounded overview sentence. ", 4),
		Motivation:    strings.Repeat("The application needs this flow to keep transport and business responsibilities explicit. ", 3),
		Details:       strings.Repeat("The request crosses a validated sequence of local calls, changes the order state, and reaches the repository through an observed boundary. ", 4),
		Claims:        []Claim{{Text: "calls", EvidenceIDs: []string{"ev:1"}}},
		Trace:         []string{"node-1", "edge-1"},
		Flows:         []CodemapFlow{{Title: "Flow", EntryNodeID: "node-1", Steps: []CodemapFlowStep{{Label: "1a", NodeID: "node-1", Text: "Starts"}}}},
	}
	if err := ValidateCodemap(allowedEvidence, allowedStructure, AllowSet([]string{"node-1"}), narr); err != nil {
		t.Fatalf("valid codemap rejected: %v", err)
	}
	narr.Trace = []string{"node-INVENTADO"}
	if err := ValidateCodemap(allowedEvidence, allowedStructure, AllowSet([]string{"node-1"}), narr); err == nil {
		t.Fatal("trace with invented structural ID accepted")
	}
	narr.Trace = nil
	narr.Flows[0].Steps[0].NodeID = "node-INVENTADO"
	if err := ValidateCodemap(allowedEvidence, allowedStructure, AllowSet([]string{"node-1"}), narr); err == nil {
		t.Fatal("flow step with invented node ID accepted")
	}
}

func TestValidateCodemapRequiresNarrativeDepth(t *testing.T) {
	t.Parallel()
	base := CodemapNarrative{
		SchemaVersion: CodemapSchemaVersion,
		Title:         "Order creation",
		Overview:      strings.Repeat("Grounded overview sentence. ", 4),
		Motivation:    strings.Repeat("The application needs this flow to keep transport and business responsibilities explicit. ", 3),
		Details:       strings.Repeat("The request crosses a validated sequence of local calls, changes the order state, and reaches the repository through an observed boundary. ", 4),
	}
	for _, test := range []struct {
		name   string
		mutate func(*CodemapNarrative)
		field  string
	}{
		{name: "overview", mutate: func(n *CodemapNarrative) { n.Overview = "Too short." }, field: "overview"},
		{name: "motivation", mutate: func(n *CodemapNarrative) { n.Motivation = "Too short." }, field: "motivation"},
		{name: "details", mutate: func(n *CodemapNarrative) { n.Details = "Too short." }, field: "details"},
	} {
		t.Run(test.name, func(t *testing.T) {
			narrative := base
			test.mutate(&narrative)
			err := ValidateCodemap(AllowSet(nil), AllowSet(nil), AllowSet(nil), narrative)
			if err == nil || !strings.Contains(err.Error(), test.field) {
				t.Fatalf("short %s error = %v, want narrative depth rejection", test.field, err)
			}
		})
	}
}

// --- AllowSet ---

func TestAllowSetContainsMembersOnly(t *testing.T) {
	t.Parallel()
	set := AllowSet([]string{"ev:1", "ev:2", "ev:3"})
	for _, id := range []string{"ev:1", "ev:2", "ev:3"} {
		if _, ok := set[id]; !ok {
			t.Errorf("AllowSet missing member %q", id)
		}
	}
	if _, ok := set["ev:99"]; ok {
		t.Error("AllowSet should not contain ev:99")
	}
}

func TestAllowSetNilInput(t *testing.T) {
	t.Parallel()
	set := AllowSet(nil)
	if len(set) != 0 {
		t.Errorf("AllowSet(nil) should be empty, got len=%d", len(set))
	}
}

func TestAllowSetEmptySlice(t *testing.T) {
	t.Parallel()
	set := AllowSet([]string{})
	if len(set) != 0 {
		t.Errorf("AllowSet(empty) should be empty, got len=%d", len(set))
	}
}

// --- ValidationError ---

func TestValidationErrorNoProblemsFallback(t *testing.T) {
	t.Parallel()
	err := &ValidationError{}
	if err.Error() != "model output invalid" {
		t.Errorf("Error() = %q, want 'model output invalid'", err.Error())
	}
}

func TestValidationErrorJoinsProblemsWithSemicolon(t *testing.T) {
	t.Parallel()
	err := &ValidationError{Problems: []string{"alpha", "beta"}}
	msg := err.Error()
	if !strings.Contains(msg, "alpha") || !strings.Contains(msg, "beta") {
		t.Errorf("Error() should contain both problems: %q", msg)
	}
	if !strings.Contains(msg, ";") {
		t.Errorf("Error() should join problems with semicolon: %q", msg)
	}
}

func TestValidationErrorSummaryCapAt10(t *testing.T) {
	t.Parallel()
	problems := make([]string, 15)
	for i := range problems {
		problems[i] = "issue"
	}
	ve := &ValidationError{Problems: problems}
	summary := ve.Summary()
	lines := strings.Split(strings.TrimRight(summary, "\n"), "\n")
	if len(lines) > 10 {
		t.Errorf("Summary() should return at most 10 lines, got %d", len(lines))
	}
}

func TestValidationErrorSummaryFewProblems(t *testing.T) {
	t.Parallel()
	ve := &ValidationError{Problems: []string{"first", "second", "third"}}
	summary := ve.Summary()
	for _, p := range []string{"first", "second", "third"} {
		if !strings.Contains(summary, p) {
			t.Errorf("Summary() missing %q: %q", p, summary)
		}
	}
}

// --- DecodeStrict edge cases ---

func TestDecodeStrictRejectsInvalidUTF8(t *testing.T) {
	t.Parallel()
	var v any
	if err := DecodeStrict([]byte{'"', 0xff, 0xfe, '"'}, &v); err == nil {
		t.Fatal("invalid UTF-8 should be rejected")
	}
}

func TestDecodeStrictAcceptsValidMinimalJSON(t *testing.T) {
	t.Parallel()
	var v any
	if err := DecodeStrict([]byte(`{}`), &v); err != nil {
		t.Fatalf("DecodeStrict({}) = %v, want nil", err)
	}
}

func TestDecodeStrictSizeExactlyAtLimitIsAccepted(t *testing.T) {
	t.Parallel()
	prefix := `{"k":"`
	suffix := `"}`
	padding := strings.Repeat("a", MaxResponseBytes-len(prefix)-len(suffix))
	data := []byte(prefix + padding + suffix)
	if len(data) != MaxResponseBytes {
		t.Fatalf("test setup: payload is %d bytes, want %d", len(data), MaxResponseBytes)
	}
	var v map[string]string
	if err := DecodeStrict(data, &v); err != nil {
		t.Errorf("DecodeStrict at exactly the limit should succeed: %v", err)
	}
}

// --- ValidateWiki ---

func validWikiPage() WikiPageContent {
	return WikiPageContent{
		SchemaVersion: WikiPageSchemaVersion,
		Title:         "My Wiki",
		Sections: []WikiSection{
			{
				Heading: "Introduction",
				Claims:  []Claim{{Text: "This service handles payments.", EvidenceIDs: []string{"ev:1"}}},
			},
		},
	}
}

func TestValidateWikiAcceptsValidPage(t *testing.T) {
	t.Parallel()
	if err := ValidateWiki(allowed(), validWikiPage()); err != nil {
		t.Fatalf("ValidateWiki = %v, want nil", err)
	}
}

func TestValidateWikiWrongSchemaVersion(t *testing.T) {
	t.Parallel()
	page := validWikiPage()
	page.SchemaVersion = "wiki-page/v99"
	if err := ValidateWiki(allowed(), page); err == nil {
		t.Fatal("expected rejection for wrong schema version")
	}
}

func TestValidateWikiEmptyTitle(t *testing.T) {
	t.Parallel()
	page := validWikiPage()
	page.Title = ""
	if err := ValidateWiki(allowed(), page); err == nil {
		t.Fatal("expected rejection for empty title")
	}
}

func TestValidateWikiUnknownEvidenceInSection(t *testing.T) {
	t.Parallel()
	page := validWikiPage()
	page.Sections[0].Claims[0].EvidenceIDs = []string{"ev:UNKNOWN"}
	if err := ValidateWiki(allowed(), page); err == nil {
		t.Fatal("expected rejection for unknown evidence ID")
	}
}

func TestValidateWikiTooManySections(t *testing.T) {
	t.Parallel()
	page := validWikiPage()
	for i := 0; i <= MaxSections; i++ {
		page.Sections = append(page.Sections, WikiSection{
			Heading: "Extra",
			Claims:  []Claim{{Text: "claim", EvidenceIDs: []string{"ev:1"}}},
		})
	}
	if err := ValidateWiki(allowed(), page); err == nil {
		t.Fatal("expected rejection when sections exceed MaxSections")
	}
}

func TestValidateWikiLimitationRequiresReasonEvenWithEvidence(t *testing.T) {
	t.Parallel()
	page := validWikiPage()
	page.Limitations = []Uncertainty{
		{Text: "Some limit", EvidenceIDs: []string{"ev:1"}, Reason: ""},
	}
	if err := ValidateWiki(allowed(), page); err == nil || !strings.Contains(err.Error(), "reason") {
		t.Fatalf("limitation without reason error = %v, want required reason", err)
	}
}

func TestValidateWikiLimitationAcceptsRequiredReasonWithoutEvidence(t *testing.T) {
	t.Parallel()
	page := validWikiPage()
	page.Limitations = []Uncertainty{
		{Text: "Unknown", Reason: "No runtime evidence is indexed.", EvidenceIDs: nil},
	}
	if err := ValidateWiki(allowed(), page); err != nil {
		t.Fatalf("limitation with reason rejected: %v", err)
	}
}

func TestValidateWikiClaimLongTextRejected(t *testing.T) {
	t.Parallel()
	longText := strings.Repeat("x", MaxClaimRunes+1)
	page := WikiPageContent{
		SchemaVersion: WikiPageSchemaVersion,
		Title:         "T",
		Sections: []WikiSection{
			{Heading: "H", Claims: []Claim{{Text: longText, EvidenceIDs: []string{"ev:1"}}}},
		},
	}
	if err := ValidateWiki(allowed(), page); err == nil {
		t.Fatal("expected rejection for claim text exceeding MaxClaimRunes")
	}
}

func TestValidateWikiTooManyClaimsPerSection(t *testing.T) {
	t.Parallel()
	claims := make([]Claim, MaxClaims+1)
	for i := range claims {
		claims[i] = Claim{Text: "c", EvidenceIDs: []string{"ev:1"}}
	}
	page := WikiPageContent{
		SchemaVersion: WikiPageSchemaVersion,
		Title:         "T",
		Sections:      []WikiSection{{Heading: "H", Claims: claims}},
	}
	if err := ValidateWiki(allowed(), page); err == nil {
		t.Fatal("expected rejection when section claims exceed MaxClaims")
	}
}

// --- ValidateCodemap extra edge cases ---

func TestValidateCodemapEmptyTitle(t *testing.T) {
	t.Parallel()
	narr := CodemapNarrative{
		SchemaVersion: CodemapSchemaVersion,
		Title:         "",
		Overview:      "overview",
	}
	if err := ValidateCodemap(AllowSet(nil), AllowSet(nil), AllowSet(nil), narr); err == nil {
		t.Fatal("expected rejection for empty title")
	}
}

func TestValidateCodemapTooManyTraceSteps(t *testing.T) {
	t.Parallel()
	trace := make([]string, MaxTraceSteps+1)
	for i := range trace {
		trace[i] = "n"
	}
	narr := CodemapNarrative{
		SchemaVersion: CodemapSchemaVersion,
		Title:         "T",
		Overview:      "O",
		Trace:         trace,
	}
	if err := ValidateCodemap(AllowSet(nil), AllowSet([]string{"n"}), AllowSet([]string{"n"}), narr); err == nil {
		t.Fatal("expected rejection when trace exceeds MaxTraceSteps")
	}
}

func TestValidateExplanationTooManyObservations(t *testing.T) {
	t.Parallel()
	exp := validExplanation()
	for i := 0; i <= MaxClaims; i++ {
		exp.Observations = append(exp.Observations, Claim{Text: "obs", EvidenceIDs: []string{"ev:1"}})
	}
	if err := ValidateExplanation(allowed(), exp, false); err == nil {
		t.Fatal("expected rejection when observations exceed MaxClaims")
	}
}

func TestValidateExplanationTooManyEvidenceIDs(t *testing.T) {
	t.Parallel()
	ids := make([]string, MaxEvidencePerClaim+1)
	for i := range ids {
		ids[i] = "ev:1"
	}
	exp := validExplanation()
	exp.Observations = []Claim{{Text: "obs", EvidenceIDs: ids}}
	if err := ValidateExplanation(allowed(), exp, false); err == nil {
		t.Fatal("expected rejection when evidence IDs per claim exceed MaxEvidencePerClaim")
	}
}
