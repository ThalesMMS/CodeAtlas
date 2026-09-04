import { spawn } from 'node:child_process';
import { mkdtemp, rm } from 'node:fs/promises';
import http from 'node:http';
import net from 'node:net';
import os from 'node:os';
import path from 'node:path';
import process from 'node:process';

import { resolveFakeLSPPath } from './lsp-launchers.mjs';

const isolatedSettingKeys = Object.freeze([
  'CODEATLAS_WORKSPACE', 'CODEATLAS_LISTEN', 'CODEATLAS_MAX_FILE_BYTES',
  'CODEATLAS_LLM_BASE_URL', 'CODEATLAS_LLM_API_KEY', 'CODEATLAS_LLM_MODEL',
  'CODEATLAS_LLM_REASONING_EFFORT', 'CODEATLAS_LLM_TIMEOUT',
  'CODEATLAS_GOPLS', 'CODEATLAS_GOPLS_PATH',
  'CODEATLAS_TYPESCRIPT_LSP', 'CODEATLAS_TYPESCRIPT_LSP_PATH', 'CODEATLAS_TYPESCRIPT_SDK_PATH',
  'CODEATLAS_SWIFT_LSP', 'CODEATLAS_SWIFT_LSP_PATH',
  'CODEATLAS_PYTHON_LSP', 'CODEATLAS_PYTHON_LSP_PATH',
  'CODEATLAS_RUST_LSP', 'CODEATLAS_RUST_LSP_PATH',
]);

export async function createIsolatedUserProfile(prefix = 'codeatlas-e2e-profile-') {
  const configRoot = await mkdtemp(path.join(os.tmpdir(), prefix));
  const environment = process.platform === 'win32'
    ? { APPDATA: configRoot }
    : process.platform === 'darwin'
      ? { HOME: configRoot }
      : { XDG_CONFIG_HOME: configRoot };
  const settingsPath = process.platform === 'darwin'
    ? path.join(configRoot, 'Library', 'Application Support', 'CodeAtlas', 'settings.json')
    : path.join(configRoot, 'CodeAtlas', 'settings.json');
  let closed = false;
  return {
    configRoot,
    environment,
    settingsPath,
    async close() {
      if (closed) return;
      closed = true;
      await rm(configRoot, { recursive: true, force: true });
    },
  };
}

