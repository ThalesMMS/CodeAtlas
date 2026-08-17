// Package lspconv converts between the CodeAtlas internal position model (UTF-8
// byte offsets and byte columns, as Tree-sitter uses) and LSP positions (0-based
// line, character counted in the server's negotiated encoding — usually UTF-16).
// It is a single, tested component so every adapter (gopls, the TypeScript server)
// shares correct surrogate-pair and CRLF handling. It also converts paths to/from
// file:// URIs within a workspace.
package lspconv

import (
	"fmt"
	"sort"
	"unicode/utf8"
)

// Encodings recognized by the converter.
const (
	EncodingUTF8  = "utf-8"
	EncodingUTF16 = "utf-16"
	EncodingUTF32 = "utf-32"
)

// LineStarts returns the byte offset at the start of each line. lineStarts[i] is
// the start of line i (0-based). It is built once per document version and may be
// cached by the caller.
func LineStarts(source []byte) []int {
	starts := make([]int, 0, 64)
	starts = append(starts, 0)
	for i := 0; i < len(source); i++ {
		if source[i] == '\n' {
			starts = append(starts, i+1)
		}
	}
	return starts
}

// lineSlice returns the bytes of line (0-based), excluding the trailing newline,
// and the line's start offset.
func lineSlice(source []byte, lineStarts []int, line int) ([]byte, int, error) {
	if line < 0 || line >= len(lineStarts) {
		return nil, 0, fmt.Errorf("lspconv: line %d out of range (0..%d)", line, len(lineStarts)-1)
	}
	start := lineStarts[line]
	end := len(source)
	if line+1 < len(lineStarts) {
		end = lineStarts[line+1]
	}
	content := source[start:end]
	// Trim the trailing \n and optional \r so columns are over the visible content.
	for len(content) > 0 && (content[len(content)-1] == '\n' || content[len(content)-1] == '\r') {
		content = content[:len(content)-1]
	}
	return content, start, nil
}

// encodedUnits returns how many code units rune r occupies in the encoding.
func encodedUnits(r rune, encoding string) int {
	switch encoding {
	case EncodingUTF8:
		return utf8.RuneLen(r)
	case EncodingUTF32:
		return 1
	default: // utf-16
		if r > 0xFFFF {
			return 2 // surrogate pair
		}
		return 1
	}
}

// LSPToByteOffset converts an LSP position (0-based line, character in encoding)
// to a UTF-8 byte offset into source. A character index landing in the middle of a
// rune (or past the line) is rejected.
func LSPToByteOffset(source []byte, lineStarts []int, line, character int, encoding string) (int, error) {
	if character < 0 {
		return 0, fmt.Errorf("lspconv: negative character %d", character)
	}
	content, start, err := lineSlice(source, lineStarts, line)
	if err != nil {
		return 0, err
	}
	units := 0
	for offset := 0; offset < len(content); {
		if units == character {
			return start + offset, nil
		}
		r, size := utf8.DecodeRune(content[offset:])
		if r == utf8.RuneError && size == 1 {
			return 0, fmt.Errorf("lspconv: invalid UTF-8 at line %d", line)
		}
		units += encodedUnits(r, encoding)
		// The target character must not fall inside a multi-unit rune.
		if units > character {
			return 0, fmt.Errorf("lspconv: character %d falls inside a multi-unit rune", character)
		}
		offset += size
	}
	if units == character {
		return start + len(content), nil // end-of-line position
	}
	return 0, fmt.Errorf("lspconv: character %d beyond line %d length", character, line)
}

// ByteOffsetToLSP converts a UTF-8 byte offset to an LSP position in encoding.
func ByteOffsetToLSP(source []byte, lineStarts []int, offset int, encoding string) (line, character int, err error) {
	if offset < 0 || offset > len(source) {
		return 0, 0, fmt.Errorf("lspconv: offset %d out of range", offset)
	}
	line = lineForOffset(lineStarts, offset)
	content, start, err := lineSlice(source, lineStarts, line)
	if err != nil {
		return 0, 0, err
	}
	within := offset - start
	if within > len(content) {
		within = len(content) // offset was on the trailing newline
	}
	if within < 0 {
		return 0, 0, fmt.Errorf("lspconv: offset %d precedes line start", offset)
	}
	units := 0
	for pos := 0; pos < within; {
		r, size := utf8.DecodeRune(content[pos:])
		if r == utf8.RuneError && size == 1 {
			return 0, 0, fmt.Errorf("lspconv: invalid UTF-8 scanning to offset %d", offset)
		}
		if pos+size > within {
			return 0, 0, fmt.Errorf("lspconv: offset %d falls inside a rune", offset)
		}
		units += encodedUnits(r, encoding)
		pos += size
	}
	return line, units, nil
}

// InternalToLSP converts a CodeAtlas position (1-based line, 1-based byte column)
// to an LSP position in encoding.
func InternalToLSP(source []byte, lineStarts []int, line, byteColumn int, encoding string) (lspLine, lspChar int, err error) {
	if line < 1 {
		return 0, 0, fmt.Errorf("lspconv: internal line %d must be >= 1", line)
	}
	content, start, err := lineSlice(source, lineStarts, line-1)
	if err != nil {
		return 0, 0, err
	}
	byteWithin := byteColumn - 1
	if byteWithin < 0 {
		byteWithin = 0
	}
	if byteWithin > len(content) {
		byteWithin = len(content)
	}
	return ByteOffsetToLSP(source, lineStarts, start+byteWithin, encoding)
}

// LSPToInternal converts an LSP position to a CodeAtlas position (1-based line,
// 1-based byte column).
func LSPToInternal(source []byte, lineStarts []int, line, character int, encoding string) (internalLine, byteColumn int, err error) {
	offset, err := LSPToByteOffset(source, lineStarts, line, character, encoding)
	if err != nil {
		return 0, 0, err
	}
	lineFound := lineForOffset(lineStarts, offset)
	return lineFound + 1, offset - lineStarts[lineFound] + 1, nil
}

func lineForOffset(lineStarts []int, offset int) int {
	line := sort.Search(len(lineStarts), func(index int) bool {
		return lineStarts[index] > offset
	}) - 1
	if line < 0 {
		return 0
	}
	return line
}
