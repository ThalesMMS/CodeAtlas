package service

import (
	"io"
	"os"
	"path/filepath"
	"strings"
	"unicode"
	"unicode/utf16"

	"github.com/ThalesMMS/CodeAtlas/internal/apperror"
	"github.com/ThalesMMS/CodeAtlas/internal/domain"
)

type Workspace struct {
	root string
}

func NewWorkspace(root string) *Workspace { return &Workspace{root: root} }
func (w *Workspace) Root() string         { return w.root }

func (w *Workspace) Resolve(relative string) (string, error) {
	if strings.ContainsRune(relative, '\x00') || filepath.IsAbs(relative) {
		return "", apperror.InvalidArgumentMessage("path", "Invalid path.", nil)
	}
	clean := filepath.Clean(filepath.FromSlash(relative))
	if clean == "." || clean == "" || clean == ".." || strings.HasPrefix(clean, ".."+string(filepath.Separator)) {
		return "", apperror.PathOutsideWorkspace(relative)
	}
	absolute := filepath.Join(w.root, clean)
	if !isWithin(w.root, absolute) {
		return "", apperror.PathOutsideWorkspace(relative)
	}

	// Resolve the complete path, not only the final component. This blocks a
	// workspace subdirectory symlink from redirecting reads/writes outside root.
	resolved, err := filepath.EvalSymlinks(absolute)
	if err != nil {
		return "", err
	}
	resolvedRoot, err := filepath.EvalSymlinks(w.root)
	if err != nil {
		return "", err
	}
	if !isWithin(resolvedRoot, resolved) {
		return "", apperror.PathOutsideWorkspace(relative)
	}
	return resolved, nil
}

func isWithin(root, candidate string) bool {
	relative, err := filepath.Rel(root, candidate)
	return err == nil && relative != ".." && !strings.HasPrefix(relative, ".."+string(filepath.Separator))
}

func (w *Workspace) Read(relative string) ([]byte, error) {
	path, err := w.Resolve(relative)
	if err != nil {
		return nil, err
	}
	return os.ReadFile(path)
}

// ReadLimited reads at most maxBytes from a regular workspace file. The open
// file is statted first for the common fast rejection, then read through a
// maxBytes+1 limiter so a concurrent growth cannot bypass the cap.
func (w *Workspace) ReadLimited(relative string, maxBytes int64) ([]byte, error) {
	if maxBytes < 0 {
		return nil, apperror.InvalidArgumentMessage("maxBytes", "The read limit is invalid.", nil)
	}
	path, err := w.Resolve(relative)
	if err != nil {
		return nil, err
	}
	file, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer file.Close() //nolint:errcheck
	info, err := file.Stat()
	if err != nil {
		return nil, err
	}
	if !info.Mode().IsRegular() {
		return nil, apperror.InvalidArgumentMessage("path", "The path is not a regular file.", nil)
	}
	if info.Size() > maxBytes {
		return nil, apperror.RequestTooLarge(maxBytes)
	}
	readLimit := maxBytes + 1
	if readLimit < maxBytes { // int64 overflow at MaxInt64
		readLimit = maxBytes
	}
	content, err := io.ReadAll(io.LimitReader(file, readLimit))
	if err != nil {
		return nil, err
	}
	if int64(len(content)) > maxBytes {
		return nil, apperror.RequestTooLarge(maxBytes)
	}
	return content, nil
}

func (w *Workspace) Write(relative string, content []byte) error {
	path, err := w.Resolve(relative)
	if err != nil {
		return err
	}
	info, err := os.Stat(path)
	if err != nil {
		return err
	}
	if !info.Mode().IsRegular() {
		return apperror.InvalidArgumentMessage("path", "The path is not a regular file.", nil)
	}
	return os.WriteFile(path, content, info.Mode().Perm())
}

func IdentifierAt(source []byte, line, column int) string {
	identifier, _, ok := IdentifierAtWithRange(source, line, column)
	if !ok {
		return ""
	}
	return identifier
}

// IdentifierAtWithRange resolves the UTF-16 cursor position used by the editor
// and returns the identifier plus its internal UTF-8 byte-column range. Keeping
// both values from the same scan prevents semantic queries and ContextPack
// targets from drifting to different tokens on lines containing non-BMP runes.
func IdentifierAtWithRange(source []byte, line, column int) (string, domain.Range, bool) {
	if line < 1 || column < 1 {
		return "", domain.Range{}, false
	}
	lines := strings.Split(string(source), "\n")
	if line > len(lines) {
		return "", domain.Range{}, false
	}
	runes := []rune(strings.TrimSuffix(lines[line-1], "\r"))
	if len(runes) == 0 {
		return "", domain.Range{}, false
	}
	index := runeIndexForUTF16Column(runes, column)
	if !isIdentifierRune(runes[index]) && index > 0 && isIdentifierRune(runes[index-1]) {
		index--
	}
	if !isIdentifierRune(runes[index]) {
		return "", domain.Range{}, false
	}
	start, end := index, index+1
	for start > 0 && isIdentifierRune(runes[start-1]) {
		start--
	}
	for end < len(runes) && isIdentifierRune(runes[end]) {
		end++
	}
	startColumn := len([]byte(string(runes[:start]))) + 1
	endColumn := len([]byte(string(runes[:end]))) + 1
	return string(runes[start:end]), domain.Range{
		Start: domain.Position{Line: line, Column: startColumn, Encoding: "utf-8"},
		End:   domain.Position{Line: line, Column: endColumn, Encoding: "utf-8"},
	}, true
}

func runeIndexForUTF16Column(runes []rune, column int) int {
	target := column - 1
	if target <= 0 {
		return 0
	}
	units := 0
	for index, value := range runes {
		width := utf16.RuneLen(value)
		if width < 1 {
			width = 1
		}
		if target < units+width {
			return index
		}
		units += width
	}
	return len(runes) - 1
}

func isIdentifierRune(r rune) bool {
	return unicode.IsLetter(r) || unicode.IsDigit(r) || r == '_' || r == '$'
}
