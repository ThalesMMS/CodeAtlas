package indexer

import (
	"os"
	"path/filepath"
	"testing"
)

func TestLoadIgnoreMatcherReadsCodeatlasignore(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	// A dedicated .codeatlasignore excludes a large generated tree without touching
	// .gitignore.
	if err := os.WriteFile(filepath.Join(root, ".codeatlasignore"), []byte("# comment\nbackend/internal/treesitter/grammars/\ngenerated/\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	matcher := loadIgnoreMatcher(root)

	if !matcher.ignored("backend/internal/treesitter/grammars", true) {
		t.Fatal("grammars dir should be ignored via .codeatlasignore")
	}
	if !matcher.ignored("backend/internal/treesitter/grammars/go/parser.c", false) {
		t.Fatal("file under an ignored dir should be ignored")
	}
	if !matcher.ignored("generated", true) {
		t.Fatal("generated/ should be ignored")
	}
	if matcher.ignored("backend/internal/treesitter/treesitter.go", false) {
		t.Fatal("the hand-written wrapper must stay indexable")
	}
}

func TestLoadIgnoreMatcherUnionsGitignoreAndCodeatlasignore(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, ".gitignore"), []byte("dist/\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, ".codeatlasignore"), []byte("vendored/\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	matcher := loadIgnoreMatcher(root)
	if !matcher.ignored("dist", true) {
		t.Fatal("pattern from .gitignore should still apply")
	}
	if !matcher.ignored("vendored", true) {
		t.Fatal("pattern from .codeatlasignore should apply")
	}
}
