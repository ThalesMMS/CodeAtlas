package main

import "testing"

func TestFixtureForExecutableUsesClosedMap(t *testing.T) {
	for _, test := range []struct {
		name string
		want string
		ok   bool
	}{
		{name: "fake-gopls.exe", want: "fake-gopls.mjs", ok: true},
		{name: "fake-typescript-lsp", want: "fake-typescript-lsp.mjs", ok: true},
		{name: "fake-sourcekit-lsp.exe", want: "fake-sourcekit-lsp.mjs", ok: true},
		{name: "fake-pyright-langserver.exe", want: "fake-pyright-langserver.mjs", ok: true},
		{name: "fake-rust-analyzer.exe", want: "fake-rust-analyzer.mjs", ok: true},
		{name: "untrusted.exe", ok: false},
	} {
		t.Run(test.name, func(t *testing.T) {
			got, ok := fixtureForExecutable(test.name)
			if ok != test.ok || got != test.want {
				t.Fatalf("fixtureForExecutable(%q) = %q/%v, want %q/%v", test.name, got, ok, test.want, test.ok)
			}
		})
	}
}

func TestProbeOutputIsLimitedToKnownHelpers(t *testing.T) {
	if got, ok := probeOutput("pyright.exe", []string{"--version"}); !ok || got != "pyright 1.1.400\n" {
		t.Fatalf("pyright probe = %q/%v", got, ok)
	}
	if got, ok := probeOutput("swiftc.exe", []string{"--version"}); !ok || got != "Apple Swift version 6.3.3-fake (codeatlas fixture)\n" {
		t.Fatalf("swiftc probe = %q/%v", got, ok)
	}
	if _, ok := probeOutput("pyright.exe", []string{"--help"}); ok {
		t.Fatal("pyright accepted a non-version probe")
	}
}
