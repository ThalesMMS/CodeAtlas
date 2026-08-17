import { after, test } from 'node:test';
import assert from 'node:assert/strict';
import fs from 'node:fs/promises';
import path from 'node:path';
import { fileURLToPath } from 'node:url';

import { chromium } from 'playwright-core';

import { startFakeProvider } from '../harness/fake-provider.mjs';
import { startBackend, stopAll } from '../harness/process-manager.mjs';
import { copyFixtureWorkspace } from '../harness/workspace-manager.mjs';

const root = path.resolve(path.dirname(fileURLToPath(import.meta.url)), '../..');
const cleanup = [];
const modifier = process.platform === 'darwin' ? 'Meta' : 'Control';

after(async () => {
  await stopAll(cleanup);
});

test('production Monaco edits every shipped language family and preserves lease invariants', { timeout: 120_000 }, async () => {
  const provider = await startFakeProvider({ scenario: 'happy-path' });
  cleanup.push(provider);
  const workspace = await copyFixtureWorkspace({
    root,
    fixture: 'examples/tinycommerce',
    prefix: 'codeatlas-monaco-e2e-',
  });
  cleanup.push(workspace);

  const fixtures = new Map([
    ['editor/main.go', 'package editor\n\nfunc MainValue() int { return 1 }\n// GO_ERROR\n'],
    ['editor/app.js', 'export function jsValue() { return 1; }\n'],
    ['editor/component.jsx', 'export const Component = () => <div>one</div>;\n'],
    ['editor/app.ts', 'export const tsValue: number = 1;\n// TYPE_ERROR\n'],
    ['editor/component.tsx', 'export const Typed = (): JSX.Element => <span>one</span>;\n'],
    ['editor/module.mjs', 'export const moduleValue = 1;\n'],
    ['editor/common.cjs', 'exports.commonValue = 1;\n'],
    ['editor/module.mts', 'export const moduleValue: number = 1;\n'],
    ['editor/common.cts', 'export const commonValue: number = 1;\n'],
    ['editor/second.go', 'package editor\n\nfunc Second() {}\n'],
	['editor/order.swift', 'struct Order { let id: String }\n// SWIFT_ERROR\n'],
	['editor/service.py', 'def service_value() -> int:\n    return 1\n# PYTHON_ERROR\n'],
	['editor/repository.rs', 'pub fn repository_value() -> i32 { 1 }\n// RUST_ERROR\n'],
  ]);
  await fs.mkdir(path.join(workspace.path, 'editor'), { recursive: true });
  for (const [relativePath, content] of fixtures) {
    await fs.writeFile(path.join(workspace.path, relativePath), content);
  }
  const handoffPath = 'handoff/main.go';
  await fs.mkdir(path.join(workspace.path, 'handoff'), { recursive: true });
  await fs.writeFile(path.join(workspace.path, handoffPath), 'package handoff\n\nfunc Value() int { return 1 }\n');
  const largePath = 'editor/large.js';
  // One valid multiline token keeps backend parsing deterministic while still
  // exercising Monaco's 1.5 MiB model/layout boundary.
  const largeContent = `export const first = 1;\n/*\n${`${'x'.repeat(1023)}\n`.repeat(1536)}*/\n`;
  assert.ok(Buffer.byteLength(largeContent) >= 1.5 * 1024 * 1024);
  // Index a tiny placeholder so the file is navigable in the tree. The exact
  // 1.5 MiB editor payload is installed after readiness; the polling watcher is
  // intentionally quiescent so this E2E measures the overlay/editor path rather
  // than repository extraction of a synthetic benchmark corpus.
  await fs.writeFile(path.join(workspace.path, largePath), 'export const first = 1;\n');

  const backend = await startBackend({
    root,
    workspaceDir: workspace.path,
    providerBaseURL: provider.baseURL,
    maxFileBytes: 2 * 1024 * 1024,
    pollInterval: '1h',
    typescriptLSPPath: path.join(root, 'e2e/harness/fake-typescript-lsp.mjs'),
    goplsPath: path.join(root, 'e2e/harness/fake-gopls.mjs'),
	swiftLSPPath: path.join(root, 'e2e/harness/fake-sourcekit-lsp.mjs'),
	pythonLSPPath: path.join(root, 'e2e/harness/fake-pyright-langserver.mjs'),
	rustLSPPath: path.join(root, 'e2e/harness/fake-rust-analyzer.mjs'),
  });
  cleanup.push(backend);
  await fs.writeFile(path.join(workspace.path, largePath), largeContent);

  const browser = await chromium.launch({ channel: 'chrome', headless: true });
  cleanup.push({ close: () => browser.close() });
  const primary = await browser.newContext({ locale: 'en-US' });
  cleanup.push({ close: () => primary.close() });
  const page = await primary.newPage();
  const browserErrors = [];
  const networkOrigins = new Set();
  page.on('console', (message) => {
    if (message.type() === 'error' && !message.text().startsWith('Failed to load resource:')) {
      browserErrors.push(`[console] ${message.text()} @ ${JSON.stringify(message.location())}`);
    }
  });
  page.on('pageerror', (error) => browserErrors.push(`[pageerror] ${error.stack || error.message}`));
  page.on('request', (request) => networkOrigins.add(new URL(request.url()).origin));
  page.on('dialog', (dialog) => dialog.accept());

  await page.goto(backend.baseURL, { waitUntil: 'domcontentloaded' });
  await waitForEditor(page);
  assert.equal(await page.locator('textarea.adapter-textarea').count(), 0);

  // A clean reload used to race the old page's keepalive DELETE against the
  // new page's reclaim GET, intermittently surfacing DOCUMENT_ALREADY_OPEN.
  // Reclaim must rotate ownership atomically, and an explicit close must make
  // the same path immediately openable again.
  const cleanReloadPath = 'editor/main.go';
  await openPath(page, cleanReloadPath);
  const leaseBeforeCleanReload = await storedLeaseForPath(page, cleanReloadPath);
  assert.ok(leaseBeforeCleanReload?.leaseId);
  await page.reload({ waitUntil: 'domcontentloaded' });
  await waitForEditor(page);
  await openPath(page, cleanReloadPath);
  const leaseAfterCleanReload = await storedLeaseForPath(page, cleanReloadPath);
  assert.ok(leaseAfterCleanReload?.leaseId);
  assert.notEqual(leaseAfterCleanReload.leaseId, leaseBeforeCleanReload.leaseId, 'reload transfers lease ownership');
  assert.equal(await page.locator('.toast.error').filter({ hasText: /already open for editing/i }).count(), 0);
  await page.locator(`.editor-tab[data-tab-path="${cleanReloadPath}"] .tab-close`).click();
  await page.locator(`.editor-tab[data-tab-path="${cleanReloadPath}"]`).waitFor({ state: 'detached' });
  await openPath(page, cleanReloadPath);

  // The actionable card must not move when the slow Hover replaces its compact
  // loading state. Crossing blank editor space on the way to See more must keep
  // the resolved target, and dismissing the tooltip must not abort See More.
  await provider.withScenario('chat-slow', async () => {
    const mainValueToken = page.locator('.monaco-editor .semantic-token.semantic-type-function')
      .filter({ hasText: 'MainValue' });
    await mainValueToken.waitFor({ state: 'visible' });
    await mainValueToken.click();
    await page.evaluate(() => {
      const target = document.querySelector('#editor-mount');
      target.dispatchEvent(new KeyboardEvent('keydown', { key: 'k', ctrlKey: true, bubbles: true }));
      target.dispatchEvent(new KeyboardEvent('keydown', { key: 'i', ctrlKey: true, bubbles: true }));
    });

    const hoverCard = page.locator('#hover-card');
    await hoverCard.waitFor({ state: 'visible' });
    const loadingBox = await hoverCard.boundingBox();
    assert.ok(loadingBox);
    await page.locator('#hover-status').filter({ hasText: 'ready' }).waitFor({ timeout: 10_000 });
    const readyBox = await hoverCard.boundingBox();
    assert.ok(readyBox);
    assert.ok(Math.abs(readyBox.x - loadingBox.x) <= 1, `Hover must keep its horizontal anchor: ${JSON.stringify({ loadingBox, readyBox })}`);
    assert.ok(Math.abs(readyBox.y - loadingBox.y) <= 1, `Hover must not jump when content expands: ${JSON.stringify({ loadingBox, readyBox })}`);

    const editorBox = await page.locator('#editor-shell').boundingBox();
    assert.ok(editorBox);
    await page.mouse.move(editorBox.x + 8, editorBox.y + editorBox.height - 8);
    await page.waitForTimeout(120);
    await page.locator('#see-more-button').click();
    await page.locator('#panel-explain.active').waitFor();
    await eventually(async () => {
      const content = await page.locator('#explain-content').textContent();
      return /See More/u.test(content || '') && !/Generating See More/u.test(content || '');
    }, 'expanded explanation after hover dismissal');
  });

	const editedPaths = new Set([
		...[...fixtures.keys()].slice(0, 5),
		'editor/order.swift', 'editor/service.py', 'editor/repository.rs',
	]);
  for (const relativePath of editedPaths) {
    await openPath(page, relativePath);
    await assertHighlighted(page, relativePath);
	if (/\.(?:go|js|jsx|ts|tsx|swift|py|rs)$/u.test(relativePath)) await assertSemanticTokens(page, relativePath);
    if (relativePath === 'editor/main.go') await assertLSPDiagnostics(page, relativePath);
    if (relativePath === 'editor/app.ts') await assertLSPDiagnostics(page, relativePath);
	if (/\.(?:swift|py|rs)$/u.test(relativePath)) await assertLSPDiagnostics(page, relativePath);
    const marker = `E2E_${path.extname(relativePath).slice(1).toUpperCase()}`;
	await appendAndSave(page, marker, relativePath.endsWith('.py') ? '#' : '//');
    await eventually(async () => {
      const response = await backend.json(`/api/file?path=${encodeURIComponent(relativePath)}`);
      return response.status === 200 && response.body.content.includes(marker);
    }, `save ${relativePath}`);
  }

	for (const relativePath of [...fixtures.keys()].filter((candidate) => !editedPaths.has(candidate))) {
    await openPath(page, relativePath);
    await assertHighlighted(page, relativePath);
  }
	const boundedPaths = [...fixtures.keys()].slice(-10);
	assert.equal(await page.locator('.editor-tab').count(), boundedPaths.length, 'Monaco models/tabs remain bounded');

  const switchTimes = [];
  for (let index = 0; index < 20; index += 1) {
	const relativePath = boundedPaths[index % boundedPaths.length];
    switchTimes.push(await page.evaluate((wantedPath) => new Promise((resolve, reject) => {
      const selector = `.editor-tab[data-tab-path="${CSS.escape(wantedPath)}"]`;
      const tab = document.querySelector(selector);
      if (!tab) {
        reject(new Error(`missing tab ${wantedPath}`));
        return;
      }
      const started = performance.now();
      tab.click();
      const observe = () => {
        if (document.querySelector(selector)?.classList.contains('active')) {
          resolve(performance.now() - started);
          return;
        }
        if (performance.now() - started > 1000) {
          reject(new Error(`tab ${wantedPath} did not activate`));
          return;
        }
        requestAnimationFrame(observe);
      };
      requestAnimationFrame(observe);
    }), relativePath));
  }
  switchTimes.sort((a, b) => a - b);
  const p95 = switchTimes[Math.ceil(switchTimes.length * 0.95) - 1];
	assert.ok(p95 <= 100, `${boundedPaths.length}-tab switch p95 ${p95.toFixed(1)}ms exceeds 100ms`);

  await openPath(page, largePath);
  await page.locator('.monaco-editor .view-lines').waitFor();
  assert.match(await page.locator('#file-status').textContent(), /large\.js/);

  const reclaimPath = 'editor/app.ts';
  await openPath(page, reclaimPath);
  await appendText(page, 'DIRTY_RECLAIM');
  await page.waitForTimeout(700);
  const storedLease = await storedLeaseForPath(page, reclaimPath);
  assert.ok(storedLease?.documentId && storedLease?.leaseId, 'same session persists its lease identity');

  await page.reload({ waitUntil: 'domcontentloaded' });
  await waitForEditor(page);
  await openPath(page, reclaimPath);
  await eventually(async () => (await page.locator('.view-lines').textContent()).includes('DIRTY_RECLAIM'), 'dirty reload reclaim');
  assert.equal(await page.locator(`.editor-tab[data-tab-path="${reclaimPath}"] .dirty-dot`).count(), 1);

  const competing = await browser.newContext({ locale: 'en-US' });
  cleanup.push({ close: () => competing.close() });
  const competitor = await competing.newPage();
  await competitor.goto(backend.baseURL, { waitUntil: 'domcontentloaded' });
  await waitForEditor(competitor);
  await openPath(competitor, reclaimPath, { expectOpen: false });
  await competitor.locator('.toast.error').filter({ hasText: /already open for editing/i }).first().waitFor();
  assert.equal(await competitor.locator(`.editor-tab[data-tab-path="${reclaimPath}"]`).count(), 0);

  await appendAndSave(page, 'RECLAIM_SAVED');
  await eventually(async () => {
    const response = await backend.json(`/api/file?path=${encodeURIComponent(reclaimPath)}`);
    return response.status === 200 && response.body.content.includes('RECLAIM_SAVED');
  }, 'save reclaimed document');
  await page.locator(`.editor-tab[data-tab-path="${reclaimPath}"] .tab-close`).click();
  await page.locator(`.editor-tab[data-tab-path="${reclaimPath}"]`).waitFor({ state: 'detached' });
  await openPath(competitor, reclaimPath);

  // pagehide writes a synchronous same-origin handoff before its best-effort
  // DELETE. Simulate a dropped DELETE, then prove a fresh page can rotate the
  // lease without allowing an unrelated active tab to steal it.
  const handoffContext = await browser.newContext({ locale: 'en-US' });
  cleanup.push({ close: () => handoffContext.close() });
  const handoffOwner = await handoffContext.newPage();
  await handoffOwner.goto(backend.baseURL, { waitUntil: 'domcontentloaded' });
  await waitForEditor(handoffOwner);
  await openPath(handoffOwner, handoffPath);
  const leaseBeforeHandoff = await storedLeaseForPath(handoffOwner, handoffPath);
  assert.ok(leaseBeforeHandoff?.leaseId);
  await handoffOwner.route('**/api/documents/**', async (route) => {
    if (route.request().method() === 'DELETE') await route.abort();
    else await route.continue();
  });
  await handoffOwner.evaluate(() => {
    window.dispatchEvent(new PageTransitionEvent('pagehide', { persisted: false }));
  });
  await handoffOwner.close();

  const handoffReceiver = await handoffContext.newPage();
  await handoffReceiver.goto(backend.baseURL, { waitUntil: 'domcontentloaded' });
  await waitForEditor(handoffReceiver);
  await openPath(handoffReceiver, handoffPath);
  const leaseAfterHandoff = await storedLeaseForPath(handoffReceiver, handoffPath);
  assert.ok(leaseAfterHandoff?.leaseId);
  assert.notEqual(leaseAfterHandoff.leaseId, leaseBeforeHandoff.leaseId);

  const stats = await backend.json('/api/stats');
  assert.equal(stats.body.frontend.editor, 'monaco');
  assert.equal(stats.body.frontend.editorVersion, '0.53.0');
  assert.ok(stats.body.frontend.assets.workers.length >= 2);
  assert.deepEqual([...networkOrigins], [new URL(backend.baseURL).origin], 'browser runtime stays same-origin');
  assert.ok([...networkOrigins].every((origin) => !origin.includes('100.98.1.45')));
  assert.deepEqual(browserErrors, [], `browser errors:\n${browserErrors.join('\n')}`);
});

