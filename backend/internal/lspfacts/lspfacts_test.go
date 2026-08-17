package lspfacts

import (
	"encoding/json"
	"strings"
	"testing"
	"unicode/utf8"
)

func TestBoundStringPreservesUTF8AtByteLimit(t *testing.T) {
	t.Parallel()
	got := BoundString(strings.Repeat("x", MaxHoverBytes-1) + "érest")
	if !utf8.ValidString(got) {
		t.Fatalf("BoundString returned invalid UTF-8")
	}
	if want := strings.Repeat("x", MaxHoverBytes-1) + "…"; got != want {
		t.Fatalf("BoundString result differs at rune boundary: got len=%d want len=%d", len(got), len(want))
	}
}

func TestBoundHoverTextAcceptsMarkedStringObject(t *testing.T) {
	t.Parallel()
	contents := json.RawMessage(`{"language":"typescript","value":"const answer: number"}`)
	if got := BoundHoverText(contents); got != "const answer: number" {
		t.Fatalf("BoundHoverText() = %q", got)
	}
}
