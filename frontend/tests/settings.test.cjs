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
  const styles = fs.readFileSync(path.join(frontendRoot, 'styles.css'), 'utf8');
  const reindex = html.indexOf('id="reindex-button"');
  const gear = html.indexOf('id="settings-button"');
  assert.ok(reindex >= 0 && gear > reindex, 'Settings button must follow reindex');
  assert.match(html, /id="settings-button"[^>]+aria-label="Settings"/);
  assert.match(html, /class="bootstrap-actions"[\s\S]*id="bootstrap-settings-button"/);
  assert.match(html, /id="settings-drawer"[^>]+role="dialog"[^>]+aria-modal="true"/);
  for (const group of ['general', 'llm', 'embeddings', 'languageServers']) {
    assert.match(html, new RegExp(`data-settings-group="${group}"`));
  }
  assert.match(styles, /\.bootstrap-overlay\s*\{[^}]*overflow:\s*auto;/s, 'a short window must scroll the startup overlay');
  assert.match(styles, /\.bootstrap-card\s*\{[^}]*margin-block:\s*auto;/s, 'the startup card should remain centered when it fits');
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
  const restored = [];
  const statuses = [];
  const fieldErrors = [];
  let cleared = 0;
  const snapshot = sampleSnapshot();
  const fresh = { ...sampleSnapshot(), revision: 8 };
  const view = {
    bind() {}, open() {}, close() {}, setBusy() {},
    render(value) { rendered.push(value); },
    readEdits() { return { CODEATLAS_LLM_API_KEY: { mode: 'replace', value: 'transient-secret' } }; },
    restoreEdits(edits, markForReview) { restored.push([edits, markForReview]); },
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
  assert.deepEqual(restored.at(-1), [{
    CODEATLAS_LLM_API_KEY: { mode: 'replace', value: 'transient-secret' },
  }, true]);
  assert.ok(cleared >= 1);
  assert.match(statuses.at(-1)[0], /changed/i);

  controller.setSnapshot(fresh);
  const renderedBefore = rendered.length;
  const clearedBefore = cleared;
  controller.setAPI(async () => {
    const error = new Error('invalid');
    error.code = 'SETTINGS_VALIDATION_FAILED';
    error.details = { fields: [{ field: 'CODEATLAS_LLM_MODEL', message: 'Model is required.' }], snapshot: fresh };
    throw error;
  });
  await controller.apply();
  assert.deepEqual(fieldErrors.at(-1), { CODEATLAS_LLM_MODEL: 'Model is required.' });
  assert.match(statuses.at(-1)[0], /invalid/i);
  assert.equal(rendered.length, renderedBefore, 'validation failure must not re-render (would wipe typed values)');
  assert.equal(cleared, clearedBefore, 'validation failure must not clear typed secrets');
});

test('controller keeps typed values and secrets after prepare failures, clears secrets on success', async () => {
  const rendered = [];
  const fieldErrors = [];
  let cleared = 0;
  const edits = { CODEATLAS_LLM_API_KEY: { mode: 'replace', value: 'typed-secret' } };
  const view = {
    bind() {}, open() {}, close() {}, setBusy() {}, setStatus() {},
    setFieldErrors(errors) { fieldErrors.push(errors); },
    render(value) { rendered.push(value); },
    readEdits() { return edits; },
    clearSecrets() { cleared += 1; },
  };
  const snapshot = sampleSnapshot();
  const controller = settings.createSettingsController({
    token: 'token', view,
    api: async () => {
      const error = new Error('probe failed');
      error.code = 'SETTINGS_PREPARE_FAILED';
      error.details = { fields: [{ field: 'CODEATLAS_LLM_BASE_URL', message: 'Probe failed.' }], snapshot: sampleSnapshot() };
      throw error;
    },
  });
  controller.setSnapshot(snapshot);
  rendered.length = 0;
  await controller.apply();
  assert.equal(rendered.length, 0, 'prepare failure must not re-render the form');
  assert.equal(cleared, 0, 'prepare failure must not clear typed secrets');
  assert.deepEqual(fieldErrors.at(-1), { CODEATLAS_LLM_BASE_URL: 'Probe failed.' });
  assert.equal(edits.CODEATLAS_LLM_API_KEY.value, 'typed-secret');

  controller.setAPI(async () => ({ snapshot: { ...sampleSnapshot(), revision: 8 }, applied: ['llm'] }));
  await controller.apply();
  assert.equal(cleared, 1, 'successful apply clears typed secrets');
  assert.equal(rendered.at(-1).revision, 8, 'successful apply re-renders the fresh snapshot');
});

