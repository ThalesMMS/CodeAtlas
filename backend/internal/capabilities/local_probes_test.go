package capabilities

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"testing/fstest"

	"github.com/ThalesMMS/CodeAtlas/internal/treesitter"
	"github.com/ThalesMMS/CodeAtlas/internal/webui"
)

func runProbeDirect(t *testing.T, p Probe) Result {
	t.Helper()
	return p.Run(context.Background())
}

func TestWorkspaceProbe(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	file := filepath.Join(dir, "file.txt")
	if err := os.WriteFile(file, []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	cases := []struct {
		name      string
		path      string
		wantState CapabilityState
		wantCode  string
	}{
		{"ok", dir, CapabilityAvailable, ""},
		{"missing", filepath.Join(dir, "nope"), CapabilityUnavailable, ErrCodeWorkspaceMissing},
		{"not-dir", file, CapabilityUnavailable, ErrCodeWorkspaceNotDir},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			probe := workspaceProbe{probeBase{CapabilityWorkspace, Required}, tc.path}
			result := runProbeDirect(t, probe)
			if result.State != tc.wantState || result.ErrorCode != tc.wantCode {
				t.Fatalf("result = %#v, want state=%s code=%s", result, tc.wantState, tc.wantCode)
			}
		})
	}
}

func TestStateAreaProbeWritesAndCleansUp(t *testing.T) {
	t.Parallel()
	stateDir := t.TempDir()
	probe := stateAreaProbe{probeBase{CapabilityStateArea, Required}, stateDir}
	result := runProbeDirect(t, probe)
	if result.State != CapabilityAvailable {
		t.Fatalf("result = %#v, want available", result)
	}
	// No residue must remain after a successful probe.
	entries, err := os.ReadDir(filepath.Join(stateDir, "probes"))
	if err != nil {
		t.Fatalf("ReadDir error = %v", err)
	}
	if len(entries) != 0 {
		t.Fatalf("probe left %d residual files: %v", len(entries), entries)
	}
}

func TestStateAreaProbeRejectsUnusableProbeDirectory(t *testing.T) {
	t.Parallel()
	stateDir := t.TempDir()
	if err := os.WriteFile(filepath.Join(stateDir, "probes"), []byte("not a directory"), 0o600); err != nil {
		t.Fatal(err)
	}
	probe := stateAreaProbe{probeBase{CapabilityStateArea, Required}, stateDir}
	result := runProbeDirect(t, probe)
	if result.State != CapabilityUnavailable || result.ErrorCode != ErrCodeStateAreaUnwritable {
		t.Fatalf("result = %#v, want unavailable/%s", result, ErrCodeStateAreaUnwritable)
	}
}

func TestStoreProbe(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	write := func(name, content string) string {
		path := filepath.Join(dir, name)
		if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
			t.Fatal(err)
		}
		return path
	}
	cases := []struct {
		name      string
		path      string
		wantState CapabilityState
		wantCode  string
	}{
		{"missing-is-empty-initial", filepath.Join(dir, "absent.json"), CapabilityAvailable, ""},
		{"valid", write("valid.json", `{"version":1}`), CapabilityAvailable, ""},
		{"corrupted", write("corrupt.json", `{not json`), CapabilityUnavailable, ErrCodeStoreCorrupted},
		{"bad-version", write("badver.json", `{"version":999}`), CapabilityUnavailable, ErrCodeStoreCorrupted},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			probe := storeProbe{probeBase{CapabilityStore, Required}, tc.path}
			result := runProbeDirect(t, probe)
			if result.State != tc.wantState || result.ErrorCode != tc.wantCode {
				t.Fatalf("result = %#v, want state=%s code=%s", result, tc.wantState, tc.wantCode)
			}
		})
	}
}

func TestTreeSitterProbesAllLanguages(t *testing.T) {
	t.Parallel()
	probes := localProbesWithAssets("", "", "", fstest.MapFS{})
	wantLanguages := map[CapabilityID]bool{
		CapabilityTreeSitterGo:     true,
		CapabilityTreeSitterJS:     true,
		CapabilityTreeSitterTS:     true,
		CapabilityTreeSitterTSX:    true,
		CapabilityTreeSitterSwift:  true,
		CapabilityTreeSitterPython: true,
		CapabilityTreeSitterRust:   true,
	}
	seen := 0
	for _, probe := range probes {
		if !wantLanguages[probe.ID()] {
			continue
		}
		seen++
		result := runProbeDirect(t, probe)
		if result.State != CapabilityAvailable {
			t.Fatalf("tree-sitter probe %s = %#v, want available", probe.ID(), result)
		}
	}
	if seen != len(wantLanguages) {
		t.Fatalf("found %d tree-sitter probes, want %d", seen, len(wantLanguages))
	}
}

