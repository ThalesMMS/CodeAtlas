import fs from 'node:fs/promises';
import os from 'node:os';
import path from 'node:path';

const excludedNames = new Set(['.codeatlas', '.git', 'node_modules']);

export async function copyFixtureWorkspace({ root, fixture, prefix = 'codeatlas-e2e-' } = {}) {
  const source = path.join(root, fixture);
  const destination = await fs.mkdtemp(path.join(os.tmpdir(), prefix));
  await copyDir(source, destination);
  return {
    path: destination,
    close: () => fs.rm(destination, { recursive: true, force: true }),
  };
}

async function copyDir(source, destination) {
  await fs.mkdir(destination, { recursive: true });
  const entries = await fs.readdir(source, { withFileTypes: true });
  for (const entry of entries) {
    if (excludedNames.has(entry.name)) {
      continue;
    }
    const from = path.join(source, entry.name);
    const to = path.join(destination, entry.name);
    if (entry.isDirectory()) {
      await copyDir(from, to);
    } else if (entry.isFile()) {
      await fs.copyFile(from, to);
    } else if (entry.isSymbolicLink()) {
      const target = await fs.readlink(from);
      await fs.symlink(target, to);
    }
  }
}
