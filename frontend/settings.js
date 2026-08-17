'use strict';

const settingsFieldInventory = Object.freeze([
  Object.freeze({ key: 'CODEATLAS_WORKSPACE', label: 'Workspace', group: 'general', kind: 'string', prefill: true }),
  Object.freeze({ key: 'CODEATLAS_LISTEN', label: 'Listen address', group: 'general', kind: 'string', prefill: true }),
  Object.freeze({ key: 'CODEATLAS_MAX_FILE_BYTES', label: 'Maximum file bytes', group: 'general', kind: 'integer', prefill: true }),

  Object.freeze({ key: 'CODEATLAS_LLM_BASE_URL', label: 'LLM base URL', group: 'llm', kind: 'string', prefill: true }),
  Object.freeze({ key: 'CODEATLAS_LLM_API_KEY', label: 'LLM API key', group: 'llm', kind: 'secret', prefill: false }),
  Object.freeze({ key: 'CODEATLAS_LLM_MODEL', label: 'LLM model', group: 'llm', kind: 'string', prefill: true }),
  Object.freeze({ key: 'CODEATLAS_LLM_REASONING_EFFORT', label: 'Reasoning effort', group: 'llm', kind: 'string', prefill: true }),
  Object.freeze({ key: 'CODEATLAS_LLM_TIMEOUT', label: 'LLM timeout', group: 'llm', kind: 'duration', prefill: true }),

  Object.freeze({ key: 'CODEATLAS_ENABLE_EMBEDDINGS', label: 'Enable embeddings', group: 'embeddings', kind: 'boolean', prefill: true }),
  Object.freeze({ key: 'CODEATLAS_EMBEDDING_MODEL', label: 'Embedding model', group: 'embeddings', kind: 'string', prefill: true }),
  Object.freeze({ key: 'CODEATLAS_EMBEDDING_BASE_URL', label: 'Embedding base URL', group: 'embeddings', kind: 'string', prefill: true }),
  Object.freeze({ key: 'CODEATLAS_EMBEDDINGS_API_KEY', label: 'Embeddings API key', group: 'embeddings', kind: 'secret', prefill: false }),

  Object.freeze({ key: 'CODEATLAS_GOPLS', label: 'Go language server mode', group: 'languageServers', kind: 'string', prefill: true }),
  Object.freeze({ key: 'CODEATLAS_GOPLS_PATH', label: 'gopls executable', group: 'languageServers', kind: 'string', prefill: true }),
  Object.freeze({ key: 'CODEATLAS_TYPESCRIPT_LSP', label: 'TypeScript language server mode', group: 'languageServers', kind: 'string', prefill: true }),
  Object.freeze({ key: 'CODEATLAS_TYPESCRIPT_LSP_PATH', label: 'TypeScript language server executable', group: 'languageServers', kind: 'string', prefill: true }),
  Object.freeze({ key: 'CODEATLAS_TYPESCRIPT_SDK_PATH', label: 'TypeScript SDK path', group: 'languageServers', kind: 'string', prefill: true }),
  Object.freeze({ key: 'CODEATLAS_SWIFT_LSP', label: 'Swift language server mode', group: 'languageServers', kind: 'string', prefill: true }),
  Object.freeze({ key: 'CODEATLAS_SWIFT_LSP_PATH', label: 'SourceKit-LSP executable', group: 'languageServers', kind: 'string', prefill: true }),
  Object.freeze({ key: 'CODEATLAS_PYTHON_LSP', label: 'Python language server mode', group: 'languageServers', kind: 'string', prefill: true }),
  Object.freeze({ key: 'CODEATLAS_PYTHON_LSP_PATH', label: 'Python language server executable', group: 'languageServers', kind: 'string', prefill: true }),
  Object.freeze({ key: 'CODEATLAS_RUST_LSP', label: 'Rust language server mode', group: 'languageServers', kind: 'string', prefill: true }),
  Object.freeze({ key: 'CODEATLAS_RUST_LSP_PATH', label: 'rust-analyzer executable', group: 'languageServers', kind: 'string', prefill: true }),
]);

