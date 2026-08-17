package store

import (
	"strings"
	"testing"
	"unicode/utf8"

	"github.com/ThalesMMS/CodeAtlas/internal/domain"
	"github.com/ThalesMMS/CodeAtlas/internal/storederive"
)

func TestSnippetKeepsUTF8Boundaries(t *testing.T) {
	t.Parallel()
	content := strings.Repeat("a", 89) + "é" + strings.Repeat("b", 400)
	got := storederive.Snippet(domain.Symbol{DocComment: content}, []string{"é"})
	if !utf8.ValidString(got) {
		t.Fatalf("snippet returned invalid UTF-8: %q", got)
	}
}
