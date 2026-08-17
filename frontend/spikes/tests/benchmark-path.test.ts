import test from 'node:test';
import assert from 'node:assert/strict';
import { dirname, join, resolve } from 'node:path';
import { fileURLToPath } from 'node:url';
import { spawnSync } from 'node:child_process';

const spikeRoot = resolve(dirname(fileURLToPath(import.meta.url)), '..');

test('benchmark resolves its workspace root on Windows', { skip: process.platform !== 'win32' }, () => {
  const output = runBenchmarkWithoutPath();

  assert.doesNotMatch(output, /[A-Z]:\\[A-Z]:\\/i);
});

test('benchmark invokes npm through the Windows command processor', { skip: process.platform !== 'win32' }, () => {
  const output = runBenchmarkWithoutPath();

  assert.match(output, /Command failed: .*cmd\.exe/is);
});

function runBenchmarkWithoutPath(): string {
  const result = spawnSync(process.execPath, [
    join(spikeRoot, 'node_modules', 'tsx', 'dist', 'cli.mjs'),
    join(spikeRoot, 'benchmarks', 'run-benchmarks.ts'),
  ], {
    cwd: spikeRoot,
    encoding: 'utf8',
    env: { ...process.env, PATH: '' },
  });

  return `${result.stdout}\n${result.stderr}`;
}
