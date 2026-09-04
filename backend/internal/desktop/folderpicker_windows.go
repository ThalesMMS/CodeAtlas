//go:build windows

package desktop

import (
	"errors"
	"fmt"
	"syscall"
	"unsafe"

	"golang.org/x/sys/windows"
)

var (
	ole32DLL   = windows.NewLazySystemDLL("ole32.dll")
	shell32DLL = windows.NewLazySystemDLL("shell32.dll")

	procCoInitializeEx              = ole32DLL.NewProc("CoInitializeEx")
	procCoUninitialize              = ole32DLL.NewProc("CoUninitialize")
	procCoCreateInstance            = ole32DLL.NewProc("CoCreateInstance")
	procCoTaskMemFree               = ole32DLL.NewProc("CoTaskMemFree")
	procSHCreateItemFromParsingName = shell32DLL.NewProc("SHCreateItemFromParsingName")

	clsidFileOpenDialog = windows.GUID{Data1: 0xDC1C5A9C, Data2: 0xE88A, Data3: 0x4DDE, Data4: [8]byte{0xA5, 0xA1, 0x60, 0xF8, 0x2A, 0x20, 0xAE, 0xF7}}
	iidIFileOpenDialog  = windows.GUID{Data1: 0xD57C7288, Data2: 0xD4AD, Data3: 0x4768, Data4: [8]byte{0xBE, 0x02, 0x9D, 0x96, 0x95, 0x32, 0xD9, 0x60}}
	iidIShellItem       = windows.GUID{Data1: 0x43826D1E, Data2: 0xE718, Data3: 0x42EE, Data4: [8]byte{0xBC, 0x55, 0xA1, 0xE2, 0x61, 0xC3, 0x7B, 0xFE}}
)

const (
	coinitApartmentThreaded = 0x2
	clsctxInprocServer      = 0x1
	fosPickFolders          = 0x20
	fosForceFileSystem      = 0x40
	fosPathMustExist        = 0x800
	sigdnFileSysPath        = 0x80058000
	hresultSFalse           = 0x1
	hresultCancelled        = 0x800704C7
	hresultRPCChangedMode   = 0x80010106

	// IFileDialog / IFileOpenDialog vtable slots (IUnknown occupies 0-2).
	slotRelease          = 2
	slotShow             = 3
	slotSetOptions       = 9
	slotGetOptions       = 10
	slotSetFolder        = 12
	slotSetTitle         = 17
	slotSetOkButtonLabel = 18
	slotGetResult        = 20
	// IShellItem vtable slot.
	slotGetDisplayName = 5
)

func comMethod(object unsafe.Pointer, slot int) uintptr {
	vtable := *(**[32]uintptr)(object)
	return vtable[slot]
}

func comFailed(hr uintptr) bool { return int32(uint32(hr)) < 0 }

func comRelease(object unsafe.Pointer) {
	if object != nil {
		_, _, _ = syscall.SyscallN(comMethod(object, slotRelease), uintptr(object))
	}
}

// pickFolderNative shows the modern folder chooser (IFileOpenDialog with
// FOS_PICKFOLDERS) owned by the given window handle. It must run on the UI
// thread, which is where webview bindings are invoked.
func pickFolderNative(parent uintptr, initial string) (string, bool, error) {
	hr, _, _ := procCoInitializeEx.Call(0, coinitApartmentThreaded)
	switch uint32(hr) {
	case 0, hresultSFalse:
		defer procCoUninitialize.Call()
	case hresultRPCChangedMode:
		// COM is already initialized on this thread with another model; reuse it.
	default:
		return "", false, fmt.Errorf("CoInitializeEx failed: 0x%08X", uint32(hr))
	}

	var dialog unsafe.Pointer
	hr, _, _ = procCoCreateInstance.Call(
		uintptr(unsafe.Pointer(&clsidFileOpenDialog)), 0, clsctxInprocServer,
		uintptr(unsafe.Pointer(&iidIFileOpenDialog)), uintptr(unsafe.Pointer(&dialog)),
	)
	if comFailed(hr) || dialog == nil {
		return "", false, fmt.Errorf("could not create the folder chooser: 0x%08X", uint32(hr))
	}
	defer comRelease(dialog)

	var options uint32
	if hr, _, _ = syscall.SyscallN(comMethod(dialog, slotGetOptions), uintptr(dialog), uintptr(unsafe.Pointer(&options))); comFailed(hr) {
		return "", false, fmt.Errorf("could not read folder chooser options: 0x%08X", uint32(hr))
	}
	options |= fosPickFolders | fosForceFileSystem | fosPathMustExist
	if hr, _, _ = syscall.SyscallN(comMethod(dialog, slotSetOptions), uintptr(dialog), uintptr(options)); comFailed(hr) {
		return "", false, fmt.Errorf("could not configure the folder chooser: 0x%08X", uint32(hr))
	}
	if title, err := windows.UTF16PtrFromString("Choose workspace"); err == nil {
		_, _, _ = syscall.SyscallN(comMethod(dialog, slotSetTitle), uintptr(dialog), uintptr(unsafe.Pointer(title)))
	}
	if label, err := windows.UTF16PtrFromString("Choose"); err == nil {
		_, _, _ = syscall.SyscallN(comMethod(dialog, slotSetOkButtonLabel), uintptr(dialog), uintptr(unsafe.Pointer(label)))
	}
	if initial != "" {
		if item := shellItemFromPath(initial); item != nil {
			_, _, _ = syscall.SyscallN(comMethod(dialog, slotSetFolder), uintptr(dialog), uintptr(item))
			comRelease(item)
		}
	}

	hr, _, _ = syscall.SyscallN(comMethod(dialog, slotShow), uintptr(dialog), parent)
	if uint32(hr) == hresultCancelled {
		return "", true, nil
	}
	if comFailed(hr) {
		return "", false, fmt.Errorf("the folder chooser failed: 0x%08X", uint32(hr))
	}

	var result unsafe.Pointer
	if hr, _, _ = syscall.SyscallN(comMethod(dialog, slotGetResult), uintptr(dialog), uintptr(unsafe.Pointer(&result))); comFailed(hr) || result == nil {
		return "", false, fmt.Errorf("the folder chooser returned no selection: 0x%08X", uint32(hr))
	}
	defer comRelease(result)

	var name *uint16
	if hr, _, _ = syscall.SyscallN(comMethod(result, slotGetDisplayName), uintptr(result), sigdnFileSysPath, uintptr(unsafe.Pointer(&name))); comFailed(hr) || name == nil {
		return "", false, fmt.Errorf("the selected folder has no file-system path: 0x%08X", uint32(hr))
	}
	defer procCoTaskMemFree.Call(uintptr(unsafe.Pointer(name)))
	path := windows.UTF16PtrToString(name)
	if path == "" {
		return "", false, errors.New("the folder chooser returned an empty path")
	}
	return path, false, nil
}

func shellItemFromPath(path string) unsafe.Pointer {
	pathPointer, err := windows.UTF16PtrFromString(path)
	if err != nil {
		return nil
	}
	var item unsafe.Pointer
	hr, _, _ := procSHCreateItemFromParsingName.Call(
		uintptr(unsafe.Pointer(pathPointer)), 0,
		uintptr(unsafe.Pointer(&iidIShellItem)), uintptr(unsafe.Pointer(&item)),
	)
	if comFailed(hr) {
		return nil
	}
	return item
}
