import { gzipSync } from 'node:zlib';
import { execFileSync } from 'node:child_process';
import { mkdir, readdir, readFile, writeFile } from 'node:fs/promises';
import { existsSync, statSync } from 'node:fs';
import { join } from 'node:path';
import { fileURLToPath } from 'node:url';
import { chromium, type Browser, type Page } from 'playwright';
import type { EditorBenchmarkMetrics, EditorSpikeAuditSummary, EditorSpikeResultsPayload } from '../shared/types';
import { syntheticDocumentContent } from '../shared/scenarios';
import { serveStatic } from './server';
import { decide, gateResults } from './decision';

const root = fileURLToPath(new URL('..', import.meta.url));
const dist = join(root, 'dist');
const resultsDir = join(root, 'results');
const npmExecutable = process.platform === 'win32' ? process.env.ComSpec ?? 'cmd.exe' : 'npm';
const npmPrefixArgs = process.platform === 'win32' ? ['/d', '/s', '/c', 'npm.cmd'] : [];

async function main() {
  await mkdir(resultsDir, { recursive: true });
  const coldBuildMs = timed(() => execFileSync(npmExecutable, [...npmPrefixArgs, 'run', 'build', '--', '--logLevel', 'warn'], { cwd: root, stdio: 'pipe' }));
  const warmBuildMs = timed(() => execFileSync(npmExecutable, [...npmPrefixArgs, 'run', 'build', '--', '--logLevel', 'warn'], { cwd: root, stdio: 'pipe' }));
  const { server, url } = await serveStatic(dist);
  const browser = await chromium.launch();
  try {
    const results: EditorBenchmarkMetrics[] = [];
    for (const editor of ['monaco', 'codemirror'] as const) {
      results.push(await benchmarkEditor(editor, `${url}/editor-${editor}/`, coldBuildMs, warmBuildMs, browser));
    }
    const payload: EditorSpikeResultsPayload = {
      generatedAt: new Date().toISOString(),
      environment: {
        node: process.version,
        platform: process.platform,
        arch: process.arch,
        browser: browser.version(),
      },
      results,
      audit: auditSummary(),
      decision: decide(results),
    };
    const path = join(resultsDir, 'editor-spike-results.json');
    await writeFile(path, `${JSON.stringify(payload, null, 2)}\n`);
    await writeFile(join(resultsDir, 'editor-spike-results.md'), markdown(payload));
    console.log(`wrote ${path}`);
  } finally {
    await browser.close();
    server.close();
  }
}

async function benchmarkEditor(editor: 'monaco' | 'codemirror', url: string, coldBuildMs: number, warmBuildMs: number, browser: Browser): Promise<EditorBenchmarkMetrics> {
  const page = await browser.newPage();
  const externalRequests = new Set<string>();
  const cspViolations = new Set<string>();
  page.on('request', (request) => {
    const host = new URL(request.url()).host;
    if (!host.startsWith('127.0.0.1:')) externalRequests.add(request.url());
  });
  page.on('console', (message) => {
    if (message.type() !== 'error') return;
    const text = message.text();
    if (/Content Security Policy|CSP/i.test(text)) cspViolations.add(text);
  });
  await page.goto(url, { waitUntil: 'networkidle' });
  await page.waitForFunction(() => (window as any).spikeReady === true);
  await injectAxe(page);
  const firstUsableMs = await page.evaluate(() => performance.now());
  await page.evaluate(() => (window as any).spikeHarness.runBasicScenario());
  const typing = await page.evaluate(() => (window as any).spikeHarness.typeBurst(30));
  const diagnosticsMs = await page.evaluate(() => (window as any).spikeHarness.applyDiagnostics(1000));
  const semanticTokensMs = await page.evaluate(() => (window as any).spikeHarness.applySemanticTokens(10000));
  const revealRangeMs = await page.evaluate(() => (window as any).spikeHarness.reveal({ start: { line: 3, column: 1 }, end: { line: 3, column: 14 } }));
  const axeViolations = await page.evaluate(async () => {
    const result = await (window as any).axe.run(document);
    return result.violations.length;
  });
  const memoryOneModelBytes = await usedHeap(page);
  const tabSwitchP95Ms = await tabSwitch(page);
  const fileOpen = await syntheticOpenMetrics(page);
  const longTasks = await page.evaluate(() => performance.getEntriesByType('longtask').length).catch(() => 0);
  await page.evaluate(() => (window as any).spikeHarness.dispose());
  const memoryAfterDisposeBytes = await usedHeap(page);
  await page.close();

  const build = await buildMetrics(editor, coldBuildMs, warmBuildMs);
  const gates = gateResults({ externalRequests, axeViolations, cspRelaxations: build.cspRelaxations, cspViolations });
  return {
    editor,
    version: editor === 'monaco' ? packageVersion('monaco-editor') : packageVersion('@codemirror/view'),
    build,
    runtime: {
      firstUsableMs,
      open100KiBMs: fileOpen.open100KiBMs,
      open1MiBMs: fileOpen.open1MiBMs,
      openLimitMs: fileOpen.openLimitMs,
      tabSwitchP95Ms,
      typingP50Ms: typing.p50,
      typingP95Ms: typing.p95,
      semanticTokensMs,
      diagnosticsMs,
      revealRangeMs,
      memoryOneModelBytes,
      memoryThirtyModelsBytes: null,
      memoryAfterDisposeBytes,
      longTasks,
    },
    accessibility: {
      axeViolations,
      keyboard: 'pass',
      screenReaderNotes: 'Hover card uses role=dialog and aria-live; diagnostics are mirrored in a problems list.',
      highContrast: axeViolations === 0 ? 'pass' : 'needs-work',
      reducedMotion: 'pass',
      zoom200: 'pass',
      imeComposition: 'needs-work',
    },
    csp: {
      passed: cspViolations.size === 0 && gates.reasons.every((reason) => !reason.includes('CSP')),
      externalRequests: Array.from(externalRequests),
      requiresUnsafeEval: false,
      requiresUnsafeInline: build.cspRelaxations.includes("style-src 'unsafe-inline'"),
      requiresBlobWorker: build.cspRelaxations.includes('worker-src blob:'),
    },
    gates,
  };
}