function storedLeaseForPath(page, wantedPath) {
  return page.evaluate((pathValue) => {
    const leases = JSON.parse(sessionStorage.getItem('codeatlas.open-document-leases.v1') || '[]');
    return leases.find((lease) => lease.path === pathValue) || null;
  }, wantedPath);
}

async function waitForEditor(page) {
  await page.locator('#bootstrap-overlay.hidden').waitFor({ state: 'attached', timeout: 30_000 });
  // The editor host is mounted at READY, while its shell remains intentionally
  // hidden until the first explicit document is installed.
  await page.locator('.adapter-monaco-host .monaco-editor').waitFor({ state: 'attached', timeout: 30_000 });
}

async function openPath(page, relativePath, { expectOpen = true } = {}) {
  const item = page.locator(`[role="treeitem"][data-kind="file"][data-path="${relativePath}"]`);
  await item.waitFor({ state: 'attached' });
  await item.evaluate((element) => {
    let details = element.closest('details');
    while (details) {
      details.open = true;
      details = details.parentElement?.closest('details') || null;
    }
    element.click();
  });
  if (expectOpen) {
    await page.locator(`.editor-tab[data-tab-path="${relativePath}"].active`).waitFor({ timeout: 10_000 });
    await page.locator('.monaco-editor .view-lines').waitFor();
  }
}