export async function startBackend({
  root,
  workspaceDir,
  providerBaseURL,
  llmAPIKey = 'sk-live-test-key',
  llmModel = providerBaseURL ? 'fake-codeatlas' : '',
  watchMode = 'polling',
  pollInterval = '1s',
  maxFileBytes = '',
  typescriptLSPPath = '',
  goplsPath = '',
  swiftLSPPath = '',
  pythonLSPPath = '',
  rustLSPPath = '',
  scenario = 'happy-path',
  timeoutMs = 30_000,
  waitFor = 'ready',
  userProfile = null,
  environment = {},
} = {}) {
  const port = await freePort();
  const baseURL = `http://127.0.0.1:${port}`;
  const binary = path.join(root, process.platform === 'win32' ? 'dist/codeatlas.exe' : 'dist/codeatlas');
  const logs = [];
  const startedAt = performance.now();
  const resolvedLSPPaths = {
    typescript: resolveFakeLSPPath({ root, configuredPath: typescriptLSPPath }),
    go: resolveFakeLSPPath({ root, configuredPath: goplsPath }),
    swift: resolveFakeLSPPath({ root, configuredPath: swiftLSPPath }),
    python: resolveFakeLSPPath({ root, configuredPath: pythonLSPPath }),
    rust: resolveFakeLSPPath({ root, configuredPath: rustLSPPath }),
  };
  const ownedProfile = userProfile ? null : await createIsolatedUserProfile();
  const profile = userProfile ?? ownedProfile;
  const childEnv = { ...process.env };
  for (const key of isolatedSettingKeys) delete childEnv[key];
  Object.assign(childEnv, profile.environment);
  if (maxFileBytes) childEnv.CODEATLAS_MAX_FILE_BYTES = String(maxFileBytes);
  const providerEnvironment = {
    ...(providerBaseURL ? { CODEATLAS_LLM_BASE_URL: providerBaseURL } : {}),
    ...(llmAPIKey ? { CODEATLAS_LLM_API_KEY: llmAPIKey } : {}),
    ...(llmModel ? { CODEATLAS_LLM_MODEL: llmModel } : {}),
  };
  const child = spawn(binary, [
    '-workspace',
    workspaceDir,
    '-listen',
    `127.0.0.1:${port}`,
    '-db',
    path.join(workspaceDir, '.codeatlas', 'e2e-codeatlas.db'),
  ], {
    cwd: root,
    env: {
      ...childEnv,
      ...providerEnvironment,
      CODEATLAS_PROBE_TIMEOUT: '2s',
      CODEATLAS_WATCH_MODE: watchMode,
      CODEATLAS_POLL_INTERVAL: pollInterval,
      CODEATLAS_WATCH_RECONCILE_INTERVAL: '1h',
      CODEATLAS_FAKE_SCENARIO: scenario,
      CODEATLAS_TYPESCRIPT_LSP: resolvedLSPPaths.typescript ? 'true' : 'false',
      ...(resolvedLSPPaths.typescript ? { CODEATLAS_TYPESCRIPT_LSP_PATH: resolvedLSPPaths.typescript } : {}),
      CODEATLAS_GOPLS: resolvedLSPPaths.go ? 'true' : 'false',
      ...(resolvedLSPPaths.go ? { CODEATLAS_GOPLS_PATH: resolvedLSPPaths.go } : {}),
      CODEATLAS_SWIFT_LSP: resolvedLSPPaths.swift ? 'true' : 'false',
      ...(resolvedLSPPaths.swift ? { CODEATLAS_SWIFT_LSP_PATH: resolvedLSPPaths.swift } : {}),
      CODEATLAS_PYTHON_LSP: resolvedLSPPaths.python ? 'true' : 'false',
      ...(resolvedLSPPaths.python ? { CODEATLAS_PYTHON_LSP_PATH: resolvedLSPPaths.python } : {}),
      CODEATLAS_RUST_LSP: resolvedLSPPaths.rust ? 'true' : 'false',
      ...(resolvedLSPPaths.rust ? { CODEATLAS_RUST_LSP_PATH: resolvedLSPPaths.rust } : {}),
      TZ: 'UTC',
      LC_ALL: 'C',
      ...environment,
      ...profile.environment,
    },
    stdio: ['ignore', 'pipe', 'pipe'],
  });

  child.stdout.setEncoding('utf8');
  child.stderr.setEncoding('utf8');
  child.stdout.on('data', (data) => logs.push(data));
  child.stderr.on('data', (data) => logs.push(data));

  try {
    await waitForBackendState(baseURL, child, waitFor, timeoutMs);
  } catch (error) {
    await stopProcess(child).catch(() => {});
    if (ownedProfile) await ownedProfile.close().catch(() => {});
    error.logs = logs;
    throw error;
  }
  const readyAt = performance.now();
  let settingsToken = '';
  let closed = false;
  const settings = async (urlPath, options = {}) => {
    if (!settingsToken) settingsToken = await readSettingsToken(baseURL);
    if (options.rawHost) {
      return requestJSONWithHost(baseURL, urlPath, {
        ...options,
        headers: {
          origin: baseURL,
          ...(options.headers ?? {}),
          'X-CodeAtlas-Settings-Token': settingsToken,
        },
      });
    }
    return requestJSON(baseURL, urlPath, {
      ...options,
      headers: {
        origin: baseURL,
        ...(options.headers ?? {}),
        'X-CodeAtlas-Settings-Token': settingsToken,
      },
    });
  };

  return {
    baseURL,
    process: child,
    logs,
    timings: { readyMs: Math.round(readyAt - startedAt) },
    environment: {
      node: process.version,
      platform: process.platform,
      arch: process.arch,
      osRelease: os.release(),
      baseURL,
    },
    json: (urlPath, options) => requestJSON(baseURL, urlPath, options),
    text: (urlPath, options) => requestText(baseURL, urlPath, options),
    settings,
    waitForState: (state, stateTimeoutMs = timeoutMs) => waitForBackendState(baseURL, child, state, stateTimeoutMs),
    profile,
    close: async () => {
      if (closed) return;
      closed = true;
      try {
        await stopProcess(child);
      } finally {
        if (ownedProfile) await ownedProfile.close();
      }
    },
  };
}

export async function stopAll(resources) {
  const errors = [];
  for (const resource of [...resources].reverse()) {
    if (!resource || typeof resource.close !== 'function') {
      continue;
    }
    try {
      await resource.close();
    } catch (error) {
      errors.push(error);
    }
  }
  resources.splice(0, resources.length);
  if (errors.length > 0) {
    throw new AggregateError(errors, 'failed to clean up E2E resources');
  }
}

