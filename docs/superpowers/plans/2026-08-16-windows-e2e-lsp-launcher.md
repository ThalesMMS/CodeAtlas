# Windows E2E LSP Launcher Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Run all repository-owned Node fake language servers as native child processes during Windows browser E2E tests.

**Architecture:** A standalone Go launcher is built into an ignored directory before the E2E suite and copied under a closed set of fake tool names. The JavaScript harness maps `.mjs` fixture paths to those `.exe` files on Windows; POSIX keeps the current shebang execution.

**Tech Stack:** Node.js 26, npm 11.16, Go, `node:test`, Playwright browser E2E.

**Spec:** `docs/superpowers/specs/2026-08-16-llm-windows-e2e-reliability-design.md`

## Global Constraints

- No executable binary is checked in.
- The launcher accepts only five repository-owned fake LSP names plus the `pyright` and `swiftc` probe helpers.
- LSP stdio and arguments are forwarded unchanged.
- Workspace files are never interpreted or executed.
- POSIX behavior remains unchanged.
- Generated launcher files live under `e2e/.generated/` and are gitignored.

---

### Task 1: Define and test platform path resolution

**Files:**
- Create: `e2e/harness/lsp-launchers.mjs`
- Create: `e2e/tests/lsp-launcher.test.mjs`
- Modify: `e2e/harness/process-manager.mjs`

**Interfaces:**
- Produces: `resolveFakeLSPPath({root, configuredPath, platform}) string`
- Consumes: repository root and the existing fake `.mjs` paths

- [ ] **Step 1: Write the failing resolver tests**

Create `e2e/tests/lsp-launcher.test.mjs`:

```js
import assert from 'node:assert/strict';
import path from 'node:path';
import test from 'node:test';

import { resolveFakeLSPPath } from '../harness/lsp-launchers.mjs';

const root = path.resolve('C:/workspace/CodeAtlas');

test('POSIX keeps the executable script path', () => {
  const configuredPath = path.join(root, 'e2e/harness/fake-gopls.mjs');
  assert.equal(resolveFakeLSPPath({ root, configuredPath, platform: 'linux' }), configuredPath);
});

test('Windows resolves a known fake LSP to a generated executable', () => {
  const configuredPath = path.join(root, 'e2e/harness/fake-rust-analyzer.mjs');
  assert.equal(
    resolveFakeLSPPath({ root, configuredPath, platform: 'win32' }),
    path.join(root, 'e2e/.generated/lsp/fake-rust-analyzer.exe'),
  );
});

test('Windows rejects an arbitrary script path', () => {
  assert.throws(
    () => resolveFakeLSPPath({
      root,
      configuredPath: path.join(root, 'workspace/untrusted.mjs'),
      platform: 'win32',
    }),
    /not an allowlisted E2E language server/u,
  );
});
```

- [ ] **Step 2: Run the resolver tests and prove they fail**

```powershell
Set-Location e2e
node --test tests/lsp-launcher.test.mjs
```

Expected: module-not-found failure for `harness/lsp-launchers.mjs`.

- [ ] **Step 3: Implement the closed resolver**

Create `e2e/harness/lsp-launchers.mjs` with:

```js
import path from 'node:path';

const fakeLSPNames = new Set([
  'fake-gopls',
  'fake-typescript-lsp',
  'fake-sourcekit-lsp',
  'fake-pyright-langserver',
  'fake-rust-analyzer',
]);

export function resolveFakeLSPPath({ root, configuredPath, platform = process.platform }) {
  if (!configuredPath || platform !== 'win32') return configuredPath;
  const name = path.basename(configuredPath, path.extname(configuredPath));
  const expectedDirectory = path.resolve(root, 'e2e/harness');
  if (path.resolve(path.dirname(configuredPath)) !== expectedDirectory || !fakeLSPNames.has(name)) {
    throw new Error(`${configuredPath} is not an allowlisted E2E language server`);
  }
  return path.join(root, 'e2e/.generated/lsp', `${name}.exe`);
}

export const generatedLauncherNames = Object.freeze([
  ...fakeLSPNames,
  'pyright',
  'swiftc',
]);
```

- [ ] **Step 4: Run resolver tests**

```powershell
node --test tests/lsp-launcher.test.mjs
```

Expected: all three tests pass.

