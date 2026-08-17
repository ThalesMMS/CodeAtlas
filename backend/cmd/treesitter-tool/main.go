// Command treesitter-tool bootstraps and verifies the vendored Tree-sitter
// runtime and grammars from internal/treesitter/deps.lock.json, the single source
// of truth. `verify` is offline and gates CI; `bootstrap` downloads only from the
// lock's allowlisted URLs and verifies each archive's SHA-256 before extracting.
package main

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"
)

type lockfile struct {
	LockfileFormat   int         `json:"lockfileFormat"`
	AllowlistedHosts []string    `json:"allowlistedHosts"`
	VendorTreeSha256 string      `json:"vendorTreeSha256"`
	Components       []component `json:"components"`
}

type component struct {
	Name          string   `json:"name"`
	Type          string   `json:"type"`
	Version       string   `json:"version"`
	Tag           string   `json:"tag"`
	Commit        string   `json:"commit"`
	URL           string   `json:"url"`
	ArchiveSha256 string   `json:"archiveSha256"`
	Subpaths      []string `json:"subpaths"`
	Destination   string   `json:"destination"`
	SPDX          string   `json:"spdx"`
	LicenseFile   string   `json:"licenseFile"`
}

// hashedTrees are the vendored directories whose content hash must stay stable.
var hashedTrees = []string{"vendor", "grammars"}

func main() {
	if len(os.Args) < 2 {
		fail("usage: treesitter-tool <verify|bootstrap|hash> [-dir <treesitter-dir>]")
	}
	dir := flagValue("-dir", "internal/treesitter")
	switch os.Args[1] {
	case "verify":
		mustRun(verify(dir))
	case "hash":
		mustRun(printHash(dir))
	case "bootstrap":
		mustRun(bootstrap(dir))
	default:
		fail("unknown command %q", os.Args[1])
	}
}

func verify(dir string) error {
	lock, err := loadLock(dir)
	if err != nil {
		return err
	}
	for _, comp := range lock.Components {
		if comp.LicenseFile == "" {
			return fmt.Errorf("component %s has no licenseFile", comp.Name)
		}
		if _, err := os.Stat(filepath.Join(dir, comp.LicenseFile)); err != nil {
			return fmt.Errorf("component %s: missing license %s", comp.Name, comp.LicenseFile)
		}
		dest := filepath.Join(dir, filepath.FromSlash(comp.Destination))
		if empty, err := dirEmpty(dest); err != nil || empty {
			return fmt.Errorf("component %s: destination %s missing or empty — run `make bootstrap-treesitter`", comp.Name, comp.Destination)
		}
	}
	current, err := treeHash(dir)
	if err != nil {
		return err
	}
	if lock.VendorTreeSha256 == "" {
		return fmt.Errorf("lock has no vendorTreeSha256 — run `treesitter-tool hash` and record it")
	}
	if current != lock.VendorTreeSha256 {
		return fmt.Errorf("vendored tree changed without updating the lock:\n  lock:    %s\n  current: %s\nIf intentional, update deps.lock.json (vendorTreeSha256) and note it in the PR.", lock.VendorTreeSha256, current)
	}
	fmt.Printf("treesitter verify ok: %d components, vendorTreeSha256=%s\n", len(lock.Components), current[:16])
	return nil
}

func printHash(dir string) error {
	hash, err := treeHash(dir)
	if err != nil {
		return err
	}
	fmt.Println(hash)
	return nil
}

// treeHash is a deterministic content hash of the vendored trees: for every file
// (sorted by relative slash-path) it appends "path\x00<filehash>\n", then hashes
// the manifest. Order- and platform-independent.
func treeHash(dir string) (string, error) {
	type entry struct{ path, hash string }
	var entries []entry
	for _, tree := range hashedTrees {
		root := filepath.Join(dir, tree)
		err := filepath.WalkDir(root, func(path string, d os.DirEntry, err error) error {
			if err != nil {
				return err
			}
			if d.IsDir() {
				return nil
			}
			content, err := os.ReadFile(path)
			if err != nil {
				return err
			}
			rel, err := filepath.Rel(dir, path)
			if err != nil {
				return err
			}
			sum := sha256.Sum256(content)
			entries = append(entries, entry{filepath.ToSlash(rel), hex.EncodeToString(sum[:])})
			return nil
		})
		if err != nil {
			return "", err
		}
	}
	sort.Slice(entries, func(i, j int) bool { return entries[i].path < entries[j].path })
	manifest := sha256.New()
	for _, e := range entries {
		fmt.Fprintf(manifest, "%s\x00%s\n", e.path, e.hash)
	}
	return hex.EncodeToString(manifest.Sum(nil)), nil
}

