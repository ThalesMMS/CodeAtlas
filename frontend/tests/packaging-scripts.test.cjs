const assert = require('node:assert/strict');
const { execFileSync, spawnSync } = require('node:child_process');
const fs = require('node:fs');
const os = require('node:os');
const path = require('node:path');
const test = require('node:test');

const repoRoot = path.resolve(__dirname, '..', '..');

function fixture(t, scriptName) {
  const root = fs.mkdtempSync(path.join(os.tmpdir(), 'codeatlas-package-'));
  const outside = fs.mkdtempSync(path.join(os.tmpdir(), 'codeatlas-cwd-'));
  t.after(() => fs.rmSync(root, { recursive: true, force: true }));
  t.after(() => fs.rmSync(outside, { recursive: true, force: true }));

  for (const directory of [
    'frontend',
    path.join('backend', 'cmd', 'codeatlas'),
    path.join('examples', 'tinycommerce'),
  ]) {
    fs.mkdirSync(path.join(root, directory), { recursive: true });
  }
  fs.writeFileSync(path.join(root, '.env'), 'CODEATLAS_TEST_ENV=loaded-from-dotenv\n');

  const source = path.join(repoRoot, scriptName);
  assert.ok(fs.existsSync(source), `${scriptName} must exist at the repository root`);
  const target = path.join(root, scriptName);
  fs.copyFileSync(source, target);
  fs.chmodSync(target, 0o755);

  const templates = scriptName.endsWith('.cmd')
    ? [path.join('packaging', 'windows', 'codeatlas-server.cmd')]
    : [
      path.join('packaging', 'macos', 'CodeAtlas.Info.plist'),
      path.join('packaging', 'macos', 'CodeAtlas.icns'),
      path.join('packaging', 'macos', 'codeatlas-server'),
      path.join('packaging', 'licenses', 'gopls-LICENSE'),
      path.join('packaging', 'licenses', 'pyright-LICENSE'),
      path.join('packaging', 'licenses', 'typescript-LICENSE'),
      path.join('packaging', 'licenses', 'typescript-language-server-LICENSE'),
      path.join('packaging', 'macos', 'lsp-bin', 'pyright'),
      path.join('packaging', 'macos', 'lsp-bin', 'pyright-langserver'),
      path.join('packaging', 'macos', 'lsp-bin', 'typescript-language-server'),
      path.join('packaging', 'lsp', 'package.json'),
      path.join('packaging', 'lsp', 'package-lock.json'),
    ];
  for (const relative of templates) {
    const templateSource = path.join(repoRoot, relative);
    assert.ok(fs.existsSync(templateSource), `${relative} must be source controlled`);
    const templateTarget = path.join(root, relative);
    fs.mkdirSync(path.dirname(templateTarget), { recursive: true });
    fs.copyFileSync(templateSource, templateTarget);
    fs.chmodSync(templateTarget, 0o755);
  }

  return {
    root,
    outside,
    script: target,
    log: path.join(root, 'packaging.log'),
    bin: path.join(root, 'fake-bin'),
  };
}

function readLog(log) {
  return fs.readFileSync(log, 'utf8').replaceAll('\\', '/');
}

function prependPath(bin) {
  return `${bin}${path.delimiter}${process.env.PATH}`;
}