- [ ] **Step 5: Apply resolution in `startBackend`**

Import `resolveFakeLSPPath` in `process-manager.mjs`. Before constructing `childEnv`, create:

```js
const resolvedLSPPaths = {
  typescript: resolveFakeLSPPath({ root, configuredPath: typescriptLSPPath }),
  go: resolveFakeLSPPath({ root, configuredPath: goplsPath }),
  swift: resolveFakeLSPPath({ root, configuredPath: swiftLSPPath }),
  python: resolveFakeLSPPath({ root, configuredPath: pythonLSPPath }),
  rust: resolveFakeLSPPath({ root, configuredPath: rustLSPPath }),
};
```

Use these five resolved values for the corresponding mode and path environment variables. An empty input remains empty.

- [ ] **Step 6: Commit resolver behavior**

```powershell
git add e2e/harness/lsp-launchers.mjs e2e/tests/lsp-launcher.test.mjs e2e/harness/process-manager.mjs
git commit -m "test: resolve fake LSP launchers on Windows"
```

---

### Task 2: Implement the native closed-map launcher

**Files:**
- Create: `e2e/harness/lsp-launcher/main.go`
- Create: `e2e/harness/lsp-launcher/main_test.go`

**Interfaces:**
- Produces: `fixtureForExecutable(string) (string, bool)`
- Produces: native executable behavior for five fake LSPs, `pyright`, and `swiftc`
- Consumes: Node from inherited `PATH` and repository fixtures under `e2e/harness`

- [ ] **Step 1: Write failing Go unit tests**

Create `main_test.go`:

```go
package main

import "testing"

func TestFixtureForExecutableUsesClosedMap(t *testing.T) {
	for _, test := range []struct {
		name string
		want string
		ok   bool
	}{
		{name: "fake-gopls.exe", want: "fake-gopls.mjs", ok: true},
		{name: "fake-typescript-lsp", want: "fake-typescript-lsp.mjs", ok: true},
		{name: "fake-sourcekit-lsp.exe", want: "fake-sourcekit-lsp.mjs", ok: true},
		{name: "fake-pyright-langserver.exe", want: "fake-pyright-langserver.mjs", ok: true},
		{name: "fake-rust-analyzer.exe", want: "fake-rust-analyzer.mjs", ok: true},
		{name: "untrusted.exe", ok: false},
	} {
		t.Run(test.name, func(t *testing.T) {
			got, ok := fixtureForExecutable(test.name)
			if ok != test.ok || got != test.want {
				t.Fatalf("fixtureForExecutable(%q) = %q/%v, want %q/%v", test.name, got, ok, test.want, test.ok)
			}
		})
	}
}

func TestProbeOutputIsLimitedToKnownHelpers(t *testing.T) {
	if got, ok := probeOutput("pyright.exe", []string{"--version"}); !ok || got != "pyright 1.1.400\n" {
		t.Fatalf("pyright probe = %q/%v", got, ok)
	}
	if got, ok := probeOutput("swiftc.exe", []string{"--version"}); !ok || got != "Apple Swift version 6.3.3-fake (codeatlas fixture)\n" {
		t.Fatalf("swiftc probe = %q/%v", got, ok)
	}
	if _, ok := probeOutput("pyright.exe", []string{"--help"}); ok {
		t.Fatal("pyright accepted a non-version probe")
	}
}
```

- [ ] **Step 2: Run named-file Go tests and prove they fail**

From the repository root:

```powershell
Set-Location backend
go test ../e2e/harness/lsp-launcher/main.go ../e2e/harness/lsp-launcher/main_test.go -count=1
```

Expected: compilation fails because `fixtureForExecutable` and `probeOutput` do not exist.

- [ ] **Step 3: Implement the launcher**

Create `main.go`:

```go
package main

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
)

var fixtures = map[string]string{
	"fake-gopls":              "fake-gopls.mjs",
	"fake-typescript-lsp":     "fake-typescript-lsp.mjs",
	"fake-sourcekit-lsp":      "fake-sourcekit-lsp.mjs",
	"fake-pyright-langserver": "fake-pyright-langserver.mjs",
	"fake-rust-analyzer":      "fake-rust-analyzer.mjs",
}

func executableName(value string) string {
	base := strings.ToLower(filepath.Base(value))
	return strings.TrimSuffix(base, filepath.Ext(base))
}

func fixtureForExecutable(value string) (string, bool) {
	fixture, ok := fixtures[executableName(value)]
	return fixture, ok
}

func probeOutput(value string, args []string) (string, bool) {
	if len(args) != 1 || args[0] != "--version" {
		return "", false
	}
	switch executableName(value) {
	case "pyright":
		return "pyright 1.1.400\n", true
	case "swiftc":
		return "Apple Swift version 6.3.3-fake (codeatlas fixture)\n", true
	default:
		return "", false
	}
}

func main() {
	if output, ok := probeOutput(os.Args[0], os.Args[1:]); ok {
		_, _ = os.Stdout.WriteString(output)
		return
	}
	fixture, ok := fixtureForExecutable(os.Args[0])
	if !ok {
		fmt.Fprintln(os.Stderr, "unsupported CodeAtlas E2E launcher name")
		os.Exit(64)
	}
	executable, err := os.Executable()
	if err != nil {
		fmt.Fprintln(os.Stderr, "resolve launcher executable:", err)
		os.Exit(70)
	}
	harnessDir := filepath.Clean(filepath.Join(filepath.Dir(executable), "..", "..", "harness"))
	script := filepath.Join(harnessDir, fixture)
	arguments := append([]string{script}, os.Args[1:]...)
	command := exec.Command("node", arguments...)
	command.Stdin = os.Stdin
	command.Stdout = os.Stdout
	command.Stderr = os.Stderr
	if err := command.Run(); err != nil {
		var exitError *exec.ExitError
		if errors.As(err, &exitError) {
			os.Exit(exitError.ExitCode())
		}
		fmt.Fprintln(os.Stderr, "start repository E2E fixture:", err)
		os.Exit(70)
	}
}
```

Add `errors` to the import list. The launcher resolves its own generated directory to `e2e/harness`; it never consumes a workspace-supplied script path.

- [ ] **Step 4: Run launcher unit tests**

```powershell
Set-Location backend
go test ../e2e/harness/lsp-launcher/main.go ../e2e/harness/lsp-launcher/main_test.go -count=1
```

Expected: both tests pass.

- [ ] **Step 5: Commit native launcher source**

```powershell
Set-Location ..
git add e2e/harness/lsp-launcher/main.go e2e/harness/lsp-launcher/main_test.go
git commit -m "test: add native fake LSP launcher"
```

---

### Task 3: Build launchers before E2E and test real execution

**Files:**
- Modify: `e2e/harness/lsp-launchers.mjs`
- Create: `e2e/harness/prepare-lsp-launchers.mjs`
- Modify: `e2e/tests/lsp-launcher.test.mjs`
- Modify: `e2e/package.json`
- Modify: `.gitignore`

**Interfaces:**
- Produces: `prepareFakeLSPLaunchers({root, platform}) Promise<void>`
- Produces on Windows: seven executables under `e2e/.generated/lsp`
- Consumes: `generatedLauncherNames` from Task 1 and `main.go` from Task 2

- [ ] **Step 1: Add failing Windows execution tests**

Extend `lsp-launcher.test.mjs`:

```js
import { execFile } from 'node:child_process';
import { promisify } from 'node:util';
import { fileURLToPath } from 'node:url';
import { prepareFakeLSPLaunchers } from '../harness/lsp-launchers.mjs';

const execFileAsync = promisify(execFile);
const repositoryRoot = path.resolve(path.dirname(fileURLToPath(import.meta.url)), '../..');

test('generated Windows launchers forward version probes', { skip: process.platform !== 'win32' }, async () => {
  await prepareFakeLSPLaunchers({ root: repositoryRoot });
  const generated = path.join(repositoryRoot, 'e2e/.generated/lsp');
  const gopls = await execFileAsync(path.join(generated, 'fake-gopls.exe'), ['version']);
  assert.match(gopls.stdout, /gopls v0\.22\.0-fake/u);
  const pyright = await execFileAsync(path.join(generated, 'pyright.exe'), ['--version']);
  assert.equal(pyright.stdout, 'pyright 1.1.400\r\n');
});
```

Normalize the pyright assertion with `pyright.stdout.trim()` if Node returns platform-normalized output; assert the literal `pyright 1.1.400` after trimming.