function flattenedFields(snapshot) {
  const fields = new Map();
  for (const groupFields of Object.values((snapshot && snapshot.groups) || {})) {
    for (const field of Array.isArray(groupFields) ? groupFields : []) {
      if (field && field.key) fields.set(field.key, field);
    }
  }
  return fields;
}

function normalizedInputValue(kind, value) {
  if (kind === 'integer') {
    const parsed = Number(value);
    return Number.isSafeInteger(parsed) ? parsed : value;
  }
  if (kind === 'boolean') return value === true || value === 'true';
  return value == null ? '' : String(value);
}

function sameSettingValue(kind, left, right) {
  return normalizedInputValue(kind, left) === normalizedInputValue(kind, right);
}

function buildUpdateRequest(snapshot, edits = {}) {
  const fields = flattenedFields(snapshot);
  const overrides = {};
  const secrets = {};
  for (const descriptor of settingsFieldInventory) {
    const field = fields.get(descriptor.key) || descriptor;
    const edit = edits[descriptor.key];
    if (descriptor.kind === 'secret') {
      if (edit && edit.mode === 'inherit') {
        secrets[descriptor.key] = { operation: 'inherit' };
      } else if (edit && edit.mode === 'replace' && String(edit.value || '') !== '') {
        secrets[descriptor.key] = { operation: 'replace', value: String(edit.value) };
      } else {
        secrets[descriptor.key] = { operation: 'preserve' };
      }
      continue;
    }
    if (!edit || edit.mode === 'preserve') continue;
    if (edit.mode === 'inherit') {
      overrides[descriptor.key] = { operation: 'inherit' };
      continue;
    }
    const value = normalizedInputValue(field.kind || descriptor.kind, edit.value);
    if (!sameSettingValue(field.kind || descriptor.kind, value, field.value)) {
      overrides[descriptor.key] = { operation: 'replace', value };
    }
  }
  const request = { revision: Number(snapshot && snapshot.revision) || 0 };
  if (Object.keys(overrides).length) request.overrides = overrides;
  if (Object.keys(secrets).length) request.secrets = secrets;
  return request;
}

function renderFieldStatus(field = {}) {
  const sources = { settings: 'Settings', env: '.env', default: 'Default', none: 'Not configured' };
  let secret = '';
  if (field.kind === 'secret') {
    if (!field.configured) secret = 'Not configured';
    else if (field.source === 'settings') secret = 'Saved in system keychain';
    else if (field.source === 'env') secret = 'Using .env';
    else secret = 'Not configured';
  }
  const restartPending = field.applyMode === 'restart' && field.value !== field.runningValue;
  return {
    source: sources[field.source] || 'Not configured',
    apply: field.applyMode === 'restart' ? 'Restart required' : 'Live',
    secret,
    restartPending,
  };
}

function fieldErrorMap(fields) {
  const mapped = {};
  for (const issue of Array.isArray(fields) ? fields : []) {
    if (issue && issue.field && !mapped[issue.field]) {
      mapped[issue.field] = issue.message || 'Invalid value.';
    }
  }
  return mapped;
}

