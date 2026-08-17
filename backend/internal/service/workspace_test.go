package service

import (
	"os"
	"path/filepath"
	"runtime"
	"testing"

	"github.com/ThalesMMS/CodeAtlas/internal/apperror"
)

func TestIdentifierAt(t *testing.T) {
	t.Parallel()
	source := []byte("const result = checkout.submitOrder(orderID)\n")
	if got := IdentifierAt(source, 1, 28); got != "submitOrder" {
		t.Fatalf("IdentifierAt() = %q, want submitOrder", got)
	}
	if got := IdentifierAt(source, 1, 1); got != "const" {
		t.Fatalf("IdentifierAt() at first column = %q, want const", got)
	}

	utf16Source := []byte("😀😀😀 a b c\n")
	if got := IdentifierAt(utf16Source, 1, 10); got != "b" {
		t.Fatalf("IdentifierAt() with UTF-16 column = %q, want b", got)
	}
	identifier, rng, ok := IdentifierAtWithRange(utf16Source, 1, 10)
	if !ok || identifier != "b" || rng.Start.Column != 16 || rng.End.Column != 17 || rng.Start.Encoding != "utf-8" {
		t.Fatalf("IdentifierAtWithRange() = %q %+v/%v, want UTF-8 byte range 16-17", identifier, rng, ok)
	}
}

func TestWorkspaceReadLimitedAllowsBoundaryAndRejectsOversize(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "payload.txt"), []byte("12345"), 0o600); err != nil {
		t.Fatal(err)
	}
	workspace := NewWorkspace(root)
	content, err := workspace.ReadLimited("payload.txt", 5)
	if err != nil || string(content) != "12345" {
		t.Fatalf("boundary read = %q, %v", content, err)
	}
	if _, err := workspace.ReadLimited("payload.txt", 4); err == nil {
		t.Fatal("oversized read succeeded")
	} else if appErr, ok := apperror.As(err); !ok || appErr.Code != apperror.CodeRequestTooLarge || appErr.Details["limitBytes"] != int64(4) {
		t.Fatalf("oversized read error = %#v", err)
	}
}

func TestWorkspaceRejectsTraversalAndEscapingSymlink(t *testing.T) {
	root := t.TempDir()
	outside := t.TempDir()
	insidePath := filepath.Join(root, "inside.go")
	if err := os.WriteFile(insidePath, []byte("package sample\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	workspace := NewWorkspace(root)

	if _, err := workspace.Resolve("../outside.go"); err == nil {
		t.Fatal("expected traversal to be rejected")
	}
	resolved, err := workspace.Resolve("inside.go")
	if err != nil {
		t.Fatalf("Resolve(inside.go) error = %v", err)
	}
	expectedInsidePath, err := filepath.EvalSymlinks(insidePath)
	if err != nil {
		t.Fatal(err)
	}
	if resolved != expectedInsidePath {
		t.Fatalf("resolved path = %q, want %q", resolved, expectedInsidePath)
	}

	if runtime.GOOS == "windows" {
		t.Skip("symlink creation may require elevated privileges on Windows")
	}
	outsideFile := filepath.Join(outside, "secret.go")
	if err := os.WriteFile(outsideFile, []byte("package secret\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(outside, filepath.Join(root, "linked")); err != nil {
		t.Skipf("symlink unavailable: %v", err)
	}
	if _, err := workspace.Resolve("linked/secret.go"); err == nil {
		t.Fatal("expected a parent symlink escaping the workspace to be rejected")
	}
}
