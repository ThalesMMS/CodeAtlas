package service

import (
	"strings"
	"testing"

	"github.com/ThalesMMS/CodeAtlas/internal/aiout"
	"github.com/ThalesMMS/CodeAtlas/internal/contextpack"
)

func TestWikiSourceLinkEscapesMarkdownLabelSyntax(t *testing.T) {
	t.Parallel()
	got := aiout.RenderReference(aiout.SourceReference{
		Label: "label ](https://evil.example)[", Path: "pkg/(unsafe).go", StartLine: 3, EndLine: 4,
	})
	if strings.Contains(got, "](https://evil.example)") {
		t.Fatalf("source link label escaped its backend-owned target: %q", got)
	}
	for _, escaped := range []string{`\]`, `\(`, `\)`, `\[`} {
		if !strings.Contains(got, escaped) {
			t.Fatalf("source link %q does not contain %q", got, escaped)
		}
	}
}

func TestSanitizeWikiPageReferencesDropsModelInventedIDs(t *testing.T) {
	t.Parallel()
	pack := contextpack.ContextPack{Evidence: []contextpack.Evidence{
		{ID: "ev:code", Path: "main.go", DisplayCode: "func main() {}"},
		{ID: "ev:semantic", Path: "main.go"},
	}}
	content := aiout.WikiPageContent{
		SchemaVersion: aiout.WikiPageSchemaVersion,
		Title:         "Overview",
		Sections: []aiout.WikiSection{{
			Heading: "Flow",
			Claims: []aiout.Claim{
				{Text: "supported", EvidenceIDs: []string{"ev:code", "ev:invented"}},
				{Text: "unsupported", EvidenceIDs: []string{"ev:invented"}},
			},
			CodeEvidenceIDs: []string{"ev:code", "ev:semantic", "ev:invented"},
			Tables: []aiout.WikiTable{
				{Kind: "table", Columns: []string{"Name"}, Rows: [][]string{{"value"}}, EvidenceIDs: []string{"ev:semantic", "ev:invented"}},
				{Kind: "table", Columns: []string{"Name"}, Rows: [][]string{{"value"}}, EvidenceIDs: []string{"ev:invented"}},
			},
		}},
		RelatedPages: []string{"architecture", "invented", "architecture"},
		Inferences: []aiout.Inference{
			{Text: "supported inference", EvidenceIDs: []string{"ev:semantic", "ev:invented"}, Confidence: 0.8},
			{Text: "unsupported inference", EvidenceIDs: []string{"ev:invented"}, Confidence: 0.8},
		},
		Limitations: []aiout.Uncertainty{{Text: "bounded", Reason: "scope", EvidenceIDs: []string{"ev:invented"}}},
	}

	pageAllow := aiout.AllowSet([]string{"architecture"})
	sanitizeWikiPageReferences(&content, pack, pageAllow)

	section := content.Sections[0]
	if len(section.Claims) != 1 || strings.Join(section.Claims[0].EvidenceIDs, ",") != "ev:code" {
		t.Fatalf("claims = %+v, want only the grounded claim", section.Claims)
	}
	if strings.Join(section.CodeEvidenceIDs, ",") != "ev:code" {
		t.Fatalf("codeEvidenceIds = %v, want only renderable evidence", section.CodeEvidenceIDs)
	}
	if len(section.Tables) != 1 || strings.Join(section.Tables[0].EvidenceIDs, ",") != "ev:semantic" {
		t.Fatalf("tables = %+v, want only grounded table", section.Tables)
	}
	if strings.Join(content.RelatedPages, ",") != "architecture" || len(content.Inferences) != 1 || len(content.Limitations[0].EvidenceIDs) != 0 {
		t.Fatalf("sanitized references = related:%v inferences:%+v limitations:%+v", content.RelatedPages, content.Inferences, content.Limitations)
	}
	if err := aiout.ValidateWiki(packAllowSet(pack), content, pageAllow); err != nil {
		t.Fatalf("sanitized page validation error = %v", err)
	}
	if err := validateCodeSelections(pack, section.CodeEvidenceIDs); err != nil {
		t.Fatalf("sanitized code selection error = %v", err)
	}
}
