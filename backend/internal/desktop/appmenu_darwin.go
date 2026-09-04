//go:build darwin && cgo

package desktop

/*
#cgo CFLAGS: -x objective-c
#cgo LDFLAGS: -framework Cocoa
#import <Cocoa/Cocoa.h>
#include <stdlib.h>

// codeatlasQuitMenuSelected is implemented in Go. AppKit calls it on the main
// thread when the Quit item is chosen.
extern void codeatlasQuitMenuSelected(void);

@interface CodeAtlasQuitTarget : NSObject
- (void)quit:(id)sender;
@end

@implementation CodeAtlasQuitTarget
- (void)quit:(id)sender {
	(void)sender;
	codeatlasQuitMenuSelected();
}
@end

// codeatlas_install_quit_menu gives the application a menu bar holding the
// standard Quit item, which is what binds Command-Q. The webview window is
// created without a main menu, so without this the shortcut does nothing. The
// item targets our own handler rather than NSApplication's "terminate:" so the
// Go side closes the window and shuts the server down instead of the process
// exiting from under it. Must run on the main thread.
static void codeatlas_install_quit_menu(const char *appName) {
	@autoreleasepool {
		// The menu outlives every call, so a single handler is retained for
		// the lifetime of the process.
		static CodeAtlasQuitTarget *target = nil;
		if (target == nil) {
			target = [[CodeAtlasQuitTarget alloc] init];
		}
		NSString *name = nil;
		if (appName != NULL && appName[0] != '\0') {
			name = [NSString stringWithUTF8String:appName];
		}
		if (name == nil) {
			name = @"CodeAtlas";
		}
		NSMenu *menuBar = [[NSMenu alloc] init];
		NSMenuItem *applicationItem = [[NSMenuItem alloc] init];
		[menuBar addItem:applicationItem];
		NSMenu *applicationMenu = [[NSMenu alloc] initWithTitle:name];
		NSMenuItem *quitItem = [[NSMenuItem alloc]
			initWithTitle:[NSString stringWithFormat:@"Quit %@", name]
				   action:@selector(quit:)
			keyEquivalent:@"q"];
		[quitItem setTarget:target];
		[applicationMenu addItem:quitItem];
		[applicationItem setSubmenu:applicationMenu];
		[[NSApplication sharedApplication] setMainMenu:menuBar];
		[quitItem release];
		[applicationMenu release];
		[applicationItem release];
		[menuBar release];
	}
}
*/
import "C"

import (
	"sync"
	"unsafe"
)

// quitMenu holds the callback the Quit item invokes. The menu belongs to the
// application rather than to a window, so one handler covers the process.
var quitMenu struct {
	mu     sync.Mutex
	onQuit func()
}

func installQuitMenuNative(appName string, onQuit func()) {
	quitMenu.mu.Lock()
	quitMenu.onQuit = onQuit
	quitMenu.mu.Unlock()
	name := C.CString(appName)
	defer C.free(unsafe.Pointer(name))
	C.codeatlas_install_quit_menu(name)
}