- [ ] **Step 2: Run the execution test and prove it fails**

```powershell
Set-Location e2e
node --test tests/lsp-launcher.test.mjs
```

Expected on Windows: import failure because `prepareFakeLSPLaunchers` does not exist.

- [ ] **Step 3: Implement the Windows build preparation**

Add to `lsp-launchers.mjs`:

```js
import { execFile } from 'node:child_process';
import { copyFile, mkdir } from 'node:fs/promises';
import { promisify } from 'node:util';

const execFileAsync = promisify(execFile);

export async function prepareFakeLSPLaunchers({ root, platform = process.platform }) {
  if (platform !== 'win32') return;
  const outputDirectory = path.join(root, 'e2e/.generated/lsp');
  await mkdir(outputDirectory, { recursive: true });
  const baseExecutable = path.join(outputDirectory, 'codeatlas-e2e-lsp-launcher.exe');
  await execFileAsync('go', [
    'build', '-trimpath', '-o', baseExecutable,
    path.join(root, 'e2e/harness/lsp-launcher/main.go'),
  ], { cwd: path.join(root, 'backend'), windowsHide: true });
  await Promise.all(generatedLauncherNames.map((name) =>
    copyFile(baseExecutable, path.join(outputDirectory, `${name}.exe`))));
}
```

The closed output names come from source, not from user input. Existing files are atomically overwritten by the build/copy operations without recursively deleting an unresolved path.

- [ ] **Step 4: Add the preparation entry point**

Create `prepare-lsp-launchers.mjs`:

```js
import path from 'node:path';
import { fileURLToPath } from 'node:url';

import { prepareFakeLSPLaunchers } from './lsp-launchers.mjs';

const root = path.resolve(path.dirname(fileURLToPath(import.meta.url)), '../..');
await prepareFakeLSPLaunchers({ root });
```

- [ ] **Step 5: Wire npm and ignore generated binaries**

Add to `e2e/package.json` scripts:

```json
"pretest": "node harness/prepare-lsp-launchers.mjs"
```

Append the new test file to the `test` command:

```text
tests/lsp-launcher.test.mjs
```

Add to `.gitignore`:

```gitignore
/e2e/.generated/
```

- [ ] **Step 6: Run unit and native launcher tests**

```powershell
Set-Location e2e
node harness/prepare-lsp-launchers.mjs
node --test tests/lsp-launcher.test.mjs
```

Expected: resolver tests and Windows execution tests pass; `git status --short` does not list `e2e/.generated`.

- [ ] **Step 7: Commit preparation and execution tests**

```powershell
Set-Location ..
git add e2e/harness/lsp-launchers.mjs e2e/harness/prepare-lsp-launchers.mjs e2e/tests/lsp-launcher.test.mjs e2e/package.json .gitignore
git commit -m "test: build fake LSP launchers for Windows E2E"
```

---

### Task 4: Run the complete browser E2E suite

**Files:**
- Generated and ignored: `e2e/.generated/`, `e2e/reports/`

**Interfaces:**
- Consumes: Tasks 1-3 and the existing `dist/codeatlas.exe`
- Produces: passing full E2E evidence for every advanced semantic provider

- [ ] **Step 1: Build production assets and backend**

```powershell
Set-Location ..
make build
```

Expected: frontend assets and `dist/codeatlas.exe` build successfully.

- [ ] **Step 2: Run the full E2E command uncached**

```powershell
Set-Location e2e
npm test
```

Expected: all 10 existing browser scenarios plus the launcher contract tests pass. Go, TypeScript, Swift, Python, Rust, and Monaco semantic providers are available; no `.mjs is not a valid Win32 application` or cascading Monaco failure remains.

- [ ] **Step 3: Inspect generated E2E reports**

Assert every report has a passing status and contains no API key, endpoint URL, absolute fixture launcher path, or raw provider response. Verify the language-provider reports identify the expected provider without exposing `fake-*.mjs` paths.

- [ ] **Step 4: Run npm audit**

```powershell
npm audit --audit-level=high
```

Expected: zero high/critical vulnerabilities and exit code zero.

- [ ] **Step 5: Record fresh pass counts and durations**

Report the exact Node test count, browser scenario count, failed/skipped count, and total duration. Generated binaries and reports remain unstaged.