test('Windows script builds the embedded executable and runs it from the repository root', { skip: process.platform !== 'win32' }, (t) => {
  const f = fixture(t, 'build_package_and_run.cmd');
  fs.mkdirSync(f.bin);
  fs.mkdirSync(path.join(f.root, 'dist'));
  fs.writeFileSync(path.join(f.root, 'dist', 'stale-secret.txt'), 'must not survive packaging');

  const helperSource = path.join(f.root, 'runner.go');
  const helper = path.join(f.root, 'runner.exe');
  fs.writeFileSync(helperSource, String.raw`package main
import (
  "fmt"
  "os"
  "strings"
)
func main() {
  file, err := os.OpenFile(os.Getenv("CODEATLAS_TEST_LOG"), os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0600)
  if err != nil { panic(err) }
  defer file.Close()
  cwd, _ := os.Getwd()
  fmt.Fprintf(file, "run|cwd=%s|dotenv=%s|workspace=%s|args=%s\n", cwd, os.Getenv("CODEATLAS_TEST_ENV"), os.Getenv("CODEATLAS_WORKSPACE"), strings.Join(os.Args[1:], " "))
}
`);
  execFileSync('go', ['build', '-o', helper, helperSource], { stdio: 'pipe' });

  fs.writeFileSync(path.join(f.bin, 'npm.cmd'), String.raw`@echo off
if "%~1"=="--version" echo 11.16.0& exit /b 0
>>"%CODEATLAS_TEST_LOG%" echo npm^|cwd=%CD%^|args=%*
if "%~1"=="run" if "%~2"=="build" (
  if not exist "..\backend\internal\webui\dist" mkdir "..\backend\internal\webui\dist"
  >"..\backend\internal\webui\dist\index.html" echo embedded
)
exit /b 0
`);
  fs.writeFileSync(path.join(f.bin, 'go.cmd'), String.raw`@echo off
if "%~1"=="version" echo go version go1.26.4 windows/amd64& exit /b 0
>>"%CODEATLAS_TEST_LOG%" echo go^|cwd=%CD%^|cgo=%CGO_ENABLED%^|cc=%CC%^|cxx=%CXX%^|args=%*
set "OUTPUT="
:parse
if "%~1"=="" goto done
if "%~1"=="-o" (
  set "OUTPUT=%~2"
  shift
)
shift
goto parse
:done
if defined OUTPUT copy /y "%CODEATLAS_TEST_HELPER%" "%OUTPUT%" >nul
exit /b 0
`);
  fs.writeFileSync(path.join(f.bin, 'gcc.cmd'), '@echo off\r\nexit /b 0\r\n');
  fs.writeFileSync(path.join(f.bin, 'g++.cmd'), '@echo off\r\nexit /b 0\r\n');

  const result = spawnSync('cmd.exe', ['/d', '/c', f.script, '-listen', '127.0.0.1:19090'], {
    cwd: f.outside,
    encoding: 'utf8',
    env: {
      ...process.env,
      PATH: prependPath(f.bin),
      CODEATLAS_TEST_HELPER: helper,
      CODEATLAS_TEST_LOG: f.log,
      GO_VERSION: 'go0.1.0',
      NODE_VERSION: 'v1.0.0',
      NPM_VERSION: '1.0.0',
    },
  });

  assert.equal(result.status, 0, `${result.stdout}\n${result.stderr}`);
  assert.match(result.stdout, /Starting CodeAtlas\.\.\./);
  assert.ok(fs.existsSync(path.join(f.root, 'dist', 'codeatlas.exe')));
  assert.ok(fs.existsSync(path.join(f.root, 'dist', 'codeatlas-server.exe')));
  assert.ok(fs.existsSync(path.join(f.root, 'dist', 'codeatlas-server.cmd')));
  assert.equal(fs.existsSync(path.join(f.root, 'dist', 'stale-secret.txt')), false);
  const serverResult = spawnSync('cmd.exe', ['/d', '/c', path.join(f.root, 'dist', 'codeatlas-server.cmd'), '-listen', '127.0.0.1:19091'], {
    cwd: f.outside,
    encoding: 'utf8',
    env: {
      ...process.env,
      PATH: prependPath(f.bin),
      CODEATLAS_TEST_LOG: f.log,
      CODEATLAS_TEST_ENV: '',
      CODEATLAS_WORKSPACE: '',
    },
  });
  assert.equal(serverResult.status, 0, `${serverResult.stdout}\n${serverResult.stderr}`);
  const log = readLog(f.log);
  assert.match(log, /npm\|cwd=.*\/frontend\|args=ci/);
  assert.match(log, /npm\|cwd=.*\/frontend\|args=run build/);
  assert.match(log, /go\|cwd=.*\/backend\|cgo=1\|cc=gcc\|cxx=g\+\+\|args=build -tags "?fts5 desktop"? -trimpath -ldflags "?-H=windowsgui"? -o "?.*\/dist\.staging\/codeatlas\.exe"? \.\/cmd\/codeatlas/);
  assert.match(log, /go\|cwd=.*\/backend\|cgo=1\|cc=gcc\|cxx=g\+\+\|args=build -tags "?fts5 desktop"? -trimpath -o "?.*\/dist\.staging\/codeatlas-server\.exe"? \.\/cmd\/codeatlas/);
  assert.match(log, /run\|cwd=.*codeatlas-package-[^/]+\|dotenv=loaded-from-dotenv\|workspace=.*\/examples\/tinycommerce\|args=-listen 127\.0\.0\.1:19090/);
  assert.match(log, /run\|cwd=.*codeatlas-cwd-[^/]+\|dotenv=\|workspace=\|args=-desktop=false -listen 127\.0\.0\.1:19091/);
});

