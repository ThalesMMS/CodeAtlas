//go:build darwin && cgo

// The preamble of a file that uses //export may hold declarations only, so the
// menu's Objective-C lives in appmenu_darwin.go and this file stays empty of C.

package desktop

import "C"

// codeatlasQuitMenuSelected runs the callback registered by
// installQuitMenuNative. AppKit calls it on the main thread, which is where the
// window expects to be terminated from.
//
//export codeatlasQuitMenuSelected
func codeatlasQuitMenuSelected() {
	quitMenu.mu.Lock()
	onQuit := quitMenu.onQuit
	quitMenu.mu.Unlock()
	if onQuit != nil {
		onQuit()
	}
}
