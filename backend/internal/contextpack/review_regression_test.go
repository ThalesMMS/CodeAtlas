package contextpack

import (
	"testing"

	"github.com/ThalesMMS/CodeAtlas/internal/domain"
)

func TestWorkspacePackageDirSupportsNestedModules(t *testing.T) {
	t.Parallel()
	files := []domain.File{{Path: "services/payments/go.mod", Content: "module example.com/payments\n"}}
	if got := workspacePackageDir(files, "example.com/payments/internal/ledger"); got != "services/payments/internal/ledger" {
		t.Fatalf("workspacePackageDir() = %q, want nested module directory", got)
	}
}

func TestProjectFileInScopeContinuesPastRootDirectory(t *testing.T) {
	t.Parallel()
	request := ContextRequest{Options: ContextOptions{Scope: ScopeModule}}
	if !projectFileInScope("pkg/config.yaml", request, []string{".", "pkg"}) {
		t.Fatal("nested project file was rejected after checking root scope")
	}
	if projectFileInScope("other/config.yaml", request, []string{".", "pkg"}) {
		t.Fatal("unscoped nested project file was accepted")
	}
}

func TestPackageAPIRankDoesNotMasqueradeAsLexicalRank(t *testing.T) {
	t.Parallel()
	candidate := Candidate{PackageAPIRank: 1}
	if candidate.LexicalRank != 0 {
		t.Fatalf("package API candidate lexical rank = %d, want zero", candidate.LexicalRank)
	}
	if reciprocalRankFusion(candidate) <= 0 {
		t.Fatal("package API rank did not contribute to deterministic ordering")
	}
}
