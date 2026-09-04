//go:build darwin && cgo

package desktop

/*
#cgo CFLAGS: -x objective-c
#cgo LDFLAGS: -framework Cocoa
#import <Cocoa/Cocoa.h>
#include <stdlib.h>
#include <string.h>

// codeatlas_pick_folder shows a modal NSOpenPanel restricted to directories.
// It returns a malloc'ed UTF-8 path, or NULL when the user cancelled. It must
// be called on the main thread, which is where webview bindings run.
static char *codeatlas_pick_folder(const char *initialPath) {
	@autoreleasepool {
		NSOpenPanel *panel = [NSOpenPanel openPanel];
		panel.canChooseFiles = NO;
		panel.canChooseDirectories = YES;
		panel.allowsMultipleSelection = NO;
		panel.canCreateDirectories = YES;
		panel.resolvesAliases = YES;
		panel.title = @"Choose workspace";
		panel.message = @"Choose the folder with the code repository CodeAtlas should analyze.";
		panel.prompt = @"Choose";
		if (initialPath != NULL && initialPath[0] != '\0') {
			NSString *initial = [NSString stringWithUTF8String:initialPath];
			if (initial != nil) {
				panel.directoryURL = [NSURL fileURLWithPath:initial isDirectory:YES];
			}
		}
		if ([panel runModal] != NSModalResponseOK) {
			return NULL;
		}
		NSURL *url = panel.URLs.firstObject;
		if (url == nil || url.path == nil) {
			return NULL;
		}
		const char *utf8 = [url.path UTF8String];
		if (utf8 == NULL) {
			return NULL;
		}
		return strdup(utf8);
	}
}
*/
import "C"

import (
	"errors"
	"unsafe"
)

func pickFolderNative(_ uintptr, initial string) (string, bool, error) {
	initialCString := C.CString(initial)
	defer C.free(unsafe.Pointer(initialCString))
	selected := C.codeatlas_pick_folder(initialCString)
	if selected == nil {
		return "", true, nil
	}
	defer C.free(unsafe.Pointer(selected))
	path := C.GoString(selected)
	if path == "" {
		return "", false, errors.New("the folder chooser returned an empty path")
	}
	return path, false, nil
}
