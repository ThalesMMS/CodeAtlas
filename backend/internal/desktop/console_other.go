//go:build !windows

package desktop

func PrepareHeadlessConsole() error { return nil }
