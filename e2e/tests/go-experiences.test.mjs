import { after, test } from 'node:test';
import assert from 'node:assert/strict';
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

test('multi-file Go overlays feed exact gopls semantics and all grounded experiences', { timeout: 180_000 }, async () => {
  const provider = await startFakeProvider({ scenario: 'happy-path' });
  cleanup.push(provider);
  const workspace = await copyFixtureWorkspace({
    root,
    fixture: 'examples/tinycommerce',
    prefix: 'codeatlas-go-experiences-',
  });
  cleanup.push(workspace);

  const backend = await startBackend({
    root,
    workspaceDir: workspace.path,
    providerBaseURL: provider.baseURL,
    pollInterval: '1h',
    goplsPath: path.join(root, 'e2e/harness/fake-gopls.mjs'),
  });
  cleanup.push(backend);

  const capabilities = await backend.json('/api/capabilities');
  assert.equal(capabilities.status, 200);
  const gopls = capabilities.body.capabilities.find((item) => item.id === 'gopls');
  assert.equal(gopls?.state, 'available', JSON.stringify(capabilities.body));
  assert.equal(gopls?.metadata?.languageFamily, 'go');
  assert.equal(gopls?.metadata?.extensions, '.go');
  assert.equal(gopls?.metadata?.semanticTokensFull, 'true');
  assert.equal(gopls?.metadata?.documentSync, 'full');
  assert.doesNotMatch(JSON.stringify(gopls), /fake-gopls\.mjs|\/Volumes\//u);

  const opened = await backend.json('/api/documents/open', {
    method: 'POST',
    body: { path: 'internal/order/service.go' },
  });
  assert.equal(opened.status, 201, JSON.stringify(opened.body));
  const overlayContent = `${opened.body.content}\n// GO_ERROR\nvar overlayValue = 1\n`;
  const changed = await backend.json(`/api/documents/${encodeURIComponent(opened.body.documentId)}/content`, {
    method: 'PUT',
    body: {
      leaseId: opened.body.leaseId,
      expectedVersion: opened.body.version,
      newVersion: opened.body.version + 1,
      content: overlayContent,
    },
  });
  assert.equal(changed.status, 200, JSON.stringify(changed.body));
  assert.equal(changed.body.version, 2);

  const tokens = await backend.json(`/api/documents/${encodeURIComponent(opened.body.documentId)}/semantic-tokens?version=2`);
  assert.equal(tokens.status, 200, JSON.stringify(tokens.body));
  assert.equal(tokens.body.legendVersion, 'codeatlas-semantic-tokens/v1');
  assert.equal(tokens.body.documentVersion, 2);
  assert.equal(tokens.body.contentHash, changed.body.contentHash);
  assert.equal(tokens.body.semanticCoverage.providerState, 'available');
  assert.match(tokens.body.providerSession, /^gopls:/u);
  assert.ok(tokens.body.tokens.some((token) => token.tokenType === 'type'));
  assert.ok(tokens.body.tokens.some((token) => token.tokenType === 'method'));
  assert.ok(tokens.body.tokens.some((token) => token.tokenType === 'variable'));

  const diagnostics = await eventuallyJSON(backend, `/api/documents/${encodeURIComponent(opened.body.documentId)}/diagnostics?version=2`, (response) => (
    response.status === 200 && response.body.diagnostics.some((item) => item.code === 'FAKE_GO_ERROR')
  ));
  assert.equal(diagnostics.body.contentHash, changed.body.contentHash);
  assert.equal(diagnostics.body.providerSession, tokens.body.providerSession);

  const position = { line: 18, column: 20, encoding: 'utf-16' };
  const target = {
    path: 'internal/order/service.go',
    documentId: opened.body.documentId,
    documentVersion: 2,
    position,
  };
  const navigation = await backend.json('/api/navigation/query', {
    method: 'POST',
    body: { kind: 'definition', ...target },
  });
  assert.equal(navigation.status, 200, JSON.stringify(navigation.body));
  assert.equal(navigation.body.documentVersion, 2);
  assert.ok(navigation.body.targets.some((item) => item.label === 'Submit'));

  const hover = await backend.json('/api/explain', {
    method: 'POST',
    body: { feature: 'hover', ...target },
    timeoutMs: 30_000,
  });
  assert.equal(hover.status, 200, JSON.stringify(hover.body));
  assert.equal(hover.body.feature, 'hover');
  assert.equal(hover.body.documentVersion, 2);
  assert.equal(hover.body.symbol.name, 'Submit');
  assert.ok(hover.body.evidence.some((item) => item.path === target.path));

  const seeMore = await backend.json('/api/explain', {
    method: 'POST',
    body: { feature: 'see_more', ...target },
    timeoutMs: 30_000,
  });
  assert.equal(seeMore.status, 200, JSON.stringify(seeMore.body));
  assert.equal(seeMore.body.feature, 'see_more');
  assert.equal(seeMore.body.documentVersion, 2);
  assert.ok(seeMore.body.evidence.some((item) => item.path === target.path));

  const mainOpened = await backend.json('/api/documents/open', {
    method: 'POST',
    body: { path: 'cmd/api/main.go' },
  });
  assert.equal(mainOpened.status, 201, JSON.stringify(mainOpened.body));
  const mainTarget = {
    path: 'cmd/api/main.go',
    documentId: mainOpened.body.documentId,
    documentVersion: mainOpened.body.version,
  };
  const mainTokens = [
    { name: 'main', kind: 'function', path: 'cmd/api/main.go', line: 10, column: 7, definitionLine: 10, definitionColumn: 6, provenance: 'gopls' },
    { name: 'order', kind: 'package', path: 'cmd/api/main.go', line: 11, column: 18, definitionLine: 7, definitionColumn: 2, provenance: 'tree-sitter' },
    { name: 'NewMemoryRepository', kind: 'function', path: 'internal/order/repository.go', line: 11, column: 27, definitionLine: 19, definitionColumn: 6, provenance: 'gopls' },
    { name: 'repository', kind: 'variable', path: 'cmd/api/main.go', line: 11, column: 5, definitionLine: 11, definitionColumn: 2, provenance: 'gopls' },
    { name: 'NewService', kind: 'function', path: 'internal/order/service.go', line: 12, column: 23, definitionLine: 14, definitionColumn: 6, provenance: 'gopls' },
    { name: 'service', kind: 'variable', path: 'cmd/api/main.go', line: 12, column: 5, definitionLine: 12, definitionColumn: 2, provenance: 'gopls' },
    { name: 'NewHandler', kind: 'function', path: 'internal/order/http.go', line: 13, column: 23, definitionLine: 12, definitionColumn: 6, provenance: 'gopls' },
    { name: 'handler', kind: 'variable', path: 'cmd/api/main.go', line: 13, column: 5, definitionLine: 13, definitionColumn: 2, provenance: 'gopls' },
  ];
  for (const token of mainTokens) {
    const response = await backend.json('/api/explain', {
      method: 'POST',
      body: {
        feature: 'hover',
        ...mainTarget,
        position: { line: token.line, column: token.column, encoding: 'utf-16' },
      },
      timeoutMs: 30_000,
    });
    assert.equal(response.status, 200, `${token.name}: ${JSON.stringify(response.body)}`);
    assert.equal(response.body.symbol.name, token.name, JSON.stringify(response.body.symbol));
    assert.equal(response.body.symbol.kind, token.kind, JSON.stringify(response.body.symbol));
    assert.equal(response.body.symbol.path, token.path, JSON.stringify(response.body.symbol));

    const definition = await backend.json('/api/navigation/query', {
      method: 'POST',
      body: {
        kind: 'definition',
        ...mainTarget,
        position: { line: token.line, column: token.column, encoding: 'utf-16' },
      },
    });
    assert.equal(definition.status, 200, `${token.name}: ${JSON.stringify(definition.body)}`);
    assert.equal(definition.body.targets.length, 1, `${token.name}: ${JSON.stringify(definition.body.targets)}`);
    const definitionTarget = definition.body.targets[0];
    assert.equal(definitionTarget.label, token.name, JSON.stringify(definitionTarget));
    assert.equal(definitionTarget.path, token.path, JSON.stringify(definitionTarget));
    assert.equal(definitionTarget.range.start.line, token.definitionLine, JSON.stringify(definitionTarget));
    assert.equal(definitionTarget.range.start.column, token.definitionColumn, JSON.stringify(definitionTarget));
    assert.ok(definitionTarget.provenance.some((item) => item.source === token.provenance), JSON.stringify(definitionTarget));
  }
  const mainClosed = await backend.json(`/api/documents/${encodeURIComponent(mainOpened.body.documentId)}?discard=true`, {
    method: 'DELETE',
    headers: { 'X-Document-Lease': mainOpened.body.leaseId },
  });
  assert.equal(mainClosed.status, 204, JSON.stringify(mainClosed.body));

  const browser = await chromium.launch({ channel: 'chrome', headless: true });
  cleanup.push({ close: () => browser.close() });
  const browserContext = await browser.newContext({ locale: 'en-US' });
  cleanup.push({ close: () => browserContext.close() });
  const page = await browserContext.newPage();
  await page.goto(backend.baseURL, { waitUntil: 'domcontentloaded' });
  await waitForEditor(page);
  await openPath(page, 'cmd/api/main.go');
  for (const token of mainTokens) {
    await assertHoverAt(page, token);
  }

  const codemapRef = await backend.json('/api/codemaps', {
    method: 'POST',
    body: { query: 'order Service Submit repository Save Go', maxNodes: 16 },
  });
  assert.equal(codemapRef.status, 202, JSON.stringify(codemapRef.body));
  await waitForJob(backend, codemapRef.body.job.id, 30_000);
  const codemap = await backend.json(`/api/jobs/${encodeURIComponent(codemapRef.body.job.id)}/result`, { timeoutMs: 30_000 });
  assert.equal(codemap.status, 200, JSON.stringify(codemap.body));
  assert.ok(codemap.body.nodes.some((node) => node.path === 'internal/order/service.go'));

  const wikiRef = await backend.json('/api/deepwiki/refresh', { method: 'POST', body: {} });
  assert.equal(wikiRef.status, 202, JSON.stringify(wikiRef.body));
  await waitForJob(backend, wikiRef.body.job.id, 90_000);
  const wiki = await backend.json('/api/deepwiki', { timeoutMs: 30_000 });
  assert.equal(wiki.status, 200, JSON.stringify(wiki.body));
  assert.equal(wiki.body.status, 'ready');
  assert.ok(wiki.body.pages.some((page) => page.scopePaths.some((scopePath) => scopePath.endsWith('.go'))));
});

async function eventuallyJSON(backend, urlPath, accept, timeoutMs = 10_000) {
  const deadline = Date.now() + timeoutMs;
  let latest;
  while (Date.now() < deadline) {
    latest = await backend.json(urlPath);
    if (accept(latest)) return latest;
    await new Promise((resolve) => setTimeout(resolve, 25));
  }
  assert.fail(`response did not satisfy predicate: ${JSON.stringify(latest?.body)}`);
}

async function waitForJob(backend, id, timeoutMs) {
  const deadline = Date.now() + timeoutMs;
  let latest;
  while (Date.now() < deadline) {
    const response = await backend.json(`/api/jobs/${encodeURIComponent(id)}`);
    assert.equal(response.status, 200);
    latest = response.body.job;
    if (latest.state === 'succeeded') return latest;
    if (['failed', 'stale', 'canceled'].includes(latest.state)) {
      assert.fail(`job ${id} reached ${latest.state}: ${JSON.stringify(latest.error)}`);
    }
    await new Promise((resolve) => setTimeout(resolve, 25));
  }
  assert.fail(`job ${id} did not succeed: ${JSON.stringify(latest)}`);
}

async function waitForEditor(page) {
  await page.locator('#bootstrap-overlay.hidden').waitFor({ state: 'attached', timeout: 30_000 });
  await page.locator('.adapter-monaco-host .monaco-editor').waitFor({ state: 'attached', timeout: 30_000 });
}

async function openPath(page, relativePath) {
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
  await page.locator(`.editor-tab[data-tab-path="${relativePath}"].active`).waitFor({ timeout: 10_000 });
  await page.locator('.monaco-editor .view-lines').waitFor();
}

async function assertHoverAt(page, token) {
  const close = page.locator('#hover-close-button');
  if (await close.isVisible()) await close.click();
  await page.locator('.monaco-editor .view-lines').click();
  await page.keyboard.press(`${modifier}+Home`);
  for (let line = 1; line < token.line; line += 1) await page.keyboard.press('ArrowDown');
  for (let column = 1; column < token.column; column += 1) await page.keyboard.press('ArrowRight');
  await page.waitForFunction(({ line, column }) => (
    document.querySelector('#cursor-status')?.textContent === `Ln ${line}, Col ${column}`
  ), { line: token.line, column: token.column });
  await page.evaluate(() => {
    const target = document.querySelector('#editor-mount');
    target.dispatchEvent(new KeyboardEvent('keydown', { key: 'k', ctrlKey: true, bubbles: true }));
    target.dispatchEvent(new KeyboardEvent('keydown', { key: 'i', ctrlKey: true, bubbles: true }));
  });
  await page.locator('#hover-status').filter({ hasText: 'ready' }).waitFor({ timeout: 10_000 });
  await page.locator('#hover-name').filter({ hasText: token.name }).waitFor({ timeout: 10_000 });
}