async function assertHighlighted(page, relativePath) {
  let observed = [];
  try {
    await eventually(async () => {
      observed = await page.locator('.monaco-editor .view-line span[class*="mtk"]').evaluateAll(
      (items) => [...new Set(items.map((item) => item.className).filter(Boolean))],
      );
      return observed.length >= 2;
    }, `syntax highlighting ${relativePath}`);
  } catch (error) {
    throw new Error(`${error.message}; observed token classes: ${JSON.stringify(observed)}`);
  }
}

async function assertSemanticTokens(page, relativePath) {
  await eventually(async () => {
    const status = await page.locator('#semantic-status').textContent();
    const rendered = await page.locator('.monaco-editor .semantic-token').count();
    return /^semantic: [1-9]\d*$/u.test(status || '') && rendered > 0;
  }, `semantic tokens ${relativePath}`);
}

async function assertLSPDiagnostics(page, relativePath) {
  await eventually(async () => {
    const status = await page.locator('#diagnostics-status').textContent();
    const rendered = await page.locator('.monaco-editor .diagnostic-marker').count();
    return status === 'diagnostics: 1/0' && rendered > 0;
  }, `versioned LSP diagnostics ${relativePath}`);
}

async function appendText(page, marker, comment = '//') {
  const editor = page.locator('.monaco-editor .view-lines');
  await editor.click();
  await page.keyboard.press(`${modifier}+End`);
	await page.keyboard.type(`\n${comment} ${marker}`);
}

async function appendAndSave(page, marker, comment = '//') {
	await appendText(page, marker, comment);
  await page.keyboard.press(`${modifier}+s`);
}

async function eventually(check, label, timeoutMs = 10_000) {
  const deadline = performance.now() + timeoutMs;
  let lastError = null;
  while (performance.now() < deadline) {
    try {
      if (await check()) return;
    } catch (error) {
      lastError = error;
    }
    await new Promise((resolve) => setTimeout(resolve, 75));
  }
  throw new Error(`timed out waiting for ${label}${lastError ? `: ${lastError.message}` : ''}`);
}
