import { execFile } from 'node:child_process';
import { access, copyFile, mkdir } from 'node:fs/promises';
import path from 'node:path';
import { promisify } from 'node:util';

const execFileAsync = promisify(execFile);

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

export async function prepareFakeLSPLaunchers({ root, platform = process.platform, force = false }) {
  if (platform !== 'win32') return;
  const outputDirectory = path.join(root, 'e2e/.generated/lsp');
  await mkdir(outputDirectory, { recursive: true });
  const generatedPaths = generatedLauncherNames.map((name) => path.join(outputDirectory, `${name}.exe`));
  if (!force) {
    try {
      await Promise.all(generatedPaths.map((generatedPath) => access(generatedPath)));
      return;
    } catch {
      // A missing launcher requires one complete rebuild before processes start.
    }
  }
  const baseExecutable = path.join(outputDirectory, 'codeatlas-e2e-lsp-launcher.exe');
  await execFileAsync('go', [
    'build', '-trimpath', '-o', baseExecutable,
    path.join(root, 'e2e/harness/lsp-launcher/main.go'),
  ], { cwd: path.join(root, 'backend'), windowsHide: true });
  await Promise.all(generatedPaths.map((generatedPath) => copyFile(baseExecutable, generatedPath)));
}