test('controller fetches a fresh snapshot when a conflict omits one', async () => {
  const rendered = [];
  const statuses = [];
  const snapshot = sampleSnapshot();
  const fresh = { ...sampleSnapshot(), revision: 8 };
  const view = {
    bind() {}, open() {}, close() {}, setBusy() {}, clearSecrets() {}, setFieldErrors() {},
    render(value) { rendered.push(value); },
    readEdits() { return {}; },
    setStatus(message, tone) { statuses.push([message, tone]); },
  };
  let call = 0;
  const controller = settings.createSettingsController({
    token: 'token', view,
    api: async (_path, options) => {
      call += 1;
      if (call === 1) return snapshot;
      if (call === 2 && options.method === 'PUT') {
        const error = new Error('conflict');
        error.code = 'SETTINGS_REVISION_CONFLICT';
        error.details = {};
        throw error;
      }
      return fresh;
    },
  });
  await controller.open();
  await controller.apply();
  assert.equal(call, 3);
  assert.equal(rendered.at(-1).revision, 8);
  assert.match(statuses.at(-1)[0], /latest values were loaded/i);
});

test('controller rejects malformed snapshots without claiming success or clearing edits', async () => {
  const rendered = [];
  const statuses = [];
  let cleared = 0;
  let applied = 0;
  const view = {
    bind() {}, open() {}, close() {}, setBusy() {}, setFieldErrors() {},
    render(value) { rendered.push(value); },
    readEdits() { return { CODEATLAS_LLM_API_KEY: { mode: 'replace', value: 'typed-secret' } }; },
    clearSecrets() { cleared += 1; },
    setStatus(message, tone) { statuses.push([message, tone]); },
  };
  const controller = settings.createSettingsController({
    token: 'token', view, onApplied: () => { applied += 1; },
    confirmReset: async () => true,
    api: async () => ({ error: 'malformed snapshot' }),
  });

  await controller.open();
  assert.equal(rendered.length, 0);
  assert.equal(controller.snapshot(), null);
  assert.equal(statuses.at(-1)[1], 'error');
  assert.equal(controller.setSnapshot({}), false);
  assert.equal(controller.setSnapshot(sampleSnapshot()), true);

  const clearedAfterOpen = cleared;
  controller.setAPI(async () => ({ snapshot: {}, applied: ['llm'] }));
  await controller.apply();
  assert.equal(cleared, clearedAfterOpen);
  assert.equal(applied, 0);
  assert.equal(statuses.at(-1)[1], 'error');

  controller.setAPI(async () => ({ snapshot: {} }));
  await controller.reset();
  assert.equal(cleared, clearedAfterOpen);
  assert.equal(applied, 0);
  assert.equal(statuses.at(-1)[1], 'error');
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

test('workspace field declares a directory picker and the desktop binding is wired', () => {
  const workspace = settings.settingsFieldInventory.find((field) => field.key === 'CODEATLAS_WORKSPACE');
  assert.equal(workspace.picker, 'directory');
  assert.equal(settings.settingsFieldInventory.filter((field) => field.picker).length, 1);
  const source = fs.readFileSync(path.join(frontendRoot, 'settings.js'), 'utf8');
  assert.match(source, /Choose folder…/);
  assert.match(source, /data-settings-picker|dataset\.settingsPicker/);
  const app = fs.readFileSync(path.join(frontendRoot, 'app.js'), 'utf8');
  assert.match(app, /codeatlasPickWorkspaceFolder/);
  assert.match(app, /pickDirectory: nativeDirectoryPicker\(\)/);
  const styles = fs.readFileSync(path.join(frontendRoot, 'styles.css'), 'utf8');
  assert.match(styles, /\.settings-field-controls\.has-picker/);
});

test('controller restarts only when supported, with the loaded revision, and reports the outcome', async () => {
  const statuses = [];
  const calls = [];
  let confirmations = 0;
  let restarted = 0;
  const view = {
    bind() {}, open() {}, close() {}, render() {}, clearSecrets() {}, setBusy() {}, setFieldErrors() {},
    setStatus(message, tone) { statuses.push([message, tone]); },
    readEdits() { return {}; },
  };
  const controller = settings.createSettingsController({
    token: 'token', view,
    confirmRestart: async () => { confirmations += 1; return true; },
    onRestart: () => { restarted += 1; },
    api: async (path, options) => {
      calls.push([path, options]);
      if (path === '/api/settings/restart') return { restarting: true, revision: 7 };
      return { ...sampleSnapshot(), restartRequired: ['CODEATLAS_WORKSPACE'] };
    },
  });

  controller.setSnapshot({ ...sampleSnapshot(), restartRequired: ['CODEATLAS_WORKSPACE'] });
  assert.equal(controller.restartSupported(), false);
  await controller.restart();
  assert.equal(confirmations, 0);
  assert.equal(restarted, 0);
  assert.match(statuses.at(-1)[0], /cannot restart itself/i);
  assert.equal(calls.length, 0, 'unsupported restart must not hit the API');

  controller.setSnapshot({ ...sampleSnapshot(), restartRequired: ['CODEATLAS_WORKSPACE'], restartSupported: true });
  assert.equal(controller.restartSupported(), true);
  assert.equal(controller.snapshot().restartSupported, true);
  await controller.restart();
  assert.equal(confirmations, 1);
  assert.equal(restarted, 1);
  const [restartPath, restartOptions] = calls.at(-1);
  assert.equal(restartPath, '/api/settings/restart');
  assert.equal(restartOptions.method, 'POST');
  assert.equal(restartOptions.headers['X-CodeAtlas-Settings-Token'], 'token');
  assert.deepEqual(JSON.parse(restartOptions.body), { revision: 7 });
  assert.equal(statuses.at(-1)[1], 'success');

  // restartSupported is remembered when a later snapshot (for example from an
  // error envelope) omits it.
  controller.setSnapshot({ ...sampleSnapshot(), revision: 8 });
  assert.equal(controller.snapshot().restartSupported, true);
});

test('controller restart surfaces a revision conflict by reloading the latest snapshot', async () => {
  const rendered = [];
  const statuses = [];
  const view = {
    bind() {}, open() {}, close() {}, clearSecrets() {}, setBusy() {}, setFieldErrors() {}, restoreEdits() {},
    render(value) { rendered.push(value); },
    setStatus(message, tone) { statuses.push([message, tone]); },
    readEdits() { return {}; },
  };
  const fresh = { ...sampleSnapshot(), revision: 9, restartSupported: true };
  let onRestartCalls = 0;
  const controller = settings.createSettingsController({
    token: 'token', view, onRestart: () => { onRestartCalls += 1; },
    api: async () => {
      const error = new Error('conflict');
      error.code = 'SETTINGS_REVISION_CONFLICT';
      error.details = { snapshot: fresh };
      throw error;
    },
  });
  controller.setSnapshot({ ...sampleSnapshot(), restartSupported: true });
  await controller.restart();
  assert.equal(onRestartCalls, 0);
  assert.equal(rendered.at(-1).revision, 9);
  assert.match(statuses.at(-1)[0], /changed/i);
});

test('the restart banner is rendered from settings.js with an explicit restart control', () => {
  const source = fs.readFileSync(path.join(frontendRoot, 'settings.js'), 'utf8');
  assert.match(source, /id = 'settings-restart-button'/);
  assert.match(source, /Restart CodeAtlas/);
  const html = fs.readFileSync(path.join(frontendRoot, 'index.html'), 'utf8');
  assert.match(html, /id="settings-restart-banner"/);
});

test('settings module is imported before the application', () => {
  const main = fs.readFileSync(path.join(frontendRoot, 'src', 'main.ts'), 'utf8');
  assert.ok(main.indexOf("import '../settings.js'") >= 0);
  assert.ok(main.indexOf("import '../settings.js'") < main.indexOf("import '../app.js'"));
});