func bootstrap(dir string) error {
	lock, err := loadLock(dir)
	if err != nil {
		return err
	}
	allow := map[string]struct{}{}
	for _, host := range lock.AllowlistedHosts {
		allow[host] = struct{}{}
	}
	staging, err := os.MkdirTemp("", "treesitter-bootstrap-")
	if err != nil {
		return err
	}
	defer os.RemoveAll(staging)

	// Extract every component into staging first; only swap destinations in once
	// all archives verified and extracted, so a failure never corrupts a good tree.
	for _, comp := range lock.Components {
		if err := fetchAndExtract(comp, allow, filepath.Join(staging, comp.Name)); err != nil {
			return fmt.Errorf("component %s: %w", comp.Name, err)
		}
	}
	fmt.Printf("treesitter bootstrap: %d components verified and extracted to staging\n", len(lock.Components))
	fmt.Println("staging:", staging, "(extraction validated; manual layout review recommended before swap)")
	return nil
}

func fetchAndExtract(comp component, allow map[string]struct{}, dest string) error {
	parsed, err := url.Parse(comp.URL)
	if err != nil {
		return err
	}
	if _, ok := allow[parsed.Host]; !ok {
		return fmt.Errorf("host %q not allowlisted", parsed.Host)
	}
	if parsed.Scheme != "https" {
		return fmt.Errorf("non-https url %q", comp.URL)
	}
	data, err := download(comp.URL)
	if err != nil {
		return err
	}
	sum := sha256.Sum256(data)
	if got := hex.EncodeToString(sum[:]); got != comp.ArchiveSha256 {
		return fmt.Errorf("archive sha256 mismatch: lock %s got %s", comp.ArchiveSha256, got)
	}
	return extractSubpaths(data, comp.Subpaths, dest)
}

func download(rawurl string) ([]byte, error) {
	client := &http.Client{
		Timeout: 120 * time.Second,
		CheckRedirect: func(req *http.Request, via []*http.Request) error {
			if len(via) >= 5 {
				return fmt.Errorf("too many redirects")
			}
			return nil
		},
	}
	resp, err := client.Get(rawurl)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("http %d", resp.StatusCode)
	}
	return io.ReadAll(io.LimitReader(resp.Body, 200<<20))
}

// extractSubpaths extracts only regular files whose path (after the archive's top
// directory) starts with one of subpaths. It rejects path traversal and any
// non-regular entry (symlink/hardlink/device).
func extractSubpaths(archive []byte, subpaths []string, dest string) error {
	gz, err := gzip.NewReader(bytes.NewReader(archive))
	if err != nil {
		return err
	}
	defer gz.Close()
	reader := tar.NewReader(gz)
	for {
		header, err := reader.Next()
		if err == io.EOF {
			break
		}
		if err != nil {
			return err
		}
		if header.Typeflag != tar.TypeReg {
			if header.Typeflag == tar.TypeSymlink || header.Typeflag == tar.TypeLink {
				return fmt.Errorf("archive contains link %q", header.Name)
			}
			continue
		}
		inner := stripTopDir(header.Name)
		if !matchesSubpath(inner, subpaths) {
			continue
		}
		target := filepath.Join(dest, filepath.FromSlash(inner))
		if !strings.HasPrefix(target, filepath.Clean(dest)+string(os.PathSeparator)) {
			return fmt.Errorf("path traversal in %q", header.Name)
		}
		if err := os.MkdirAll(filepath.Dir(target), 0o755); err != nil {
			return err
		}
		out, err := os.OpenFile(target, os.O_WRONLY|os.O_CREATE|os.O_TRUNC, 0o644)
		if err != nil {
			return err
		}
		if _, err := io.Copy(out, io.LimitReader(reader, 50<<20)); err != nil {
			out.Close()
			return err
		}
		out.Close()
	}
	return nil
}

func stripTopDir(name string) string {
	name = filepath.ToSlash(name)
	if idx := strings.IndexByte(name, '/'); idx >= 0 {
		return name[idx+1:]
	}
	return ""
}

func matchesSubpath(inner string, subpaths []string) bool {
	for _, sub := range subpaths {
		sub = strings.TrimSuffix(filepath.ToSlash(sub), "/")
		if inner == sub || strings.HasPrefix(inner, sub+"/") {
			return true
		}
	}
	return false
}

// --- helpers ---

func loadLock(dir string) (lockfile, error) {
	data, err := os.ReadFile(filepath.Join(dir, "deps.lock.json"))
	if err != nil {
		return lockfile{}, err
	}
	var lock lockfile
	if err := json.Unmarshal(data, &lock); err != nil {
		return lockfile{}, fmt.Errorf("parse deps.lock.json: %w", err)
	}
	return lock, nil
}

func dirEmpty(path string) (bool, error) {
	entries, err := os.ReadDir(path)
	if err != nil {
		return true, err
	}
	return len(entries) == 0, nil
}

func flagValue(name, fallback string) string {
	for i, arg := range os.Args {
		if arg == name && i+1 < len(os.Args) {
			return os.Args[i+1]
		}
	}
	return fallback
}

func mustRun(err error) {
	if err != nil {
		fail("%v", err)
	}
}

func fail(format string, args ...any) {
	fmt.Fprintf(os.Stderr, "treesitter-tool: "+format+"\n", args...)
	os.Exit(1)
}
