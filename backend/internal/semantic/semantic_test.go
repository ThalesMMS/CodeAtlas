package semantic

import (
	"errors"
	"strings"
	"testing"
	"time"
	"unicode/utf8"
)

func TestConfidencePolicyV1(t *testing.T) {
	t.Parallel()
	cases := []struct {
		source, kind string
		resolved     bool
		want         float64
	}{
		{SourceTreeSitter, KindDefinition, false, ConfidenceASTDeclaration},
		{SourceTreeSitter, KindSyntacticCall, false, ConfidenceASTNameCall},
		{SourceTreeSitter, KindSyntacticCall, true, ConfidenceASTDeclaration},
		{SourceTreeSitter, KindReference, false, ConfidenceStructuralUnresolved},
		{SourceGopls, KindDefinition, true, ConfidenceLanguageServerResolved},
		{SourceGopls, KindDiagnostic, true, ConfidenceDiagnostic},
		{SourceRuntime, KindCallOutgoing, true, ConfidenceRuntimeTrace},
	}
	for _, tc := range cases {
		if got := ConfidenceFor(tc.source, tc.kind, tc.resolved); got != tc.want {
			t.Errorf("ConfidenceFor(%s,%s,%v) = %v, want %v", tc.source, tc.kind, tc.resolved, got, tc.want)
		}
	}
}

func TestValidateFact(t *testing.T) {
	t.Parallel()
	valid := SemanticFact{
		Kind:       KindDefinition,
		Confidence: 0.95,
		Provenance: Provenance{Source: SourceGopls, ProviderID: "gopls", ObservedAt: time.Unix(1, 0)},
	}
	if err := ValidateFact(valid); err != nil {
		t.Fatalf("valid fact rejected: %v", err)
	}
	bad := []SemanticFact{
		{Kind: "", Confidence: 1, Provenance: Provenance{Source: "x", ObservedAt: time.Unix(1, 0)}},
		{Kind: KindDefinition, Confidence: 1, Provenance: Provenance{ObservedAt: time.Unix(1, 0)}},                // no source
		{Kind: KindDefinition, Confidence: 1, Provenance: Provenance{Source: "x"}},                                // no observedAt
		{Kind: KindDefinition, Confidence: 1.5, Provenance: Provenance{Source: "x", ObservedAt: time.Unix(1, 0)}}, // confidence > 1
	}
	for i, fact := range bad {
		if err := ValidateFact(fact); err == nil {
			t.Errorf("case %d: invalid fact accepted", i)
		}
	}
}

func TestCapabilityUnsupportedIsTyped(t *testing.T) {
	t.Parallel()
	err := CapabilityUnsupported("textDocument/implementation")
	if !errors.Is(err, ErrCapabilityUnsupported) {
		t.Fatal("CapabilityUnsupported should wrap ErrCapabilityUnsupported")
	}
	if !strings.Contains(err.Error(), "implementation") {
		t.Fatalf("error should name the method: %v", err)
	}
}

func TestSanitizeDetailBounds(t *testing.T) {
	t.Parallel()
	if got := SanitizeDetail("  hi  "); got != "hi" {
		t.Fatalf("trim failed: %q", got)
	}
	long := strings.Repeat("x", maxDetail+500)
	if got := SanitizeDetail(long); len(got) <= maxDetail || !strings.HasSuffix(got, "…") {
		t.Fatalf("oversized detail not bounded: len=%d", len(got))
	}
	multibyte := strings.Repeat("x", maxDetail-1) + "érest"
	if got := SanitizeDetail(multibyte); !utf8.ValidString(got) {
		t.Fatalf("SanitizeDetail returned invalid UTF-8")
	}
}

func TestQueryDeclaresOpenDocument(t *testing.T) {
	t.Parallel()
	if (SemanticQuery{SnapshotID: "s"}).UsesOpenDocument() {
		t.Fatal("a snapshot query should not be an open-document query")
	}
	if !(SemanticQuery{DocumentID: "doc1", DocumentVersion: 3}).UsesOpenDocument() {
		t.Fatal("a query with a documentId should be an open-document query")
	}
}