test('Windows script rejects non-CodeAtlas dotenv variables before packaging', { skip: process.platform !== 'win32' }, async (t) => {
  for (const key of ['ROOT', 'PATH', 'CC', 'CXX']) {
    await t.test(key, (st) => {
      const f = fixture(st, 'build_package_and_run.cmd');
      fs.mkdirSync(f.bin);
      fs.writeFileSync(path.join(f.root, '.env'), `${key}=untrusted\n`);
      fs.writeFileSync(path.join(f.bin, 'go.cmd'), '@echo off\r\nif "%~1"=="version" echo go version go1.26.4 windows/amd64\r\nexit /b 0\r\n');
      fs.writeFileSync(path.join(f.bin, 'node.cmd'), '@echo off\r\nif "%~1"=="--version" echo v26.7.0\r\nexit /b 0\r\n');
      fs.writeFileSync(path.join(f.bin, 'npm.cmd'), '@echo off\r\nif "%~1"=="--version" echo 11.16.0& exit /b 0\r\n>>"%CODEATLAS_TEST_LOG%" echo npm-started\r\nexit /b 0\r\n');
      fs.writeFileSync(path.join(f.bin, 'gcc.cmd'), '@echo off\r\nexit /b 0\r\n');
      fs.writeFileSync(path.join(f.bin, 'g++.cmd'), '@echo off\r\nexit /b 0\r\n');
      const result = spawnSync('cmd.exe', ['/d', '/c', f.script], {
        cwd: f.outside,
        encoding: 'utf8',
        timeout: 5000,
        env: { ...process.env, PATH: prependPath(f.bin), CODEATLAS_TEST_LOG: f.log },
      });
      assert.equal(result.status, 1, `${result.stdout}\n${result.stderr}`);
      assert.match(result.stderr, new RegExp(`Unsupported \\.env variable on line 1: ${key}`));
      assert.equal(fs.existsSync(f.log), false, 'dependency installation must not start after an unsafe dotenv entry');
    });
  }
});

test('Windows script refuses to move staging beneath an undeletable package', () => {
  const script = fs.readFileSync(path.join(repoRoot, 'build_package_and_run.cmd'), 'utf8');
  assert.match(
    script,
    /if exist "%ROOT%dist" rmdir \/s \/q "%ROOT%dist"\s+if exist "%ROOT%dist" \(\s+echo Error: Could not replace the existing package directory\.>&2\s+goto build_failed\s+\)\s+move "%STAGING%" "%ROOT%dist"/i,
  );
});

test('Windows script rejects unsupported build-tool versions before installation', { skip: process.platform !== 'win32' }, async (t) => {
  const cases = [
    { name: 'Go', go: '1.22.9', node: '26.7.0', npm: '11.16.0', message: /Go 1\.23 or newer is required.*1\.22\.9/s },
    { name: 'Node.js', go: '1.26.4', node: '25.9.0', npm: '11.16.0', message: /Node\.js 26 or newer is required.*25\.9\.0/s },
    { name: 'npm', go: '1.26.4', node: '26.7.0', npm: '12.0.0', message: /npm >=11\.16\.0 and <12 is required.*12\.0\.0/s },
  ];
  for (const item of cases) {
    await t.test(item.name, (st) => {
      const f = fixture(st, 'build_package_and_run.cmd');
      fs.mkdirSync(f.bin);
      fs.writeFileSync(path.join(f.bin, 'go.cmd'), '@echo off\r\nif "%~1"=="version" echo go version go%CODEATLAS_TEST_GO_VERSION% windows/amd64\r\nexit /b 0\r\n');
      fs.writeFileSync(path.join(f.bin, 'node.cmd'), '@echo off\r\nif "%~1"=="--version" echo v%CODEATLAS_TEST_NODE_VERSION%\r\nexit /b 0\r\n');
      fs.writeFileSync(path.join(f.bin, 'npm.cmd'), '@echo off\r\nif "%~1"=="--version" echo %CODEATLAS_TEST_NPM_VERSION%\r\nexit /b 0\r\n');
      fs.writeFileSync(path.join(f.bin, 'gcc.cmd'), '@echo off\r\nexit /b 0\r\n');
      const result = spawnSync('cmd.exe', ['/d', '/c', f.script], {
        cwd: f.outside,
        encoding: 'utf8',
        env: {
          ...process.env,
          PATH: prependPath(f.bin),
          CODEATLAS_TEST_GO_VERSION: item.go,
          CODEATLAS_TEST_NODE_VERSION: item.node,
          CODEATLAS_TEST_NPM_VERSION: item.npm,
        },
      });
      assert.equal(result.status, 1, `${result.stdout}\n${result.stderr}`);
      assert.match(result.stderr, item.message);
    });
  }
});

