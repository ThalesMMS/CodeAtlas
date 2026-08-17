import { after, test } from 'node:test';
import assert from 'node:assert/strict';
import fs from 'node:fs/promises';
import path from 'node:path';
import { fileURLToPath } from 'node:url';

import { startFakeProvider } from '../harness/fake-provider.mjs';
import {
  createIsolatedUserProfile,
  startBackend,
  stopAll,
} from '../harness/process-manager.mjs';
import { copyFixtureWorkspace } from '../harness/workspace-manager.mjs';

const root = path.resolve(path.dirname(fileURLToPath(import.meta.url)), '../..');
const cleanup = [];

after(async () => {
  await stopAll(cleanup);
});

function field(snapshot, key) {
  for (const fields of Object.values(snapshot.groups || {})) {
    const found = fields.find((item) => item.key === key);
    if (found) return found;
  }
  assert.fail(`settings field not found: ${key}`);
}

async function eventually(predicate, label, timeoutMs = 10_000) {
  const deadline = Date.now() + timeoutMs;
  let last;
  while (Date.now() < deadline) {
    last = await predicate();
    if (last) return last;
    await new Promise((resolve) => setTimeout(resolve, 25));
  }
  assert.fail(`${label} did not become true: ${JSON.stringify(last)}`);
}

async function waitForJob(backend, id) {
  return eventually(async () => {
    const response = await backend.json(`/api/jobs/${encodeURIComponent(id)}`);
    assert.equal(response.status, 200, JSON.stringify(response.body));
    const job = response.body.job;
    if (job.state === 'succeeded') return job;
    if (['failed', 'stale', 'canceled'].includes(job.state)) assert.fail(JSON.stringify(job));
    return null;
  }, `embedding job ${id}`, 30_000);
}

test('settings repair first-run configuration without restart and keep sentinels out of outputs', { timeout: 120_000 }, async () => {
  const sentinel = 'settings-e2e-api-key-sentinel';
  const provider = await startFakeProvider({ scenario: 'happy-path' });
  cleanup.push(provider);
  const workspace = await copyFixtureWorkspace({ root, fixture: 'examples/tinycommerce', prefix: 'codeatlas-settings-first-run-' });
  cleanup.push(workspace);
  const profile = await createIsolatedUserProfile('codeatlas-settings-first-run-');
  cleanup.push(profile);
  const backend = await startBackend({
    root,
    workspaceDir: workspace.path,
    providerBaseURL: '',
    llmModel: '',
    llmAPIKey: sentinel,
    userProfile: profile,
    waitFor: 'awaiting-configuration',
  });
  cleanup.push(backend);

  const awaiting = await backend.json('/api/health/ready');
  assert.equal(awaiting.status, 503);
  assert.equal(awaiting.body.state, 'AWAITING_CONFIGURATION');
  const initial = await backend.settings('/api/settings');
  assert.equal(initial.status, 200, JSON.stringify(initial.body));
  assert.equal(initial.body.revision, 0);
  assert.equal(field(initial.body, 'CODEATLAS_LLM_API_KEY').configured, true);
  assert.equal(Object.hasOwn(field(initial.body, 'CODEATLAS_LLM_API_KEY'), 'value'), false);

  const applied = await backend.settings('/api/settings', {
    method: 'PUT',
    body: {
      revision: 0,
      overrides: {
        CODEATLAS_LLM_BASE_URL: { operation: 'replace', value: provider.baseURL },
        CODEATLAS_LLM_MODEL: { operation: 'replace', value: 'fake-codeatlas' },
      },
    },
  });
  assert.equal(applied.status, 200, JSON.stringify(applied.body));
  assert.equal(applied.body.snapshot.revision, 1);
  await backend.waitForState('READY', 60_000);

  const hostileHost = await backend.settings('/api/settings', { rawHost: 'example.test' });
  assert.equal(hostileHost.status, 403, JSON.stringify(hostileHost.body));
  const html = await backend.text('/');
  assert.equal(html.status, 200);
  assert.doesNotMatch(html.body, new RegExp(sentinel, 'u'));
  assert.doesNotMatch(JSON.stringify(applied.body), new RegExp(sentinel, 'u'));
  assert.doesNotMatch(backend.logs.join(''), new RegExp(sentinel, 'u'));
  assert.doesNotMatch(JSON.stringify(provider.requests), new RegExp(sentinel, 'u'));
});