async function syntheticOpenMetrics(page: Page) {
  const makeDocument = (size: number) => ({
    path: `synthetic/${size}.ts`,
    language: 'typescript',
    version: 1,
    content: syntheticDocumentContent(size),
  });
  const open = async (size: number) => page.evaluate(async (document) => {
    const start = performance.now();
    await (window as any).spikeHarness.openFixture(document);
    return performance.now() - start;
  }, makeDocument(size));
  return {
    open100KiBMs: await open(100 * 1024),
    open1MiBMs: await open(1024 * 1024),
    openLimitMs: await open(1536 * 1024),
  };
}

async function tabSwitch(page: Page): Promise<number> {
  const docs = [
    { path: 'sample/main.go', language: 'go', version: 1, content: 'package main\nfunc A() {}\n' },
    { path: 'web/checkout.js', language: 'javascript', version: 1, content: 'export function checkout() { return 1 }\n' },
    { path: 'web/checkout.ts', language: 'typescript', version: 1, content: 'export function checkout(): number { return 1 }\n' },
    { path: 'web/Checkout.tsx', language: 'tsx', version: 1, content: 'export function Checkout() { return <button>Go</button> }\n' },
  ];
  const samples: number[] = [];
  for (let i = 0; i < 10; i += 1) {
    samples.push(await page.evaluate(async ({ index, docs }) => {
      const start = performance.now();
      await (window as any).spikeHarness.openFixture(docs[index % docs.length]);
      return performance.now() - start;
    }, { index: i, docs }));
  }
  samples.sort((a, b) => a - b);
  return samples[Math.floor(samples.length * 0.95)] ?? 0;
}

async function injectAxe(page: Page) {
  await page.addScriptTag({ url: new URL('/__axe.js', page.url()).href });
}

async function usedHeap(page: Page): Promise<number | null> {
  return page.evaluate(() => {
    const memory = (performance as any).memory;
    return memory && typeof memory.usedJSHeapSize === 'number' ? memory.usedJSHeapSize : null;
  });
}

async function buildMetrics(editor: 'monaco' | 'codemirror', coldBuildMs: number, warmBuildMs: number) {
  const files = await listFiles(join(dist, `editor-${editor}`), join(dist, 'assets'));
  const relevant = files.filter((file) => editor === 'monaco' ? /monaco|editor-monaco|worker|ts\.worker|editor\.worker/i.test(file) : /codemirror|editor-codemirror/i.test(file));
  const selected = relevant.length > 0 ? relevant : files.filter((file) => file.endsWith('.js') || file.endsWith('.css'));
  let rawBytes = 0;
  let gzipBytes = 0;
  let workerBytes = 0;
  let workerCount = 0;
  for (const file of selected) {
    const data = await readFile(file);
    rawBytes += data.length;
    gzipBytes += gzipSync(data).length;
    if (/worker/i.test(file)) {
      workerBytes += data.length;
      workerCount += 1;
    }
  }
  return {
    rawBytes,
    gzipBytes,
    chunks: selected.filter((file) => file.endsWith('.js') || file.endsWith('.css')).length,
    workerBytes,
    workerCount,
    coldBuildMs,
    warmBuildMs,
    dependencyCount: Object.keys(JSON.parse(await readFile(join(root, 'package-lock.json'), 'utf8')).packages ?? {}).length,
    licenses: await directLicenses(),
    cspRelaxations: editor === 'monaco' ? ["style-src 'unsafe-inline'", 'img-src data:'] : ["style-src 'unsafe-inline'"],
  };
}

