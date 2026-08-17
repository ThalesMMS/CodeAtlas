package lspconv

import (
	"fmt"
	"net/url"
	"path/filepath"
	"strings"
)

// PathToURI converts an absolute path to a file:// URI. It normalizes separators
// and percent-encoding so it is correct on Linux/macOS/Windows.
func PathToURI(absPath string) string {
	slashed := filepath.ToSlash(absPath)
	if !strings.HasPrefix(slashed, "/") {
		// Windows drive path like C:/...; file URIs use an empty authority + /C:/...
		slashed = "/" + slashed
	}
	return (&url.URL{Scheme: "file", Path: slashed}).String()
}

// URIToPath converts a file:// URI back to a local path, rejecting unexpected
// schemes without panicking.
func URIToPath(uri string) (string, error) {
	parsed, err := url.Parse(uri)
	if err != nil {
		return "", fmt.Errorf("lspconv: bad URI: %w", err)
	}
	if parsed.Scheme != "file" {
		return "", fmt.Errorf("lspconv: unsupported URI scheme %q", parsed.Scheme)
	}
	path := parsed.Path
	// Strip a leading slash from a Windows drive path (/C:/...).
	if len(path) >= 3 && path[0] == '/' && path[2] == ':' {
		path = path[1:]
	}
	return filepath.FromSlash(path), nil
}

// WorkspaceRelative converts a file:// URI to a workspace-relative slash path.
// ok is false when the URI is outside the workspace (the caller represents it as
// an external boundary rather than a local file) or not a file URI.
func WorkspaceRelative(workspaceRoot, uri string) (relative string, ok bool) {
	path, err := URIToPath(uri)
	if err != nil {
		return "", false
	}
	absRoot, err := filepath.Abs(workspaceRoot)
	if err != nil {
		return "", false
	}
	rel, err := filepath.Rel(absRoot, path)
	if err != nil {
		return "", false
	}
	rel = filepath.ToSlash(rel)
	if rel == ".." || strings.HasPrefix(rel, "../") {
		return "", false // outside the workspace
	}
	return rel, true
}