test('settings hot-swap providers, rebuild embeddings, and preserve the old LSP on rejection', { timeout: 180_000 }, async () => {
  const providerA = await startFakeProvider({ scenario: 'happy-path' });
  const providerB = await startFakeProvider({ scenario: 'happy-path' });
  cleanup.push(providerA, providerB);
  const workspace = await copyFixtureWorkspace({ root, fixture: 'examples/tinycommerce', prefix: 'codeatlas-settings-runtime-' });
  cleanup.push(workspace);
  const backend = await startBackend({
    root,
    workspaceDir: workspace.path,
    providerBaseURL: providerA.baseURL,
    embeddingBaseURL: providerA.baseURL,
    enableEmbeddings: true,
    embeddingModel: 'fake-embedding-a',
    pollInterval: '1h',
    goplsPath: path.join(root, 'e2e/harness/fake-gopls.mjs'),
  });
  cleanup.push(backend);

  const initial = await backend.settings('/api/settings');
  const beforeA = providerA.requests.filter((request) => request.path.endsWith('/chat/completions')).length;
  const release = providerA.blockChats();
  const oldRequest = backend.json('/api/explain', {
    method: 'POST',
    body: { feature: 'hover', path: 'cmd/api/main.go', position: { line: 13, column: 20, encoding: 'utf-16' } },
    timeoutMs: 30_000,
  });
  await eventually(
    () => providerA.requests.filter((request) => request.path.endsWith('/chat/completions')).length > beforeA,
    'old provider request',
  );
  const switched = await backend.settings('/api/settings', {
    method: 'PUT',
    body: {
      revision: initial.body.revision,
      overrides: { CODEATLAS_LLM_BASE_URL: { operation: 'replace', value: providerB.baseURL } },
    },
  });
  assert.equal(switched.status, 200, JSON.stringify(switched.body));
  release();
  assert.equal((await oldRequest).status, 200);
  const beforeB = providerB.requests.filter((request) => request.path.endsWith('/chat/completions')).length;
  const nextRequest = await backend.json('/api/explain', {
    method: 'POST',
    body: { feature: 'hover', path: 'cmd/api/main.go', position: { line: 13, column: 20, encoding: 'utf-16' } },
    timeoutMs: 30_000,
  });
  assert.equal(nextRequest.status, 200, JSON.stringify(nextRequest.body));
  assert.ok(providerB.requests.filter((request) => request.path.endsWith('/chat/completions')).length > beforeB);

  providerB.setScenario('embeddings-slow');
  const embeddingUpdate = await backend.settings('/api/settings', {
    method: 'PUT',
    body: {
      revision: switched.body.snapshot.revision,
      overrides: {
        CODEATLAS_EMBEDDING_BASE_URL: { operation: 'replace', value: providerB.baseURL },
        CODEATLAS_EMBEDDING_MODEL: { operation: 'replace', value: 'fake-embedding-b' },
      },
    },
  });
  assert.equal(embeddingUpdate.status, 200, JSON.stringify(embeddingUpdate.body));
  assert.ok(embeddingUpdate.body.embeddingJobId);
  const lexicalDuringRebuild = await backend.json('/api/search?q=Submit', { timeoutMs: 10_000 });
  assert.equal(lexicalDuringRebuild.status, 200, JSON.stringify(lexicalDuringRebuild.body));
  assert.ok(Array.isArray(lexicalDuringRebuild.body));
  await waitForJob(backend, embeddingUpdate.body.embeddingJobId);
  assert.ok(providerB.requests.some((request) => request.path.endsWith('/embeddings')));
  providerB.setScenario('happy-path');

  const opened = await backend.json('/api/documents/open', { method: 'POST', body: { path: 'internal/order/service.go' } });
  assert.equal(opened.status, 201, JSON.stringify(opened.body));
  const tokensBefore = await backend.json(`/api/documents/${encodeURIComponent(opened.body.documentId)}/semantic-tokens?version=1`);
  assert.equal(tokensBefore.status, 200, JSON.stringify(tokensBefore.body));
  assert.match(tokensBefore.body.providerSession, /^gopls:/u);
  const brokenLSP = await backend.settings('/api/settings', {
    method: 'PUT',
    body: {
      revision: embeddingUpdate.body.snapshot.revision,
      overrides: { CODEATLAS_GOPLS_PATH: { operation: 'replace', value: path.join(workspace.path, 'missing-gopls') } },
    },
  });
  assert.equal(brokenLSP.status, 503, JSON.stringify(brokenLSP.body));
  assert.equal(brokenLSP.body.error.code, 'SETTINGS_PREPARE_FAILED');
  assert.equal(brokenLSP.body.error.details.snapshot.revision, embeddingUpdate.body.snapshot.revision);
  const tokensAfter = await backend.json(`/api/documents/${encodeURIComponent(opened.body.documentId)}/semantic-tokens?version=1`);
  assert.equal(tokensAfter.status, 200, JSON.stringify(tokensAfter.body));
  assert.equal(tokensAfter.body.providerSession, tokensBefore.body.providerSession);
});

