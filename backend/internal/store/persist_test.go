package store

import (
	"bytes"
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/ThalesMMS/CodeAtlas/internal/changeset"
	"github.com/ThalesMMS/CodeAtlas/internal/domain"
)

// faultFS wraps a real fileSystem and injects a failure at one protocol step.
type faultFS struct {
	inner  fileSystem
	failOn string
}

func (f *faultFS) MkdirAll(path string, perm os.FileMode) error { return f.inner.MkdirAll(path, perm) }
func (f *faultFS) Remove(name string) error                     { return f.inner.Remove(name) }
func (f *faultFS) ReadFile(name string) ([]byte, error)         { return f.inner.ReadFile(name) }
func (f *faultFS) Glob(pattern string) ([]string, error)        { return f.inner.Glob(pattern) }
func (f *faultFS) SyncDir(dir string) error                     { return f.inner.SyncDir(dir) }

func (f *faultFS) Rename(oldpath, newpath string) error {
	if f.failOn == "rename" {
		return errors.New("injected rename failure")
	}
	return f.inner.Rename(oldpath, newpath)
}

func (f *faultFS) CreateTemp(dir, pattern string) (tempHandle, error) {
	if f.failOn == "create" {
		return nil, errors.New("injected create failure")
	}
	handle, err := f.inner.CreateTemp(dir, pattern)
	if err != nil {
		return nil, err
	}
	return &faultTemp{inner: handle, failOn: f.failOn}, nil
}

type faultTemp struct {
	inner  tempHandle
	failOn string
}

func (t *faultTemp) Name() string { return t.inner.Name() }
func (t *faultTemp) Write(p []byte) (int, error) {
	if t.failOn == "write" {
		return 0, errors.New("injected write failure")
	}
	return t.inner.Write(p)
}
func (t *faultTemp) Sync() error {
	if t.failOn == "sync" {
		return errors.New("injected sync failure")
	}
	return t.inner.Sync()
}
func (t *faultTemp) Close() error {
	err := t.inner.Close()
	if t.failOn == "close" {
		return errors.New("injected close failure")
	}
	return err
}

func parsed(path string) domain.ParsedFile {
	return domain.ParsedFile{
		File: domain.File{Path: path, Language: "go", Hash: path},
		Symbols: []domain.Symbol{
			{ID: path, Path: path, Name: path, Kind: "file", Range: domain.Range{Start: domain.Position{Line: 1, Column: 1}, End: domain.Position{Line: 2, Column: 1}}},
		},
	}
}

func commit(t *testing.T, repository *Store, path string) {
	t.Helper()
	cs, err := changeset.NewBuilder().Upsert(parsed(path)).Build(time.Date(2026, 6, 24, 0, 0, 0, 0, time.UTC))
	if err != nil {
		t.Fatalf("Build() error = %v", err)
	}
	prepared, err := repository.Prepare(cs)
	if err != nil {
		t.Fatalf("Prepare() error = %v", err)
	}
	if err := repository.CommitPrepared(prepared); err != nil {
		t.Fatalf("CommitPrepared() error = %v", err)
	}
}

func tempResidue(t *testing.T, dir string) []string {
	t.Helper()
	matches, _ := filepath.Glob(filepath.Join(dir, "index.json.*.tmp"))
	return matches
}

func TestCommitFaultInjectionLeavesStateUntouched(t *testing.T) {
	t.Parallel()
	for _, failOn := range []string{"create", "write", "sync", "close", "rename"} {
		t.Run(failOn, func(t *testing.T) {
			t.Parallel()
			dir := t.TempDir()
			path := filepath.Join(dir, "index.json")
			repository, err := Open(path)
			if err != nil {
				t.Fatalf("Open() error = %v", err)
			}
			commit(t, repository, "a.go") // baseline official snapshot
			baseData, _ := os.ReadFile(path)
			baseVersion := repository.Version()

			repository.persister.fs = &faultFS{inner: osFileSystem{}, failOn: failOn}
			cs, err := changeset.NewBuilder().Upsert(parsed("b.go")).Build(time.Date(2026, 6, 24, 0, 0, 0, 0, time.UTC))
			if err != nil {
				t.Fatal(err)
			}
			prepared, err := repository.Prepare(cs)
			if err != nil {
				t.Fatalf("Prepare() error = %v", err)
			}
			if err := repository.CommitPrepared(prepared); err == nil {
				t.Fatalf("CommitPrepared succeeded despite injected %s failure", failOn)
			}

			if got, _ := os.ReadFile(path); !bytes.Equal(got, baseData) {
				t.Fatal("official snapshot changed after a failed commit")
			}
			if repository.Version() != baseVersion {
				t.Fatalf("memory version advanced to %d after failure", repository.Version())
			}
			if _, ok := repository.FileHash("b.go"); ok {
				t.Fatal("candidate leaked into active memory after failure")
			}
			if residue := tempResidue(t, dir); len(residue) != 0 {
				t.Fatalf("temp residue left after failure: %v", residue)
			}
		})
	}
}

