package scheduler

import (
	"reflect"
	"testing"

	"github.com/ThalesMMS/CodeAtlas/internal/domain"
)

func TestScopeResolverKeepsFileHintsAsSortedPaths(t *testing.T) {
	scope := NewScopeResolver().Resolve([]domain.ChangeHint{
		{Sequence: 4, Path: "pkg/b.go", Operations: domain.OpWrite, Kind: "file"},
		{Sequence: 2, Path: "./pkg/a.go", Operations: domain.OpCreate, Kind: "file"},
	})

	if scope.Mode != domain.ReconcilePaths {
		t.Fatalf("mode = %s, want paths", scope.Mode)
	}
	if !reflect.DeepEqual(scope.Paths, []string{"pkg/a.go", "pkg/b.go"}) {
		t.Fatalf("paths = %v, want sorted canonical paths", scope.Paths)
	}
	if scope.MinSequence != 2 || scope.MaxSequence != 4 {
		t.Fatalf("sequence bounds = %d..%d, want 2..4", scope.MinSequence, scope.MaxSequence)
	}
}

func TestScopeResolverEscalatesDirectoriesAndIgnoreChanges(t *testing.T) {
	resolver := NewScopeResolver()

	dirScope := resolver.Resolve([]domain.ChangeHint{
		{Sequence: 1, Path: "pkg/new", Operations: domain.OpCreate, Kind: "directory"},
	})
	if dirScope.Mode != domain.ReconcileSubtree || dirScope.Root != "pkg" {
		t.Fatalf("directory scope = %#v, want subtree rooted at safe parent pkg", dirScope)
	}
	if !containsString(dirScope.ReasonCodes, ReasonDirectoryHint) {
		t.Fatalf("directory reason codes = %v, want %s", dirScope.ReasonCodes, ReasonDirectoryHint)
	}

	ignoreScope := resolver.Resolve([]domain.ChangeHint{
		{Sequence: 2, Path: "pkg/.codeatlasignore", Operations: domain.OpWrite, Kind: "file"},
	})
	if ignoreScope.Mode != domain.ReconcileSubtree || ignoreScope.Root != "pkg" {
		t.Fatalf("nested ignore scope = %#v, want subtree pkg", ignoreScope)
	}

	rootIgnoreScope := resolver.Resolve([]domain.ChangeHint{
		{Sequence: 3, Path: ".gitignore", Operations: domain.OpWrite, Kind: "file"},
	})
	if rootIgnoreScope.Mode != domain.ReconcileFull {
		t.Fatalf("root ignore scope = %#v, want full", rootIgnoreScope)
	}
}

func TestScopeResolverEscalatesDesyncToFull(t *testing.T) {
	scope := NewScopeResolver().Resolve([]domain.ChangeHint{
		{Sequence: 1, Operations: domain.OpRescanRequired, Kind: "unknown", ReasonCode: ReasonWatcherDesync},
		{Sequence: 2, Path: "pkg/a.go", Operations: domain.OpWrite, Kind: "file"},
	})

	if scope.Mode != domain.ReconcileFull {
		t.Fatalf("mode = %s, want full", scope.Mode)
	}
	if scope.MinSequence != 1 || scope.MaxSequence != 2 {
		t.Fatalf("sequence bounds = %d..%d, want 1..2", scope.MinSequence, scope.MaxSequence)
	}
	if !containsString(scope.ReasonCodes, ReasonWatcherDesync) {
		t.Fatalf("reason codes = %v, want desync", scope.ReasonCodes)
	}
}

func TestScopeResolverDoesNotDropPathsWhenBatchEscalatesToSubtree(t *testing.T) {
	scope := NewScopeResolver().Resolve([]domain.ChangeHint{
		{Sequence: 1, Path: "a/x.go", Operations: domain.OpWrite, Kind: "file"},
		{Sequence: 2, Path: "b/sub", Operations: domain.OpRename, Kind: "directory"},
	})

	if scope.Mode != domain.ReconcileFull {
		t.Fatalf("mixed unrelated paths scope = %#v, want full coverage", scope)
	}
	if scope.MinSequence != 1 || scope.MaxSequence != 2 {
		t.Fatalf("sequence bounds = %d..%d, want 1..2", scope.MinSequence, scope.MaxSequence)
	}
}

func TestScopeResolverDropsUnsafeFilePath(t *testing.T) {
	scope := NewScopeResolver().Resolve([]domain.ChangeHint{
		{Sequence: 9, Path: `..\secret.go`, Operations: domain.OpWrite, Kind: "file"},
	})
	if scope.Mode != domain.ReconcilePaths || len(scope.Paths) != 0 {
		t.Fatalf("unsafe file scope = %#v, want empty path reconciliation", scope)
	}
	if scope.MinSequence != 9 || scope.MaxSequence != 9 {
		t.Fatalf("unsafe file sequence bounds = %d..%d, want 9..9", scope.MinSequence, scope.MaxSequence)
	}
}

func containsString(values []string, want string) bool {
	for _, value := range values {
		if value == want {
			return true
		}
	}
	return false
}