function createSettingsController(options = {}) {
  let requestAPI = options.api;
  const token = String(options.token || '');
  const view = options.view || createDOMSettingsView(options);
  let snapshot = null;

  const request = (path, requestOptions = {}) => requestAPI(path, {
    cache: 'no-store',
    ...requestOptions,
    headers: {
      ...(requestOptions.headers || {}),
      'X-CodeAtlas-Settings-Token': token,
    },
  });

  const setSnapshot = (next) => {
    if (!next || typeof next !== 'object') return;
    snapshot = next;
    view.render(snapshot);
  };

  const showFailure = (error) => {
    const details = (error && error.details) || {};
    if (details.snapshot) setSnapshot(details.snapshot);
    if (error && error.code === 'SETTINGS_REVISION_CONFLICT') {
      view.setStatus('Settings changed in another window. The latest values were loaded; review and apply again.', 'warning');
      options.announce?.('Settings changed. Latest values loaded.', 'assertive');
      return;
    }
    if (error && (error.code === 'SETTINGS_VALIDATION_FAILED' || error.code === 'SETTINGS_PREPARE_FAILED')) {
      view.setFieldErrors(fieldErrorMap(details.fields));
      view.setStatus('Some settings are invalid. Review the highlighted fields.', 'error');
      options.announce?.('Some settings are invalid.', 'assertive');
      return;
    }
    view.setStatus((error && error.message) || 'Settings could not be applied.', 'error');
    options.announce?.('Settings could not be applied.', 'assertive');
  };

  const controller = {
    bind() {
      view.bind({
        open: () => controller.open(),
        close: () => controller.close(),
        apply: () => controller.apply(),
        reset: () => controller.reset(),
      });
      return controller;
    },
    async open() {
      view.open();
      view.setBusy(true);
      view.setFieldErrors({});
      view.setStatus('Loading settings…', 'neutral');
      try {
        setSnapshot(await request('/api/settings'));
        view.setStatus('', 'neutral');
      } catch (error) {
        showFailure(error);
      } finally {
        view.clearSecrets();
        view.setBusy(false);
      }
    },
    close() {
      view.clearSecrets();
      view.setFieldErrors({});
      view.close();
    },
    async apply() {
      if (!snapshot) return controller.open();
      view.setBusy(true);
      view.setFieldErrors({});
      view.setStatus('Testing and applying…', 'neutral');
      try {
        const result = await request('/api/settings', {
          method: 'PUT',
          body: JSON.stringify(buildUpdateRequest(snapshot, view.readEdits())),
        });
        setSnapshot(result.snapshot);
        view.markApplied?.(result.applied);
        const applied = Array.isArray(result.applied) && result.applied.length
          ? `Applied: ${result.applied.join(', ')}.${result.embeddingJobId ? ` Rebuild job: ${result.embeddingJobId}.` : ''}`
          : 'Settings saved.';
        view.setStatus(applied, 'success');
        options.announce?.(applied);
        options.onApplied?.(result);
      } catch (error) {
        showFailure(error);
      } finally {
        view.clearSecrets();
        view.setBusy(false);
      }
    },
    async reset() {
      if (!snapshot) return;
      const confirmed = options.confirmReset ? await options.confirmReset() : true;
      if (!confirmed) return;
      view.setBusy(true);
      view.setFieldErrors({});
      view.setStatus('Resetting overrides…', 'neutral');
      try {
        const result = await request('/api/settings/overrides', {
          method: 'DELETE',
          body: JSON.stringify({ revision: snapshot.revision }),
        });
        setSnapshot(result.snapshot);
        view.setStatus('Overrides reset. Values now come from .env or defaults.', 'success');
        options.announce?.('Settings overrides reset.');
        options.onApplied?.(result);
      } catch (error) {
        showFailure(error);
      } finally {
        view.clearSecrets();
        view.setBusy(false);
      }
    },
    setSnapshot,
    setAPI(nextAPI) { requestAPI = nextAPI; },
    snapshot() { return snapshot; },
  };
  return controller;
}

