package gopls

import (
	"strings"
	"testing"
	"unicode/utf8"
)

func TestBoundStringPreservesUTF8AtByteLimit(t *testing.T) {
	t.Parallel()
	got := boundString(strings.Repeat("x", maxHoverBytes-1) + "érest")
	if !utf8.ValidString(got) {
		t.Fatalf("boundString returned invalid UTF-8")
	}
}