async function waitForBackendState(baseURL, child, target, timeoutMs) {
  const targetState = target === 'awaiting-configuration' ? 'AWAITING_CONFIGURATION' : String(target).toUpperCase();
  const deadline = Date.now() + timeoutMs;
  let lastError;
  while (Date.now() < deadline) {
    if (child.exitCode !== null) {
      throw new Error(`backend exited early with code ${child.exitCode}`);
    }
    let terminalFailure;
    try {
      const response = await fetch(`${baseURL}/api/health/ready`, { signal: AbortSignal.timeout(1000) });
      const body = await response.text();
      const readiness = parseOptionalJSON(body);
      if (readiness?.state === targetState || (targetState === 'READY' && response.status === 200)) {
        return;
      }
      if (readiness?.state === 'FAILED') {
        terminalFailure = new Error(`backend readiness failed: ${body}`);
      }
      lastError = new Error(`readiness status ${response.status}: ${body}`);
    } catch (error) {
      lastError = error;
    }
    if (terminalFailure) {
      throw terminalFailure;
    }
    await new Promise((resolve) => setTimeout(resolve, 150));
  }
  throw new Error(`backend did not reach ${targetState} within ${timeoutMs}ms: ${lastError?.message ?? 'no response'}`);
}

export async function readSettingsToken(baseURL) {
  const response = await requestText(baseURL, '/');
  if (response.status !== 200) throw new Error(`settings token HTML status ${response.status}`);
  const match = response.body.match(/<meta name="codeatlas-settings-token" content="([A-Za-z0-9_-]+)">/u);
  if (!match || !match[1] || match[1] === '__CODEATLAS_SETTINGS_TOKEN__') {
    throw new Error('settings token meta tag is unavailable');
  }
  return match[1];
}

function parseOptionalJSON(value) {
  try {
    return JSON.parse(value);
  } catch {
    return null;
  }
}

async function requestJSON(baseURL, urlPath, options = {}) {
  const startedAt = performance.now();
  const response = await fetch(`${baseURL}${urlPath}`, {
    method: options.method ?? 'GET',
    headers: {
      ...(options.body ? { 'content-type': 'application/json' } : {}),
      ...(options.headers ?? {}),
    },
    body: options.body ? JSON.stringify(options.body) : undefined,
    signal: options.signal ?? AbortSignal.timeout(options.timeoutMs ?? 10_000),
  });
  const raw = await response.text();
  let body = null;
  if (raw !== '') {
    body = JSON.parse(raw);
  }
  return {
    status: response.status,
    body,
    headers: response.headers,
    durationMs: Math.round(performance.now() - startedAt),
  };
}

function requestJSONWithHost(baseURL, urlPath, options = {}) {
  const startedAt = performance.now();
  const target = new URL(baseURL);
  const encodedBody = options.body ? JSON.stringify(options.body) : '';
  return new Promise((resolve, reject) => {
    const request = http.request({
      host: target.hostname,
      port: target.port,
      path: urlPath,
      method: options.method ?? 'GET',
      headers: {
        ...(encodedBody ? { 'content-type': 'application/json', 'content-length': Buffer.byteLength(encodedBody) } : {}),
        ...(options.headers ?? {}),
        host: options.rawHost,
      },
    }, (response) => {
      const chunks = [];
      response.on('data', (chunk) => chunks.push(chunk));
      response.on('end', () => {
        const raw = Buffer.concat(chunks).toString('utf8');
        resolve({
          status: response.statusCode,
          body: raw ? JSON.parse(raw) : null,
          headers: new Headers(response.headers),
          durationMs: Math.round(performance.now() - startedAt),
        });
      });
    });
    request.once('error', reject);
    request.setTimeout(options.timeoutMs ?? 10_000, () => request.destroy(new Error('request timed out')));
    if (encodedBody) request.write(encodedBody);
    request.end();
  });
}

async function requestText(baseURL, urlPath, options = {}) {
  const startedAt = performance.now();
  const response = await fetch(`${baseURL}${urlPath}`, {
    method: options.method ?? 'GET',
    headers: options.headers,
    signal: options.signal ?? AbortSignal.timeout(options.timeoutMs ?? 10_000),
  });
  return {
    status: response.status,
    body: await response.text(),
    headers: response.headers,
    durationMs: Math.round(performance.now() - startedAt),
  };
}

async function freePort() {
  const server = net.createServer();
  await new Promise((resolve, reject) => {
    server.once('error', reject);
    server.listen(0, '127.0.0.1', resolve);
  });
  const port = server.address().port;
  await new Promise((resolve, reject) => server.close((error) => (error ? reject(error) : resolve())));
  return port;
}

function stopProcess(child) {
  return new Promise((resolve, reject) => {
    if (child.exitCode !== null) {
      resolve();
      return;
    }
    const timer = setTimeout(() => {
      child.kill('SIGKILL');
      reject(new Error('backend did not stop after SIGTERM'));
    }, 5_000);
    child.once('exit', () => {
      clearTimeout(timer);
      resolve();
    });
    child.kill('SIGTERM');
  });
}
