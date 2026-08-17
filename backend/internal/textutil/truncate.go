// Package textutil contains small, shared text-boundary helpers.
package textutil

import (
	"strings"
	"unicode/utf8"
)

// TruncateUTF8 returns a prefix no larger than maxBytes without splitting a
// UTF-8 rune. The byte budget intentionally excludes any suffix a caller may
// append after truncation.
func TruncateUTF8(value string, maxBytes int) string {
	if maxBytes <= 0 {
		return ""
	}
	if len(value) <= maxBytes {
		return value
	}
	cut := maxBytes
	for cut > 0 && !utf8.RuneStart(value[cut]) {
		cut--
	}
	return value[:cut]
}

// TruncateRunes bounds text by Unicode code points. When truncation is needed,
// the standard omission marker is included within maxRunes.
func TruncateRunes(value string, maxRunes int) string {
	if maxRunes <= 0 {
		return ""
	}
	runes := []rune(value)
	if len(runes) <= maxRunes {
		return value
	}
	if maxRunes == 1 {
		return "…"
	}
	return string(runes[:maxRunes-1]) + "…"
}

// CompactCode trims code and, when necessary, truncates it at a UTF-8 boundary
// before appending the standard omission marker.
func CompactCode(value string, maxBytes int) string {
	value = strings.TrimSpace(value)
	if maxBytes <= 0 || len(value) <= maxBytes {
		return value
	}
	return TruncateUTF8(value, maxBytes) + "\n…"
}

// CompactMessage collapses whitespace and bounds a user-facing message by
// runes, appending the standard omission marker when truncated.
func CompactMessage(value string, maxRunes int) string {
	value = strings.Join(strings.Fields(value), " ")
	return TruncateRunes(value, maxRunes)
}