func TestTreeSitterProbeUnexpectedRoot(t *testing.T) {
	t.Parallel()
	probe := treeSitterProbe{probeBase{CapabilityTreeSitterGo, Required}, treesitter.LanguageGo, "program", "package p\n"}
	result := runProbeDirect(t, probe)
	if result.State != CapabilityUnavailable || result.ErrorCode != ErrCodeTreeSitterRoot {
		t.Fatalf("result = %#v, want unavailable/%s", result, ErrCodeTreeSitterRoot)
	}
}

func TestFrontendAssetsProbe(t *testing.T) {
	t.Parallel()
	good := fstest.MapFS{
		"index.html":                       {Data: []byte(`<script type="module" src="/assets/main-Abc12345.js"></script><link rel="stylesheet" href="/assets/main-Abc12345.css">`)},
		"assets/main-Abc12345.js":          {Data: []byte("console.log(1)")},
		"assets/main-Abc12345.css":         {Data: []byte("body{}")},
		"assets/editor.worker-Abc12345.js": {Data: []byte("self.onmessage=()=>{}")},
		"assets/ts.worker-Abc12345.js":     {Data: []byte("self.onmessage=()=>{}")},
		"codeatlas-manifest.json":          {Data: []byte(`{"buildVersion":"test","editor":"monaco","editorVersion":"0.53.0","assets":{"entrypoints":["assets/main-Abc12345.js"],"styles":["assets/main-Abc12345.css"],"workers":["assets/editor.worker-Abc12345.js","assets/ts.worker-Abc12345.js"],"other":[]}}`)},
	}
	missing := fstest.MapFS{
		"index.html":              {Data: []byte(`<script type="module" src="/assets/main-Abc12345.js"></script>`)},
		"codeatlas-manifest.json": {Data: []byte(`{"buildVersion":"test","editor":"textarea","editorVersion":"native","assets":{"entrypoints":["assets/main-Abc12345.js"],"styles":[],"workers":[],"other":[]}}`)},
	}
	empty := fstest.MapFS{
		"index.html":              {Data: []byte(`<script type="module" src="/assets/main-Abc12345.js"></script>`)},
		"assets/main-Abc12345.js": {Data: []byte{}},
		"codeatlas-manifest.json": {Data: []byte(`{"buildVersion":"test","editor":"textarea","editorVersion":"native","assets":{"entrypoints":["assets/main-Abc12345.js"],"styles":[],"workers":[],"other":[]}}`)},
	}
	cases := []struct {
		name      string
		assets    fstest.MapFS
		wantState CapabilityState
		wantCode  string
	}{
		{"ok", good, CapabilityAvailable, ""},
		{"missing", missing, CapabilityUnavailable, ErrCodeFrontendAssetMissing},
		{"empty", empty, CapabilityUnavailable, ErrCodeFrontendAssetEmpty},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			probe := frontendAssetsProbe{probeBase{CapabilityFrontendAssets, Required}, tc.assets}
			result := runProbeDirect(t, probe)
			if result.State != tc.wantState || result.ErrorCode != tc.wantCode {
				t.Fatalf("result = %#v, want state=%s code=%s", result, tc.wantState, tc.wantCode)
			}
		})
	}
}

func TestFrontendAssetsProbeEmbedded(t *testing.T) {
	t.Parallel()
	probe := frontendAssetsProbe{probeBase{CapabilityFrontendAssets, Required}, webui.Assets()}
	result := runProbeDirect(t, probe)
	if result.State != CapabilityAvailable {
		t.Fatalf("embedded assets probe = %#v, want available (run `make frontend-build`?)", result)
	}
}

func TestLocalProbesDeterministicSet(t *testing.T) {
	t.Parallel()
	results := Runner{}.Run(context.Background(), LocalProbes(t.TempDir(), t.TempDir(), filepath.Join(t.TempDir(), "index.json")), nil)
	ids := make([]CapabilityID, len(results))
	for i, result := range results {
		ids[i] = result.ID
	}
	want := []CapabilityID{
		CapabilityFrontendAssets,
		CapabilityStateArea,
		CapabilityStore,
		CapabilityTreeSitterGo,
		CapabilityTreeSitterJS,
		CapabilityTreeSitterPython,
		CapabilityTreeSitterRust,
		CapabilityTreeSitterSwift,
		CapabilityTreeSitterTS,
		CapabilityTreeSitterTSX,
		CapabilityWorkspace,
	}
	if len(ids) != len(want) {
		t.Fatalf("got %d capabilities %v, want %d", len(ids), ids, len(want))
	}
	for i := range want {
		if ids[i] != want[i] {
			t.Fatalf("ordering = %v, want %v", ids, want)
		}
	}
}
