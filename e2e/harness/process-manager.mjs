import { spawn } from 'node:child_process';
import net from 'node:net';
import os from 'node:os';
import path from 'node:path';
import process from 'node:process';

import { resolveFakeLSPPath } from './lsp-launchers.mjs';

export async function startBackend({
  root,
  workspaceDir,
  providerBaseURL,
  embeddingBaseURL = providerBaseURL,
  llmAPIKey = 'sk-live-test-key',
  embeddingsAPIKey = llmAPIKey,
  enableEmbeddings = false,
  embeddingModel = enableEmbeddings ? 'fake-embedding' : '',
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
	const childEnv = { ...process.env };
	delete childEnv.CODEATLAS_MAX_FILE_BYTES;
	if (maxFileBytes) childEnv.CODEATLAS_MAX_FILE_BYTES = String(maxFileBytes);
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
      CODEATLAS_LLM_BASE_URL: providerBaseURL,
      CODEATLAS_LLM_API_KEY: llmAPIKey,
      CODEATLAS_LLM_MODEL: 'fake-codeatlas',
      CODEATLAS_ENABLE_EMBEDDINGS: String(enableEmbeddings),
      CODEATLAS_EMBEDDING_MODEL: embeddingModel,
      CODEATLAS_EMBEDDING_BASE_URL: embeddingBaseURL,
      CODEATLAS_EMBEDDINGS_API_KEY: embeddingsAPIKey,
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
    },
    stdio: ['ignore', 'pipe', 'pipe'],
  });

  child.stdout.setEncoding('utf8');
  child.stderr.setEncoding('utf8');
  child.stdout.on('data', (data) => logs.push(data));
  child.stderr.on('data', (data) => logs.push(data));

  try {
    await waitForReady(baseURL, child, timeoutMs);
  } catch (error) {
    await stopProcess(child).catch(() => {});
    error.logs = logs;
    throw error;
  }
  const readyAt = performance.now();

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
    close: async () => stopProcess(child),
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

async function waitForReady(baseURL, child, timeoutMs) {
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
      if (response.status === 200) {
        return;
      }
      const readiness = parseOptionalJSON(body);
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
  throw new Error(`backend did not become ready within ${timeoutMs}ms: ${lastError?.message ?? 'no response'}`);
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
