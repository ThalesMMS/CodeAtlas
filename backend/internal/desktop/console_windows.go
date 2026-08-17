//go:build windows

package desktop

import (
	"errors"
	"fmt"
	"os"

	"golang.org/x/sys/windows"
)

var attachConsole = windows.NewLazySystemDLL("kernel32.dll").NewProc("AttachConsole")

func PrepareHeadlessConsole() error {
	const attachParentProcess = ^uint32(0)
	attached, _, callErr := attachConsole.Call(uintptr(attachParentProcess))
	if attached == 0 && !errors.Is(callErr, windows.ERROR_ACCESS_DENIED) {
		return fmt.Errorf("attach to parent console: %w", callErr)
	}
	stdin, err := os.OpenFile("CONIN$", os.O_RDONLY, 0)
	if err != nil {
		return fmt.Errorf("open console input: %w", err)
	}
	stdout, err := os.OpenFile("CONOUT$", os.O_WRONLY, 0)
	if err != nil {
		stdin.Close()
		return fmt.Errorf("open console output: %w", err)
	}
	stderr, err := os.OpenFile("CONOUT$", os.O_WRONLY, 0)
	if err != nil {
		stdin.Close()
		stdout.Close()
		return fmt.Errorf("open console error output: %w", err)
	}
	os.Stdin = stdin
	os.Stdout = stdout
	os.Stderr = stderr
	return nil
}
