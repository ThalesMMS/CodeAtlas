// Package ignore owns workspace ignore matching shared by the scanner and file
// watcher. It deliberately supports only the small pattern subset CodeAtlas uses
// today: comments, negation lines ignored, slash-relative prefixes and globs.
package ignore

import (
	"bufio"
	"os"
	"path/filepath"
	"strings"
)

var DefaultIgnoredDirectories = map[string]struct{}{
	".git": {}, ".codeatlas": {}, "node_modules": {}, "vendor": {}, "dist": {}, "build": {},
	".venv": {}, "venv": {}, ".tox": {}, "__pycache__": {}, ".mypy_cache": {}, ".pytest_cache": {}, ".ruff_cache": {},
	"coverage": {}, ".next": {}, ".idea": {}, ".vscode": {}, "target": {}, "bin": {}, "obj": {},
}

type Matcher struct {
	Patterns []string
}

// LoadMatcher reads ignore patterns from the workspace root. It honors a
// dedicated `.codeatlasignore` in addition to `.gitignore`, so generated or
// vendored trees can be excluded from CodeAtlas without changing Git behavior.
func LoadMatcher(root string) Matcher {
	matcher := Matcher{}
	for _, name := range []string{".gitignore", ".codeatlasignore"} {
		matcher.Patterns = append(matcher.Patterns, readIgnorePatterns(filepath.Join(root, name))...)
	}
	return matcher
}

func readIgnorePatterns(path string) []string {
	file, err := os.Open(path)
	if err != nil {
		return nil
	}
	defer file.Close()
	var patterns []string
	scanner := bufio.NewScanner(file)
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if line == "" || strings.HasPrefix(line, "#") || strings.HasPrefix(line, "!") {
			continue
		}
		patterns = append(patterns, strings.TrimPrefix(line, "/"))
	}
	return patterns
}

func (m Matcher) Ignored(relative string, isDir bool) bool {
	relative = filepath.ToSlash(relative)
	for _, pattern := range m.Patterns {
		cleanPattern := strings.TrimSuffix(filepath.ToSlash(pattern), "/")
		if cleanPattern == "" {
			continue
		}
		if matched, _ := filepath.Match(cleanPattern, relative); matched {
			return true
		}
		if strings.HasPrefix(relative, cleanPattern+"/") || (isDir && relative == cleanPattern) {
			return true
		}
		if !strings.Contains(cleanPattern, "/") {
			for _, part := range strings.Split(relative, "/") {
				if matched, _ := filepath.Match(cleanPattern, part); matched {
					return true
				}
			}
		}
	}
	return false
}
