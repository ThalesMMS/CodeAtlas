//go:build !windows && !(darwin && cgo)

package desktop

import "errors"

func pickFolderNative(uintptr, string) (string, bool, error) {
	return "", false, errors.New("a native folder chooser is not available on this platform")
}
