import assert from 'node:assert/strict';
import { execFile } from 'node:child_process';
import path from 'node:path';
import test from 'node:test';
import { promisify } from 'node:util';
import { fileURLToPath } from 'node:url';

import { prepareFakeLSPLaunchers, resolveFakeLSPPath } from '../harness/lsp-launchers.mjs';

const root = path.resolve('C:/workspace/CodeAtlas');
const execFileAsync = promisify(execFile);
const repositoryRoot = path.resolve(path.dirname(fileURLToPath(import.meta.url)), '../..');

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

test('generated Windows launchers forward version probes', { skip: process.platform !== 'win32' }, async () => {
  await prepareFakeLSPLaunchers({ root: repositoryRoot });
  const generated = path.join(repositoryRoot, 'e2e/.generated/lsp');
  const gopls = await execFileAsync(path.join(generated, 'fake-gopls.exe'), ['version']);
  assert.match(gopls.stdout, /gopls v0\.22\.0-fake/u);
  const pyright = await execFileAsync(path.join(generated, 'pyright.exe'), ['--version']);
  assert.equal(pyright.stdout.trim(), 'pyright 1.1.400');
});
