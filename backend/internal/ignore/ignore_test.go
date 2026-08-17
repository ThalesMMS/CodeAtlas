package ignore

import (
	"os"
	"path/filepath"
	"testing"
)

func TestLoadMatcherUnionsGitignoreAndCodeatlasignore(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, ".gitignore"), []byte("dist/\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, ".codeatlasignore"), []byte("backend/internal/treesitter/grammars/\ngenerated/\n"), 0o600); err != nil {
		t.Fatal(err)
	}

	matcher := LoadMatcher(root)
	if !matcher.Ignored("dist", true) {
		t.Fatal("pattern from .gitignore should apply")
	}
	if !matcher.Ignored("backend/internal/treesitter/grammars/go/parser.c", false) {
		t.Fatal("pattern from .codeatlasignore should apply")
	}
	if matcher.Ignored("backend/internal/treesitter/treesitter.go", false) {
		t.Fatal("hand-written Tree-sitter wrapper should stay visible")
	}
}

func TestDefaultIgnoredDirectoriesCoverStateAndDependencyTrees(t *testing.T) {
	t.Parallel()
	for _, name := range []string{".git", ".codeatlas", "node_modules", "vendor", "dist", "build", ".venv", "venv", ".tox", "__pycache__", ".mypy_cache", ".pytest_cache", ".ruff_cache"} {
		if _, ok := DefaultIgnoredDirectories[name]; !ok {
			t.Fatalf("%s missing from default ignored dirs", name)
		}
	}
}
