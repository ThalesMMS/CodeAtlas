package gopls

import "github.com/ThalesMMS/CodeAtlas/internal/lspadapter"

// resolveBundledExecutable prefers the gopls binary shipped inside packaged
// desktop builds when the default command name is configured.
func resolveBundledExecutable(configured string) string {
	return lspadapter.ResolveBundledExecutable(configured, "gopls")
}

func resolveBundledExecutableAt(configured, executable, goos string) string {
	return lspadapter.ResolveBundledExecutableAt(configured, "gopls", executable, goos)
}
