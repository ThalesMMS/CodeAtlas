package desktop

import (
	"context"
	"net"
)

type SizeHint int

const (
	SizeDefault SizeHint = iota
	SizeMinimum
)

type Window interface {
	SetTitle(string)
	SetSize(width, height int, hint SizeHint)
	Navigate(string)
	SetHTML(string)
	Dispatch(func())
	Run()
	Terminate()
	Destroy()
	// Bind exposes a Go function to the page as window.<name>(...): a function
	// returning a Promise. Bindings must be registered before the first
	// navigation so every document sees them.
	Bind(name string, function any) error
}

// QuitMenu is implemented by windows whose platform hosts an application menu
// bar. Installing it adds the standard Quit item, which is what binds the
// platform's quit shortcut; the item closes the window so the application shuts
// down through the same path as its close button.
type QuitMenu interface {
	InstallQuitMenu(applicationName string)
}

type WindowFactory interface {
	New() (Window, error)
	ShowFatal(title, message string)
}

type ServerFunc func(context.Context, func(net.Addr)) error

// FolderPickerBinding is the page-visible function name through which the
// embedded frontend opens the native folder chooser:
// window.codeatlasPickWorkspaceFolder(initialPath) resolves to a FolderSelection.
const FolderPickerBinding = "codeatlasPickWorkspaceFolder"

// FolderSelection is the JSON result of the native folder chooser.
type FolderSelection struct {
	Path     string `json:"path"`
	Canceled bool   `json:"canceled"`
}

// FolderPicker is implemented by windows that can show a native folder
// chooser modal to themselves. The initial path is a hint only.
type FolderPicker interface {
	PickFolder(initial string) (FolderSelection, error)
}
