package scheduler

import (
	"path/filepath"
	"sort"
	"strings"

	"github.com/ThalesMMS/CodeAtlas/internal/domain"
	"github.com/ThalesMMS/CodeAtlas/internal/pathutil"
)

type ScopeResolver struct{}

func NewScopeResolver() ScopeResolver { return ScopeResolver{} }

func (ScopeResolver) Resolve(hints []domain.ChangeHint) domain.ReconcileScope {
	scope := domain.ReconcileScope{Mode: domain.ReconcilePaths}
	paths := make(map[string]struct{})
	reasons := make(map[string]struct{})
	subtreeRoot := ""

	for _, hint := range hints {
		path := cleanRelative(hint.Path)
		if hint.Sequence > 0 {
			if scope.MinSequence == 0 || hint.Sequence < scope.MinSequence {
				scope.MinSequence = hint.Sequence
			}
			if hint.Sequence > scope.MaxSequence {
				scope.MaxSequence = hint.Sequence
			}
		}
		if hint.ReasonCode != "" {
			reasons[hint.ReasonCode] = struct{}{}
		}
		if hint.Operations.Has(domain.OpRescanRequired) || hint.ReasonCode == ReasonWatcherDesync {
			scope.Mode = domain.ReconcileFull
			reasons[ReasonWatcherDesync] = struct{}{}
			continue
		}
		if isRootIgnoreFile(path) {
			scope.Mode = domain.ReconcileFull
			reasons[ReasonIgnoreChange] = struct{}{}
			continue
		}
		if isNestedIgnoreFile(path) && scope.Mode != domain.ReconcileFull {
			scope.Mode = domain.ReconcileSubtree
			reasons[ReasonIgnoreChange] = struct{}{}
			subtreeRoot = commonRoot(subtreeRoot, parent(path))
			continue
		}
		if hint.Kind == "directory" || hint.Operations.Has(domain.OpRename) {
			if scope.Mode != domain.ReconcileFull {
				scope.Mode = domain.ReconcileSubtree
				reasons[ReasonDirectoryHint] = struct{}{}
				subtreeRoot = commonRoot(subtreeRoot, parent(path))
			}
			continue
		}
		if path != "" {
			paths[path] = struct{}{}
		}
	}

	switch scope.Mode {
	case domain.ReconcileFull:
		scope.Paths = nil
		scope.Root = ""
	case domain.ReconcileSubtree:
		for path := range paths {
			subtreeRoot = commonRoot(subtreeRoot, parent(path))
		}
		if subtreeRoot == "." {
			scope.Mode = domain.ReconcileFull
			scope.Paths = nil
			scope.Root = ""
			break
		}
		scope.Paths = nil
		scope.Root = subtreeRoot
		if scope.Root == "" {
			scope.Root = "."
		}
	default:
		scope.Paths = sortedKeys(paths)
	}
	scope.ReasonCodes = sortedKeys(reasons)
	return scope
}

func cleanRelative(value string) string {
	value, err := pathutil.NormalizeWorkspaceRelative(value)
	if err != nil {
		return ""
	}
	return value
}

func isRootIgnoreFile(path string) bool {
	return path == ".gitignore" || path == ".codeatlasignore"
}

func isNestedIgnoreFile(path string) bool {
	return strings.HasSuffix(path, "/.gitignore") || strings.HasSuffix(path, "/.codeatlasignore")
}

func parent(path string) string {
	if path == "" {
		return "."
	}
	dir := filepath.ToSlash(filepath.Dir(path))
	if dir == "." || dir == "" {
		return "."
	}
	return dir
}

func commonRoot(a, b string) string {
	if a == "" {
		return b
	}
	if b == "" {
		return a
	}
	if a == "." || b == "." {
		return "."
	}
	ap := strings.Split(a, "/")
	bp := strings.Split(b, "/")
	limit := len(ap)
	if len(bp) < limit {
		limit = len(bp)
	}
	out := make([]string, 0, limit)
	for i := 0; i < limit; i++ {
		if ap[i] != bp[i] {
			break
		}
		out = append(out, ap[i])
	}
	if len(out) == 0 {
		return "."
	}
	return strings.Join(out, "/")
}

func sortedKeys[T any](input map[string]T) []string {
	out := make([]string, 0, len(input))
	for key := range input {
		out = append(out, key)
	}
	sort.Strings(out)
	return out
}
