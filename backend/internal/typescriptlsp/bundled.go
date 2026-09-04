package typescriptlsp

import "github.com/ThalesMMS/CodeAtlas/internal/lspadapter"

// resolveBundledExecutable prefers the typescript-language-server launcher
// shipped inside packaged desktop builds when the default command name is
// configured. The launcher runs the pinned npm package on the bundled Node.js
// runtime.
func resolveBundledExecutable(configured string) string {
	return lspadapter.ResolveBundledExecutable(configured, "typescript-language-server")
}

func resolveBundledExecutableAt(configured, executable, goos string) string {
	return lspadapter.ResolveBundledExecutableAt(configured, "typescript-language-server", executable, goos)
}

// resolveBundledSDKPath fills an empty SDK path with the pinned TypeScript
// package shipped next to the bundled language server, so tsserver resolution
// never depends on a workspace or global TypeScript installation.
func resolveBundledSDKPath(configured string) string {
	if configured != "" {
		return configured
	}
	return lspadapter.BundledResourceDir("lsp", "node_modules", "typescript", "lib")
}
