//go:build !darwin || !cgo

package desktop

// Platforms without an application menu bar bind their close shortcut to the
// window itself, so there is nothing to install.
func installQuitMenuNative(string, func()) {}
