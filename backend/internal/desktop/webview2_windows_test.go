//go:build desktop && windows

package desktop

import (
	"errors"
	"strings"
	"testing"

	"golang.org/x/sys/windows/registry"
)

func TestCheckWebView2FindsPerUserAndMachineRuntimes(t *testing.T) {
	for _, tc := range []struct {
		name string
		root registry.Key
		path string
	}{
		{"per user", registry.CURRENT_USER, webView2ClientPath},
		{"machine 64 bit", registry.LOCAL_MACHINE, webView2ClientPath},
		{"machine 32 bit", registry.LOCAL_MACHINE, webView2WOW64ClientPath},
	} {
		t.Run(tc.name, func(t *testing.T) {
			lookup := func(root registry.Key, path, name string) (string, error) {
				if name != "pv" {
					t.Fatalf("registry value = %q, want pv", name)
				}
				if root == tc.root && path == tc.path {
					return "126.0.0.0", nil
				}
				return "", registry.ErrNotExist
			}
			if err := checkWebView2(lookup); err != nil {
				t.Fatal(err)
			}
		})
	}
}

func TestCheckWebView2ReportsUnavailableForMissingOrZeroVersions(t *testing.T) {
	for _, version := range []string{"", "0.0.0.0"} {
		t.Run(version, func(t *testing.T) {
			lookup := func(registry.Key, string, string) (string, error) {
				if version == "" {
					return "", registry.ErrNotExist
				}
				return version, nil
			}
			if err := checkWebView2(lookup); !errors.Is(err, ErrWebView2Unavailable) {
				t.Fatalf("checkWebView2() error = %v, want unavailable", err)
			}
		})
	}
}

func TestCheckWebView2SanitizesRegistryAndVersionErrors(t *testing.T) {
	t.Run("access", func(t *testing.T) {
		lookup := func(registry.Key, string, string) (string, error) {
			return "", errors.New("access denied api_key=sk-WEBVIEW-SECRET")
		}
		err := checkWebView2(lookup)
		if err == nil || errors.Is(err, ErrWebView2Unavailable) || strings.Contains(err.Error(), "sk-WEBVIEW-SECRET") {
			t.Fatalf("checkWebView2() error = %v", err)
		}
	})

	t.Run("malformed version", func(t *testing.T) {
		lookup := func(registry.Key, string, string) (string, error) { return "current", nil }
		err := checkWebView2(lookup)
		if err == nil || errors.Is(err, ErrWebView2Unavailable) {
			t.Fatalf("checkWebView2() error = %v, want malformed data", err)
		}
	})
}
