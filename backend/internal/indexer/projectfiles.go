package indexer

import (
	"path/filepath"
	"strings"

	"github.com/ThalesMMS/CodeAtlas/internal/domain"
)

// ProjectFileContentLimit bounds persisted non-code bytes independently from
// the scanner's source-file ceiling. The full bytes are still hashed, so a
// change beyond this preview invalidates the snapshot without bloating the DB.
const ProjectFileContentLimit = 8 * 1024

type projectFileSpec struct {
	language     string
	metadataOnly bool
}

// ProjectFileData is the bounded, safe projection shared by the runtime scanner
// and offline eval corpus loader. Hashing and file timestamps remain caller
// responsibilities because both paths already own those concerns.
type ProjectFileData struct {
	Language         string
	Content          string
	ContentTruncated bool
}

// projectFileSpecFor is a deliberately small allowlist. In particular, .env is
// never eligible; only the conventionally non-secret .env.example is indexed.
func projectFileSpecFor(path string) (projectFileSpec, bool) {
	base := filepath.Base(filepath.ToSlash(path))
	lower := strings.ToLower(base)
	switch {
	case lower == "go.mod":
		return projectFileSpec{language: "gomod"}, true
	case lower == "go.sum":
		return projectFileSpec{language: "text", metadataOnly: true}, true
	case lower == "package.json" || lower == "tsconfig.json":
		return projectFileSpec{language: "json"}, true
	case lower == "makefile":
		return projectFileSpec{language: "makefile"}, true
	case lower == ".env.example":
		return projectFileSpec{language: "dotenv"}, true
	case lower == "dockerfile" || strings.HasPrefix(lower, "dockerfile."):
		return projectFileSpec{language: "dockerfile"}, true
	case lower == "readme" || strings.HasPrefix(lower, "readme."):
		return projectFileSpec{language: "markdown"}, true
	default:
		return projectFileSpec{}, false
	}
}

// IsProjectFile reports whether path belongs to the non-code allowlist without
// reading it. Callers can avoid opening unrelated or binary files.
func IsProjectFile(path string) bool {
	_, ok := projectFileSpecFor(path)
	return ok
}

func parseProjectFile(item candidate) (domain.ParsedFile, bool) {
	data, ok := InspectProjectFile(item.relative, item.source)
	if !ok {
		return domain.ParsedFile{}, false
	}
	file := domain.File{
		Path: item.relative, Language: data.Language, Hash: item.hash, Size: item.info.Size(),
		ModifiedAt: item.info.ModTime().UTC(), IndexedAt: item.info.ModTime().UTC(),
		Content: data.Content, ContentTruncated: data.ContentTruncated,
	}
	return domain.ParsedFile{File: file, Symbols: []domain.Symbol{}, Edges: []domain.Edge{}}, true
}

// InspectProjectFile applies the authoritative non-code allowlist and returns a
// bounded UTF-8 preview. Unknown files (including .env) return ok=false.
func InspectProjectFile(path string, source []byte) (data ProjectFileData, ok bool) {
	spec, ok := projectFileSpecFor(path)
	if !ok {
		return ProjectFileData{}, false
	}
	data.Language = spec.language
	data.Content, data.ContentTruncated = projectFilePreview(source, spec.metadataOnly)
	return data, true
}

func projectFilePreview(source []byte, metadataOnly bool) (string, bool) {
	if metadataOnly || len(source) == 0 {
		return "", false
	}
	truncated := len(source) > ProjectFileContentLimit
	if truncated {
		source = source[:ProjectFileContentLimit]
		if newline := strings.LastIndexByte(string(source), '\n'); newline > ProjectFileContentLimit/2 {
			source = source[:newline+1]
		}
	}
	return strings.ToValidUTF8(string(source), "�"), truncated
}
