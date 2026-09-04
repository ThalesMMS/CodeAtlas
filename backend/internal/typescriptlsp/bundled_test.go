package typescriptlsp

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/ThalesMMS/CodeAtlas/internal/lspadapter"
)

func TestResolveBundledExecutablePrefersPackagedLauncherForDefault(t *testing.T) {
	root := t.TempDir()
	executable := filepath.Join(root, "CodeAtlas.app", "Contents", "MacOS", "codeatlas")
	bundled := filepath.Join(root, "CodeAtlas.app", "Contents", "Resources", "bin", "typescript-language-server")
	if err := os.MkdirAll(filepath.Dir(bundled), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(bundled, []byte("#!/bin/sh\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	if got := resolveBundledExecutableAt("typescript-language-server", executable, "darwin"); got != bundled {
		t.Fatalf("resolved path = %q, want %q", got, bundled)
	}
	if got := resolveBundledExecutableAt("/opt/custom/typescript-language-server", executable, "darwin"); got != "/opt/custom/typescript-language-server" {
		t.Fatalf("explicit path changed to %q", got)
	}
}

func TestResolveBundledExecutableFallsBackWhenBundleIsMissing(t *testing.T) {
	executable := filepath.Join(t.TempDir(), "CodeAtlas.app", "Contents", "MacOS", "codeatlas")
	if got := resolveBundledExecutableAt("typescript-language-server", executable, "darwin"); got != "typescript-language-server" {
		t.Fatalf("missing bundled path resolved to %q", got)
	}
}

func TestBundledSDKPathUsesPackagedTypeScriptOnlyWhenPresent(t *testing.T) {
	root := t.TempDir()
	executable := filepath.Join(root, "CodeAtlas.app", "Contents", "MacOS", "codeatlas")
	if got := lspadapter.BundledResourceDirAt(executable, "darwin", "lsp", "node_modules", "typescript", "lib"); got != "" {
		t.Fatalf("missing SDK resolved to %q", got)
	}
	sdk := filepath.Join(root, "CodeAtlas.app", "Contents", "Resources", "lsp", "node_modules", "typescript", "lib")
	if err := os.MkdirAll(sdk, 0o755); err != nil {
		t.Fatal(err)
	}
	if got := lspadapter.BundledResourceDirAt(executable, "darwin", "lsp", "node_modules", "typescript", "lib"); got != sdk {
		t.Fatalf("SDK path = %q, want %q", got, sdk)
	}
	if got := resolveBundledSDKPath("/opt/ts/lib"); got != "/opt/ts/lib" {
		t.Fatalf("explicit SDK path changed to %q", got)
	}
}
