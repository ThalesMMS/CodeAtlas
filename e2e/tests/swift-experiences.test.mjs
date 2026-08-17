import { after, test } from 'node:test';
import assert from 'node:assert/strict';
import path from 'node:path';
import { fileURLToPath } from 'node:url';

import { startFakeProvider } from '../harness/fake-provider.mjs';
import { startBackend, stopAll } from '../harness/process-manager.mjs';
import { copyFixtureWorkspace } from '../harness/workspace-manager.mjs';

const root = path.resolve(path.dirname(fileURLToPath(import.meta.url)), '../..');
const cleanup = [];

after(async () => {
  await stopAll(cleanup);
});

test('multi-file Swift overlays feed exact SourceKit-LSP semantics and all grounded experiences', { timeout: 180_000 }, async () => {
  const provider = await startFakeProvider({ scenario: 'happy-path' });
  cleanup.push(provider);
  const workspace = await copyFixtureWorkspace({
    root,
    fixture: 'examples/swiftcommerce',
    prefix: 'codeatlas-swift-experiences-',
  });
  cleanup.push(workspace);

  const backend = await startBackend({
    root,
    workspaceDir: workspace.path,
    providerBaseURL: provider.baseURL,
    pollInterval: '1h',
    swiftLSPPath: path.join(root, 'e2e/harness/fake-sourcekit-lsp.mjs'),
  });
  cleanup.push(backend);

  const capabilities = await backend.json('/api/capabilities');
  assert.equal(capabilities.status, 200);
  const sourcekit = capabilities.body.capabilities.find((item) => item.id === 'sourcekit-lsp');
  assert.equal(sourcekit?.state, 'available', JSON.stringify(capabilities.body));
  assert.equal(sourcekit?.metadata?.languageFamily, 'swift');
  assert.equal(sourcekit?.metadata?.extensions, '.swift');
  assert.equal(sourcekit?.metadata?.semanticTokensFull, 'true');
  assert.equal(sourcekit?.metadata?.documentSync, 'full');
  assert.doesNotMatch(JSON.stringify(sourcekit), /fake-sourcekit-lsp\.mjs|\/Volumes\//u);

  const opened = await backend.json('/api/documents/open', {
    method: 'POST',
    body: { path: 'Sources/Commerce/MemoryOrderRepository.swift' },
  });
  assert.equal(opened.status, 201, JSON.stringify(opened.body));
  const overlayContent = `${opened.body.content}\n// SWIFT_ERROR\nlet overlayValue = 1\n`;
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
  assert.match(tokens.body.providerSession, /^sourcekit-lsp:/u);
  assert.ok(tokens.body.tokens.some((token) => token.tokenType === 'type'));
  assert.ok(tokens.body.tokens.some((token) => token.tokenType === 'method'));
  assert.ok(tokens.body.tokens.some((token) => token.tokenType === 'variable'));

  const diagnostics = await eventuallyJSON(backend, `/api/documents/${encodeURIComponent(opened.body.documentId)}/diagnostics?version=2`, (response) => (
    response.status === 200 && response.body.diagnostics.some((item) => item.code === 'FAKE_SWIFT_ERROR')
  ));
  assert.equal(diagnostics.body.contentHash, changed.body.contentHash);
  assert.equal(diagnostics.body.providerSession, tokens.body.providerSession);

  const position = { line: 10, column: 10, encoding: 'utf-16' };
  const target = {
    path: 'Sources/Commerce/MemoryOrderRepository.swift',
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
  assert.ok(navigation.body.targets.some((item) => item.label === 'save'));

  const hover = await backend.json('/api/explain', {
    method: 'POST',
    body: { feature: 'hover', ...target },
    timeoutMs: 30_000,
  });
  assert.equal(hover.status, 200, JSON.stringify(hover.body));
  assert.equal(hover.body.feature, 'hover');
  assert.equal(hover.body.documentVersion, 2);
  assert.equal(hover.body.symbol.name, 'save');
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

  const codemapRef = await backend.json('/api/codemaps', {
    method: 'POST',
    body: { query: 'MemoryOrderRepository save find OrderRepository Swift', maxNodes: 16 },
  });
  assert.equal(codemapRef.status, 202, JSON.stringify(codemapRef.body));
  await waitForJob(backend, codemapRef.body.job.id, 30_000);
  const codemap = await backend.json(`/api/jobs/${encodeURIComponent(codemapRef.body.job.id)}/result`, { timeoutMs: 30_000 });
  assert.equal(codemap.status, 200, JSON.stringify(codemap.body));
  assert.ok(codemap.body.nodes.some((node) => node.path === 'Sources/Commerce/MemoryOrderRepository.swift'));

  const wikiRef = await backend.json('/api/deepwiki/refresh', { method: 'POST', body: {} });
  assert.equal(wikiRef.status, 202, JSON.stringify(wikiRef.body));
  await waitForJob(backend, wikiRef.body.job.id, 90_000);
  const wiki = await backend.json('/api/deepwiki', { timeoutMs: 30_000 });
  assert.equal(wiki.status, 200, JSON.stringify(wiki.body));
  assert.equal(wiki.body.status, 'ready');
  assert.ok(wiki.body.pages.some((page) => page.scopePaths.some((scopePath) => scopePath.endsWith('.swift'))));
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
