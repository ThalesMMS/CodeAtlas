'use strict';

const test = require('node:test');
const assert = require('node:assert/strict');
const fs = require('node:fs');
const path = require('node:path');

const frontendRoot = path.resolve(__dirname, '..');
const repoRoot = path.resolve(frontendRoot, '..');
const settings = require('../settings.js');

function envKeys() {
  return fs.readFileSync(path.join(repoRoot, '.env.example'), 'utf8')
    .split(/\r?\n/)
    .map((line) => line.match(/^(CODEATLAS_[A-Z0-9_]+)=/))
    .filter(Boolean)
    .map((match) => match[1]);
}

function sampleSnapshot() {
  return {
    revision: 7,
    restartRequired: [],
    groups: {
      general: [
        { key: 'CODEATLAS_WORKSPACE', kind: 'string', value: '.', runningValue: '.', source: 'default', applyMode: 'restart' },
        { key: 'CODEATLAS_MAX_FILE_BYTES', kind: 'integer', value: 1500000, runningValue: 1500000, source: 'default', applyMode: 'restart' },
      ],
      llm: [
        { key: 'CODEATLAS_LLM_MODEL', kind: 'string', value: 'old-model', source: 'env', applyMode: 'live' },
        { key: 'CODEATLAS_LLM_API_KEY', kind: 'secret', configured: true, source: 'settings', applyMode: 'live' },
      ],
      embeddings: [
        { key: 'CODEATLAS_ENABLE_EMBEDDINGS', kind: 'boolean', value: false, source: 'default', applyMode: 'live' },
        { key: 'CODEATLAS_EMBEDDINGS_API_KEY', kind: 'secret', configured: true, source: 'env', applyMode: 'live' },
      ],
      languageServers: [],
    },
  };
}

test('Settings controls are discreet, accessible, and available during bootstrap', () => {
  const html = fs.readFileSync(path.join(frontendRoot, 'index.html'), 'utf8');
  const reindex = html.indexOf('id="reindex-button"');
  const gear = html.indexOf('id="settings-button"');
  assert.ok(reindex >= 0 && gear > reindex, 'Settings button must follow reindex');
  assert.match(html, /id="settings-button"[^>]+aria-label="Settings"/);
  assert.match(html, /class="bootstrap-actions"[\s\S]*id="bootstrap-settings-button"/);
  assert.match(html, /id="settings-drawer"[^>]+role="dialog"[^>]+aria-modal="true"/);
  for (const group of ['general', 'llm', 'embeddings', 'languageServers']) {
    assert.match(html, new RegExp(`data-settings-group="${group}"`));
  }
});

test('Settings inventory matches every .env.example key exactly once', () => {
  const expected = envKeys().sort();
  const actual = settings.settingsFieldInventory.map((field) => field.key).sort();
  assert.equal(actual.length, 23);
  assert.deepEqual(actual, expected);
  assert.equal(new Set(actual).size, actual.length);
  assert.deepEqual([...new Set(settings.settingsFieldInventory.map((field) => field.group))].sort(), [
    'embeddings', 'general', 'languageServers', 'llm',
  ]);
  for (const field of settings.settingsFieldInventory.filter((item) => item.kind === 'secret')) {
    assert.equal(field.prefill, false);
  }
});

test('buildUpdateRequest omits unchanged normal values and preserves untouched secrets', () => {
  const result = settings.buildUpdateRequest(sampleSnapshot(), {
    CODEATLAS_LLM_MODEL: { mode: 'set', value: 'old-model' },
  });
  assert.deepEqual(result, {
    revision: 7,
    secrets: {
      CODEATLAS_LLM_API_KEY: { operation: 'preserve' },
      CODEATLAS_EMBEDDINGS_API_KEY: { operation: 'preserve' },
    },
  });
});

test('buildUpdateRequest preserves types and represents inherit explicitly', () => {
  const result = settings.buildUpdateRequest(sampleSnapshot(), {
    CODEATLAS_MAX_FILE_BYTES: { mode: 'set', value: '2048' },
    CODEATLAS_ENABLE_EMBEDDINGS: { mode: 'set', value: true },
    CODEATLAS_LLM_MODEL: { mode: 'inherit', value: 'ignored' },
  });
  assert.deepEqual(result.overrides, {
    CODEATLAS_MAX_FILE_BYTES: { operation: 'replace', value: 2048 },
    CODEATLAS_ENABLE_EMBEDDINGS: { operation: 'replace', value: true },
    CODEATLAS_LLM_MODEL: { operation: 'inherit' },
  });
});

test('buildUpdateRequest replaces secrets only from non-empty local input', () => {
  const replaced = settings.buildUpdateRequest(sampleSnapshot(), {
    CODEATLAS_LLM_API_KEY: { mode: 'replace', value: 'local-secret' },
    CODEATLAS_EMBEDDINGS_API_KEY: { mode: 'replace', value: '' },
  });
  assert.deepEqual(replaced.secrets, {
    CODEATLAS_LLM_API_KEY: { operation: 'replace', value: 'local-secret' },
    CODEATLAS_EMBEDDINGS_API_KEY: { operation: 'preserve' },
  });
  const inherited = settings.buildUpdateRequest(sampleSnapshot(), {
    CODEATLAS_LLM_API_KEY: { mode: 'inherit', value: 'must-not-be-sent' },
  });
  assert.deepEqual(inherited.secrets.CODEATLAS_LLM_API_KEY, { operation: 'inherit' });
});

