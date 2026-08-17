package main

import (
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
)

var fixtures = map[string]string{
	"fake-gopls":              "fake-gopls.mjs",
	"fake-typescript-lsp":     "fake-typescript-lsp.mjs",
	"fake-sourcekit-lsp":      "fake-sourcekit-lsp.mjs",
	"fake-pyright-langserver": "fake-pyright-langserver.mjs",
	"fake-rust-analyzer":      "fake-rust-analyzer.mjs",
}

func executableName(value string) string {
	base := strings.ToLower(filepath.Base(value))
	return strings.TrimSuffix(base, filepath.Ext(base))
}

func fixtureForExecutable(value string) (string, bool) {
	fixture, ok := fixtures[executableName(value)]
	return fixture, ok
}

func probeOutput(value string, args []string) (string, bool) {
	if len(args) != 1 || args[0] != "--version" {
		return "", false
	}
	switch executableName(value) {
	case "pyright":
		return "pyright 1.1.400\n", true
	case "swiftc":
		return "Apple Swift version 6.3.3-fake (codeatlas fixture)\n", true
	default:
		return "", false
	}
}

func main() {
	if output, ok := probeOutput(os.Args[0], os.Args[1:]); ok {
		_, _ = os.Stdout.WriteString(output)
		return
	}
	fixture, ok := fixtureForExecutable(os.Args[0])
	if !ok {
		fmt.Fprintln(os.Stderr, "unsupported CodeAtlas E2E launcher name")
		os.Exit(64)
	}
	executable, err := os.Executable()
	if err != nil {
		fmt.Fprintln(os.Stderr, "resolve launcher executable:", err)
		os.Exit(70)
	}
	harnessDir := filepath.Clean(filepath.Join(filepath.Dir(executable), "..", "..", "harness"))
	if err := os.Chdir(harnessDir); err != nil {
		fmt.Fprintln(os.Stderr, "enter repository E2E harness:", err)
		os.Exit(70)
	}
	script := filepath.Join(harnessDir, fixture)
	arguments := append([]string{script}, os.Args[1:]...)
	command := exec.Command("node", arguments...)
	command.Stdin = os.Stdin
	command.Stdout = os.Stdout
	command.Stderr = os.Stderr
	if err := command.Run(); err != nil {
		var exitError *exec.ExitError
		if errors.As(err, &exitError) {
			os.Exit(exitError.ExitCode())
		}
		fmt.Fprintln(os.Stderr, "start repository E2E fixture:", err)
		os.Exit(70)
	}
}
