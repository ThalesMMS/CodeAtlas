//go:build desktop && windows

package desktop

import (
	"errors"
	"os"
	"testing"

	"golang.org/x/sys/windows/registry"
)

func TestManualUnavailableWebView2Dialog(t *testing.T) {
	if os.Getenv("CODEATLAS_MANUAL_DIALOG_SMOKE") != "1" {
		t.Skip("set CODEATLAS_MANUAL_DIALOG_SMOKE=1 for the interactive native-dialog smoke")
	}
	err := checkWebView2(func(registry.Key, string, string) (string, error) {
		return "", registry.ErrNotExist
	})
	if !errors.Is(err, ErrWebView2Unavailable) {
		t.Fatalf("checkWebView2() error = %v, want unavailable", err)
	}
	showFatalDialog("CodeAtlas could not start", err.Error()+" api_key=sk-WEBVIEW-SECRET")
}
