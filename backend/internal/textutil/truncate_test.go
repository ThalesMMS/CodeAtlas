package textutil

import (
	"strings"
	"testing"
	"unicode/utf8"
)

func TestTruncateUTF8HonorsByteBudgetAtRuneBoundary(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name     string
		value    string
		maxBytes int
		want     string
	}{
		{name: "unchanged", value: "café", maxBytes: 5, want: "café"},
		{name: "exact boundary", value: "café", maxBytes: 5, want: "café"},
		{name: "inside two-byte rune", value: "café", maxBytes: 4, want: "caf"},
		{name: "inside four-byte rune", value: "ab😀cd", maxBytes: 4, want: "ab"},
		{name: "zero budget", value: "café", maxBytes: 0, want: ""},
		{name: "negative budget", value: "café", maxBytes: -1, want: ""},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := TruncateUTF8(tc.value, tc.maxBytes)
			if got != tc.want {
				t.Fatalf("TruncateUTF8(%q, %d) = %q, want %q", tc.value, tc.maxBytes, got, tc.want)
			}
			budget := tc.maxBytes
			if budget < 0 {
				budget = 0
			}
			if len(got) > budget {
				t.Fatalf("result uses %d bytes, budget %d", len(got), tc.maxBytes)
			}
			if !utf8.ValidString(got) {
				t.Fatalf("result is not valid UTF-8: %q", got)
			}
		})
	}
}

func TestCompactCodePreservesUTF8AtByteLimit(t *testing.T) {
	t.Parallel()
	got := CompactCode(strings.Repeat("x", 9)+"érest", 10)
	if !utf8.ValidString(got) {
		t.Fatalf("CompactCode returned invalid UTF-8")
	}
	if got != strings.Repeat("x", 9)+"\n…" {
		t.Fatalf("CompactCode() = %q", got)
	}
}

func TestTruncateRunesIncludesMarkerWithinBudget(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name     string
		value    string
		maxRunes int
		want     string
	}{
		{name: "unchanged", value: "café", maxRunes: 4, want: "café"},
		{name: "truncated unicode", value: "long café", maxRunes: 4, want: "lon…"},
		{name: "marker only", value: "long", maxRunes: 1, want: "…"},
		{name: "zero budget", value: "long", maxRunes: 0, want: ""},
		{name: "negative budget", value: "long", maxRunes: -1, want: ""},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			got := TruncateRunes(tc.value, tc.maxRunes)
			if got != tc.want {
				t.Fatalf("TruncateRunes(%q, %d) = %q, want %q", tc.value, tc.maxRunes, got, tc.want)
			}
			if tc.maxRunes >= 0 && utf8.RuneCountInString(got) > tc.maxRunes {
				t.Fatalf("result uses %d runes, budget %d", utf8.RuneCountInString(got), tc.maxRunes)
			}
		})
	}
}

func TestCompactMessageCollapsesWhitespaceAndTruncatesRunes(t *testing.T) {
	t.Parallel()
	if got := CompactMessage("  error\n with\t spaces  ", 100); got != "error with spaces" {
		t.Fatalf("CompactMessage() = %q", got)
	}
	if got := CompactMessage("long café", 4); got != "lon…" {
		t.Fatalf("CompactMessage() truncated = %q, want lon…", got)
	}
	if got := CompactMessage("error", 0); got != "" {
		t.Fatalf("CompactMessage() zero budget = %q", got)
	}
}
