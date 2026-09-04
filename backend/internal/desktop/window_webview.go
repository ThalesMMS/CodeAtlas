//go:build desktop && (windows || darwin)

package desktop

import (
	"errors"

	webview "github.com/webview/webview_go"
)

type webviewWindow struct {
	native webview.WebView
}

func (w *webviewWindow) SetTitle(value string) { w.native.SetTitle(value) }
func (w *webviewWindow) Navigate(value string) { w.native.Navigate(value) }
func (w *webviewWindow) SetHTML(value string)  { w.native.SetHtml(value) }
func (w *webviewWindow) Dispatch(f func())     { w.native.Dispatch(f) }
func (w *webviewWindow) Run()                  { w.native.Run() }
func (w *webviewWindow) Terminate()            { w.native.Terminate() }
func (w *webviewWindow) Destroy()              { w.native.Destroy() }

func (w *webviewWindow) Bind(name string, function any) error {
	return w.native.Bind(name, function)
}

// PickFolder runs the platform folder chooser. Bound functions are invoked on
// the UI thread, so the modal dialog can run synchronously here.
func (w *webviewWindow) PickFolder(initial string) (FolderSelection, error) {
	path, canceled, err := pickFolderNative(uintptr(w.native.Window()), initial)
	if err != nil {
		return FolderSelection{}, err
	}
	return FolderSelection{Path: path, Canceled: canceled}, nil
}

func (w *webviewWindow) SetSize(width, height int, hint SizeHint) {
	nativeHint := webview.Hint(webview.HintNone)
	if hint == SizeMinimum {
		nativeHint = webview.HintMin
	}
	w.native.SetSize(width, height, nativeHint)
}

type nativeFactory struct{}

func NativeFactory() WindowFactory { return nativeFactory{} }

func (nativeFactory) New() (Window, error) {
	if err := platformWebViewPreflight(); err != nil {
		return nil, err
	}
	native := webview.New(false)
	if native == nil || native.Window() == nil {
		if native != nil {
			native.Destroy()
		}
		return nil, errors.New("could not create the native CodeAtlas window")
	}
	return &webviewWindow{native: native}, nil
}

func (nativeFactory) ShowFatal(title, message string) {
	showFatalDialog(title, message)
}
