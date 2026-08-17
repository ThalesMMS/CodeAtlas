package service

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/ThalesMMS/CodeAtlas/internal/contenthash"
	"github.com/ThalesMMS/CodeAtlas/internal/repository"
)

// observableState is the canonical, content-bearing view used to prove the
// atomicity invariant: after any return or recovery the source file, the on-disk
// snapshot and the in-memory store all represent the old or the new version.
type observableState struct {
	sourceHash   string
	diskVersion  uint64
	storeVersion uint64
}

func (f *commitFixture) observe(t *testing.T) observableState {
	t.Helper()
	source, err := os.ReadFile(filepath.Join(f.root, "a.go"))
	if err != nil {
		t.Fatal(err)
	}
	diskVersion, _ := snapshotVersion(f.indexPath)
	repository, err := repository.OpenJSON(f.indexPath)
	if err != nil {
		t.Fatalf("reopen store error = %v", err)
	}
	return observableState{
		sourceHash:   contenthash.HashContent(source),
		diskVersion:  diskVersion,
		storeVersion: repository.Version(),
	}
}

// TestSaveCommitReadersSeeOnlyCanonicalState runs readers concurrently with a
// journaled commit. Every ReadView observation must be a complete old or new
// pair (source hash and store version together), never a mixed combination.
func TestSaveCommitReadersSeeOnlyCanonicalState(t *testing.T) {
	t.Parallel()
	f := setupCommit(t)
	oldHash := contenthash.HashContent([]byte(oldSource))
	newHash := contenthash.HashContent([]byte(newSource))
	prepared := f.prepare(t)

	stop := make(chan struct{})
	readerDone := make(chan struct{})
	go func() {
		defer close(readerDone)
		for {
			select {
			case <-stop:
				return
			default:
			}
			_, hash, version, err := f.coord.ReadView("a.go")
			if err != nil {
				continue
			}
			isOld := hash == oldHash && version == 0
			isNew := hash == newHash && version == 1
			if !isOld && !isNew {
				t.Errorf("mixed state observed: hash=%s version=%d", hash, version)
				return
			}
		}
	}()

	version, err := f.coord.Commit(prepared, nil)
	if err != nil {
		t.Fatalf("Commit() error = %v", err)
	}
	if version != 1 {
		t.Fatalf("commit version = %d, want 1", version)
	}
	close(stop)
	<-readerDone

	// The HTTP-visible result must match what is actually published.
	final := f.observe(t)
	if final.sourceHash != newHash || final.diskVersion != 1 || final.storeVersion != 1 {
		t.Fatalf("post-commit state = %+v, want new/new", final)
	}
}

// TestSaveCrashRecoveryReachesCanonicalState crashes after each journal phase,
// runs recovery twice (idempotence), and asserts the full observable state is
// exactly the old or the new version — never mixed.
func TestSaveCrashRecoveryReachesCanonicalState(t *testing.T) {
	t.Parallel()
	oldHash := contenthash.HashContent([]byte(oldSource))
	newHash := contenthash.HashContent([]byte(newSource))
	cases := []struct {
		phase TxPhase
		want  observableState
	}{
		{PhasePrepared, observableState{sourceHash: oldHash, diskVersion: 0, storeVersion: 0}},
		{PhaseSourcePublished, observableState{sourceHash: oldHash, diskVersion: 0, storeVersion: 0}},
		{PhaseCommitted, observableState{sourceHash: newHash, diskVersion: 1, storeVersion: 1}},
	}
	for _, tc := range cases {
		tc := tc
		t.Run(string(tc.phase), func(t *testing.T) {
			t.Parallel()
			f := setupCommit(t)
			if _, err := f.coord.Commit(f.prepare(t), func(p TxPhase) bool { return p == tc.phase }); err == nil {
				t.Fatal("Commit should have reported a simulated crash")
			}
			for i := 0; i < 2; i++ {
				if err := RecoverTransactions(f.root, f.indexPath, f.journalDir); err != nil {
					t.Fatalf("RecoverTransactions() #%d error = %v", i, err)
				}
			}
			if got := f.observe(t); got != tc.want {
				t.Fatalf("after recovery state = %+v, want %+v", got, tc.want)
			}
			f.assertNoResidue(t)
		})
	}
}
