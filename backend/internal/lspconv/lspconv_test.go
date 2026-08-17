package lspconv

import (
	"path/filepath"
	"testing"
)

func TestLSPRoundTripUTF16WithSurrogates(t *testing.T) {
	t.Parallel()
	// Line 1 has a BMP accented char (é, 1 UTF-16 unit, 2 bytes) and a supplementary
	// emoji (🚀, surrogate pair = 2 UTF-16 units, 4 bytes).
	source := []byte("café 🚀 x\nsecond line\n")
	starts := LineStarts(source)

	// The space before "x" is after "café " (4 chars + space=5) + "🚀"(2) + space(1) = 8 UTF-16 units.
	offset, err := LSPToByteOffset(source, starts, 0, 8, EncodingUTF16)
	if err != nil {
		t.Fatalf("LSPToByteOffset: %v", err)
	}
	if source[offset] != 'x' {
		t.Fatalf("expected to land on 'x', got %q at offset %d", source[offset], offset)
	}
	// Round-trip the offset back to an LSP position.
	line, character, err := ByteOffsetToLSP(source, starts, offset, EncodingUTF16)
	if err != nil {
		t.Fatalf("ByteOffsetToLSP: %v", err)
	}
	if line != 0 || character != 8 {
		t.Fatalf("round-trip = (%d,%d), want (0,8)", line, character)
	}
}

func TestUTF8EncodingUsesByteColumns(t *testing.T) {
	t.Parallel()
	source := []byte("café x\n")
	starts := LineStarts(source)
	// In utf-8, the 'x' is at byte column 6 (café = 5 bytes + space).
	offset, err := LSPToByteOffset(source, starts, 0, 6, EncodingUTF8)
	if err != nil || source[offset] != 'x' {
		t.Fatalf("utf-8 offset = %d (%v), want 'x'", offset, err)
	}
}

func TestRejectsMidRuneAndOutOfRange(t *testing.T) {
	t.Parallel()
	source := []byte("🚀x\n") // emoji is 2 UTF-16 units
	starts := LineStarts(source)
	// Character 1 falls inside the surrogate pair.
	if _, err := LSPToByteOffset(source, starts, 0, 1, EncodingUTF16); err == nil {
		t.Fatal("a character inside a surrogate pair should be rejected")
	}
	// Character beyond the line.
	if _, err := LSPToByteOffset(source, starts, 0, 99, EncodingUTF16); err == nil {
		t.Fatal("a character beyond the line should be rejected")
	}
	// Line out of range.
	if _, err := LSPToByteOffset(source, starts, 9, 0, EncodingUTF16); err == nil {
		t.Fatal("a line out of range should be rejected")
	}
}

func TestCRLFColumns(t *testing.T) {
	t.Parallel()
	source := []byte("ab\r\ncd\r\n")
	starts := LineStarts(source)
	// End of line 0 (after "ab") is character 2, regardless of CRLF.
	offset, err := LSPToByteOffset(source, starts, 0, 2, EncodingUTF16)
	if err != nil {
		t.Fatalf("LSPToByteOffset: %v", err)
	}
	if source[offset] != '\r' {
		t.Fatalf("end-of-line offset should point at \\r, got %q", source[offset])
	}
}

func TestInternalConversions(t *testing.T) {
	t.Parallel()
	source := []byte("func A() {}\n")
	starts := LineStarts(source)
	// Internal (1-based line, 1-based byte column) for the 'A' at byte column 6.
	lspLine, lspChar, err := InternalToLSP(source, starts, 1, 6, EncodingUTF16)
	if err != nil {
		t.Fatalf("InternalToLSP: %v", err)
	}
	internalLine, byteCol, err := LSPToInternal(source, starts, lspLine, lspChar, EncodingUTF16)
	if err != nil {
		t.Fatalf("LSPToInternal: %v", err)
	}
	if internalLine != 1 || byteCol != 6 {
		t.Fatalf("internal round-trip = (%d,%d), want (1,6)", internalLine, byteCol)
	}
}

func TestLineForOffsetFindsLastLineAndExactBoundaries(t *testing.T) {
	t.Parallel()
	starts := make([]int, 10_000)
	for index := range starts {
		starts[index] = index * 10
	}
	for _, test := range []struct {
		offset int
		line   int
	}{{0, 0}, {9, 0}, {10, 1}, {99_999, 9_999}} {
		if got := lineForOffset(starts, test.offset); got != test.line {
			t.Fatalf("lineForOffset(%d) = %d, want %d", test.offset, got, test.line)
		}
	}
}

func TestURIRoundTripAndWorkspaceScope(t *testing.T) {
	t.Parallel()
	workspace := filepath.Join(t.TempDir(), "ws")
	inside := filepath.Join(workspace, "pkg", "a.go")
	outside := filepath.Join(t.TempDir(), "outside.go")
	uri := PathToURI(inside)
	decoded, err := URIToPath(uri)
	if err != nil || filepath.Clean(decoded) != filepath.Clean(inside) {
		t.Fatalf("uri round-trip = %q (%v), want %q", decoded, err, inside)
	}
	if rel, ok := WorkspaceRelative(workspace, uri); !ok || rel != "pkg/a.go" {
		t.Fatalf("workspace relative = %q/%v, want pkg/a.go", rel, ok)
	}
	if _, ok := WorkspaceRelative(workspace, PathToURI(outside)); ok {
		t.Fatal("a path outside the workspace must not be workspace-relative")
	}
	if _, err := URIToPath("https://evil.example/x"); err == nil {
		t.Fatal("a non-file URI scheme must be rejected")
	}
}
