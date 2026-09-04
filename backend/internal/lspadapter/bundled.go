package lspadapter

import (
	"os"
	"path/filepath"
	"runtime"
)

// ResolveBundledExecutable keeps explicit operator paths intact. When the
// default command name is in use, packaged desktop builds prefer the language
// server shipped with the application so Finder/Explorer PATH differences do
// not disable semantic features.
func ResolveBundledExecutable(configured, defaultName string) string {
	if configured != defaultName {
		return configured
	}
	executable, err := os.Executable()
	if err != nil {
		return configured
	}
	return ResolveBundledExecutableAt(configured, defaultName, executable, runtime.GOOS)
}

// ResolveBundledExecutableAt is the testable core of ResolveBundledExecutable.
func ResolveBundledExecutableAt(configured, defaultName, executable, goos string) string {
	if configured != defaultName {
		return configured
	}
	var candidate string
	switch goos {
	case "darwin":
		candidate = filepath.Clean(filepath.Join(filepath.Dir(executable), "..", "Resources", "bin", defaultName))
	case "windows":
		candidate = filepath.Join(filepath.Dir(executable), defaultName+".exe")
	default:
		return configured
	}
	info, err := os.Stat(candidate)
	if err != nil || !info.Mode().IsRegular() {
		return configured
	}
	if goos != "windows" && info.Mode().Perm()&0o111 == 0 {
		return configured
	}
	return candidate
}

// BundledResourceDir returns the bundle resource directory holding relative
// when it exists next to the running executable, or "" otherwise. It is used
// for non-executable payloads such as the TypeScript SDK directory.
func BundledResourceDir(relative ...string) string {
	executable, err := os.Executable()
	if err != nil {
		return ""
	}
	return BundledResourceDirAt(executable, runtime.GOOS, relative...)
}

// BundledResourceDirAt is the testable core of BundledResourceDir.
func BundledResourceDirAt(executable, goos string, relative ...string) string {
	var base string
	switch goos {
	case "darwin":
		base = filepath.Clean(filepath.Join(filepath.Dir(executable), "..", "Resources"))
	case "windows":
		base = filepath.Dir(executable)
	default:
		return ""
	}
	candidate := filepath.Join(append([]string{base}, relative...)...)
	info, err := os.Stat(candidate)
	if err != nil || !info.IsDir() {
		return ""
	}
	return candidate
}