func TestSuccessfulCommitLeavesNoTemp(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	path := filepath.Join(dir, "index.json")
	repository, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	commit(t, repository, "a.go")
	commit(t, repository, "b.go")
	if residue := tempResidue(t, dir); len(residue) != 0 {
		t.Fatalf("temp residue after successful commits: %v", residue)
	}
}

func TestVersionConflictDiscardsTemp(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	path := filepath.Join(dir, "index.json")
	repository, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	commit(t, repository, "a.go")
	official, _ := os.ReadFile(path)

	build := func(p string) *changeset.ChangeSet {
		cs, err := changeset.NewBuilder().Upsert(parsed(p)).Build(time.Date(2026, 6, 24, 0, 0, 0, 0, time.UTC))
		if err != nil {
			t.Fatal(err)
		}
		return cs
	}
	first, _ := repository.Prepare(build("b.go"))
	second, _ := repository.Prepare(build("c.go"))
	if err := repository.CommitPrepared(first); err != nil {
		t.Fatalf("first commit error = %v", err)
	}
	afterFirst, _ := os.ReadFile(path)
	if err := repository.CommitPrepared(second); !errors.Is(err, ErrVersionConflict) {
		t.Fatalf("second commit error = %v, want ErrVersionConflict", err)
	}
	// The conflicted commit must not publish and must leave no temp.
	if got, _ := os.ReadFile(path); !bytes.Equal(got, afterFirst) {
		t.Fatal("conflicted commit changed the official snapshot")
	}
	if bytes.Equal(official, afterFirst) {
		t.Fatal("first commit did not change the official snapshot (test setup invalid)")
	}
	if residue := tempResidue(t, dir); len(residue) != 0 {
		t.Fatalf("temp residue after version conflict: %v", residue)
	}
}

func TestOpenCleansStrayTemps(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	path := filepath.Join(dir, "index.json")
	repository, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	commit(t, repository, "a.go")

	stray := filepath.Join(dir, "index.json.stray.tmp")
	if err := os.WriteFile(stray, []byte("garbage"), 0o600); err != nil {
		t.Fatal(err)
	}

	reopened, err := Open(path)
	if err != nil {
		t.Fatalf("Open(reopen) error = %v", err)
	}
	if _, ok := reopened.FileHash("a.go"); !ok {
		t.Fatal("official snapshot not loaded after reopen")
	}
	if _, err := os.Stat(stray); !errors.Is(err, os.ErrNotExist) {
		t.Fatal("stray temp was not removed on Open")
	}
}

func TestOpenRecoversFromValidBackup(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	path := filepath.Join(dir, "index.json")
	repository, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	commit(t, repository, "a.go")
	valid, _ := os.ReadFile(path)

	// Simulate a crash that left a corrupt official and a valid backup.
	if err := os.WriteFile(path+".bak", valid, 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte("{ truncated"), 0o600); err != nil {
		t.Fatal(err)
	}

	recovered, err := Open(path)
	if err != nil {
		t.Fatalf("Open did not recover from a valid backup: %v", err)
	}
	if _, ok := recovered.FileHash("a.go"); !ok {
		t.Fatal("recovery did not restore the backed-up state")
	}
}

func TestOpenRejectsCorruptOfficialWithoutBackup(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	path := filepath.Join(dir, "index.json")
	if err := os.WriteFile(path, []byte("{ truncated"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := Open(path); err == nil {
		t.Fatal("Open accepted a corrupt official snapshot with no backup")
	}
}