test('Windows script requires a matching C++ compiler before installation', { skip: process.platform !== 'win32' }, (t) => {
  const f = fixture(t, 'build_package_and_run.cmd');
  fs.mkdirSync(f.bin);
  fs.writeFileSync(path.join(f.bin, 'go.cmd'), '@echo off\r\nif "%~1"=="version" echo go version go1.26.4 windows/amd64\r\nexit /b 0\r\n');
  fs.writeFileSync(path.join(f.bin, 'node.cmd'), '@echo off\r\nif "%~1"=="--version" echo v26.7.0\r\nexit /b 0\r\n');
  fs.writeFileSync(path.join(f.bin, 'npm.cmd'), '@echo off\r\nif "%~1"=="--version" echo 11.16.0& exit /b 0\r\n>>"%CODEATLAS_TEST_LOG%" echo npm-started\r\nexit /b 0\r\n');
  fs.writeFileSync(path.join(f.bin, 'gcc.cmd'), '@echo off\r\nexit /b 0\r\n');
  const system32 = path.join(process.env.SystemRoot, 'System32');
  const result = spawnSync('cmd.exe', ['/d', '/c', f.script], {
    cwd: f.outside,
    encoding: 'utf8',
    env: { ...process.env, PATH: `${f.bin}${path.delimiter}${system32}`, CODEATLAS_TEST_LOG: f.log },
  });
  assert.equal(result.status, 1, `${result.stdout}\n${result.stderr}`);
  assert.match(result.stderr, /C\+\+ compiler.*g\+\+/i);
  assert.equal(fs.existsSync(f.log), false, 'npm must not run without a matching C++ compiler');
});

