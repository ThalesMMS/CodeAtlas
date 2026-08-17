import fs from 'node:fs/promises';
import path from 'node:path';
import { gzipSync } from 'node:zlib';

export async function loadJSON(filePath) {
  return JSON.parse(await fs.readFile(filePath, 'utf8'));
}

export async function checkBundleBudget({ root, distDir, budgetPath }) {
  const budget = await loadJSON(budgetPath);
  const manifest = await loadJSON(path.join(distDir, 'codeatlas-manifest.json'));
  const htmlPath = path.join(distDir, 'index.html');
  const html = await fs.readFile(htmlPath);
  const entrypoints = manifest.assets?.entrypoints ?? [];
  const styles = manifest.assets?.styles ?? [];
  const discoveredWorkers = await findWorkerAssets(distDir);
  const initialAssets = [...entrypoints, ...styles];

  const mainJsGzipBytes = await gzipSum(distDir, entrypoints);
  const styleGzipBytes = await gzipSum(distDir, styles);
  const htmlGzipBytes = gzipSync(html).byteLength;
  const workerGzipBytes = await gzipSum(distDir, discoveredWorkers);
  const totalInitialGzipBytes = mainJsGzipBytes + styleGzipBytes + htmlGzipBytes;
  const publicSourceMaps = await findFiles(distDir, (filePath) => filePath.endsWith('.map'));

  const measurements = {
    budgetVersion: budget.budgetVersion,
    mainJsGzipBytes,
    styleGzipBytes,
    htmlGzipBytes,
    totalInitialGzipBytes,
    workerGzipBytes,
    initialRequests: initialAssets.length,
    initialAssets,
    workerAssets: discoveredWorkers,
  };

  const failures = [];
  compare(failures, budget.budgetVersion, 'mainJsGzipBytes', measurements.mainJsGzipBytes, budget.mainJsGzipBytes);
  compare(failures, budget.budgetVersion, 'totalInitialGzipBytes', measurements.totalInitialGzipBytes, budget.totalInitialGzipBytes);
  compare(failures, budget.budgetVersion, 'workerGzipBytes', measurements.workerGzipBytes, budget.workerGzipBytes);
  compare(failures, budget.budgetVersion, 'maxInitialRequests', measurements.initialRequests, budget.maxInitialRequests);
  if (publicSourceMaps.length > 0) {
    failures.push(`production source maps must not be public: ${publicSourceMaps.map((file) => path.relative(root, file)).join(', ')}`);
  }

  return {
    ok: failures.length === 0,
    budget,
    measurements,
    failures,
  };
}

async function gzipSum(distDir, relativeFiles) {
  let total = 0;
  for (const relativeFile of relativeFiles) {
    const data = await fs.readFile(path.join(distDir, relativeFile));
    total += gzipSync(data).byteLength;
  }
  return total;
}

function compare(failures, budgetVersion, label, actual, limit) {
  if (typeof limit !== 'number' || limit <= 0) {
    failures.push(`${label} budget is not populated`);
    return;
  }
  if (actual > limit) {
    failures.push(`${label} ${actual} exceeds ${budgetVersion} budget ${limit}`);
  }
}

async function findWorkerAssets(distDir) {
  const files = await findFiles(distDir, (filePath) => /worker.*\.js$/.test(path.basename(filePath)));
  return files.map((filePath) => path.relative(distDir, filePath)).sort();
}

async function findFiles(dir, predicate) {
  const out = [];
  const entries = await fs.readdir(dir, { withFileTypes: true });
  for (const entry of entries) {
    const fullPath = path.join(dir, entry.name);
    if (entry.isDirectory()) {
      out.push(...await findFiles(fullPath, predicate));
    } else if (entry.isFile() && predicate(fullPath)) {
      out.push(fullPath);
    }
  }
  return out;
}
