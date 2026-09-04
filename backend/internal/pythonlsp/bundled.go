package pythonlsp

import "github.com/ThalesMMS/CodeAtlas/internal/lspadapter"

// resolveBundledExecutable prefers the pyright-langserver launcher shipped
// inside packaged desktop builds when the default command name is configured.
// The launcher runs the pinned Pyright package on the bundled Node.js runtime
// and installs a sibling `pyright` CLI for the version probe.
func resolveBundledExecutable(configured string) string {
	return lspadapter.ResolveBundledExecutable(configured, "pyright-langserver")
}

func resolveBundledExecutableAt(configured, executable, goos string) string {
	return lspadapter.ResolveBundledExecutableAt(configured, "pyright-langserver", executable, goos)
}