test('macOS script builds the embedded executable and runs it from the repository root', { skip: process.platform === 'win32' && !findBash() }, (t) => {
  const bash = findBash();
  if (!bash) return t.skip('bash is unavailable');

  const f = fixture(t, 'build_package_and_run.sh');
  fs.mkdirSync(f.bin);
  writeDarwinUname(f.bin);
  fs.mkdirSync(path.join(f.root, 'dist', 'CodeAtlas.app', 'Contents', 'Resources'), { recursive: true });
  fs.writeFileSync(path.join(f.root, 'dist', 'CodeAtlas.app', 'Contents', 'Resources', 'stale-secret.txt'), 'must not survive packaging');
  const dotenvMarker = path.join(f.root, 'dotenv-command-ran');
  fs.writeFileSync(path.join(f.root, '.env'), 'CODEATLAS_TEST_ENV=$(touch "$CODEATLAS_TEST_MARKER")\n');
  const helper = path.join(f.root, 'runner');
  fs.writeFileSync(helper, `#!/usr/bin/env bash\nprintf 'run|cwd=%s|dotenv=%s|workspace=%s|args=%s\\n' "$PWD" "$CODEATLAS_TEST_ENV" "$CODEATLAS_WORKSPACE" "$*" >> "$CODEATLAS_TEST_LOG"\n`);
  fs.chmodSync(helper, 0o755);

  fs.writeFileSync(path.join(f.bin, 'npm'), `#!/usr/bin/env bash
if [[ "$1" == --version ]]; then printf '11.16.0\\n'; exit 0; fi
printf 'npm|cwd=%s|args=%s\\n' "$PWD" "$*" >> "$CODEATLAS_TEST_LOG"
if [[ "$1" == run && "$2" == build ]]; then
  mkdir -p ../backend/internal/webui/dist
  printf embedded > ../backend/internal/webui/dist/index.html
fi
if [[ "$1" == ci && "$PWD" == */packaging/lsp ]]; then
  mkdir -p node_modules/pyright node_modules/typescript-language-server/lib node_modules/typescript/lib
  printf stub > node_modules/pyright/index.js
  printf stub > node_modules/pyright/langserver.index.js
  printf stub > node_modules/typescript-language-server/lib/cli.mjs
  printf stub > node_modules/typescript/lib/tsserver.js
fi
`);
  fs.writeFileSync(path.join(f.bin, 'go'), `#!/usr/bin/env bash
if [[ "$1" == version ]]; then printf 'go version go1.26.4 darwin/arm64\\n'; exit 0; fi
printf 'go|cwd=%s|cgo=%s|cc=%s|cxx=%s|args=%s\\n' "$PWD" "$CGO_ENABLED" "$CC" "$CXX" "$*" >> "$CODEATLAS_TEST_LOG"
if [[ "$1" == install && -n "$GOBIN" ]]; then cp "$CODEATLAS_TEST_HELPER" "$GOBIN/gopls"; chmod +x "$GOBIN/gopls"; exit 0; fi
output=''
while (($#)); do
  if [[ "$1" == -o ]]; then output="$2"; shift; fi
  shift
done
if [[ -n "$output" ]]; then cp "$CODEATLAS_TEST_HELPER" "$output"; chmod +x "$output"; fi
`);
  fs.writeFileSync(path.join(f.bin, 'curl'), `#!/usr/bin/env bash
set -Eeuo pipefail
out=''
while (($#)); do
  if [[ "$1" == -o ]]; then out="$2"; shift; fi
  url="$1"; shift
done
printf 'curl|url=%s\\n' "$url" >> "$CODEATLAS_TEST_LOG"
dist="\${url##*/}"
work="$(mktemp -d)"
if [[ "$dist" == SHASUMS256.txt ]]; then
  : > "$out"
  exit 0
fi
name="\${dist%.tar.gz}"
mkdir -p "$work/$name/bin"
cp "$CODEATLAS_TEST_HELPER" "$work/$name/bin/node"
printf 'node license' > "$work/$name/LICENSE"
tar -czf "$out" -C "$work" "$name"
version="$(basename "$(dirname "$url")")"
printf '%s  %s\\n' "$(shasum -a 256 "$out" | awk '{ print $1 }')" "$dist" > "$(dirname "$out")/SHASUMS256-$version.txt"
`);
  fs.writeFileSync(path.join(f.bin, 'cc'), '#!/usr/bin/env bash\nexit 0\n');
  fs.writeFileSync(path.join(f.bin, 'c++'), '#!/usr/bin/env bash\nexit 0\n');
  fs.writeFileSync(path.join(f.bin, 'open'), `#!/usr/bin/env bash
set -Eeuo pipefail
[[ "$1" == -W ]]
bundle="$2"
shift 2
[[ "$1" == --args ]]
shift
exec "$bundle/Contents/MacOS/codeatlas" "$@"
`);
  for (const executable of ['npm', 'go', 'curl', 'cc', 'c++', 'open']) fs.chmodSync(path.join(f.bin, executable), 0o755);

  const result = spawnSync(bash, [f.script, '-listen', '127.0.0.1:19090'], {
    cwd: f.outside,
    encoding: 'utf8',
    env: {
      ...process.env,
      PATH: prependPath(f.bin),
      CODEATLAS_TEST_HELPER: helper,
      CODEATLAS_TEST_LOG: f.log,
      CODEATLAS_TEST_MARKER: dotenvMarker,
    },
  });

  assert.equal(result.status, 0, `${result.stdout}\n${result.stderr}`);
  assert.match(result.stdout, /Starting CodeAtlas\.\.\./);
  assert.equal(fs.existsSync(dotenvMarker), false, '.env values must never execute shell commands');
  const app = path.join(f.root, 'dist', 'CodeAtlas.app');
  assert.ok(fs.existsSync(path.join(app, 'Contents', 'MacOS', 'codeatlas')));
  assert.ok(fs.existsSync(path.join(app, 'Contents', 'Resources', 'CodeAtlas.icns')));
  assert.ok(fs.existsSync(path.join(app, 'Contents', 'Resources', 'bin', 'gopls')));
  assert.ok(fs.existsSync(path.join(app, 'Contents', 'Resources', 'gopls-LICENSE')));
  const resources = path.join(app, 'Contents', 'Resources');
  assert.ok(fs.existsSync(path.join(resources, 'bin', 'node')));
  assert.ok(fs.statSync(path.join(resources, 'bin', 'node')).mode & 0o111, 'bundled node must be executable');
  assert.equal(fs.readFileSync(path.join(resources, 'node-LICENSE'), 'utf8'), 'node license');
  for (const launcher of ['pyright', 'pyright-langserver', 'typescript-language-server']) {
    const launcherPath = path.join(resources, 'bin', launcher);
    assert.ok(fs.existsSync(launcherPath), `${launcher} launcher must be bundled`);
    assert.ok(fs.statSync(launcherPath).mode & 0o111, `${launcher} launcher must be executable`);
  }
  assert.ok(fs.existsSync(path.join(resources, 'lsp', 'node_modules', 'pyright', 'langserver.index.js')));
  assert.ok(fs.existsSync(path.join(resources, 'lsp', 'node_modules', 'typescript-language-server', 'lib', 'cli.mjs')));
  assert.ok(fs.existsSync(path.join(resources, 'lsp', 'node_modules', 'typescript', 'lib', 'tsserver.js')));
  assert.ok(fs.existsSync(path.join(resources, 'lsp', 'package-lock.json')));
  for (const notice of ['node-LICENSE', 'pyright-LICENSE', 'typescript-LICENSE', 'typescript-language-server-LICENSE']) {
    assert.ok(fs.existsSync(path.join(resources, notice)), `${notice} must be bundled`);
  }
  assert.equal(fs.existsSync(path.join(app, 'Contents', 'Resources', 'stale-secret.txt')), false);
  const infoPlist = fs.readFileSync(path.join(app, 'Contents', 'Info.plist'), 'utf8');
  assert.match(infoPlist, /<key>CFBundleIconFile<\/key>\s*<string>CodeAtlas\.icns<\/string>/);
  const serverLauncher = path.join(f.root, 'dist', 'codeatlas-server');
  assert.ok(fs.existsSync(serverLauncher));
  const serverResult = spawnSync(bash, [serverLauncher, '-listen', '127.0.0.1:19092'], {
    cwd: f.outside,
    encoding: 'utf8',
    env: { ...process.env, PATH: prependPath(f.bin), CODEATLAS_TEST_LOG: f.log, CODEATLAS_TEST_ENV: '', CODEATLAS_WORKSPACE: '' },
  });
  assert.equal(serverResult.status, 0, `${serverResult.stdout}\n${serverResult.stderr}`);
  const log = readLog(f.log);
  assert.match(log, /npm\|cwd=.*\/frontend\|args=ci/);
  assert.match(log, /npm\|cwd=.*\/frontend\|args=run build/);
  assert.match(log, /go\|cwd=.*\/backend\|cgo=1\|cc=cc\|cxx=c\+\+\|args=build -tags fts5 desktop -trimpath -o .*\/dist\.staging\/CodeAtlas\.app\/Contents\/MacOS\/codeatlas \.\/cmd\/codeatlas/);
  assert.match(log, /go\|cwd=.*\/backend\|cgo=\|cc=cc\|cxx=c\+\+\|args=install golang\.org\/x\/tools\/gopls@v0\.23\.0/);
  assert.match(log, /npm\|cwd=.*\/packaging\/lsp\|args=ci --ignore-scripts --no-audit --no-fund/);
  assert.match(log, /curl\|url=https:\/\/nodejs\.org\/dist\/v24\.20\.0\/SHASUMS256\.txt/);
  assert.match(log, /curl\|url=https:\/\/nodejs\.org\/dist\/v24\.20\.0\/node-v24\.20\.0-darwin-(arm64|x64)\.tar\.gz/);
  assert.match(log, /run\|cwd=.*codeatlas-package-[^/]+\|dotenv=\$\(touch "\$CODEATLAS_TEST_MARKER"\)\|workspace=.*\/examples\/tinycommerce\|args=-listen 127\.0\.0\.1:19090/);
  assert.match(log, /run\|cwd=.*codeatlas-cwd-[^/]+\|dotenv=\|workspace=\|args=-desktop=false -listen 127\.0\.0\.1:19092/);
});

