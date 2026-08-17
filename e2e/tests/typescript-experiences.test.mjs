import { after, test } from 'node:test';
import assert from 'node:assert/strict';
import fs from 'node:fs/promises';
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

test('mixed JS/TS overlays feed exact semantics, navigation and all grounded experiences', { timeout: 180_000 }, async () => {
  const provider = await startFakeProvider({ scenario: 'happy-path' });
  cleanup.push(provider);
  const workspace = await copyFixtureWorkspace({
    root,
    fixture: 'examples/tinycommerce',
    prefix: 'codeatlas-typescript-experiences-',
  });
  cleanup.push(workspace);

  await fs.writeFile(path.join(workspace.path, 'web/src/price.js'), 'export const formatPrice = (value) => `$${value}`\n');
  await fs.writeFile(path.join(workspace.path, 'web/src/banner.jsx'), 'export const Banner = ({ title }) => <h1>{title}</h1>\n');
  await fs.writeFile(path.join(workspace.path, 'web/src/view.tsx'), 'export const CheckoutView = (): JSX.Element => <section>Checkout</section>\n');
  await fs.writeFile(path.join(workspace.path, 'web/src/public-api.mts'), 'export { CheckoutController } from "./checkout"\n');

  const backend = await startBackend({
    root,
    workspaceDir: workspace.path,
    providerBaseURL: provider.baseURL,
    pollInterval: '1h',
    typescriptLSPPath: path.join(root, 'e2e/harness/fake-typescript-lsp.mjs'),
  });
  cleanup.push(backend);

  const capabilities = await backend.json('/api/capabilities');
  assert.equal(capabilities.status, 200);
  const typescript = capabilities.body.capabilities.find((item) => item.id === 'typescript-lsp');
  assert.equal(typescript?.state, 'available', JSON.stringify(capabilities.body));
  assert.equal(typescript?.metadata?.languageFamily, 'javascript-typescript');
  assert.equal(typescript?.metadata?.semanticTokensFull, 'true');
  assert.doesNotMatch(JSON.stringify(typescript), /fake-typescript-lsp\.mjs|\/Volumes\//u);

  const opened = await backend.json('/api/documents/open', {
    method: 'POST',
    body: { path: 'web/src/checkout.ts' },
  });
  assert.equal(opened.status, 201, JSON.stringify(opened.body));
  const overlayContent = `${opened.body.content}\n// TYPE_ERROR\nexport const overlayValue = 1\n`;
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
  assert.match(tokens.body.providerSession, /^typescript-lsp:/u);
  assert.ok(tokens.body.tokens.some((token) => token.tokenType === 'class'));
  assert.ok(tokens.body.tokens.some((token) => token.tokenType === 'variable'));

  const diagnostics = await eventuallyJSON(backend, `/api/documents/${encodeURIComponent(opened.body.documentId)}/diagnostics?version=2`, (response) => (
    response.status === 200 && response.body.diagnostics.some((item) => item.code === 'FAKE_TS_ERROR')
  ));
  assert.equal(diagnostics.body.contentHash, changed.body.contentHash);
  assert.equal(diagnostics.body.providerSession, tokens.body.providerSession);

  const position = { line: 3, column: 14, encoding: 'utf-16' };
  const target = {
    path: 'web/src/checkout.ts',
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
  assert.ok(navigation.body.targets.some((item) => item.label === 'CheckoutController'));

  const hover = await backend.json('/api/explain', {
    method: 'POST',
    body: { feature: 'hover', ...target },
    timeoutMs: 30_000,
  });
  assert.equal(hover.status, 200, JSON.stringify(hover.body));
  assert.equal(hover.body.feature, 'hover');
  assert.equal(hover.body.documentVersion, 2);
  assert.equal(hover.body.symbol.name, 'CheckoutController');
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
    body: { query: 'CheckoutController completeCheckout TypeScript frontend', maxNodes: 16 },
  });
  assert.equal(codemapRef.status, 202, JSON.stringify(codemapRef.body));
  await waitForJob(backend, codemapRef.body.job.id, 30_000);
  const codemap = await backend.json(`/api/jobs/${encodeURIComponent(codemapRef.body.job.id)}/result`, { timeoutMs: 30_000 });
  assert.equal(codemap.status, 200, JSON.stringify(codemap.body));
  assert.ok(codemap.body.nodes.some((node) => /\.(?:js|jsx|ts|tsx|mts)$/u.test(node.path)));

  const wikiRef = await backend.json('/api/deepwiki/refresh', { method: 'POST', body: {} });
  assert.equal(wikiRef.status, 202, JSON.stringify(wikiRef.body));
  await waitForJob(backend, wikiRef.body.job.id, 90_000);
  const wiki = await backend.json('/api/deepwiki', { timeoutMs: 30_000 });
  assert.equal(wiki.status, 200, JSON.stringify(wiki.body));
  assert.equal(wiki.body.status, 'ready');
  assert.ok(wiki.body.pages.some((page) => page.scopePaths.some((scopePath) => /web\/src\/.*\.(?:js|jsx|ts|tsx|mts)$/u.test(scopePath))));
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