test('renderFieldStatus uses exact source, apply, and secret labels', () => {
  assert.deepEqual(settings.renderFieldStatus({ source: 'settings', applyMode: 'live', value: 'x' }), {
    source: 'Settings', apply: 'Live', secret: '', restartPending: false,
  });
  assert.deepEqual(settings.renderFieldStatus({ source: 'env', applyMode: 'restart', value: 'saved', runningValue: 'running' }), {
    source: '.env', apply: 'Restart required', secret: '', restartPending: true,
  });
  assert.equal(settings.renderFieldStatus({ kind: 'secret', configured: true, source: 'settings', applyMode: 'live' }).secret, 'Saved in system keychain');
  assert.equal(settings.renderFieldStatus({ kind: 'secret', configured: true, source: 'env', applyMode: 'live' }).secret, 'Using .env');
  assert.equal(settings.renderFieldStatus({ kind: 'secret', configured: false, source: 'none', applyMode: 'live' }).secret, 'Not configured');
});

test('controller refreshes on conflicts, clears secrets, and maps validation by key', async () => {
  const rendered = [];
  const statuses = [];
  const fieldErrors = [];
  let cleared = 0;
  const snapshot = sampleSnapshot();
  const fresh = { ...sampleSnapshot(), revision: 8 };
  const view = {
    bind() {}, open() {}, close() {}, setBusy() {},
    render(value) { rendered.push(value); },
    readEdits() { return { CODEATLAS_LLM_API_KEY: { mode: 'replace', value: 'transient-secret' } }; },
    clearSecrets() { cleared += 1; },
    setStatus(message, tone) { statuses.push([message, tone]); },
    setFieldErrors(errors) { fieldErrors.push(errors); },
  };
  let call = 0;
  const controller = settings.createSettingsController({
    token: 'browser-token', view,
    api: async (_path, options) => {
      call += 1;
      assert.equal(options.headers['X-CodeAtlas-Settings-Token'], 'browser-token');
      if (call === 1) return snapshot;
      const error = new Error('conflict');
      error.code = 'SETTINGS_REVISION_CONFLICT';
      error.details = { snapshot: fresh };
      throw error;
    },
  });
  await controller.open();
  await controller.apply();
  assert.equal(rendered.at(-1).revision, 8);
  assert.ok(cleared >= 1);
  assert.match(statuses.at(-1)[0], /changed/i);

  controller.setSnapshot(fresh);
  controller.setAPI(async () => {
    const error = new Error('invalid');
    error.code = 'SETTINGS_VALIDATION_FAILED';
    error.details = { fields: [{ field: 'CODEATLAS_LLM_MODEL', message: 'Model is required.' }], snapshot: fresh };
    throw error;
  });
  await controller.apply();
  assert.deepEqual(fieldErrors.at(-1), { CODEATLAS_LLM_MODEL: 'Model is required.' });
  assert.match(statuses.at(-1)[0], /invalid/i);
});

test('controller sends no-store token requests and restarts readiness after success', async () => {
  const snapshot = sampleSnapshot();
  let applied = 0;
  const calls = [];
  const view = {
    bind() {}, open() {}, close() {}, render() {}, clearSecrets() {}, setBusy() {}, setStatus() {}, setFieldErrors() {},
    readEdits() { return {}; },
  };
  const controller = settings.createSettingsController({
    token: 'token', view, onApplied: () => { applied += 1; },
    api: async (path, options) => {
      calls.push([path, options]);
      if (options.method === 'PUT') return { snapshot: { ...snapshot, revision: 8 }, applied: ['llm'], restartRequired: [] };
      return snapshot;
    },
  });
  await controller.open();
  await controller.apply();
  assert.equal(applied, 1);
  assert.equal(calls[0][1].cache, 'no-store');
  assert.equal(calls[1][1].method, 'PUT');
  assert.equal(JSON.parse(calls[1][1].body).revision, 7);
});

test('controller reset is confirmed and revisioned', async () => {
  const snapshot = sampleSnapshot();
  let request;
  const view = {
    bind() {}, open() {}, close() {}, render() {}, clearSecrets() {}, setBusy() {}, setStatus() {}, setFieldErrors() {},
    readEdits() { return {}; },
  };
  const controller = settings.createSettingsController({
    token: 'token', view, confirmReset: async () => true,
    api: async (path, options) => {
      request = [path, options];
      return { snapshot: { ...snapshot, revision: 8 }, restartRequired: [] };
    },
  });
  controller.setSnapshot(snapshot);
  await controller.reset();
  assert.equal(request[0], '/api/settings/overrides');
  assert.equal(request[1].method, 'DELETE');
  assert.deepEqual(JSON.parse(request[1].body), { revision: 7 });
});

test('settings module is imported before the application', () => {
  const main = fs.readFileSync(path.join(frontendRoot, 'src', 'main.ts'), 'utf8');
  assert.ok(main.indexOf("import '../settings.js'") >= 0);
  assert.ok(main.indexOf("import '../settings.js'") < main.indexOf("import '../app.js'"));
});
