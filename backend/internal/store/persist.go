package store

import (
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"

	"github.com/ThalesMMS/CodeAtlas/internal/domain"
)

// fileSystem is the minimal, injectable surface the snapshot protocol needs.
// Tests substitute a fault-injecting implementation to exercise every boundary;
// production uses osFileSystem.
type fileSystem interface {
	MkdirAll(path string, perm os.FileMode) error
	CreateTemp(dir, pattern string) (tempHandle, error)
	Rename(oldpath, newpath string) error
	Remove(name string) error
	ReadFile(name string) ([]byte, error)
	Glob(pattern string) ([]string, error)
	SyncDir(dir string) error
}

// tempHandle is the subset of *os.File used while writing a snapshot.
type tempHandle interface {
	Name() string
	Write(p []byte) (int, error)
	Sync() error
	Close() error
}

// osFileSystem is the production fileSystem backed by the os package. Replace is
// an atomic rename on POSIX; on Windows, Rename over an existing file is not
// atomic and a backup-based strategy (see recovery) is required — this interface
// isolates that platform difference.
type osFileSystem struct{}

func (osFileSystem) MkdirAll(path string, perm os.FileMode) error { return os.MkdirAll(path, perm) }
func (osFileSystem) Rename(oldpath, newpath string) error         { return os.Rename(oldpath, newpath) }
func (osFileSystem) Remove(name string) error                     { return os.Remove(name) }
func (osFileSystem) ReadFile(name string) ([]byte, error)         { return os.ReadFile(name) }
func (osFileSystem) Glob(pattern string) ([]string, error)        { return filepath.Glob(pattern) }

func (osFileSystem) CreateTemp(dir, pattern string) (tempHandle, error) {
	file, err := os.CreateTemp(dir, pattern)
	if err != nil {
		return nil, err
	}
	if err := file.Chmod(0o600); err != nil {
		_ = file.Close()
		_ = os.Remove(file.Name())
		return nil, err
	}
	return file, nil
}

func (osFileSystem) SyncDir(dir string) error {
	handle, err := os.Open(dir)
	if err != nil {
		return err
	}
	defer handle.Close()
	return handle.Sync()
}

// snapshotPersister implements the atomic write/replace protocol against an
// injectable fileSystem.
type snapshotPersister struct {
	fs   fileSystem
	path string
}

func (p snapshotPersister) tempPattern() string {
	return filepath.Base(p.path) + ".*.tmp"
}

func (p snapshotPersister) tempGlob() string {
	return filepath.Join(filepath.Dir(p.path), p.tempPattern())
}

func (p snapshotPersister) backupPath() string {
	return p.path + ".bak"
}

// writeTemp creates an exclusive temp file in the destination directory, writes
// the whole payload, fsyncs and closes it. Any failure removes the temp and
// returns an error, leaving the official snapshot untouched.
func (p snapshotPersister) writeTemp(data []byte) (string, error) {
	file, err := p.fs.CreateTemp(filepath.Dir(p.path), p.tempPattern())
	if err != nil {
		return "", fmt.Errorf("create snapshot temp: %w", err)
	}
	name := file.Name()
	if written, err := file.Write(data); err != nil || written != len(data) {
		_ = file.Close()
		_ = p.fs.Remove(name)
		if err == nil {
			err = io.ErrShortWrite
		}
		return "", fmt.Errorf("write snapshot temp: %w", err)
	}
	if err := file.Sync(); err != nil {
		_ = file.Close()
		_ = p.fs.Remove(name)
		return "", fmt.Errorf("sync snapshot temp: %w", err)
	}
	if err := file.Close(); err != nil {
		_ = p.fs.Remove(name)
		return "", fmt.Errorf("close snapshot temp: %w", err)
	}
	return name, nil
}

