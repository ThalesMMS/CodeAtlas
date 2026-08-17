// Package pathutil owns the canonical representation of workspace-relative
// paths. Its output always uses forward slashes, independent of the host OS.
package pathutil

import (
	"errors"
	"path"
	"strings"
)

// ErrInvalidWorkspaceRelative reports an empty, absolute, external, or
// workspace-escaping path.
var ErrInvalidWorkspaceRelative = errors.New("invalid workspace-relative path")

// NormalizeWorkspaceRelative returns a clean workspace-relative path using
// forward slashes. Both slash styles are accepted at input; absolute paths,
// URLs, NUL bytes, drive-qualified paths, and workspace escapes are rejected.
func NormalizeWorkspaceRelative(value string) (string, error) {
	value = strings.TrimSpace(value)
	if value == "" || strings.ContainsRune(value, '\x00') || strings.Contains(value, "://") {
		return "", ErrInvalidWorkspaceRelative
	}

	value = strings.ReplaceAll(value, "\\", "/")
	if path.IsAbs(value) || isDriveQualified(value) {
		return "", ErrInvalidWorkspaceRelative
	}

	normalized := path.Clean(value)
	if normalized == "." || normalized == ".." || strings.HasPrefix(normalized, "../") {
		return "", ErrInvalidWorkspaceRelative
	}
	return normalized, nil
}

func isDriveQualified(value string) bool {
	if len(value) < 2 || value[1] != ':' {
		return false
	}
	first := value[0]
	return first >= 'A' && first <= 'Z' || first >= 'a' && first <= 'z'
}
