package capabilities

import (
	"context"
	"errors"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/ThalesMMS/CodeAtlas/internal/store"
	"github.com/ThalesMMS/CodeAtlas/internal/treesitter"
	"github.com/ThalesMMS/CodeAtlas/internal/webui"
)

// LocalProbes builds the deterministic set of mandatory local probes for a given
// workspace, state directory and store snapshot path. The frontend asset tree is
// taken from the embedded UI so the probe never starts an HTTP server.
func LocalProbes(workspace, stateDir, storePath string) []Probe {
	return localProbesWithAssets(workspace, stateDir, storePath, webui.Assets())
}

// localProbesWithAssets is the testable form of LocalProbes that accepts an
// explicit asset filesystem.
func localProbesWithAssets(workspace, stateDir, storePath string, assets fs.FS) []Probe {
	return []Probe{
		workspaceProbe{probeBase{CapabilityWorkspace, Required}, workspace},
		stateAreaProbe{probeBase{CapabilityStateArea, Required}, stateDir},
		storeProbe{probeBase{CapabilityStore, Required}, storePath},
		treeSitterProbe{probeBase{CapabilityTreeSitterGo, Required}, treesitter.LanguageGo, "source_file", "package sample\nfunc main() {}\n"},
		treeSitterProbe{probeBase{CapabilityTreeSitterJS, Required}, treesitter.LanguageJavaScript, "program", "export function run() { return 1 }\n"},
		treeSitterProbe{probeBase{CapabilityTreeSitterTS, Required}, treesitter.LanguageTypeScript, "program", "export const run = (id: string): string => id\n"},
		treeSitterProbe{probeBase{CapabilityTreeSitterTSX, Required}, treesitter.LanguageTSX, "program", "export const App = () => <main>Hello</main>\n"},
		treeSitterProbe{probeBase{CapabilityTreeSitterSwift, Required}, treesitter.LanguageSwift, "source_file", "struct Order { let id: String }\n"},
		treeSitterProbe{probeBase{CapabilityTreeSitterPython, Required}, treesitter.LanguagePython, "module", "class Order:\n    id: str\n"},
		treeSitterProbe{probeBase{CapabilityTreeSitterRust, Required}, treesitter.LanguageRust, "source_file", "struct Order { id: String }\n"},
		frontendAssetsProbe{probeBase{CapabilityFrontendAssets, Required}, assets},
	}
}

// workspaceProbe verifies the workspace exists, is a traversable directory and
// resolves to a canonical path.
type workspaceProbe struct {
	probeBase
	workspace string
}

func (p workspaceProbe) Run(ctx context.Context) Result {
	start := time.Now()
	info, err := os.Stat(p.workspace)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return p.unavailable(start, ErrCodeWorkspaceMissing, "workspace does not exist")
		}
		return p.unavailable(start, ErrCodeWorkspaceUnreadable, err.Error())
	}
	if !info.IsDir() {
		return p.unavailable(start, ErrCodeWorkspaceNotDir, "workspace path is not a directory")
	}

	directory, err := os.Open(p.workspace)
	if err != nil {
		return p.unavailable(start, ErrCodeWorkspaceUnreadable, err.Error())
	}
	defer directory.Close()
	if _, err := directory.ReadDir(1); err != nil && !errors.Is(err, io.EOF) {
		return p.unavailable(start, ErrCodeWorkspaceUnreadable, err.Error())
	}

	canonical, err := filepath.EvalSymlinks(p.workspace)
	if err != nil {
		return p.unavailable(start, ErrCodeWorkspaceUnresolvable, err.Error())
	}
	symlinked := filepath.Clean(canonical) != filepath.Clean(p.workspace)
	return p.available(start, map[string]string{"symlinked": strconv.FormatBool(symlinked)})
}

// stateAreaProbe proves the state directory is writable by performing a full
// write → fsync → atomic-rename → remove cycle inside a dedicated subdirectory.
// It never touches user source files and always removes its residue.
type stateAreaProbe struct {
	probeBase
	stateDir string
}

