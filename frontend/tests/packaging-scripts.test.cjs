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
>>"%CODEATLAS_TEST_LOG%" echo go^|cwd=%CD%^|cgo=%CGO_ENABLED%^|cc=%CC%^|args=%*
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
  const log = readLog(f.log);
  assert.match(log, /npm\|cwd=.*\/frontend\|args=ci/);
  assert.match(log, /npm\|cwd=.*\/frontend\|args=run build/);
  assert.match(log, /go\|cwd=.*\/backend\|cgo=1\|cc=gcc\|args=build -tags fts5 -trimpath -o "?.*\/dist\/codeatlas\.exe"? \.\/cmd\/codeatlas/);
  assert.match(log, /run\|cwd=.*codeatlas-package-[^/]+\|dotenv=loaded-from-dotenv\|workspace=.*\/examples\/tinycommerce\|args=-listen 127\.0\.0\.1:19090/);
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

test('macOS script builds the embedded executable and runs it from the repository root', { skip: process.platform === 'win32' && !findBash() }, (t) => {
  const bash = findBash();
  if (!bash) return t.skip('bash is unavailable');

  const f = fixture(t, 'build_package_and_run.sh');
  fs.mkdirSync(f.bin);
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
`);
  fs.writeFileSync(path.join(f.bin, 'go'), `#!/usr/bin/env bash
if [[ "$1" == version ]]; then printf 'go version go1.26.4 darwin/arm64\\n'; exit 0; fi
printf 'go|cwd=%s|cgo=%s|cc=%s|args=%s\\n' "$PWD" "$CGO_ENABLED" "$CC" "$*" >> "$CODEATLAS_TEST_LOG"
output=''
while (($#)); do
  if [[ "$1" == -o ]]; then output="$2"; shift; fi
  shift
done
if [[ -n "$output" ]]; then cp "$CODEATLAS_TEST_HELPER" "$output"; chmod +x "$output"; fi
`);
  fs.writeFileSync(path.join(f.bin, 'cc'), '#!/usr/bin/env bash\nexit 0\n');
  for (const executable of ['npm', 'go', 'cc']) fs.chmodSync(path.join(f.bin, executable), 0o755);

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
  assert.ok(fs.existsSync(path.join(f.root, 'dist', 'codeatlas')));
  const log = readLog(f.log);
  assert.match(log, /npm\|cwd=.*\/frontend\|args=ci/);
  assert.match(log, /npm\|cwd=.*\/frontend\|args=run build/);
  assert.match(log, /go\|cwd=.*\/backend\|cgo=1\|cc=cc\|args=build -tags fts5 -trimpath -o .*\/dist\/codeatlas \.\/cmd\/codeatlas/);
  assert.match(log, /run\|cwd=.*codeatlas-package-[^/]+\|dotenv=\$\(touch "\$CODEATLAS_TEST_MARKER"\)\|workspace=.*\/examples\/tinycommerce\|args=-listen 127\.0\.0\.1:19090/);
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

test('macOS script rejects malformed dotenv entries before installation', { skip: process.platform === 'win32' && !findBash() }, (t) => {
  const bash = findBash();
  if (!bash) return t.skip('bash is unavailable');
  const f = fixture(t, 'build_package_and_run.sh');
  fs.mkdirSync(f.bin);
  fs.writeFileSync(path.join(f.root, '.env'), 'this is not an assignment\n');
  fs.writeFileSync(path.join(f.bin, 'go'), '#!/usr/bin/env bash\nif [[ "$1" == version ]]; then printf "go version go1.26.4 darwin/arm64\\n"; fi\nexit 0\n');
  fs.writeFileSync(path.join(f.bin, 'node'), '#!/usr/bin/env bash\nif [[ "$1" == --version ]]; then printf "v26.7.0\\n"; fi\nexit 0\n');
  fs.writeFileSync(path.join(f.bin, 'npm'), '#!/usr/bin/env bash\nif [[ "$1" == --version ]]; then printf "11.16.0\\n"; else printf installed > "$CODEATLAS_TEST_LOG"; fi\nexit 0\n');
  fs.writeFileSync(path.join(f.bin, 'cc'), '#!/usr/bin/env bash\nexit 0\n');
  for (const executable of ['go', 'node', 'npm', 'cc']) fs.chmodSync(path.join(f.bin, executable), 0o755);

  const result = spawnSync(bash, [f.script], {
    cwd: f.outside,
    encoding: 'utf8',
    env: { ...process.env, PATH: prependPath(f.bin), CODEATLAS_TEST_LOG: f.log },
  });

  assert.equal(result.status, 1, `${result.stdout}\n${result.stderr}`);
  assert.match(result.stderr, /Invalid \.env entry on line 1\. Expected KEY=VALUE\./);
  assert.equal(fs.existsSync(f.log), false, 'dependency installation must not start after invalid configuration');
});

function findBash() {
  const command = process.platform === 'win32' ? 'where.exe' : 'which';
  const result = spawnSync(command, ['bash'], { encoding: 'utf8' });
  return result.status === 0 ? result.stdout.trim().split(/\r?\n/, 1)[0] : '';
}
