package service

import (
	"bytes"
	"context"
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/ThalesMMS/CodeAtlas/internal/contenthash"
	"github.com/ThalesMMS/CodeAtlas/internal/domain"
	codeparser "github.com/ThalesMMS/CodeAtlas/internal/parser"
	"github.com/ThalesMMS/CodeAtlas/internal/repository"
)

func TestCopyFileSyncStreamsExactContent(t *testing.T) {
	t.Parallel()
	directory := t.TempDir()
	source := filepath.Join(directory, "source.bin")
	destination := filepath.Join(directory, "destination.bin")
	want := bytes.Repeat([]byte("streamed-copy-"), 32*1024)
	if err := os.WriteFile(source, want, 0o644); err != nil {
		t.Fatalf("write source: %v", err)
	}
	if err := copyFileSync(source, destination); err != nil {
		t.Fatalf("copyFileSync: %v", err)
	}
	got, err := os.ReadFile(destination)
	if err != nil {
		t.Fatalf("read destination: %v", err)
	}
	if !bytes.Equal(got, want) {
		t.Fatalf("copied %d bytes, want %d exact bytes", len(got), len(want))
	}
	info, err := os.Stat(destination)
	if err != nil {
		t.Fatal(err)
	}
	if runtime.GOOS != "windows" && info.Mode().Perm() != 0o600 {
		t.Fatalf("destination mode = %o, want 600", info.Mode().Perm())
	}
}

const oldSource = "package sample\n\nfunc Run() int { return 1 }\n"
const newSource = "package sample\n\nfunc Run() int { return 2 }\n"

type commitFixture struct {
	coord      *WorkspaceCommitCoordinator
	store      repository.Store
	root       string
	indexPath  string
	journalDir string
}

func setupCommit(t *testing.T) *commitFixture {
	t.Helper()
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "a.go"), []byte(oldSource), 0o600); err != nil {
		t.Fatal(err)
	}
	indexDir := t.TempDir()
	indexPath := filepath.Join(indexDir, "index.json")
	repository, err := repository.OpenJSON(indexPath)
	if err != nil {
		t.Fatal(err)
	}
	workspace := NewWorkspace(root)
	preparer := NewSavePreparer(workspace, repository, codeparser.New(), nil, 1_500_000)
	journalDir := filepath.Join(indexDir, "transactions")
	coord := NewWorkspaceCommitCoordinator(preparer, workspace, repository, journalDir, indexPath)
	return &commitFixture{coord: coord, store: repository, root: root, indexPath: indexPath, journalDir: journalDir}
}

func (f *commitFixture) prepare(t *testing.T) *PreparedSave {
	t.Helper()
	current, err := os.ReadFile(filepath.Join(f.root, "a.go"))
	if err != nil {
		t.Fatal(err)
	}
	prepared, err := f.coord.Prepare(context.Background(), SaveRequest{
		Path: "a.go", Content: []byte(newSource), ExpectedContentHash: contenthash.HashContent(current),
	})
	if err != nil {
		t.Fatalf("Prepare() error = %v", err)
	}
	return prepared
}

func (f *commitFixture) assertNoResidue(t *testing.T) {
	t.Helper()
	if matches, _ := filepath.Glob(filepath.Join(f.root, "*.cab-*")); len(matches) != 0 {
		t.Fatalf("source temp/backup residue: %v", matches)
	}
	if matches, _ := filepath.Glob(filepath.Join(f.journalDir, "*")); len(matches) != 0 {
		t.Fatalf("journal residue: %v", matches)
	}
}

func (f *commitFixture) reopenState(t *testing.T) (sourceContent string, storeVersion uint64) {
	t.Helper()
	repository, err := repository.OpenJSON(f.indexPath)
	if err != nil {
		t.Fatalf("reopen store error = %v", err)
	}
	data, err := os.ReadFile(filepath.Join(f.root, "a.go"))
	if err != nil {
		t.Fatal(err)
	}
	return string(data), repository.Version()
}

func TestCommitHappyPath(t *testing.T) {
	t.Parallel()
	f := setupCommit(t)
	version, err := f.coord.Commit(f.prepare(t), nil)
	if err != nil {
		t.Fatalf("Commit() error = %v", err)
	}
	if version != 1 {
		t.Fatalf("version = %d, want 1", version)
	}
	source, storeVersion := f.reopenState(t)
	if source != newSource || storeVersion != 1 {
		t.Fatalf("after commit source/version = %q/%d, want new/1", strings.TrimSpace(source), storeVersion)
	}
	f.assertNoResidue(t)
}

