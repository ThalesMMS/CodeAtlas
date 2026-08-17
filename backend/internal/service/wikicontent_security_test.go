package service

import (
	"strings"
	"testing"

	"github.com/ThalesMMS/CodeAtlas/internal/aiout"
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
