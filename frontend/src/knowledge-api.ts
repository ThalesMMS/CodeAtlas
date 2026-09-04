import { normalizeCodemap, normalizeDeepWikiCollection } from './knowledge-model';
import type { Codemap, DeepWikiCollection } from './knowledge-types';

export interface JobSnapshot {
  id: string;
  state: string;
  stage: string;
  message: string;
  progress?: number;
  resultArtifactId: string;
  error: string;
}

export async function loadDeepWiki(signal?: AbortSignal): Promise<DeepWikiCollection> {
  return normalizeDeepWikiCollection(await request('/api/deepwiki', { signal }));
}

export async function refreshDeepWiki(signal?: AbortSignal): Promise<JobSnapshot> {
  return submit('/api/deepwiki/refresh', {}, signal);
}

export async function generateCodemap(query: string, signal?: AbortSignal): Promise<JobSnapshot> {
  return submit('/api/codemaps', { query, maxNodes: 36 }, signal);
}

export async function waitForJob(initial: JobSnapshot, signal?: AbortSignal, onProgress?: (job: JobSnapshot) => void): Promise<JobSnapshot> {
  let job = initial;
  while (!isTerminal(job.state)) {
    onProgress?.(job);
    await delay(450, signal);
    job = normalizeJobSnapshot(await request(`/api/jobs/${encodeURIComponent(job.id)}`, { signal }));
  }
  onProgress?.(job);
  if (!isSuccess(job.state)) throw new Error(job.error || job.message || `Job ${job.state}`);
  return job;
}

export async function loadJobCodemap(job: JobSnapshot, signal?: AbortSignal): Promise<Codemap> {
  const payload = await request(`/api/jobs/${encodeURIComponent(job.id)}/result`, { signal });
  return normalizeCodemap(payload);
}

export async function loadSource(path: string, signal?: AbortSignal): Promise<{ path: string; content: string }> {
  const payload = asRecord(await request(`/api/file?path=${encodeURIComponent(path)}`, { signal }));
  const file = asRecord(payload.file);
  return { path: text(payload.path) || text(file.path) || path, content: text(payload.content) || text(file.content) };
}

async function submit(path: string, body: unknown, signal?: AbortSignal): Promise<JobSnapshot> {
  const payload = asRecord(await request(path, {
    method: 'POST', signal,
    headers: { 'Content-Type': 'application/json' },
    body: JSON.stringify(body),
  }));
  return normalizeJobSnapshot(payload);
}

async function request(path: string, init: RequestInit): Promise<unknown> {
  const response = await fetch(path, { credentials: 'same-origin', ...init });
  const contentType = response.headers.get('content-type') ?? '';
  const payload: unknown = contentType.includes('application/json') ? await response.json() : await response.text();
  if (!response.ok) {
    const record = asRecord(payload);
    const problem = asRecord(record.error);
    throw new Error(text(problem.message) || text(record.message) || (typeof payload === 'string' ? payload : '') || `${response.status} ${response.statusText}`);
  }
  return payload;
}

export function normalizeJobSnapshot(value: unknown): JobSnapshot {
  const envelope = asRecord(value);
  const nested = asRecord(envelope.job);
  const record = Object.keys(nested).length ? nested : envelope;
  const problem = asRecord(record.error);
  return {
    id: text(record.id), state: text(record.state), stage: text(record.stage), message: text(record.message),
    progress: normalizeProgress(record.progress),
    resultArtifactId: text(record.resultArtifactId),
    error: text(problem.message) || text(record.error),
  };
}

function normalizeProgress(value: unknown): number | undefined {
  if (typeof value === 'number' && Number.isFinite(value)) return clampPercent(value);
  const progress = asRecord(value);
  if (typeof progress.percent === 'number' && Number.isFinite(progress.percent)) return clampPercent(progress.percent);
  const completed = typeof progress.completed === 'number' ? progress.completed : 0;
  const total = typeof progress.total === 'number' ? progress.total : 0;
  return total > 0 ? clampPercent((completed / total) * 100) : undefined;
}

function clampPercent(value: number): number { return Math.max(0, Math.min(100, value)); }

function isSuccess(state: string): boolean { return ['succeeded', 'completed', 'success'].includes(state.toLowerCase()); }
function isTerminal(state: string): boolean { return isSuccess(state) || ['failed', 'canceled', 'cancelled', 'stale', 'error'].includes(state.toLowerCase()); }
function asRecord(value: unknown): Record<string, unknown> { return value && typeof value === 'object' && !Array.isArray(value) ? value as Record<string, unknown> : {}; }
function text(value: unknown): string { return typeof value === 'string' ? value : ''; }
function delay(ms: number, signal?: AbortSignal): Promise<void> {
  return new Promise((resolve, reject) => {
    const timer = window.setTimeout(resolve, ms);
    signal?.addEventListener('abort', () => { window.clearTimeout(timer); reject(new DOMException('Aborted', 'AbortError')); }, { once: true });
  });
}
