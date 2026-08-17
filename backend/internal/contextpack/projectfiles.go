package contextpack

import (
	"path"
	"sort"
	"strconv"
	"strings"

	"github.com/ThalesMMS/CodeAtlas/internal/domain"
	"github.com/ThalesMMS/CodeAtlas/internal/textutil"
)

const maxProjectFileEvidence = 16

var projectEvidenceLanguages = map[string]struct{}{
	"dockerfile": {}, "dotenv": {}, "gomod": {}, "json": {}, "makefile": {}, "markdown": {},
}

// projectFileSeeds exposes scanner-owned, bounded file previews as ordinary
// config evidence. Repository scope receives root files only; module scope may
// receive files under the directories already named by its code-file scope.
func projectFileSeeds(files []domain.File, request ContextRequest) []Candidate {
	directories := scopedDirectories(request)
	type ranked struct {
		priority int
		line     int
		value    Candidate
	}
	var rankedSeeds []ranked
	for _, file := range files {
		if file.Content == "" || !projectEvidenceFile(file) || !projectFileInScope(file.Path, request, directories) {
			continue
		}
		for _, evidence := range projectFileEvidence(file) {
			rankedSeeds = append(rankedSeeds, ranked{
				priority: projectFilePriority(file.Path), line: evidence.Range.Start.Line,
				value: Candidate{Evidence: evidence, Source: "project_file"},
			})
		}
	}
	sort.Slice(rankedSeeds, func(i, j int) bool {
		if rankedSeeds[i].priority != rankedSeeds[j].priority {
			return rankedSeeds[i].priority < rankedSeeds[j].priority
		}
		if rankedSeeds[i].value.Evidence.Path != rankedSeeds[j].value.Evidence.Path {
			return rankedSeeds[i].value.Evidence.Path < rankedSeeds[j].value.Evidence.Path
		}
		return rankedSeeds[i].line < rankedSeeds[j].line
	})
	if len(rankedSeeds) > maxProjectFileEvidence {
		rankedSeeds = rankedSeeds[:maxProjectFileEvidence]
	}
	result := make([]Candidate, len(rankedSeeds))
	for index, seed := range rankedSeeds {
		seed.value.LexicalRank = index + 1
		result[index] = seed.value
	}
	return result
}

func projectEvidenceFile(file domain.File) bool {
	_, ok := projectEvidenceLanguages[file.Language]
	return ok
}

func projectFileInScope(filePath string, request ContextRequest, directories []string) bool {
	filePath = path.Clean(strings.TrimSpace(filePath))
	if request.Options.Scope != ScopeModule {
		return path.Dir(filePath) == "."
	}
	for _, directory := range directories {
		if directory == "." {
			if path.Dir(filePath) == "." {
				return true
			}
			continue
		}
		if filePath == directory || strings.HasPrefix(filePath, directory+"/") {
			return true
		}
	}
	return false
}

func scopedDirectories(request ContextRequest) []string {
	if request.Scope == nil {
		return nil
	}
	seen := make(map[string]struct{})
	for _, filePath := range request.Scope.Paths {
		directory := path.Dir(path.Clean(filePath))
		seen[directory] = struct{}{}
	}
	result := make([]string, 0, len(seen))
	for directory := range seen {
		result = append(result, directory)
	}
	sort.Strings(result)
	return result
}

func projectFileEvidence(file domain.File) []Evidence {
	if strings.EqualFold(path.Base(file.Path), "go.mod") {
		return lineEvidence(file)
	}
	endLine := contentEndLine(file.Content)
	evidence := configEvidence(file, file.Content, 1, endLine, file.Path)
	evidence.DisplayCodeTruncated = file.ContentTruncated
	return []Evidence{evidence}
}

func lineEvidence(file domain.File) []Evidence {
	lines := strings.Split(strings.ReplaceAll(file.Content, "\r\n", "\n"), "\n")
	result := make([]Evidence, 0, len(lines))
	for index, line := range lines {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		lineNumber := index + 1
		title := file.Path + ":" + strconv.Itoa(lineNumber) + " " + truncateProjectTitle(line)
		result = append(result, configEvidence(file, line, lineNumber, lineNumber, title))
	}
	return result
}

func configEvidence(file domain.File, content string, startLine, endLine int, title string) Evidence {
	return Evidence{
		Kind: KindConfig, Path: file.Path,
		Range: domain.Range{
			Start: domain.Position{Line: startLine, Column: 1},
			End:   domain.Position{Line: endLine, Column: 1},
		},
		ContentHash: shortHash(content), Title: title, Content: content,
		Confidence: 1, Provenance: []Provenance{{Source: "indexer", Detail: "project_file"}},
		DisplayCode: content, DisplayLanguage: file.Language,
	}
}

func contentEndLine(content string) int {
	content = strings.TrimSuffix(strings.ReplaceAll(content, "\r\n", "\n"), "\n")
	if content == "" {
		return 1
	}
	return strings.Count(content, "\n") + 1
}

func truncateProjectTitle(value string) string {
	const maxBytes = 160
	if len(value) <= maxBytes {
		return value
	}
	return strings.TrimSpace(textutil.TruncateUTF8(value, maxBytes)) + "…"
}

func projectFilePriority(filePath string) int {
	lower := strings.ToLower(path.Base(filePath))
	switch {
	case lower == "go.mod":
		return 0
	case lower == "package.json":
		return 10
	case lower == "readme" || strings.HasPrefix(lower, "readme."):
		return 20
	case lower == "tsconfig.json":
		return 30
	case lower == "makefile":
		return 40
	case lower == "dockerfile" || strings.HasPrefix(lower, "dockerfile."):
		return 50
	case lower == ".env.example":
		return 60
	default:
		return 100
	}
}