func TestCommitConsistencyUnderCrashAtEachPhase(t *testing.T) {
	t.Parallel()
	cases := []struct {
		phase      TxPhase
		wantSource string
		wantVer    uint64
	}{
		{PhasePrepared, oldSource, 0},        // never published -> old/old
		{PhaseSourcePublished, oldSource, 0}, // source published but repo not committed -> rollback old/old
		{PhaseCommitted, newSource, 1},       // already done -> new/new
	}
	for _, tc := range cases {
		tc := tc
		t.Run(string(tc.phase), func(t *testing.T) {
			t.Parallel()
			f := setupCommit(t)
			_, err := f.coord.Commit(f.prepare(t), func(p TxPhase) bool { return p == tc.phase })
			if !errors.Is(err, errSimulatedCrash) {
				t.Fatalf("Commit() error = %v, want simulated crash", err)
			}

			// Recovery (run twice to prove idempotence), as at startup before store load.
			for i := 0; i < 2; i++ {
				if err := RecoverTransactions(f.root, f.indexPath, f.journalDir); err != nil {
					t.Fatalf("RecoverTransactions() #%d error = %v", i, err)
				}
			}

			source, storeVersion := f.reopenState(t)
			if strings.TrimSpace(source) != strings.TrimSpace(tc.wantSource) || storeVersion != tc.wantVer {
				t.Fatalf("after recovery source/version = %q/%d, want %q/%d", strings.TrimSpace(source), storeVersion, strings.TrimSpace(tc.wantSource), tc.wantVer)
			}
			f.assertNoResidue(t)
		})
	}
}

func TestRecoveryObserverCountsOnlyProcessedJournals(t *testing.T) {
	t.Parallel()
	f := setupCommit(t)
	_, err := f.coord.Commit(f.prepare(t), func(phase TxPhase) bool { return phase == PhasePrepared })
	if !errors.Is(err, errSimulatedCrash) {
		t.Fatalf("Commit() error = %v, want simulated crash", err)
	}
	recoveries := 0
	for run := 0; run < 2; run++ {
		if err := RecoverTransactionsForIndexesObserved(f.root, f.journalDir, func() { recoveries++ }, f.indexPath); err != nil {
			t.Fatalf("recovery run %d: %v", run, err)
		}
	}
	if recoveries != 1 {
		t.Fatalf("recovery observer calls = %d, want 1", recoveries)
	}
}

func TestCommitRevalidationDetectsExternalEdit(t *testing.T) {
	t.Parallel()
	f := setupCommit(t)
	prepared := f.prepare(t)
	// External edit between prepare and commit.
	external := "package sample\n\nfunc Run() int { return 99 }\n"
	if err := os.WriteFile(filepath.Join(f.root, "a.go"), []byte(external), 0o600); err != nil {
		t.Fatal(err)
	}
	_, err := f.coord.Commit(prepared, nil)
	var saveErr *SaveError
	if !errors.As(err, &saveErr) || saveErr.Code != CodeFileChangedOnDisk {
		t.Fatalf("Commit() error = %v, want FILE_CHANGED_ON_DISK", err)
	}
	if data, _ := os.ReadFile(filepath.Join(f.root, "a.go")); string(data) != external {
		t.Fatal("external edit was overwritten by the save")
	}
	f.assertNoResidue(t)
}

func TestCommitRollbackOnIndexConflict(t *testing.T) {
	t.Parallel()
	f := setupCommit(t)
	prepared := f.prepare(t)
	// A concurrent change advances the store version, making the prepared index stale.
	f.store.ReplaceFile(domain.ParsedFile{
		File:    domain.File{Path: "other.go", Hash: "x"},
		Symbols: []domain.Symbol{{ID: "other", Path: "other.go", Kind: "file", Range: domain.Range{Start: domain.Position{Line: 1, Column: 1}, End: domain.Position{Line: 2, Column: 1}}}},
	})
	_, err := f.coord.Commit(prepared, nil)
	var saveErr *SaveError
	if !errors.As(err, &saveErr) || saveErr.Code != CodeSaveStoreConflict {
		t.Fatalf("Commit() error = %v, want STORE_VERSION_CONFLICT", err)
	}
	// The source was rolled back to the old content (old/old).
	if data, _ := os.ReadFile(filepath.Join(f.root, "a.go")); string(data) != oldSource {
		t.Fatal("source was not rolled back after an index conflict")
	}
	f.assertNoResidue(t)
}
