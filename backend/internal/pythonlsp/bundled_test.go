package pythonlsp

import (
	"os"
	"path/filepath"
	"testing"
)

func TestResolveBundledExecutablePrefersPackagedLauncherForDefault(t *testing.T) {
	root := t.TempDir()
	executable := filepath.Join(root, "CodeAtlas.app", "Contents", "MacOS", "codeatlas")
	bundled := filepath.Join(root, "CodeAtlas.app", "Contents", "Resources", "bin", "pyright-langserver")
	if err := os.MkdirAll(filepath.Dir(bundled), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(bundled, []byte("#!/bin/sh\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	if got := resolveBundledExecutableAt("pyright-langserver", executable, "darwin"); got != bundled {
		t.Fatalf("resolved path = %q, want %q", got, bundled)
	}
	if got := resolveBundledExecutableAt("/opt/custom/pyright-langserver", executable, "darwin"); got != "/opt/custom/pyright-langserver" {
		t.Fatalf("explicit path changed to %q", got)
	}
}

func TestResolveBundledExecutableFallsBackWhenBundleIsMissing(t *testing.T) {
	executable := filepath.Join(t.TempDir(), "CodeAtlas.app", "Contents", "MacOS", "codeatlas")
	if got := resolveBundledExecutableAt("pyright-langserver", executable, "darwin"); got != "pyright-langserver" {
		t.Fatalf("missing bundled path resolved to %q", got)
	}
}
