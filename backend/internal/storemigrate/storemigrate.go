// Package storemigrate implements the one-way, recoverable JSON→SQLite store
// transition (issue #57): it plans the migration mode, takes an immutable SHA-256
// backup of the JSON source, records a phased journal, and flips the active backend
// only through an atomic marker. There is never a dual-write, and a SQLite failure
// never silently falls back to a (possibly older) JSON snapshot — rollback is an
// explicit administrative action, not an automatic path.
package storemigrate

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
)

// Backend identifies the active store backend.
type Backend string

const (
	BackendSQLite Backend = "sqlite"
	BackendJSON   Backend = "json"
)

// Mode is the migration strategy chosen by the planner.
type Mode string

const (
	ModeImport  Mode = "import"  // compatible import of a known-good JSON snapshot
	ModeRebuild Mode = "rebuild" // re-scan the workspace from source
	ModeFresh   Mode = "fresh"   // no prior JSON or DB
)

const (
	// MarkerFileName is the atomic active-backend marker.
	MarkerFileName = "store-backend.json"
	// JournalFileName tracks migration phases for recovery/diagnostics.
	JournalFileName = "store-migration.json"
	markerVersion   = 1
)

// Marker is the small, atomic record of the active backend. Paths are controlled
// filenames within the state dir — never absolute paths or traversal.
type Marker struct {
	Version        int     `json:"version"`
	Backend        Backend `json:"backend"`
	Database       string  `json:"database"`
	MigrationID    string  `json:"migrationId"`
	ActivatedAt    string  `json:"activatedAt"`
	SourceBackup   string  `json:"sourceBackup,omitempty"`
	SourceChecksum string  `json:"sourceChecksum,omitempty"`
}

// validateFilename rejects empty names, separators and traversal.
func validateFilename(name string) error {
	if name == "" || name == "." || name == ".." {
		return fmt.Errorf("invalid filename %q", name)
	}
	if strings.ContainsAny(name, "/\\") || strings.Contains(name, "..") {
		return fmt.Errorf("filename %q must not contain a path", name)
	}
	return nil
}

// WriteMarker writes the marker atomically (temp + fsync + rename) into stateDir.
func WriteMarker(stateDir string, marker Marker) error {
	if err := validateFilename(marker.Database); err != nil {
		return err
	}
	if marker.SourceBackup != "" {
		if err := validateFilename(marker.SourceBackup); err != nil {
			return err
		}
	}
	marker.Version = markerVersion
	data, err := json.MarshalIndent(marker, "", "  ")
	if err != nil {
		return err
	}
	return atomicWrite(filepath.Join(stateDir, MarkerFileName), data, 0o600)
}

// ReadMarker reads and validates the active-backend marker. Missing marker returns
// (zero, false, nil) so startup can decide between a fresh DB and a migration.
func ReadMarker(stateDir string) (Marker, bool, error) {
	data, err := os.ReadFile(filepath.Join(stateDir, MarkerFileName))
	if os.IsNotExist(err) {
		return Marker{}, false, nil
	}
	if err != nil {
		return Marker{}, false, err
	}
	var marker Marker
	if err := json.Unmarshal(data, &marker); err != nil {
		return Marker{}, false, fmt.Errorf("marker is invalid: %w", err)
	}
	if marker.Version != markerVersion || marker.Backend == "" {
		return Marker{}, false, fmt.Errorf("marker is unsupported (version %d, backend %q)", marker.Version, marker.Backend)
	}
	if err := validateFilename(marker.Database); err != nil {
		return Marker{}, false, fmt.Errorf("marker database name is unsafe: %w", err)
	}
	return marker, true, nil
}

// BackupSource copies a JSON source into an immutable, restrictively-permissioned
// backup `index.pre-sqlite.<migrationId>.json` in stateDir, verifying its SHA-256.
// The original is never altered. Returns the backup filename and checksum.
func BackupSource(sourcePath, stateDir, migrationID string) (string, string, error) {
	if migrationID == "" {
		return "", "", fmt.Errorf("migration id is required")
	}
	source, err := os.Open(sourcePath)
	if err != nil {
		return "", "", err
	}
	defer source.Close()

	backupName := fmt.Sprintf("index.pre-sqlite.%s.json", migrationID)
	backupPath := filepath.Join(stateDir, backupName)
	destination, err := os.OpenFile(backupPath, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o600)
	if err != nil {
		return "", "", err
	}
	hasher := sha256.New()
	if _, err := io.Copy(io.MultiWriter(destination, hasher), source); err != nil {
		destination.Close()
		_ = os.Remove(backupPath)
		return "", "", err
	}
	if err := destination.Sync(); err != nil {
		destination.Close()
		return "", "", err
	}
	if err := destination.Close(); err != nil {
		return "", "", err
	}
	checksum := "sha256:" + hex.EncodeToString(hasher.Sum(nil))

	// Verify the backup re-hashes to the same checksum (catch silent corruption).
	if verify, err := checksumFile(backupPath); err != nil || verify != checksum {
		return "", "", fmt.Errorf("backup checksum verification failed")
	}
	return backupName, checksum, nil
}

func checksumFile(path string) (string, error) {
	file, err := os.Open(path)
	if err != nil {
		return "", err
	}
	defer file.Close()
	hasher := sha256.New()
	if _, err := io.Copy(hasher, file); err != nil {
		return "", err
	}
	return "sha256:" + hex.EncodeToString(hasher.Sum(nil)), nil
}

// atomicWrite writes data to path via a temp file + fsync + rename + dir fsync.
func atomicWrite(path string, data []byte, perm os.FileMode) error {
	dir := filepath.Dir(path)
	temp, err := os.CreateTemp(dir, ".tmp-"+filepath.Base(path)+"-*")
	if err != nil {
		return err
	}
	tempName := temp.Name()
	cleanup := func() { _ = os.Remove(tempName) }
	if _, err := temp.Write(data); err != nil {
		temp.Close()
		cleanup()
		return err
	}
	if err := temp.Chmod(perm); err != nil {
		temp.Close()
		cleanup()
		return err
	}
	if err := temp.Sync(); err != nil {
		temp.Close()
		cleanup()
		return err
	}
	if err := temp.Close(); err != nil {
		cleanup()
		return err
	}
	if err := os.Rename(tempName, path); err != nil {
		cleanup()
		return err
	}
	if dirFile, err := os.Open(dir); err == nil {
		_ = dirFile.Sync()
		_ = dirFile.Close()
	}
	return nil
}