test('settings persist per-user restart values over conflicting environment and reset cleanly', { timeout: 180_000 }, async () => {
  const provider = await startFakeProvider({ scenario: 'happy-path' });
  cleanup.push(provider);
  const workspace = await copyFixtureWorkspace({ root, fixture: 'examples/tinycommerce', prefix: 'codeatlas-settings-persist-' });
  cleanup.push(workspace);
  const profile = await createIsolatedUserProfile('codeatlas-settings-persist-');
  cleanup.push(profile);
  const conflicts = {
    CODEATLAS_WORKSPACE: 'environment-workspace',
    CODEATLAS_LISTEN: '127.0.0.1:45670',
    CODEATLAS_MAX_FILE_BYTES: '3333',
  };
  const first = await startBackend({
    root, workspaceDir: workspace.path, providerBaseURL: provider.baseURL,
    userProfile: profile, environment: conflicts,
  });
  cleanup.push(first);
  const initial = await first.settings('/api/settings');
  const saved = await first.settings('/api/settings', {
    method: 'PUT',
    body: {
      revision: initial.body.revision,
      overrides: {
        CODEATLAS_WORKSPACE: { operation: 'replace', value: 'saved-workspace' },
        CODEATLAS_LISTEN: { operation: 'replace', value: '127.0.0.1:45671' },
        CODEATLAS_MAX_FILE_BYTES: { operation: 'replace', value: 4444 },
      },
    },
  });
  assert.equal(saved.status, 200, JSON.stringify(saved.body));
  assert.deepEqual(new Set(saved.body.restartRequired), new Set([
    'CODEATLAS_WORKSPACE', 'CODEATLAS_LISTEN', 'CODEATLAS_MAX_FILE_BYTES',
  ]));
  await first.close();

  const second = await startBackend({
    root, workspaceDir: workspace.path, providerBaseURL: provider.baseURL,
    userProfile: profile, environment: conflicts,
  });
  cleanup.push(second);
  const restarted = await second.settings('/api/settings');
  assert.equal(field(restarted.body, 'CODEATLAS_WORKSPACE').source, 'settings');
  assert.equal(field(restarted.body, 'CODEATLAS_WORKSPACE').value, 'saved-workspace');
  assert.equal(field(restarted.body, 'CODEATLAS_WORKSPACE').runningValue, workspace.path);
  assert.equal(field(restarted.body, 'CODEATLAS_LISTEN').source, 'settings');
  assert.equal(field(restarted.body, 'CODEATLAS_LISTEN').value, '127.0.0.1:45671');
  assert.equal(field(restarted.body, 'CODEATLAS_LISTEN').runningValue, new URL(second.baseURL).host);
  assert.equal(field(restarted.body, 'CODEATLAS_MAX_FILE_BYTES').source, 'settings');
  assert.equal(field(restarted.body, 'CODEATLAS_MAX_FILE_BYTES').value, 4444);
  assert.equal(field(restarted.body, 'CODEATLAS_MAX_FILE_BYTES').runningValue, 4444);

  const reset = await second.settings('/api/settings/overrides', {
    method: 'DELETE', body: { revision: restarted.body.revision },
  });
  assert.equal(reset.status, 200, JSON.stringify(reset.body));
  assert.equal(field(reset.body.snapshot, 'CODEATLAS_WORKSPACE').source, 'env');
  assert.equal(field(reset.body.snapshot, 'CODEATLAS_WORKSPACE').value, conflicts.CODEATLAS_WORKSPACE);
  assert.equal(field(reset.body.snapshot, 'CODEATLAS_MAX_FILE_BYTES').value, 3333);

  const persisted = await fs.readFile(profile.settingsPath, 'utf8');
  assert.doesNotMatch(persisted, /api[_-]?key|sk-|sentinel/iu);
});
