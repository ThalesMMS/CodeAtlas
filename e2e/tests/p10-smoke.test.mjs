import { after, test } from 'node:test';
import assert from 'node:assert/strict';
import fs from 'node:fs/promises';
import path from 'node:path';
import { fileURLToPath } from 'node:url';

import { startFakeProvider } from '../harness/fake-provider.mjs';
import { startBackend, stopAll } from '../harness/process-manager.mjs';
import { copyFixtureWorkspace } from '../harness/workspace-manager.mjs';
import { writeRunReport } from '../harness/reporter.mjs';

const root = path.resolve(path.dirname(fileURLToPath(import.meta.url)), '../..');
const cleanup = [];

after(async () => {
  await stopAll(cleanup);
});

test('P10 smoke harness starts fake provider, temp workspace and real backend without external services', async () => {
  const provider = await startFakeProvider({ scenario: 'happy-path' });
  cleanup.push(provider);

  const workspace = await copyFixtureWorkspace({
    root,
    fixture: 'examples/tinycommerce',
    prefix: 'codeatlas-p10-smoke-',
  });
  cleanup.push(workspace);

  const backend = await startBackend({
    root,
    workspaceDir: workspace.path,
    providerBaseURL: provider.baseURL,
    scenario: 'happy-path',
  });
  cleanup.push(backend);

  const ready = await backend.json('/api/health/ready');
  assert.equal(ready.status, 200);
  assert.equal(ready.body.status, 'ready');

  const html = await backend.text('/');
  assert.equal(html.status, 200);
  assert.match(html.body, /class="skip-link" href="#editor-mount"/);
  assert.match(html.body, /class="skip-link secondary" href="#file-tree"/);
  assert.doesNotMatch(html.body, /CODEATLAS_E2E_CANARY|sk-live-test-key/);
  assert.match(html.headers.get('content-security-policy') ?? '', /default-src 'self'/);

  const tree = await backend.json('/api/tree');
  assert.equal(tree.status, 200);
  assert.ok(Array.isArray(tree.body.children), 'tree response should expose workspace children');

  // Codemap seeds are picked by the internal FTS5/BM25 lexical index, so a
  // succeeding job proves the retrieval path answers a real repository query.
  const codemapRef = await backend.json('/api/codemaps', {
    method: 'POST',
    body: { query: 'order Service Submit repository Save Go', maxNodes: 16 },
  });
  assert.equal(codemapRef.status, 202, JSON.stringify(codemapRef.body));
  await waitForJob(backend, codemapRef.body.job.id, 30_000);
  const codemap = await backend.json(`/api/jobs/${encodeURIComponent(codemapRef.body.job.id)}/result`, { timeoutMs: 30_000 });
  assert.equal(codemap.status, 200, JSON.stringify(codemap.body));
  assert.ok(
    codemap.body.nodes.some((node) => node.path === 'internal/order/service.go'),
    'lexical seeds should reach the queried Go symbols',
  );

  const opened = await backend.json('/api/documents/open', {
    method: 'POST',
    body: { path: 'web/src/checkout.ts' },
  });
  assert.equal(opened.status, 201);
  assert.ok(opened.body.documentId);
  assert.ok(opened.body.leaseId);

  const editedContent = `${opened.body.content}\n// P10 smoke save\n`;
  const replaced = await backend.json(`/api/documents/${opened.body.documentId}/content`, {
    method: 'PUT',
    body: {
      leaseId: opened.body.leaseId,
      expectedVersion: opened.body.version,
      newVersion: opened.body.version + 1,
      content: editedContent,
    },
  });
  assert.equal(replaced.status, 200);
  assert.equal(replaced.body.version, opened.body.version + 1);
  assert.equal(replaced.body.dirty, true);
  assert.equal(Object.hasOwn(replaced.body, 'leaseId'), false, 'lease is never echoed after open');

  const saved = await backend.json(`/api/documents/${opened.body.documentId}/save`, {
    method: 'POST',
    body: {
      leaseId: opened.body.leaseId,
      version: replaced.body.version,
    },
  });
  assert.equal(saved.status, 200);
  assert.equal(saved.body.dirty, false);

  const onDisk = await fs.readFile(path.join(workspace.path, 'web/src/checkout.ts'), 'utf8');
  assert.match(onDisk, /P10 smoke save/);
  assert.equal(provider.requests.some((request) => request.path.includes('/v1/')), false, 'fake provider contract is prefix-free');

  const unavailable = await provider.withScenario('chat-500', async () => provider.probeChat());
  assert.equal(unavailable.status, 500);

  const report = await writeRunReport({
    root,
    name: 'p10-smoke',
    scenarios: [
      { id: 'readiness-happy-path', status: 'passed', timings: backend.timings },
      { id: 'document-open-edit-save', status: 'passed' },
      { id: 'security-csp-no-network', status: 'passed' },
    ],
    environment: backend.environment,
  });
  assert.match(report.markdownPath, /e2e[\\/]reports[\\/]p10-smoke\.md$/);
});

test('P10 smoke harness exposes provider failure as recoverable configuration state', async () => {
  const provider = await startFakeProvider({ scenario: 'chat-500' });
  cleanup.push(provider);

  const workspace = await copyFixtureWorkspace({
    root,
    fixture: 'examples/tinycommerce',
    prefix: 'codeatlas-p10-failed-',
  });
  cleanup.push(workspace);

  const startedAt = performance.now();
  const backend = await startBackend({
    root,
    workspaceDir: workspace.path,
    providerBaseURL: provider.baseURL,
    scenario: 'chat-500',
    waitFor: 'awaiting-configuration',
    timeoutMs: 10_000,
  });
  cleanup.push(backend);
  const readiness = await backend.json('/api/health/ready');
  assert.equal(readiness.status, 503);
  assert.equal(readiness.body.state, 'AWAITING_CONFIGURATION');
  assert.ok(performance.now() - startedAt < 10_000, 'recoverable readiness should not wait for the full timeout');
});

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
