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

async function waitForJob(backend, id, state = 'succeeded') {
  const deadline = Date.now() + 10_000;
  let latest;
  while (Date.now() < deadline) {
    const response = await backend.json(`/api/jobs/${encodeURIComponent(id)}`);
    assert.equal(response.status, 200);
    latest = response.body.job;
    if (latest.state === state) return latest;
    if (['failed', 'stale', 'canceled'].includes(latest.state)) {
      assert.fail(`job ${id} reached ${latest.state}: ${JSON.stringify(latest.error)}`);
    }
    await new Promise((resolve) => setTimeout(resolve, 20));
  }
  assert.fail(`job ${id} did not reach ${state}: ${JSON.stringify(latest)}`);
}

async function submitJob(backend, type) {
  const response = await backend.json('/api/jobs', { method: 'POST', body: { type, input: {} } });
  assert.equal(response.status, 202);
  return waitForJob(backend, response.body.job.id);
}

test('structured provider compatibility and index maintenance are exercised through the real backend', async () => {
  const provider = await startFakeProvider({
    scenario: 'schema-keyword-compatibility',
    apiPrefix: '/v1',
    authLabels: {
      'Bearer chat-e2e-key': 'chat',
      'Bearer embeddings-e2e-key': 'embeddings',
    },
  });
  cleanup.push(provider);

  const workspace = await copyFixtureWorkspace({
    root,
    fixture: 'examples/tinycommerce',
    prefix: 'codeatlas-provider-reindex-',
  });
  cleanup.push(workspace);

  const backend = await startBackend({
    root,
    workspaceDir: workspace.path,
    providerBaseURL: provider.baseURL,
    embeddingBaseURL: provider.baseURL,
    llmAPIKey: 'chat-e2e-key',
    embeddingsAPIKey: 'embeddings-e2e-key',
    enableEmbeddings: true,
    embeddingModel: 'fake-embedding',
    pollInterval: '1h',
    scenario: 'schema-keyword-compatibility',
  });
  cleanup.push(backend);

  assert.ok(provider.requests.some((request) => request.path === '/v1/chat/completions' && request.authorizationLabel === 'chat'));
  assert.ok(provider.requests.some((request) => request.path === '/v1/embeddings' && request.authorizationLabel === 'embeddings'));

  const explanation = await backend.json('/api/explain', {
    method: 'POST',
    body: {
      feature: 'hover',
      path: 'cmd/api/main.go',
      position: { line: 13, column: 20, encoding: 'utf-16' },
    },
    timeoutMs: 30_000,
  });
  assert.equal(explanation.status, 200, JSON.stringify(explanation.body));
  assert.equal(explanation.body.result.schemaVersion, 'explanation/v2');
  assert.ok(explanation.body.result.summary);

  const beforeNoopEmbeddings = provider.requests.filter((request) => request.path === '/v1/embeddings').length;
  const noopResponse = await backend.json('/api/reindex', { method: 'POST', body: {} });
  assert.equal(noopResponse.status, 202);
  const noop = await waitForJob(backend, noopResponse.body.job.id);
  assert.equal(noop.stage, 'no_changes');
  assert.equal(noop.message, 'index was already up to date');
  assert.equal(noop.inputSnapshotId, noop.resultSnapshotId);
  const noopResult = await backend.json(`/api/jobs/${encodeURIComponent(noop.id)}/result`);
  assert.deepEqual({
    committed: noopResult.body.committed,
    filesChanged: noopResult.body.filesChanged,
    filesRemoved: noopResult.body.filesRemoved,
  }, { committed: false, filesChanged: 0, filesRemoved: 0 });
  assert.equal(provider.requests.filter((request) => request.path === '/v1/embeddings').length, beforeNoopEmbeddings);

  await fs.appendFile(path.join(workspace.path, 'internal/order/service.go'), '\n// E2E repository change\n', 'utf8');
  const changedResponse = await backend.json('/api/reindex', { method: 'POST', body: {} });
  assert.equal(changedResponse.status, 202);
  const changed = await waitForJob(backend, changedResponse.body.job.id);
  assert.equal(changed.stage, 'publish');
  assert.notEqual(changed.inputSnapshotId, changed.resultSnapshotId);
  const changedResult = await backend.json(`/api/jobs/${encodeURIComponent(changed.id)}/result`);
  assert.equal(changedResult.body.committed, true);
  assert.ok(provider.requests.filter((request) => request.path === '/v1/embeddings').length > beforeNoopEmbeddings);

  const beforeRebuild = provider.requests.filter((request) => request.path === '/v1/embeddings').length;
  const embeddings = await submitJob(backend, 'embeddings.rebuild');
  assert.equal(embeddings.stage, 'rebuilt');
  assert.equal(embeddings.message, 'embeddings index rebuilt');
  assert.equal(embeddings.inputSnapshotId, embeddings.resultSnapshotId);
  assert.ok(provider.requests.filter((request) => request.path === '/v1/embeddings').length > beforeRebuild);

  const fts = await submitJob(backend, 'fts.rebuild');
  assert.equal(fts.stage, 'rebuilt');
  assert.equal(fts.message, 'FTS index rebuilt');
  assert.equal(fts.inputSnapshotId, fts.resultSnapshotId);
});