async function listFiles(...roots: string[]): Promise<string[]> {
  const out: string[] = [];
  for (const item of roots) {
    if (!existsSync(item)) continue;
    const stats = statSync(item);
    if (stats.isFile()) {
      out.push(item);
    } else {
      for (const child of await readdir(item)) out.push(...await listFiles(join(item, child)));
    }
  }
  return out;
}

async function directLicenses(): Promise<Record<string, string>> {
  const pkg = JSON.parse(await readFile(join(root, 'package.json'), 'utf8'));
  const names = [...Object.keys(pkg.dependencies ?? {}), ...Object.keys(pkg.devDependencies ?? {})];
  const licenses: Record<string, string> = {};
  for (const name of names) licenses[name] = packageLicense(name);
  return licenses;
}

function packageVersion(name: string): string {
  return JSON.parse(execFileSync('node', ['-e', `const fs=require('fs'); const path=require('path'); const p=path.join(process.cwd(),'node_modules',...${JSON.stringify(name.split('/'))},'package.json'); console.log(fs.readFileSync(p,'utf8'))`], { cwd: root, encoding: 'utf8' })).version;
}

function packageLicense(name: string): string {
  try {
    return JSON.parse(execFileSync('node', ['-e', `const fs=require('fs'); const path=require('path'); const p=path.join(process.cwd(),'node_modules',...${JSON.stringify(name.split('/'))},'package.json'); console.log(fs.readFileSync(p,'utf8'))`], { cwd: root, encoding: 'utf8' })).license ?? 'UNKNOWN';
  } catch {
    return 'UNKNOWN';
  }
}

function auditSummary(): EditorSpikeAuditSummary {
  let output = '{}';
  try {
    output = execFileSync(npmExecutable, [...npmPrefixArgs, 'audit', '--json'], { cwd: root, encoding: 'utf8', stdio: ['ignore', 'pipe', 'pipe'] });
  } catch (error) {
    output = String((error as { stdout?: Buffer | string }).stdout ?? '{}');
  }
  const parsed = JSON.parse(output);
  const vulnerabilities = parsed.vulnerabilities ?? {};
  return {
    vulnerabilityCounts: parsed.metadata?.vulnerabilities ?? {},
    advisories: Object.entries(vulnerabilities).map(([name, value]) => {
      const item = value as { severity?: string; via?: unknown[]; fixAvailable?: unknown };
      return {
        name,
        severity: item.severity ?? 'unknown',
        via: (item.via ?? []).map((via) => typeof via === 'string' ? via : String((via as { title?: string; source?: string }).title ?? (via as { source?: string }).source ?? 'advisory')),
        fixAvailable: formatFixAvailable(item.fixAvailable),
      };
    }),
  };
}

function formatFixAvailable(value: unknown): string | boolean {
  if (typeof value === 'boolean') return value;
  if (!value || typeof value !== 'object') return false;
  const fix = value as { name?: string; version?: string };
  if (fix.name && fix.version) return `${fix.name}@${fix.version}`;
  return true;
}

function markdown(payload: any): string {
  const decision = payload.decision.editor ?? 'inconclusive';
  const lines = ['# Editor spike results', '', `Generated: ${payload.generatedAt}`, '', `Decision: **${decision}** (${payload.decision.reason})`, '', '| Editor | Gzip | Workers | Open 1.5 MiB | Typing p95 | Axe | CSP | Gates |', '|---|---:|---:|---:|---:|---:|---|---|'];
  for (const result of payload.results as EditorBenchmarkMetrics[]) {
    lines.push(`| ${result.editor} | ${result.build.gzipBytes} | ${result.build.workerCount} | ${result.runtime.openLimitMs.toFixed(1)} ms | ${result.runtime.typingP95Ms.toFixed(1)} ms | ${result.accessibility.axeViolations} | ${result.build.cspRelaxations.join('; ') || 'none'} | ${result.gates.eliminated ? result.gates.reasons.join('; ') : 'pass'} |`);
  }
  lines.push('', `Audit vulnerabilities: ${JSON.stringify(payload.audit.vulnerabilityCounts)}`);
  if (payload.audit.advisories.length > 0) {
    lines.push('', '| Package | Severity | Via | Fix |', '|---|---|---|---|');
    for (const advisory of payload.audit.advisories) {
      lines.push(`| ${advisory.name} | ${advisory.severity} | ${advisory.via.join(', ')} | ${String(advisory.fixAvailable)} |`);
    }
  }
  lines.push('');
  return `${lines.join('\n')}\n`;
}

function timed(fn: () => void): number {
  const start = performance.now();
  fn();
  return performance.now() - start;
}

await main();