test('macOS just-build script packages without reaching the launch step', () => {
  const buildOnly = fs.readFileSync(path.join(repoRoot, 'just-build.sh'), 'utf8');
  const buildAndRun = fs.readFileSync(path.join(repoRoot, 'build_package_and_run.sh'), 'utf8');
  assert.match(buildOnly, /_CODEATLAS_BUILD_ONLY=1 exec "\$ROOT\/build_package_and_run\.sh"/);
  assert.match(
    buildAndRun,
    /printf 'Built %s\\n'[\s\S]*if \[\[ "\$\{_CODEATLAS_BUILD_ONLY:-0\}" == 1 \]\]; then\s+exit 0\s+fi\s+printf 'Starting CodeAtlas/,
  );
  assert.doesNotMatch(buildOnly, /\bopen\s+-W\b/);
});

test('macOS script rejects unsupported build-tool versions before installation', { skip: process.platform === 'win32' && !findBash() }, async (t) => {
  const bash = findBash();
  if (!bash) return t.skip('bash is unavailable');
  const cases = [
    { name: 'Go', go: '1.22.9', node: '26.7.0', npm: '11.16.0', message: /Go 1\.23 or newer is required.*1\.22\.9/s },
    { name: 'Node.js', go: '1.26.4', node: '25.9.0', npm: '11.16.0', message: /Node\.js 26 or newer is required.*25\.9\.0/s },
    { name: 'npm', go: '1.26.4', node: '26.7.0', npm: '12.0.0', message: /npm >=11\.16\.0 and <12 is required.*12\.0\.0/s },
  ];
  for (const item of cases) {
    await t.test(item.name, (st) => {
      const f = fixture(st, 'build_package_and_run.sh');
      fs.mkdirSync(f.bin);
      writeDarwinUname(f.bin);
      fs.writeFileSync(path.join(f.bin, 'go'), '#!/usr/bin/env bash\nif [[ "$1" == version ]]; then printf "go version go%s darwin/arm64\\n" "$CODEATLAS_TEST_GO_VERSION"; fi\nexit 0\n');
      fs.writeFileSync(path.join(f.bin, 'node'), '#!/usr/bin/env bash\nif [[ "$1" == --version ]]; then printf "v%s\\n" "$CODEATLAS_TEST_NODE_VERSION"; fi\nexit 0\n');
      fs.writeFileSync(path.join(f.bin, 'npm'), '#!/usr/bin/env bash\nif [[ "$1" == --version ]]; then printf "%s\\n" "$CODEATLAS_TEST_NPM_VERSION"; fi\nexit 0\n');
      fs.writeFileSync(path.join(f.bin, 'cc'), '#!/usr/bin/env bash\nexit 0\n');
      for (const executable of ['go', 'node', 'npm', 'cc']) fs.chmodSync(path.join(f.bin, executable), 0o755);
      const result = spawnSync(bash, [f.script], {
        cwd: f.outside,
        encoding: 'utf8',
        env: {
          ...process.env,
          PATH: prependPath(f.bin),
          CODEATLAS_TEST_GO_VERSION: item.go,
          CODEATLAS_TEST_NODE_VERSION: item.node,
          CODEATLAS_TEST_NPM_VERSION: item.npm,
        },
      });
      assert.equal(result.status, 1, `${result.stdout}\n${result.stderr}`);
      assert.match(result.stderr, item.message);
    });
  }
});

