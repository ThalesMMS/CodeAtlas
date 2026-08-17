//go:build desktop && windows

package desktop

import (
	"errors"
	"fmt"
	"strconv"
	"strings"

	"golang.org/x/sys/windows/registry"
)

const (
	webView2ProductID       = `{F3017226-FE2A-4295-8BDF-00C3A9A7E4C5}`
	webView2ClientPath      = `SOFTWARE\Microsoft\EdgeUpdate\Clients\` + webView2ProductID
	webView2WOW64ClientPath = `SOFTWARE\WOW6432Node\Microsoft\EdgeUpdate\Clients\` + webView2ProductID
)

var ErrWebView2Unavailable = errors.New("Microsoft WebView2 Runtime is required; install the Evergreen Runtime and start CodeAtlas again")

type registryLookup func(root registry.Key, path, name string) (string, error)

func platformWebViewPreflight() error {
	return checkWebView2(readRegistryString)
}

func readRegistryString(root registry.Key, path, name string) (string, error) {
	key, err := registry.OpenKey(root, path, registry.QUERY_VALUE)
	if err != nil {
		return "", err
	}
	defer key.Close()
	value, _, err := key.GetStringValue(name)
	return value, err
}

func checkWebView2(lookup registryLookup) error {
	locations := []struct {
		root registry.Key
		path string
	}{
		{registry.CURRENT_USER, webView2ClientPath},
		{registry.CURRENT_USER, webView2WOW64ClientPath},
		{registry.LOCAL_MACHINE, webView2ClientPath},
		{registry.LOCAL_MACHINE, webView2WOW64ClientPath},
	}
	var firstUnexpected error
	for _, location := range locations {
		version, err := lookup(location.root, location.path, "pv")
		if err != nil {
			if errors.Is(err, registry.ErrNotExist) {
				continue
			}
			if firstUnexpected == nil {
				firstUnexpected = err
			}
			continue
		}
		version = strings.TrimSpace(version)
		if version == "" || version == "0.0.0.0" {
			continue
		}
		if !validWebView2Version(version) {
			if firstUnexpected == nil {
				firstUnexpected = fmt.Errorf("invalid WebView2 Runtime version")
			}
			continue
		}
		return nil
	}
	if firstUnexpected != nil {
		return fmt.Errorf("check WebView2 Runtime: %s", safeFatalText(firstUnexpected.Error()))
	}
	return ErrWebView2Unavailable
}

func validWebView2Version(value string) bool {
	parts := strings.Split(value, ".")
	if len(parts) != 4 {
		return false
	}
	for _, part := range parts {
		if part == "" {
			return false
		}
		if _, err := strconv.ParseUint(part, 10, 32); err != nil {
			return false
		}
	}
	return true
}
