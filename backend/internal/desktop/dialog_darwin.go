//go:build darwin && cgo

package desktop

/*
#cgo LDFLAGS: -framework CoreFoundation
#include <CoreFoundation/CoreFoundation.h>
#include <stdlib.h>

static void codeatlas_show_fatal(const char *title, const char *message) {
	CFStringRef titleRef = CFStringCreateWithCString(NULL, title, kCFStringEncodingUTF8);
	CFStringRef messageRef = CFStringCreateWithCString(NULL, message, kCFStringEncodingUTF8);
	if (titleRef != NULL && messageRef != NULL) {
		CFUserNotificationDisplayAlert(
			0,
			kCFUserNotificationStopAlertLevel,
			NULL,
			NULL,
			NULL,
			titleRef,
			messageRef,
			CFSTR("OK"),
			NULL,
			NULL,
			NULL
		);
	}
	if (titleRef != NULL) CFRelease(titleRef);
	if (messageRef != NULL) CFRelease(messageRef);
}
*/
import "C"
import "unsafe"

func showFatalDialog(title, message string) {
	titleCString := C.CString(safeFatalText(title))
	messageCString := C.CString(safeFatalText(message))
	defer C.free(unsafe.Pointer(titleCString))
	defer C.free(unsafe.Pointer(messageCString))
	C.codeatlas_show_fatal(titleCString, messageCString)
}