test('macOS script requires a C++ compiler before installation', { skip: process.platform === 'win32' && !findBash() }, (t) => {
  const bash = findBash();
  if (!bash) return t.skip('bash is unavailable');
  const f = fixture(t, 'build_package_and_run.sh');
  fs.mkdirSync(f.bin);
  writeDarwinUname(f.bin);
  fs.writeFileSync(path.join(f.bin, 'go'), '#!/usr/bin/env bash\nif [[ "$1" == version ]]; then printf "go version go1.26.4 darwin/arm64\\n"; fi\nexit 0\n');
  fs.writeFileSync(path.join(f.bin, 'node'), '#!/usr/bin/env bash\nif [[ "$1" == --version ]]; then printf "v26.7.0\\n"; fi\nexit 0\n');
  fs.writeFileSync(path.join(f.bin, 'npm'), '#!/usr/bin/env bash\nif [[ "$1" == --version ]]; then printf "11.16.0\\n"; else printf installed > "$CODEATLAS_TEST_LOG"; fi\nexit 0\n');
  fs.writeFileSync(path.join(f.bin, 'cc'), '#!/usr/bin/env bash\nexit 0\n');
  fs.writeFileSync(path.join(f.bin, 'c++'), '#!/usr/bin/env bash\nexit 0\n');
  for (const executable of ['go', 'node', 'npm', 'cc', 'c++']) fs.chmodSync(path.join(f.bin, executable), 0o755);
  const result = spawnSync(bash, [f.script], {
    cwd: f.outside,
    encoding: 'utf8',
    env: {
      ...process.env,
      PATH: prependPath(f.bin),
      CC: 'cc',
      CXX: 'codeatlas-test-missing-cxx',
      CODEATLAS_TEST_LOG: f.log,
    },
  });
  assert.equal(result.status, 1, `${result.stdout}\n${result.stderr}`);
  assert.match(result.stderr, /C\+\+ compiler.*codeatlas-test-missing-cxx/i);
  assert.equal(fs.existsSync(f.log), false, 'npm must not run without a C++ compiler');
});

test('macOS script rejects malformed dotenv entries before installation', { skip: process.platform === 'win32' && !findBash() }, (t) => {
  const bash = findBash();
  if (!bash) return t.skip('bash is unavailable');
  const f = fixture(t, 'build_package_and_run.sh');
  fs.mkdirSync(f.bin);
  writeDarwinUname(f.bin);
  fs.writeFileSync(path.join(f.root, '.env'), 'this is not an assignment\n');
  fs.writeFileSync(path.join(f.bin, 'go'), '#!/usr/bin/env bash\nif [[ "$1" == version ]]; then printf "go version go1.26.4 darwin/arm64\\n"; fi\nexit 0\n');
  fs.writeFileSync(path.join(f.bin, 'node'), '#!/usr/bin/env bash\nif [[ "$1" == --version ]]; then printf "v26.7.0\\n"; fi\nexit 0\n');
  fs.writeFileSync(path.join(f.bin, 'npm'), '#!/usr/bin/env bash\nif [[ "$1" == --version ]]; then printf "11.16.0\\n"; else printf installed > "$CODEATLAS_TEST_LOG"; fi\nexit 0\n');
  fs.writeFileSync(path.join(f.bin, 'cc'), '#!/usr/bin/env bash\nexit 0\n');
  fs.writeFileSync(path.join(f.bin, 'c++'), '#!/usr/bin/env bash\nexit 0\n');
  for (const executable of ['go', 'node', 'npm', 'cc', 'c++']) fs.chmodSync(path.join(f.bin, executable), 0o755);

  const result = spawnSync(bash, [f.script], {
    cwd: f.outside,
    encoding: 'utf8',
    env: { ...process.env, PATH: prependPath(f.bin), CODEATLAS_TEST_LOG: f.log },
  });

  assert.equal(result.status, 1, `${result.stdout}\n${result.stderr}`);
  assert.match(result.stderr, /Invalid \.env entry on line 1\. Expected KEY=VALUE\./);
  assert.equal(fs.existsSync(f.log), false, 'dependency installation must not start after invalid configuration');
});

