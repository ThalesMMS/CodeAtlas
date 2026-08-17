//go:build windows

package desktop

import (
	"strings"
	"unsafe"

	"golang.org/x/sys/windows"
)

var messageBoxW = windows.NewLazySystemDLL("user32.dll").NewProc("MessageBoxW")

func showFatalDialog(title, message string) {
	titlePointer, titleErr := windows.UTF16PtrFromString(strings.ReplaceAll(safeFatalText(title), "\x00", ""))
	messagePointer, messageErr := windows.UTF16PtrFromString(strings.ReplaceAll(safeFatalText(message), "\x00", ""))
	if titleErr != nil || messageErr != nil {
		return
	}
	const messageBoxIconError = 0x00000010
	const messageBoxOK = 0x00000000
	_, _, _ = messageBoxW.Call(
		0,
		uintptr(unsafe.Pointer(messagePointer)),
		uintptr(unsafe.Pointer(titlePointer)),
		messageBoxIconError|messageBoxOK,
	)
}