function createDOMSettingsView(options = {}) {
  const doc = options.documentRef || (typeof document !== 'undefined' ? document : null);
  const focusManager = options.focusManager || {};
  if (!doc) throw new Error('Settings DOM is unavailable.');
  const byID = (id) => doc.getElementById(id);
  const backdrop = byID('settings-backdrop');
  const drawer = byID('settings-drawer');
  const form = byID('settings-form');
  const status = byID('settings-status');
  const restartBanner = byID('settings-restart-banner');
  const background = [doc.querySelector('.app-shell'), byID('bootstrap-overlay')].filter(Boolean);
  let previousAria = [];

  const inputForField = (descriptor, field) => {
    const input = doc.createElement('input');
    input.className = 'settings-input';
    input.dataset.settingsInput = descriptor.key;
    input.id = `settings-field-${descriptor.key.toLowerCase().replaceAll('_', '-')}`;
    if (descriptor.kind === 'secret') {
      input.type = 'password';
      input.autocomplete = 'off';
      input.value = '';
      input.disabled = true;
    } else if (descriptor.kind === 'boolean') {
      input.type = 'checkbox';
      input.checked = field.value === true;
    } else {
      input.type = descriptor.kind === 'integer' ? 'number' : 'text';
      input.value = field.value == null ? '' : String(field.value);
      input.spellcheck = false;
    }
    return input;
  };

  const modeForField = (descriptor, input) => {
    const select = doc.createElement('select');
    select.className = 'settings-mode';
    select.dataset.settingsMode = descriptor.key;
    select.setAttribute('aria-label', `${descriptor.label} value source`);
    const choices = descriptor.kind === 'secret'
      ? [['preserve', 'Keep current'], ['replace', 'Replace'], ['inherit', 'Use .env']]
      : [['set', 'Set value'], ['inherit', 'Use .env / default']];
    for (const [value, label] of choices) {
      const option = doc.createElement('option');
      option.value = value;
      option.textContent = label;
      select.appendChild(option);
    }
    select.addEventListener('change', () => {
      const enabledMode = descriptor.kind === 'secret' ? 'replace' : 'set';
      input.disabled = select.value !== enabledMode;
      if (descriptor.kind === 'secret' && select.value !== 'replace') input.value = '';
    });
    if (descriptor.kind === 'secret') {
      input.addEventListener('input', () => { if (input.value) select.value = 'replace'; });
    }
    return select;
  };

  const view = {
    bind(actions) {
      byID('settings-button')?.addEventListener('click', actions.open);
      byID('bootstrap-settings-button')?.addEventListener('click', actions.open);
      byID('settings-close-button')?.addEventListener('click', actions.close);
      byID('settings-cancel-button')?.addEventListener('click', actions.close);
      byID('settings-reset-button')?.addEventListener('click', actions.reset);
      form?.addEventListener('submit', (event) => {
        event.preventDefault();
        actions.apply();
      });
      backdrop?.addEventListener('click', (event) => {
        if (event.target === backdrop) actions.close();
      });
      drawer?.addEventListener('keydown', (event) => {
        if (event.key === 'Escape') {
          event.preventDefault();
          actions.close();
        }
      });
    },
    open() {
      focusManager.saveFocus?.('#settings-button');
      previousAria = background.map((element) => [element, element.getAttribute('aria-hidden')]);
      for (const [element] of previousAria) element.setAttribute('aria-hidden', 'true');
      backdrop?.classList.remove('hidden');
      focusManager.trapFocus?.(drawer);
      const focusTarget = byID('settings-close-button') || drawer;
      if (focusManager.moveFocus) focusManager.moveFocus(focusTarget);
      else focusTarget?.focus();
    },
    close() {
      backdrop?.classList.add('hidden');
      for (const [element, value] of previousAria) {
        if (value == null) element.removeAttribute('aria-hidden');
        else element.setAttribute('aria-hidden', value);
      }
      previousAria = [];
      focusManager.releaseTrap?.();
      focusManager.restoreFocus?.();
    },
    render(snapshot) {
      const fields = flattenedFields(snapshot);
      for (const group of ['general', 'llm', 'embeddings', 'languageServers']) {
        const container = doc.querySelector(`[data-settings-fields="${group}"]`);
        if (!container) continue;
        container.replaceChildren();
        for (const descriptor of settingsFieldInventory.filter((item) => item.group === group)) {
          const field = fields.get(descriptor.key) || { ...descriptor, source: 'none', applyMode: 'live' };
          const row = doc.createElement('div');
          row.className = 'settings-field';
          row.dataset.settingsField = descriptor.key;
          row.dataset.settingsApplyMode = field.applyMode || 'live';
          const heading = doc.createElement('div');
          heading.className = 'settings-field-heading';
          const label = doc.createElement('label');
          label.textContent = descriptor.label;
          const key = doc.createElement('code');
          key.textContent = descriptor.key;
          heading.append(label, key);
          const controls = doc.createElement('div');
          controls.className = 'settings-field-controls';
          const input = inputForField(descriptor, field);
          label.htmlFor = input.id;
          const mode = modeForField(descriptor, input);
          controls.append(input, mode);
          const fieldStatus = renderFieldStatus(field);
          const metadata = doc.createElement('div');
          metadata.className = 'settings-field-metadata';
          for (const value of [fieldStatus.source, fieldStatus.apply, fieldStatus.secret].filter(Boolean)) {
            const badge = doc.createElement('span');
            badge.textContent = value;
            metadata.appendChild(badge);
          }
          const error = doc.createElement('div');
          error.className = 'settings-field-error';
          error.dataset.settingsError = descriptor.key;
          error.setAttribute('role', 'alert');
          row.append(heading, controls, metadata, error);
          container.appendChild(row);
        }
      }
      const restart = Array.isArray(snapshot.restartRequired) ? snapshot.restartRequired : [];
      restartBanner?.classList.toggle('hidden', restart.length === 0);
      if (restartBanner) restartBanner.textContent = restart.length
        ? `Restart required for: ${restart.join(', ')}`
        : '';
    },
    readEdits() {
      const edits = {};
      for (const descriptor of settingsFieldInventory) {
        const input = doc.querySelector(`[data-settings-input="${descriptor.key}"]`);
        const mode = doc.querySelector(`[data-settings-mode="${descriptor.key}"]`);
        if (!input || !mode) continue;
        edits[descriptor.key] = {
          mode: mode.value,
          value: descriptor.kind === 'boolean' ? input.checked : input.value,
        };
      }
      return edits;
    },
    clearSecrets() {
      for (const descriptor of settingsFieldInventory.filter((item) => item.kind === 'secret')) {
        const input = doc.querySelector(`[data-settings-input="${descriptor.key}"]`);
        const mode = doc.querySelector(`[data-settings-mode="${descriptor.key}"]`);
        if (input) input.value = '';
        if (mode && mode.value === 'replace') {
          mode.value = 'preserve';
          if (input) input.disabled = true;
        }
      }
    },
    setBusy(busy) {
      drawer?.setAttribute('aria-busy', busy ? 'true' : 'false');
      for (const button of drawer?.querySelectorAll('button') || []) {
        if (button.id === 'settings-close-button' || button.id === 'settings-cancel-button') continue;
        button.disabled = !!busy;
      }
    },
    setStatus(message, tone) {
      if (!status) return;
      status.textContent = message || '';
      status.dataset.tone = tone || 'neutral';
    },
    setFieldErrors(errors) {
      for (const node of drawer?.querySelectorAll('[data-settings-error]') || []) {
        node.textContent = errors[node.dataset.settingsError] || '';
      }
    },
    markApplied(groups) {
      for (const node of drawer?.querySelectorAll('.settings-applied') || []) node.remove();
      const knownGroups = new Set(['general', 'llm', 'embeddings', 'languageServers']);
      for (const group of (Array.isArray(groups) ? groups : []).filter((value) => knownGroups.has(value))) {
        for (const row of drawer?.querySelectorAll(`[data-settings-group="${group}"] [data-settings-field][data-settings-apply-mode="live"]`) || []) {
          const badge = doc.createElement('span');
          badge.className = 'settings-applied';
          badge.textContent = 'Applied';
          row.querySelector('.settings-field-metadata')?.appendChild(badge);
        }
      }
    },
  };
  return view;
}

const CodeAtlasSettings = {
  createSettingsController,
  buildUpdateRequest,
  renderFieldStatus,
  settingsFieldInventory,
};

if (typeof globalThis !== 'undefined') globalThis.CodeAtlasSettings = CodeAtlasSettings;
if (typeof module !== 'undefined' && module.exports) module.exports = CodeAtlasSettings;
