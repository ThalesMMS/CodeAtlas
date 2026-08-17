package service

import (
	"strings"
	"testing"
	"unicode/utf8"
)

func TestServiceTextBoundsPreserveUTF8(t *testing.T) {
	t.Parallel()
	tests := map[string]string{
		"codemap title": truncateTitle(strings.Repeat("x", 9)+"érest", 10),
		"diagnostic":    boundDiagnosticMessage(strings.Repeat("x", maxDiagnosticMessage-1) + "érest"),
	}
	for name, got := range tests {
		if !utf8.ValidString(got) {
			t.Errorf("%s returned invalid UTF-8", name)
		}
	}
}