// replace publishes a written temp file as the official snapshot via an atomic
// rename, then best-effort fsyncs the directory. On rename failure the temp is
// removed and the official file is unchanged.
func (p snapshotPersister) replace(tempName string) error {
	if err := p.fs.Rename(tempName, p.path); err != nil {
		_ = p.fs.Remove(tempName)
		return fmt.Errorf("replace snapshot: %w", err)
	}
	_ = p.fs.SyncDir(filepath.Dir(p.path)) // official already published; durability best-effort
	return nil
}

// remove discards a temp file, ignoring errors.
func (p snapshotPersister) remove(name string) {
	_ = p.fs.Remove(name)
}

// cleanStaleTemps removes leftover temp files from a previously crashed write.
// It only matches CodeAtlas's own temp pattern for this destination.
func (p snapshotPersister) cleanStaleTemps() {
	matches, err := p.fs.Glob(p.tempGlob())
	if err != nil {
		return
	}
	for _, match := range matches {
		_ = p.fs.Remove(match)
	}
}

// encodeSnapshot serializes a state into the canonical, versioned on-disk form
// with deterministically ordered collections.
func encodeSnapshot(st *state) ([]byte, error) {
	snapshot := diskSnapshot{
		Version:           snapshotVersion,
		StoreVersion:      st.version,
		NextEdgeID:        st.nextEdgeID,
		Files:             make([]domain.File, 0, len(st.files)),
		Identities:        make([]domain.SymbolIdentity, 0, len(st.identities)),
		Occurrences:       make([]domain.SymbolOccurrence, 0, len(st.occurrences)),
		Edges:             append([]domain.Edge(nil), st.edges...),
		Wiki:              make([]domain.WikiPage, 0, len(st.wiki)),
		Embeddings:        make(map[string][]float64, len(st.embeddings)),
		EmbeddingMetadata: st.embeddingMetadata,
		SnapshotID:        computeSnapshotID(st),
		SnapshotSchema:    snapshotSchema,
		IndexedAt:         st.indexedAt,
	}
	for _, file := range st.files {
		snapshot.Files = append(snapshot.Files, file)
	}
	for _, identity := range st.identities {
		snapshot.Identities = append(snapshot.Identities, identity)
	}
	for _, occurrence := range st.occurrences {
		snapshot.Occurrences = append(snapshot.Occurrences, occurrence)
	}
	for _, page := range st.wiki {
		snapshot.Wiki = append(snapshot.Wiki, page)
	}
	for id, vector := range st.embeddings {
		snapshot.Embeddings[id] = append([]float64(nil), vector...)
	}

	sort.Slice(snapshot.Files, func(i, j int) bool { return snapshot.Files[i].Path < snapshot.Files[j].Path })
	sort.Slice(snapshot.Identities, func(i, j int) bool { return snapshot.Identities[i].ID < snapshot.Identities[j].ID })
	sort.Slice(snapshot.Occurrences, func(i, j int) bool { return snapshot.Occurrences[i].ID < snapshot.Occurrences[j].ID })
	sort.Slice(snapshot.Wiki, func(i, j int) bool { return snapshot.Wiki[i].Slug < snapshot.Wiki[j].Slug })

	data, err := json.MarshalIndent(snapshot, "", "  ")
	if err != nil {
		return nil, fmt.Errorf("encode index snapshot: %w", err)
	}
	return data, nil
}

// decodeSnapshot parses a snapshot, rejecting an unsupported version. A truncated
// or malformed payload fails as a unit; nothing is partially accepted.
func decodeSnapshot(data []byte) (diskSnapshot, error) {
	var snapshot diskSnapshot
	if err := json.Unmarshal(data, &snapshot); err != nil {
		return diskSnapshot{}, fmt.Errorf("decode index snapshot: %w", err)
	}
	if snapshot.Version != snapshotVersion {
		return diskSnapshot{}, fmt.Errorf("unsupported index snapshot version %d", snapshot.Version)
	}
	return snapshot, nil
}