func (p stateAreaProbe) Run(ctx context.Context) Result {
	start := time.Now()
	probesDir := filepath.Join(p.stateDir, "probes")
	if err := os.MkdirAll(probesDir, 0o755); err != nil {
		return p.unavailable(start, ErrCodeStateAreaUnwritable, "create probe directory: "+err.Error())
	}
	temporary := filepath.Join(probesDir, "write.tmp")
	committed := filepath.Join(probesDir, "write.committed")
	// Always remove residue, even on cancellation or partial failure.
	defer os.Remove(temporary)
	defer os.Remove(committed)

	if err := ctx.Err(); err != nil {
		return contextResult(p, start, err)
	}
	file, err := os.OpenFile(temporary, os.O_CREATE|os.O_TRUNC|os.O_WRONLY, 0o600)
	if err != nil {
		return p.unavailable(start, ErrCodeStateAreaUnwritable, "open temp file: "+err.Error())
	}
	if _, err := file.Write([]byte("codeatlas-probe")); err != nil {
		_ = file.Close()
		return p.unavailable(start, ErrCodeStateAreaUnwritable, "write temp file: "+err.Error())
	}
	// fsync when supported; some filesystems return ENOTSUP, which is tolerable.
	syncErr := file.Sync()
	if err := file.Close(); err != nil {
		return p.unavailable(start, ErrCodeStateAreaUnwritable, "close temp file: "+err.Error())
	}
	if err := ctx.Err(); err != nil {
		return contextResult(p, start, err)
	}
	if err := os.Rename(temporary, committed); err != nil {
		return p.unavailable(start, ErrCodeStateAreaUnwritable, "atomic rename: "+err.Error())
	}
	return p.available(start, map[string]string{"fsync": strconv.FormatBool(syncErr == nil)})
}

// storeProbe verifies the persisted snapshot is loadable without mutating any
// in-memory state. For SQLite paths the startup migrator performs the full
// marker/schema/integrity validation; the probe only proves the file is readable
// when present so capability probing can run before migration.
type storeProbe struct {
	probeBase
	storePath string
}

func (p storeProbe) Run(ctx context.Context) Result {
	start := time.Now()
	if filepath.Ext(p.storePath) == ".db" {
		file, err := os.Open(p.storePath)
		if errors.Is(err, os.ErrNotExist) {
			return p.available(start, map[string]string{"backend": "sqlite", "present": "false"})
		}
		if err != nil {
			return p.unavailable(start, ErrCodeStoreUnreadable, err.Error())
		}
		_ = file.Close()
		return p.available(start, map[string]string{"backend": "sqlite", "present": "true"})
	}
	if err := store.ValidateSnapshot(p.storePath); err != nil {
		code := ErrCodeStoreCorrupted
		var pathErr *fs.PathError
		if errors.As(err, &pathErr) {
			code = ErrCodeStoreUnreadable
		}
		return p.unavailable(start, code, err.Error())
	}
	return p.available(start, nil)
}

// treeSitterProbe performs a minimal parse for one language and validates the
// root node, registering each language as its own capability.
type treeSitterProbe struct {
	probeBase
	language     treesitter.Language
	expectedRoot string
	sample       string
}

func (p treeSitterProbe) Run(ctx context.Context) Result {
	start := time.Now()
	if err := ctx.Err(); err != nil {
		return contextResult(p, start, err)
	}
	tree, err := treesitter.Parse(p.language, []byte(p.sample))
	if err != nil {
		return p.unavailable(start, ErrCodeTreeSitterParse, err.Error())
	}
	defer tree.Close()
	root := tree.RootNode()
	if root.IsNull() {
		return p.unavailable(start, ErrCodeTreeSitterRoot, "root node is null")
	}
	if got := root.Type(); got != p.expectedRoot {
		return p.unavailable(start, ErrCodeTreeSitterRoot, "unexpected root type "+got)
	}
	if root.NamedChildCount() == 0 {
		return p.unavailable(start, ErrCodeTreeSitterParse, "parse produced no named children")
	}
	return p.available(start, map[string]string{
		"language": string(p.language),
		"rootType": root.Type(),
	})
}

// frontendAssetsProbe validates that the embedded UI assets are present and
// non-empty without starting an HTTP server.
type frontendAssetsProbe struct {
	probeBase
	assets fs.FS
}

func (p frontendAssetsProbe) Run(ctx context.Context) Result {
	start := time.Now()
	if err := webui.ValidateAssets(p.assets); err != nil {
		return p.unavailable(start, frontendAssetErrorCode(err), "invalid frontend assets: "+err.Error())
	}
	manifest, err := webui.Manifest(p.assets)
	if err != nil {
		return p.unavailable(start, ErrCodeFrontendAssetMissing, "invalid frontend manifest: "+err.Error())
	}
	return p.available(start, map[string]string{
		"buildVersion":  manifest.BuildVersion,
		"editor":        manifest.Editor,
		"editorVersion": manifest.EditorVersion,
	})
}

func frontendAssetErrorCode(err error) string {
	if strings.Contains(err.Error(), "empty ") {
		return ErrCodeFrontendAssetEmpty
	}
	return ErrCodeFrontendAssetMissing
}