test('macOS script rejects non-CodeAtlas dotenv variables before packaging', { skip: process.platform === 'win32' && !findBash() }, async (t) => {
  const bash = findBash();
  if (!bash) return t.skip('bash is unavailable');
  for (const key of ['ROOT', 'PATH', 'CC', 'CXX']) {
    await t.test(key, (st) => {
      const f = fixture(st, 'build_package_and_run.sh');
      fs.mkdirSync(f.bin);
      writeDarwinUname(f.bin);
      fs.writeFileSync(path.join(f.root, '.env'), `${key}=untrusted\n`);
      fs.writeFileSync(path.join(f.bin, 'go'), '#!/usr/bin/env bash\nif [[ "$1" == version ]]; then printf "go version go1.26.4 darwin/arm64\\n"; fi\nexit 0\n');
      fs.writeFileSync(path.join(f.bin, 'node'), '#!/usr/bin/env bash\nif [[ "$1" == --version ]]; then printf "v26.7.0\\n"; fi\nexit 0\n');
      fs.writeFileSync(path.join(f.bin, 'npm'), '#!/usr/bin/env bash\nif [[ "$1" == --version ]]; then printf "11.16.0\\n"; else printf installed > "$CODEATLAS_TEST_LOG"; fi\nexit 0\n');
      fs.writeFileSync(path.join(f.bin, 'cc'), '#!/usr/bin/env bash\nexit 0\n');
      fs.writeFileSync(path.join(f.bin, 'c++'), '#!/usr/bin/env bash\nexit 0\n');
      for (const executable of ['go', 'node', 'npm', 'cc', 'c++']) fs.chmodSync(path.join(f.bin, executable), 0o755);
      const result = spawnSync(bash, [f.script], {
        cwd: f.outside,
        encoding: 'utf8',
        timeout: 5000,
        env: { ...process.env, PATH: prependPath(f.bin), CODEATLAS_TEST_LOG: f.log },
      });
      assert.equal(result.status, 1, `${result.stdout}\n${result.stderr}`);
      assert.match(result.stderr, new RegExp(`Unsupported \\.env variable on line 1: ${key}`));
      assert.equal(fs.existsSync(f.log), false, 'dependency installation must not start after an unsafe dotenv entry');
    });
  }
});

test('macOS script rejects non-Darwin hosts before dependency checks', { skip: process.platform === 'win32' && !findBash() }, (t) => {
  const bash = findBash();
  if (!bash) return t.skip('bash is unavailable');
  const f = fixture(t, 'build_package_and_run.sh');
  fs.mkdirSync(f.bin);
  fs.writeFileSync(path.join(f.bin, 'uname'), '#!/usr/bin/env bash\nprintf "Linux\\n"\n');
  fs.chmodSync(path.join(f.bin, 'uname'), 0o755);
  const result = spawnSync(bash, [f.script], {
    cwd: f.outside,
    encoding: 'utf8',
    timeout: 5000,
    env: { ...process.env, PATH: prependPath(f.bin) },
  });
  assert.equal(result.status, 1, `${result.stdout}\n${result.stderr}`);
  assert.match(result.stderr, /macOS packaging requires Darwin\. Found Linux\./);
});

test('third-party notices pin bundled WebView native snapshots exactly', () => {
  const notices = fs.readFileSync(path.join(repoRoot, 'THIRD_PARTY_NOTICES.md'), 'utf8');
  assert.match(notices, /webview.*fb6b17d826041411e6346cd9a785a5ceba7987c4/is);
  assert.match(notices, /WebView2.*1\.0\.1150\.38/is);
});

function writeDarwinUname(bin) {
  fs.writeFileSync(path.join(bin, 'uname'), '#!/usr/bin/env bash\nif [[ "$1" == -m ]]; then printf "arm64\\n"; else printf "Darwin\\n"; fi\n');
  fs.chmodSync(path.join(bin, 'uname'), 0o755);
}

function findBash() {
  const command = process.platform === 'win32' ? 'where.exe' : 'which';
  const result = spawnSync(command, ['bash'], { encoding: 'utf8' });
  return result.status === 0 ? result.stdout.trim().split(/\r?\n/, 1)[0] : '';
}
