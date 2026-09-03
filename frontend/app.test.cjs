'use strict';

// Unit tests for the frontend readiness state machine's pure/injectable logic.
// Run with: node --test frontend/   (or: node --test frontend/app.test.cjs)

const test = require('node:test');
const assert = require('node:assert');
const app = require('./app.js');

test('backendStateToPhase maps every backend state', () => {
  assert.strictEqual(app.backendStateToPhase(200, 'READY'), 'ready');
  assert.strictEqual(app.backendStateToPhase(503, 'BOOTING'), 'booting');
  assert.strictEqual(app.backendStateToPhase(503, 'PROBING_CAPABILITIES'), 'probing');
  assert.strictEqual(app.backendStateToPhase(503, 'AWAITING_CONFIGURATION'), 'configuration');
  assert.strictEqual(app.backendStateToPhase(503, 'INDEXING'), 'indexing');
  assert.strictEqual(app.backendStateToPhase(503, 'GENERATING_REQUIRED_ARTIFACTS'), 'indexing');
  assert.strictEqual(app.backendStateToPhase(503, 'FAILED'), 'failed');
  assert.strictEqual(app.backendStateToPhase(503, 'SHUTTING_DOWN'), 'unreachable');
  assert.strictEqual(app.backendStateToPhase(503, 'WHATEVER'), 'booting');
  // READY reported with a non-200 status is not ready.
  assert.strictEqual(app.backendStateToPhase(503, 'READY'), 'booting');
});

test('shouldContinuePolling only for transient phases', () => {
  for (const phase of ['connecting', 'booting', 'probing', 'configuration', 'indexing', 'unreachable']) {
    assert.strictEqual(app.shouldContinuePolling(phase), true, phase);
  }
  for (const phase of ['ready', 'ready-loading', 'failed']) {
    assert.strictEqual(app.shouldContinuePolling(phase), false, phase);
  }
});

test('isDiagnosticPhase is true unless ready', () => {
  assert.strictEqual(app.isDiagnosticPhase('ready'), false);
  assert.strictEqual(app.isDiagnosticPhase('failed'), true);
  assert.strictEqual(app.isDiagnosticPhase('indexing'), true);
});

test('isAppNotReady detects the stable 503 envelope', () => {
  assert.strictEqual(app.isAppNotReady(503, { error: { code: 'APP_NOT_READY' } }), true);
  assert.strictEqual(app.isAppNotReady(503, { error: { code: 'OTHER' } }), false);
  assert.strictEqual(app.isAppNotReady(503, { foo: 1 }), false);
  assert.strictEqual(app.isAppNotReady(200, { error: { code: 'APP_NOT_READY' } }), false);
  assert.strictEqual(app.isAppNotReady(503, null), false);
});

function fakeResponse(status, body) {
  return { status, json: async () => body };
}

test('fetchReadiness maps READY', async () => {
  const result = await app.fetchReadiness(async () => fakeResponse(200, { state: 'READY' }));
  assert.strictEqual(result.reachable, true);
  assert.strictEqual(result.phase, 'ready');
});

test('fetchReadiness maps a transient backend state', async () => {
  const result = await app.fetchReadiness(async () => fakeResponse(503, { state: 'INDEXING', stage: 'indexing workspace' }));
  assert.strictEqual(result.phase, 'indexing');
  assert.strictEqual(result.body.stage, 'indexing workspace');
});

test('fetchReadiness maps FAILED with a sanitized cause', async () => {
  const body = { state: 'FAILED', error: { code: 'PROVIDER_UNREACHABLE', message: 'endpoint down' } };
  const result = await app.fetchReadiness(async () => fakeResponse(503, body));
  assert.strictEqual(result.phase, 'failed');
  assert.strictEqual(result.body.error.code, 'PROVIDER_UNREACHABLE');
});

test('fetchReadiness reports unreachable on transport failure', async () => {
  const result = await app.fetchReadiness(async () => {
    throw new Error('connection refused');
  });
  assert.strictEqual(result.reachable, false);
  assert.strictEqual(result.phase, 'unreachable');
});

test('loadMandatoryResources resolves when both succeed', async () => {
  const apiFn = async (path) => (path === '/api/stats' ? { files: 1 } : { name: 'root' });
  const result = await app.loadMandatoryResources(apiFn);
  assert.strictEqual(result.stats.files, 1);
  assert.strictEqual(result.tree.name, 'root');
});

test('See More partitions definition/usage code and derives deterministic See Also', () => {
  const explanation = {
    evidence: [
      { id: 'usage', relation: 'usage_site', symbolId: '', title: 'Usage', path: 'main.go', relevance: 1 },
      { id: 'constructor', relation: 'definition', symbolId: 'new', title: 'pkg::NewService', content: 'func NewService()', path: 'service.go', relevance: 1 },
      { id: 'order', relation: 'definition', symbolId: 'order', title: 'pkg::Order', content: 'type Order struct {}', path: 'model.go', relevance: 1 },
      { id: 'repository', relation: 'definition', symbolId: 'repository', title: 'pkg::Repository', content: 'type Repository interface {}', path: 'repository.go', relevance: 1 },
      { id: 'test', relation: 'tests', kind: 'test', symbolId: 'test', title: 'TestSubmit', path: 'service_test.go', relevance: 1 },
    ],
  };
  assert.deepStrictEqual(app.partitionExplainCodeEvidence(explanation, ['order', 'usage']), {
    definitions: ['order'], usages: ['usage'],
  });
  assert.deepStrictEqual(app.seeAlsoEvidence(explanation, 3).map((item) => item.id), ['repository', 'order', 'constructor']);
});

test('eventSeq parses monotonic event ids', () => {
  assert.strictEqual(app.eventSeq('evt-42'), 42);
  assert.strictEqual(app.eventSeq('evt-0'), 0);
  assert.strictEqual(app.eventSeq('bad'), null);
  assert.strictEqual(app.eventSeq(undefined), null);
});

test('eventGap detects skipped event sequences', () => {
  assert.strictEqual(app.eventGap(5, 'evt-6'), false); // contiguous
  assert.strictEqual(app.eventGap(5, 'evt-8'), true); // gap
  assert.strictEqual(app.eventGap(0, 'evt-9'), false); // no baseline yet
  assert.strictEqual(app.eventGap(5, 'evt-5'), false); // duplicate/out of order
});

test('job store ignores stale revisions and summarizes active work', () => {
  const store = app.createJobStore(4);
  app.applyJobEvent(store, {
    id: 'evt-1',
    type: 'job.queued',
    job: {
      id: 'job:1',
      type: 'codemap.generate',
      key: 'codemap:a',
      state: 'queued',
      revision: 1,
      stage: 'retrieve',
      progress: { indeterminate: true },
    },
  });
  app.applyJobEvent(store, {
    id: 'evt-2',
    type: 'job.progress',
    job: {
      id: 'job:1',
      type: 'codemap.generate',
      key: 'codemap:a',
      state: 'running',
      revision: 3,
      stage: 'generate',
      progress: { completed: 1, total: 2, unit: 'stage', percent: 50 },
    },
  });
  app.applyJobEvent(store, {
    id: 'evt-3',
    type: 'job.progress',
    job: {
      id: 'job:1',
      type: 'codemap.generate',
      key: 'codemap:a',
      state: 'running',
      revision: 2,
      stage: 'stale',
      progress: { completed: 0, total: 2, unit: 'stage', percent: 0 },
    },
  });

  assert.strictEqual(store.byId.get('job:1').stage, 'generate');
  assert.deepStrictEqual(app.activeJobs(store).map((job) => job.id), ['job:1']);
  assert.strictEqual(app.jobSummaryLabel(store), '1 active job');

  app.applyJobEvent(store, {
    id: 'evt-4',
    type: 'job.succeeded',
    job: {
      id: 'job:1',
      type: 'codemap.generate',
      key: 'codemap:a',
      state: 'succeeded',
      revision: 4,
      resultArtifactId: 'artifact-1',
      progress: { completed: 2, total: 2, unit: 'stage', percent: 100 },
    },
  });
  assert.strictEqual(app.activeJobs(store).length, 0);
  assert.strictEqual(app.jobSummaryLabel(store), '1 recent job');
});

test('no-op repository verification is presented without fake progress', () => {
  assert.deepStrictEqual(app.jobPresentation({
    id: 'job:no-op',
    type: 'repository.reindex',
    state: 'succeeded',
    stage: 'no_changes',
    message: 'index was already up to date',
    progress: {},
  }), {
    typeLabel: 'Index verification',
    detail: 'index was already up to date',
    noChanges: true,
    fillPercent: 0,
    progressAttributes: {
      role: 'status',
      ariaValueNow: null,
      ariaValueText: 'No changes',
    },
  });
});

test('codemap view model rejects invalid ids and dangling edges without partial render', () => {
  const duplicate = sampleCodemap({
    nodes: [
      node('node:a', 'Handler', 'api'),
      node('node:a', 'Service', 'domain'),
    ],
  });
  const duplicateResult = app.normalizeCodemapViewModel(duplicate);
  assert.strictEqual(duplicateResult.ok, false);
  assert.match(duplicateResult.errors.join('\n'), /duplicate node id node:a/);
  assert.strictEqual(duplicateResult.viewModel, null);

  const dangling = sampleCodemap({
    edges: [
      edge('edge:missing', 'node:a', 'node:missing', 'calls'),
    ],
  });
  const danglingResult = app.normalizeCodemapViewModel(dangling);
  assert.strictEqual(danglingResult.ok, false);
  assert.match(danglingResult.errors.join('\n'), /unknown target node:missing/);
  assert.strictEqual(danglingResult.viewModel, null);
});

test('codemap view model derives artifact metadata, groups and structured traces', () => {
  const result = app.normalizeCodemapViewModel(sampleCodemap());

  assert.strictEqual(result.ok, true);
  assert.strictEqual(result.viewModel.artifactId, 'artifact:codemap');
  assert.strictEqual(result.viewModel.artifactRevision, 3);
  assert.strictEqual(result.viewModel.inputSnapshotId, 'snapshot:input');
  assert.strictEqual(result.viewModel.contextPackHash, 'sha256:pack');
  assert.strictEqual(result.viewModel.status, 'stale');
  assert.deepStrictEqual(result.viewModel.staleReasons, ['snapshot_changed']);
  assert.strictEqual(result.viewModel.summary, 'A grounded summary of the request lifecycle.');
  assert.strictEqual(result.viewModel.motivation, 'Why the order flow exists and why its boundaries matter.');
  assert.strictEqual(result.viewModel.details, 'How request data crosses the handler, service, and repository.');
  assert.deepStrictEqual(result.viewModel.groups.map((group) => group.id), ['api', 'domain']);
  assert.deepStrictEqual(result.viewModel.flows[0].steps.map((step) => step.label), ['1a', '1b']);
  assert.strictEqual(result.viewModel.flows[0].steps[1].path, 'domain/service.go');
  assert.deepStrictEqual(result.viewModel.traceCandidates[0].steps.map((step) => step.id), ['trace:legacy:0:0']);
  assert.strictEqual(result.viewModel.diagram.version, 'mermaid/v1');
  assert.strictEqual(result.viewModel.diagram.sources[0].path, 'api/handler.go');
});

test('codemap narrative normalization preserves Markdown block boundaries', () => {
  const result = app.normalizeCodemapViewModel(sampleCodemap({
    overview: 'Summary.\r\n\r\n### Evidence\r\n\r\n- First\r\n- Second\u0007',
  }));

  assert.strictEqual(result.ok, true);
  assert.strictEqual(result.viewModel.overview, 'Summary.\n\n### Evidence\n\n- First\n- Second');
  assert.deepStrictEqual(app.markdownToHTMLBlocks(result.viewModel.overview), [
    '<p>Summary.</p>',
    '<h3>Evidence</h3>',
    '<ul>\n<li>First</li>\n<li>Second</li>\n</ul>',
  ]);
});

test('codemap rejects flow steps that invent node ids', () => {
  const result = app.normalizeCodemapViewModel(sampleCodemap({
    flows: [{ title: 'Broken', entryNodeId: 'node:a', steps: [{ label: '1a', nodeId: 'node:missing', text: 'Invented' }] }],
  }));
  assert.strictEqual(result.ok, false);
  assert.match(result.errors.join('\n'), /unknown node node:missing/);
});

test('codemap rejects Mermaid outside the deterministic backend subset', () => {
  const injected = sampleCodemap({ diagram: {
    version: 'mermaid/v1',
    kind: 'flowchart',
    source: 'graph TD\n  click n0 "javascript:alert(1)"',
    sources: [],
  } });
  const result = app.normalizeCodemapViewModel(injected);
  assert.strictEqual(result.ok, false);
  assert.match(result.errors.join('\n'), /deterministic Mermaid subset/);
  assert.strictEqual(app.isSafeMermaidSource('sequenceDiagram\n  participant p0 as main main.go\n  p0->>p1: calls Save'), true);
  assert.strictEqual(app.isSafeMermaidSource('graph TD\n  style n0 fill:red'), false);
});

test('codemap thresholds switch oversized maps to text mode before rendering', () => {
  const viewModel = app.normalizeCodemapViewModel(sampleCodemap({
    nodes: Array.from({ length: 151 }, (_, index) => node(`node:${index}`, `Node ${index}`, 'big')),
    edges: [],
    flows: [],
  })).viewModel;

  const decision = app.codemapRenderDecision(viewModel, app.CODEMAP_CONFIG);
  assert.strictEqual(decision.mode, 'text');
  assert.match(decision.reason, /151 nodes/);
});

test('codemap store rejects stale layout responses and preserves previous map on failure', () => {
  const first = app.normalizeCodemapViewModel(sampleCodemap({ title: 'First' })).viewModel;
  const second = app.normalizeCodemapViewModel(sampleCodemap({ title: 'Second' })).viewModel;
  const store = app.createCodemapStore();

  store.onReady(first);
  store.startGeneration({ requestId: 'req:2', inputHash: 'hash:2' });
  store.onReady(second);
  assert.strictEqual(store.snapshot().artifact.title, 'Second');

  const staleApplied = store.applyLayoutResult({
    requestId: 'req:old',
    inputHash: 'hash:old',
    nodes: [],
    edgeRoutes: [],
    bounds: { width: 1, height: 1 },
    warnings: [],
  });
  assert.strictEqual(staleApplied, false);
  assert.strictEqual(store.snapshot().layout, null);

  store.onFailed(new Error('worker down'));
  const failed = store.snapshot();
  assert.strictEqual(failed.status, 'failed');
  assert.strictEqual(failed.artifact.title, 'Second');
  assert.strictEqual(failed.previousArtifact.title, 'First');
});

test('codemap layout input is deterministic and strips source-shaped fields', () => {
  const viewModel = app.normalizeCodemapViewModel(sampleCodemap()).viewModel;
  const first = app.prepareCodemapLayoutInput(viewModel, { selectedTraceId: viewModel.traceCandidates[0].id });
  const second = app.prepareCodemapLayoutInput(viewModel, { selectedTraceId: viewModel.traceCandidates[0].id });

  assert.strictEqual(first.inputHash, second.inputHash);
  assert.strictEqual(first.nodes[0].labelLength, 'Handler'.length);
  assert.strictEqual(Object.prototype.hasOwnProperty.call(first.nodes[0], 'label'), false);
  assert.strictEqual(Object.prototype.hasOwnProperty.call(first.nodes[0], 'snippet'), false);
  assert.strictEqual(Object.prototype.hasOwnProperty.call(first.nodes[0], 'path'), false);
  assert.deepStrictEqual(first.nodes.map((item) => item.traceRank), [0, 1]);
});

test('codemap filters combine group, relation, confidence, internal and text search', () => {
  const viewModel = app.normalizeCodemapViewModel(sampleCodemap({
    nodes: [
      node('node:a', 'HTTP Handler', 'api'),
      node('node:b', 'Order Service', 'domain'),
      { ...node('node:c', 'Stripe', 'external'), kind: 'external', path: '', range: null },
    ],
    edges: [
      edge('edge:a-b', 'node:a', 'node:b', 'calls', 0.92),
      edge('edge:b-c', 'node:b', 'node:c', 'imports', 0.40),
    ],
  })).viewModel;

  const filtered = app.applyCodemapFilters(viewModel, {
    groups: ['domain'],
    kinds: [],
    edgeTypes: ['calls'],
    minConfidence: 0.8,
    internalOnly: true,
    traceId: '',
    searchText: 'service',
  });

  assert.deepStrictEqual([...filtered.visibleNodeIds], ['node:b']);
  assert.deepStrictEqual([...filtered.visibleEdgeIds], []);
  assert.deepStrictEqual(viewModel.nodes.map((item) => item.id), ['node:a', 'node:b', 'node:c']);
});

test('codemap filter-only updates preserve the structural renderer boundary', () => {
  const artifact = { artifactId: 'artifact:one' };
  const layout = { inputHash: 'layout:one' };
  const base = {
    artifact, layout, status: 'ready', viewMode: 'graph', job: null, error: null,
    layoutError: '', selectedNodeId: '', selectedEdgeId: '', selectedTraceId: '',
    selectedTraceStep: 0, collapsedGroups: new Set(), warnings: [],
    filters: { searchText: '' },
  };
  assert.strictEqual(app.isCodemapFilterOnlyUpdate(base, {
    ...base, filters: { searchText: 'service' },
  }), true);
  assert.strictEqual(app.isCodemapFilterOnlyUpdate(base, {
    ...base, selectedNodeId: 'node:a', filters: { searchText: 'service' },
  }), false);
  assert.strictEqual(app.isCodemapFilterOnlyUpdate(base, {
    ...base, layout: { inputHash: 'layout:two' }, filters: { searchText: 'service' },
  }), false);
});

test('codemap filter DOM patch toggles existing nodes and edges in place', () => {
  const filterElement = (dataset) => ({
    dataset,
    hidden: false,
    attributes: {},
    setAttribute(name, value) { this.attributes[name] = String(value); },
  });
  const nodeA = filterElement({ codemapFilterNodeId: 'node:a', codemapFilterInteractive: 'true' });
  const nodeB = filterElement({ codemapFilterNodeId: 'node:b', codemapFilterInteractive: 'true' });
  const edgeAB = filterElement({ codemapFilterEdgeId: 'edge:a-b', codemapFilterInteractive: 'true' });
  const visibleMeta = { textContent: '' };
  const count = { textContent: '' };
  const live = { textContent: '' };
  const slider = filterElement({});
  const internal = { checked: false };
  const search = { value: '' };
  const singles = new Map([
    ['.codemap-main', {}], ['.codemap-controls', {}],
    ['.codemap-visible-meta', visibleMeta], ['.codemap-count', count],
    ['.codemap-filter-live', live], ['.codemap-confidence', slider],
    ['.codemap-internal-only', internal], ['.codemap-search', search],
  ]);
  const root = {
    querySelector: (selector) => singles.get(selector) || null,
    querySelectorAll: (selector) => ({
      '[data-codemap-filter-node-id]': [nodeA, nodeB],
      '[data-codemap-filter-edge-id]': [edgeAB],
      '[data-codemap-filter-group-id]': [],
      '[data-codemap-filter-key]': [],
    })[selector] || [],
  };
  const snapshot = {
    artifact: { groups: [] }, viewMode: 'graph',
    filters: { minConfidence: 0.8, internalOnly: true, searchText: 'service' },
  };
  const filtered = {
    visibleNodeIds: new Set(['node:b']), visibleEdgeIds: new Set(),
    nodeCount: 1, edgeCount: 0, totalNodeCount: 2, totalEdgeCount: 1,
  };

  assert.strictEqual(app.applyCodemapFilterDOM(root, snapshot, filtered), true);
  assert.strictEqual(nodeA.hidden, true);
  assert.strictEqual(nodeA.attributes.tabindex, '-1');
  assert.strictEqual(nodeB.hidden, false);
  assert.strictEqual(nodeB.attributes.tabindex, '0');
  assert.strictEqual(edgeAB.hidden, true);
  assert.strictEqual(visibleMeta.textContent, '1/2 visible');
  assert.strictEqual(count.textContent, '1 nodes · 0 edges');
  assert.strictEqual(slider.value, '0.8');
  assert.strictEqual(internal.checked, true);
  assert.strictEqual(search.value, 'service');
});

test('debounced commits publish only the latest filter value', () => {
  const timers = new Map();
  let nextTimer = 0;
  const calls = [];
  const debounce = app.createDebouncedCommit((value) => calls.push(value), 120, {
    setTimeout: (callback) => {
      const id = ++nextTimer;
      timers.set(id, callback);
      return id;
    },
    clearTimeout: (id) => timers.delete(id),
  });
  debounce.push('s');
  debounce.push('service');
  assert.strictEqual(timers.size, 1);
  const [id, callback] = [...timers.entries()][0];
  timers.delete(id);
  callback();
  assert.deepStrictEqual(calls, ['service']);
  debounce.push('stale');
  debounce.cancel();
  assert.strictEqual(timers.size, 0);
  assert.deepStrictEqual(calls, ['service']);
});

test('codemap router validates encoded artifact, node and trace ids without leaking paths', () => {
  const parsed = app.parseCodemapRoute('/codemaps/artifact%3Aone?node=node%3Aa&trace=trace%3A1&path=/tmp/key');
  assert.deepStrictEqual(parsed, {
    artifactId: 'artifact:one',
    nodeId: 'node:a',
    traceId: 'trace:1',
    warning: '',
  });

  assert.strictEqual(app.codemapRouteFor({
    artifactId: 'artifact:one',
    selectedNodeId: 'node:a',
    selectedTraceId: 'trace:1',
    query: 'secret user text',
    path: '/tmp/source.go',
  }), '/codemaps/artifact%3Aone?node=node%3Aa&trace=trace%3A1');
});

test('codemap deep-link request and selection restore only ids present in the artifact', () => {
  assert.deepStrictEqual(app.codemapDeepLinkRequest('/codemaps/artifact%3Aone?node=node%3Aa&trace=trace%3A1'), {
    route: { artifactId: 'artifact:one', nodeId: 'node:a', traceId: 'trace:1', warning: '' },
    path: '/api/codemaps/artifact%3Aone',
  });
  assert.deepStrictEqual(app.codemapDeepLinkRequest('/codemaps/%E0%A4%A'), {
    route: { artifactId: '', nodeId: '', traceId: '', warning: 'invalid route' },
    path: '',
  });

  const viewModel = app.normalizeCodemapViewModel(sampleCodemap()).viewModel;
  const traceId = viewModel.traceCandidates[0].id;
  const selection = app.codemapRouteSelection(viewModel, { nodeId: 'node:b', traceId });
  assert.deepStrictEqual(selection, {
    selectedNodeId: 'node:b', selectedTraceId: traceId, warnings: [],
  });
  const store = app.createCodemapStore();
  store.onReady(viewModel, selection);
  assert.strictEqual(store.snapshot().selectedNodeId, 'node:b');
  assert.strictEqual(store.snapshot().selectedTraceId, traceId);
  assert.strictEqual(store.snapshot().filters.traceId, '');

  const missing = app.codemapRouteSelection(viewModel, { nodeId: 'node:missing', traceId: 'trace:missing' });
  assert.strictEqual(missing.selectedNodeId, '');
  assert.strictEqual(missing.selectedTraceId, viewModel.selectedTraceId);
  assert.strictEqual(missing.warnings.length, 2);
});

test('accessibility announcer dedupes repeated live messages by priority', () => {
  const announcer = app.createA11yAnnouncerState();
  assert.deepStrictEqual(app.nextA11yAnnouncement(announcer, 'Saved file', 'polite'), {
    target: 'polite',
    message: 'Saved file',
  });
  assert.strictEqual(app.nextA11yAnnouncement(announcer, 'Saved file', 'polite'), null);
  assert.deepStrictEqual(app.nextA11yAnnouncement(announcer, 'Save failed', 'assertive'), {
    target: 'assertive',
    message: 'Save failed',
  });
});

test('accessibility progress attributes do not report fake percent for indeterminate work', () => {
  assert.deepStrictEqual(app.progressA11yAttributes({ percent: 42, unit: 'stage' }), {
    role: 'progressbar',
    ariaValueNow: 42,
    ariaValueText: '42% stage',
  });
  assert.deepStrictEqual(app.progressA11yAttributes({ indeterminate: true, unit: 'stage' }), {
    role: 'progressbar',
    ariaValueNow: null,
    ariaValueText: 'Em andamento: stage',
  });
});

test('accessibility tab labels expose dirty, sync and diagnostics state as text', () => {
  const label = app.editorTabA11yLabel({
    path: 'web/app.ts',
    dirty: true,
    readOnly: false,
    overlay: { syncState: 'conflict' },
  }, { error: 2, warning: 1, info: 0, hint: 0 });

  assert.strictEqual(label, 'app.ts, unsaved changes, sync conflict, 2 errors, 1 warning');
});

test('accessibility shortcuts skip IME composition and editable text input', () => {
  assert.strictEqual(app.shouldHandleGlobalShortcut({
    isComposing: true,
    target: { tagName: 'BODY' },
  }), false);
  assert.strictEqual(app.shouldHandleGlobalShortcut({
    isComposing: false,
    target: { tagName: 'TEXTAREA' },
  }), false);
  assert.strictEqual(app.shouldHandleGlobalShortcut({
    isComposing: false,
    target: { tagName: 'BODY' },
  }), true);
  assert.strictEqual(app.shortcutDisplayLabel('focusSearch', 'darwin'), '⌘K');
  assert.strictEqual(app.shortcutDisplayLabel('focusSearch', 'linux'), 'Ctrl+K');
});

test('accessibility focus fallback prefers editor, workspace then first focusable', () => {
  assert.strictEqual(app.focusFallbackSelector({ hasActiveEditor: true, hasWorkspace: true }), '#editor-mount');
  assert.strictEqual(app.focusFallbackSelector({ hasActiveEditor: false, hasWorkspace: true }), '#file-tree');
  assert.strictEqual(app.focusFallbackSelector({ hasActiveEditor: false, hasWorkspace: false }), 'button, [href], input, select, textarea, [tabindex]:not([tabindex="-1"])');
});

test('isSaveConflictCode preserves local content for stable conflict codes', () => {
  assert.strictEqual(app.isSaveConflictCode('FILE_CHANGED_ON_DISK'), true);
  assert.strictEqual(app.isSaveConflictCode('DOCUMENT_VERSION_CONFLICT'), true);
  assert.strictEqual(app.isSaveConflictCode('DOCUMENT_CONFLICT_CHANGED'), true);
  assert.strictEqual(app.isSaveConflictCode('DOCUMENT_CONFLICT_NOT_FOUND'), false);
  assert.strictEqual(app.isSaveConflictCode('INTERNAL_ERROR'), false);
});

test('wikiStatusLabel covers every DeepWiki state', () => {
  assert.strictEqual(app.wikiStatusLabel('not_generated'), 'DeepWiki has not been generated yet');
  assert.strictEqual(app.wikiStatusLabel('generating'), 'Generating DeepWiki…');
  assert.strictEqual(app.wikiStatusLabel('stale'), 'DeepWiki is out of date');
  assert.strictEqual(app.wikiStatusLabel('failed'), 'Failed to generate DeepWiki');
  assert.strictEqual(app.wikiStatusLabel('ready'), 'DeepWiki is up to date');
});

test('DeepWiki navigation keeps only working manifest page links', () => {
  const pages = [
    { slug: 'overview', title: 'Overview' },
    { slug: '1-getting-started', title: 'Getting started' },
    { slug: '2-architecture-overview', title: 'Architecture overview' },
  ];
  const links = app.normalizedWikiLinks({
    slug: 'overview',
    relatedPages: [
      { slug: '1-getting-started', title: 'Getting started', relation: 'child' },
      { slug: 'missing', title: 'Missing', relation: 'child' },
      { slug: '1-getting-started', title: 'Duplicate', relation: 'related' },
      { slug: '2-architecture-overview', title: '', relation: 'child' },
    ],
  }, pages);
  assert.deepStrictEqual(links, [
    { slug: '1-getting-started', title: 'Getting started', relation: 'child' },
    { slug: '2-architecture-overview', title: 'Architecture overview', relation: 'child' },
  ]);
});

test('loadMandatoryResources rejects when a mandatory request fails (no partial UI)', async () => {
  const apiFn = async (path) => {
    if (path === '/api/tree') throw new Error('boom');
    return {};
  };
  await assert.rejects(() => app.loadMandatoryResources(apiFn), /boom/);
});

test('shouldApplyOverlayResponse drops stale overlay responses', () => {
  const tab = { overlay: { documentId: 'doc1', serverVersion: 5 } };
  const fresh = { ephemeral: true, documentId: 'doc1', documentVersion: 5 };
  // Matching document, version and latest sequence → apply.
  assert.strictEqual(app.shouldApplyOverlayResponse(tab, fresh, 3, 3), true);
  // A newer request started (seq < latest) → drop.
  assert.strictEqual(app.shouldApplyOverlayResponse(tab, fresh, 2, 3), false);
  // Version advanced under us → drop.
  assert.strictEqual(app.shouldApplyOverlayResponse(tab, { ephemeral: true, documentId: 'doc1', documentVersion: 4 }, 3, 3), false);
  // Different document → drop.
  assert.strictEqual(app.shouldApplyOverlayResponse(tab, { ephemeral: true, documentId: 'other', documentVersion: 5 }, 3, 3), false);
  // An open document expects an ephemeral answer; a persisted one is dropped.
  assert.strictEqual(app.shouldApplyOverlayResponse(tab, { ephemeral: false }, 3, 3), false);
  // A read-only tab (no overlay) accepts only a non-ephemeral response.
  assert.strictEqual(app.shouldApplyOverlayResponse({ overlay: null }, { ephemeral: false }, 1, 1), true);
  assert.strictEqual(app.shouldApplyOverlayResponse({ overlay: null }, { ephemeral: true }, 1, 1), false);
});

test('production position helpers use UTF-16 columns and CRLF line starts', () => {
  const content = 'const smile = "😀";\r\nnext';
  assert.deepStrictEqual(app.normalizeEditorPosition(content, app.editorPosition(1, 17)), app.editorPosition(1, 18));
  assert.strictEqual(app.editorPositionToOffset(content, app.editorPosition(1, 18)), 17);
  assert.deepStrictEqual(app.offsetToEditorPosition(content, 21), app.editorPosition(2, 1));
});

test('production wordRangeAtPosition gates whitespace and dedupes intra-word hover keys', () => {
  const content = 'const checkoutTotal = value + 1;';
  const first = app.wordRangeAtPosition(content, app.editorPosition(1, 8));
  const middle = app.wordRangeAtPosition(content, app.editorPosition(1, 16));
  const space = app.wordRangeAtPosition(content, app.editorPosition(1, 6));

  assert.deepStrictEqual(first, {
    start: app.editorPosition(1, 7),
    end: app.editorPosition(1, 20),
  });
  assert.deepStrictEqual(middle, first);
  assert.strictEqual(space, null);
});

test('hover line index resolves only the requested line across LF and CRLF', () => {
  const content = 'first\r\nconst checkoutTotal = value;\nlast';
  const starts = app.lineStartOffsets(content);

  assert.deepStrictEqual(starts, [0, 7, 36]);
  assert.deepStrictEqual(app.lineRangeAtLine(content, starts, 2), { start: 7, end: 35 });
  assert.strictEqual(app.lineRangeAtLine(content, starts, 4), null);
  assert.deepStrictEqual(
    app.wordRangeAtPosition(content, app.editorPosition(2, 10), starts),
    { start: app.editorPosition(2, 7), end: app.editorPosition(2, 20) },
  );
});

test('hover pointer throttle resolves only the latest move per interval', () => {
  let now = 0;
  let nextTimer = 1;
  const timers = new Map();
  const calls = [];
  const throttle = app.createLatestThrottle((value) => calls.push(value), 75, {
    now: () => now,
    setTimeout: (callback, delay) => {
      const id = nextTimer;
      nextTimer += 1;
      timers.set(id, { callback, delay });
      return id;
    },
    clearTimeout: (id) => timers.delete(id),
  });

  throttle.push('first');
  throttle.push('latest');
  assert.strictEqual(timers.size, 1);
  let job = [...timers.values()][0];
  assert.strictEqual(job.delay, 0);
  timers.clear();
  job.callback();
  assert.deepStrictEqual(calls, ['latest']);

  now = 10;
  throttle.push('middle');
  throttle.push('last');
  job = [...timers.values()][0];
  assert.strictEqual(job.delay, 65);
  timers.clear();
  now = 75;
  job.callback();
  assert.deepStrictEqual(calls, ['latest', 'last']);

  now = 80;
  throttle.push('cancelled');
  throttle.cancel();
  assert.strictEqual(timers.size, 0);
  assert.deepStrictEqual(calls, ['latest', 'last']);
});

test('hover card frame scheduler coalesces writes and supports cancellation', () => {
  const frames = [];
  const writes = [];
  const scheduler = app.createFrameCoalescer((value) => writes.push(value), (callback) => frames.push(callback));
  scheduler.push({ x: 1 });
  scheduler.push({ x: 2 });
  scheduler.push({ x: 3 });
  assert.strictEqual(frames.length, 1);
  frames.shift()();
  assert.deepStrictEqual(writes, [{ x: 3 }]);

  scheduler.push({ x: 4 });
  scheduler.cancel();
  frames.shift()();
  assert.deepStrictEqual(writes, [{ x: 3 }]);
});

test('visible hover card keeps its original anchor while the pointer approaches it', () => {
  const current = { clientX: 360, clientY: 330 };
  const approaching = { clientX: 390, clientY: 350 };

  assert.deepStrictEqual(app.hoverAnchorForPointer(current, approaching, true, true), current);
  assert.deepStrictEqual(app.hoverAnchorForPointer(current, approaching, false, true), approaching);
  assert.deepStrictEqual(app.hoverAnchorForPointer(current, approaching, true, false), approaching);
});

test('hover card positioning uses rendered height to keep actions inside the editor', () => {
  const shell = { left: 306, top: 96, width: 487, height: 598 };
  const position = app.hoverCardPosition(385, 332, shell, { width: 420, height: 407 });

  assert.deepStrictEqual(position, { left: 57, top: 10 });
  assert.ok(position.top + 407 <= shell.height - 10);
});

test('hover card keeps one reserved position while loading content expands', () => {
  const shell = { left: 306, top: 96, width: 487, height: 598 };
  const loading = app.stableHoverCardPosition(null, 385, 332, shell, { width: 420, height: 112 });
  const ready = app.stableHoverCardPosition(loading, 385, 332, shell, { width: 420, height: 407 });

  assert.deepStrictEqual(
    { left: ready.left, top: ready.top },
    { left: loading.left, top: loading.top },
  );
  assert.ok(ready.maxHeight >= 100);
  assert.ok(ready.top + ready.maxHeight <= shell.height - 10);
});

test('pointer misses retain the resolved target only while its hover card is visible', () => {
  const target = { key: 'main.go:15:6:16', documentId: 'doc-1', line: 15, column: 7 };

  assert.strictEqual(app.hoverTargetAfterPointerMiss(target, true), target);
  assert.strictEqual(app.hoverTargetAfterPointerMiss(target, false), null);
});

test('document release waits for DELETE and pagehide preserves dirty overlays for reclaim', async () => {
  const tab = {
    path: 'cmd/api/main.go',
    overlay: { documentId: 'doc-1', leaseId: 'lease-1' },
  };
  const calls = [];
  const fetchFn = async (path, options) => {
    calls.push({ path, options });
    return { ok: true, status: 204 };
  };

  await app.releaseDocumentLease(app.documentLeaseForTab(tab), { fetchFn });
  await Promise.all(app.releaseDocumentsOnPageHide([tab, { overlay: null }], fetchFn));

  assert.strictEqual(calls[0].path, '/api/documents/doc-1');
  assert.deepStrictEqual(calls[0].options, {
    method: 'DELETE', headers: { 'X-Document-Lease': 'lease-1' }, keepalive: false,
  });
  assert.strictEqual(calls[1].path, '/api/documents/doc-1');
  assert.strictEqual(calls[1].options.keepalive, true);
});

test('tracked document leases reclaim same-session dirty content and forget expired leases', async () => {
  const values = new Map();
  const storage = {
    getItem: (key) => values.get(key) || null,
    setItem: (key, value) => values.set(key, value),
    removeItem: (key) => values.delete(key),
  };
  const first = { path: 'cmd/api/main.go', overlay: { documentId: 'doc-1', leaseId: 'lease-1' } };
  const second = { path: 'internal/order/http.go', overlay: { documentId: 'doc-2', leaseId: 'lease-2' } };
  app.trackDocumentLease(first, storage);
  app.trackDocumentLease(second, storage);
  app.trackDocumentLease(first, storage);
  assert.deepStrictEqual(app.readTrackedDocumentLeases(storage), [
    { documentId: 'doc-2', leaseId: 'lease-2', path: 'internal/order/http.go' },
    { documentId: 'doc-1', leaseId: 'lease-1', path: 'cmd/api/main.go' },
  ]);

  const requests = [];
  const reclaimed = await app.reclaimTrackedDocument('cmd/api/main.go', {
    storage,
    request: async (path, options) => {
      requests.push({ path, options });
      return {
        documentId: 'doc-1', path: 'cmd/api/main.go', content: 'dirty', baseContent: 'clean',
        dirty: true, version: 2, leaseId: 'lease-rotated',
      };
    },
  });
  assert.strictEqual(reclaimed.leaseId, 'lease-rotated');
  assert.strictEqual(reclaimed.content, 'dirty');
  assert.strictEqual(reclaimed.baseContent, 'clean');
  assert.deepStrictEqual(requests[0], {
    path: '/api/documents/doc-1/reclaim',
    options: {
      method: 'POST', headers: { 'X-Document-Lease': 'lease-1' }, expectedErrorCodes: ['DOCUMENT_NOT_FOUND'],
    },
  });

  const missing = await app.reclaimTrackedDocument('internal/order/http.go', {
    storage,
    request: async () => {
      const error = new Error('missing');
      error.code = 'DOCUMENT_NOT_FOUND';
      throw error;
    },
  });
  assert.strictEqual(missing, null);
  assert.deepStrictEqual(app.readTrackedDocumentLeases(storage), [
    { documentId: 'doc-1', leaseId: 'lease-1', path: 'cmd/api/main.go' },
  ]);
});

test('page handoff makes a closed browser tab reclaimable without exposing active tabs', async () => {
  const createStorage = () => {
    const values = new Map();
    return {
      getItem: (key) => values.get(key) || null,
      setItem: (key, value) => values.set(key, value),
      removeItem: (key) => values.delete(key),
    };
  };
  const oldSession = createStorage();
  const newSession = createStorage();
  const handoffStorage = createStorage();
  const tab = {
    path: 'cmd/api/main.go',
    overlay: { documentId: 'doc-1', leaseId: 'lease-1' },
  };
  app.trackDocumentLease(tab, oldSession);
  app.prepareDocumentLeaseHandoff([tab], { storage: handoffStorage, now: 1000 });

  assert.deepStrictEqual(app.readDocumentLeaseHandoffs(handoffStorage, 1001), [
    { documentId: 'doc-1', leaseId: 'lease-1', path: 'cmd/api/main.go', handedOffAt: 1000 },
  ]);

  const requests = [];
  const reclaimed = await app.reclaimTrackedDocument('cmd/api/main.go', {
    storage: newSession,
    handoffStorage,
    now: 1001,
    request: async (path, options) => {
      requests.push({ path, options });
      return {
        documentId: 'doc-1', path: 'cmd/api/main.go', content: 'clean', baseContent: 'clean',
        dirty: false, version: 1, leaseId: 'lease-rotated',
      };
    },
  });

  assert.strictEqual(reclaimed.leaseId, 'lease-rotated');
  assert.deepStrictEqual(requests, [{
    path: '/api/documents/doc-1/reclaim',
    options: {
      method: 'POST', headers: { 'X-Document-Lease': 'lease-1' }, expectedErrorCodes: ['DOCUMENT_NOT_FOUND'],
    },
  }]);
  assert.deepStrictEqual(app.readDocumentLeaseHandoffs(handoffStorage, 1001), []);
});

test('document heartbeat renews every live writable lease without touching read-only tabs', async () => {
  const calls = [];
  const tab = {
    path: 'main.go',
    overlay: { documentId: 'doc-1', leaseId: 'lease-1', serverVersion: 7 },
  };
  const results = await app.renewDocumentLeases([
    tab,
    { path: 'README.md', overlay: null },
  ], async (path, options) => {
    calls.push({ path, options });
    return { documentId: 'doc-1' };
  });
  assert.strictEqual(results[0].tab, tab);
  assert.strictEqual(results[0].documentId, 'doc-1');
  assert.strictEqual(results[0].leaseId, 'lease-1');
  assert.strictEqual(results[0].settled.status, 'fulfilled');
  assert.deepStrictEqual(calls, [{
	path: '/api/documents/doc-1/renew',
	options: { method: 'POST', headers: { 'X-Document-Lease': 'lease-1' } },
  }]);
});

test('document heartbeat ignores results for reordered tabs and rotated leases', async () => {
  const original = {
    path: 'main.go',
    overlay: { documentId: 'doc-1', leaseId: 'lease-1', syncState: 'idle' },
  };
  const results = await app.renewDocumentLeases([original], async () => {
    throw new Error('expired old lease');
  });
  const other = {
    path: 'other.go',
    overlay: { documentId: 'doc-2', leaseId: 'lease-2', syncState: 'idle' },
  };

  assert.deepStrictEqual(app.applyDocumentLeaseRenewalResults([other], results), []);
  assert.strictEqual(original.overlay.syncState, 'idle');
  assert.strictEqual(other.overlay.syncState, 'idle');

  original.overlay = { documentId: 'doc-1', leaseId: 'lease-rotated', syncState: 'idle' };

  assert.deepStrictEqual(app.applyDocumentLeaseRenewalResults([other, original], results), []);
  assert.strictEqual(other.overlay.syncState, 'idle');
  assert.strictEqual(original.overlay.syncState, 'idle');
});

test('document heartbeat marks failed leases disconnected and recovery preserves local edits', async () => {
  const tab = {
    documentId: 'model-1', path: 'main.go', content: 'local edit', dirty: true,
    overlay: { documentId: 'doc-old', leaseId: 'lease-old', serverVersion: 2, syncState: 'idle' },
  };
  app.applyDocumentLeaseRenewalResults([tab], [{
    tab,
    documentId: 'doc-old',
    leaseId: 'lease-old',
    settled: { status: 'rejected', reason: new Error('expired') },
  }]);
  assert.strictEqual(tab.overlay.syncState, 'disconnected');

  let tracked = false;
  let flushed = false;
  await app.recoverDisconnectedDocumentLease(tab, {
    reclaim: async () => null,
    open: async () => ({
      documentId: 'doc-new', leaseId: 'lease-new', version: 1,
      content: 'disk', contentHash: 'sha256:disk', baseContentHash: 'sha256:disk', baseSnapshotId: 'snapshot', state: 'clean',
    }),
    track: () => { tracked = true; },
    flush: async (current) => {
      flushed = true;
      current.overlay.acknowledgedEditRevision = current.overlay.localEditRevision;
    },
  });
  assert.strictEqual(tab.documentId, 'model-1');
  assert.strictEqual(tab.overlay.documentId, 'doc-new');
  assert.strictEqual(tab.overlay.localEditRevision, 1);
  assert.strictEqual(tracked, true);
  assert.strictEqual(flushed, true);
});

test('delegatedItem resolves nested targets only inside the owning container', () => {
  const item = { dataset: { id: 'target' } };
  const target = { closest: (selector) => (selector === '[data-id]' ? item : null) };
  const owner = { contains: (candidate) => candidate === item };
  assert.strictEqual(app.delegatedItem({ target }, owner, '[data-id]'), item);
  assert.strictEqual(app.delegatedItem({ target }, { contains: () => false }, '[data-id]'), null);
  assert.strictEqual(app.delegatedItem({ target: {} }, owner, '[data-id]'), null);
});

test('file tree selection updates two items without rebuilding collapsed directories', () => {
  const treeItem = (path, active = false) => {
    const classes = new Set(['file-button', ...(active ? ['active'] : [])]);
    return {
      path,
      tabIndex: active ? 0 : -1,
      attributes: { 'aria-selected': active ? 'true' : 'false' },
      classList: {
        add: (value) => classes.add(value),
        remove: (value) => classes.delete(value),
        contains: (value) => classes.has(value),
      },
      setAttribute(name, value) { this.attributes[name] = String(value); },
    };
  };
  const previous = treeItem('internal/old.go', true);
  const next = treeItem('internal/new.go');
  const manuallyCollapsed = { open: false };
  const byPath = new Map([[previous.path, previous], [next.path, next]]);
  const root = {};

  const result = app.updateFileTreeSelection(root, previous.path, next.path, {
    findItem: (_root, path) => byPath.get(path) || null,
    isVisible: () => true,
    currentRoving: () => previous,
  });

  assert.strictEqual(result.previous, previous);
  assert.strictEqual(result.next, next);
  assert.strictEqual(previous.classList.contains('active'), false);
  assert.strictEqual(previous.attributes['aria-selected'], 'false');
  assert.strictEqual(previous.tabIndex, -1);
  assert.strictEqual(next.classList.contains('active'), true);
  assert.strictEqual(next.attributes['aria-selected'], 'true');
  assert.strictEqual(next.tabIndex, 0);
  assert.strictEqual(manuallyCollapsed.open, false);

  const hidden = treeItem('collapsed/hidden.go');
  byPath.set(hidden.path, hidden);
  app.updateFileTreeSelection(root, next.path, hidden.path, {
    findItem: (_root, path) => byPath.get(path) || null,
    isVisible: (item) => item !== hidden,
    currentRoving: () => next,
  });
  assert.strictEqual(next.tabIndex, 0, 'visible roving item should remain reachable');
  assert.strictEqual(hidden.classList.contains('active'), true);
  assert.strictEqual(hidden.attributes['aria-selected'], 'true');
  assert.strictEqual(hidden.tabIndex, -1, 'selection hidden by a collapsed directory must not be tabbable');
});

test('scheduleBatches renders bounded chunks and cancellation drops pending work', () => {
  const frames = [];
  const batches = [];
  const cancel = app.scheduleBatches([1, 2, 3, 4, 5], 2, (batch) => batches.push(batch), (callback) => frames.push(callback));
  assert.deepStrictEqual(batches, [[1, 2]]);
  assert.strictEqual(frames.length, 1);
  frames.shift()();
  assert.deepStrictEqual(batches, [[1, 2], [3, 4]]);
  cancel();
  frames.shift()();
  assert.deepStrictEqual(batches, [[1, 2], [3, 4]]);
});

test('markdown block rendering preserves safe top-level list boundaries', () => {
  const markdown = '# Title\n\n- one\n- two\n\nParagraph with **bold**.\n\n```\n<unsafe>\n```';
  const blocks = app.markdownToHTMLBlocks(markdown);
  assert.deepStrictEqual(blocks, [
    '<h1>Title</h1>',
    '<ul>\n<li>one</li>\n<li>two</li>\n</ul>',
    '<p>Paragraph with <strong>bold</strong>.</p>',
    '<pre><code>&lt;unsafe&gt;</code></pre>',
  ]);
  assert.strictEqual(app.markdownToHTML(markdown), blocks.join('\n'));
});

test('markdown block rendering produces accessible grounded tables', () => {
  const html = app.markdownToHTML('| Field | Type |\n| --- | --- |\n| ID | string |');
  assert.match(html, /<table>/);
  assert.match(html, /<th>Field<\/th>/);
  assert.match(html, /<td>ID<\/td>/);
  assert.doesNotMatch(html, /<p>\| Field/);
});

test('markdown rendering accepts only internal source links and trusted details blocks', () => {
  const markdown = [
    '<details>',
    '<summary>Relevant source files</summary>',
    '',
    '- [service file](internal/order/service%20file.go)',
    '',
    '</details>',
    '',
    'Fact ([internal/order/service.go:22-24](internal/order/service.go#L22-L24))',
    '[unsafe](javascript:alert)',
  ].join('\n');
  const html = app.markdownToHTML(markdown);
  assert.match(html, /^<details><summary>Relevant source files<\/summary>/);
  assert.match(html, /class="code-reference" href="internal\/order\/service.go#L22-L24"/);
  assert.match(html, /data-path="internal\/order\/service.go" data-line="22" data-end-line="24"/);
  assert.ok(!html.includes('href="javascript:alert"'));
  assert.match(html, /\[unsafe\]\(javascript:alert\)/);
});

test('source link helpers encode paths and reject traversal or external targets', () => {
  assert.strictEqual(
    app.sourceHref('web/src/api client.ts', { start: { line: 7 }, end: { line: 9 } }),
    'web/src/api%20client.ts#L7-L9',
  );
  assert.deepStrictEqual(app.parseSourceTarget('web/src/api%20client.ts#L7-L9'), {
    path: 'web/src/api client.ts', line: 7, endLine: 9,
  });
  assert.strictEqual(app.parseSourceTarget('../secret#L1'), null);
  assert.strictEqual(app.parseSourceTarget('/etc/passwd#L1'), null);
  assert.strictEqual(app.parseSourceTarget('javascript:alert'), null);
});

test('production explain cache keys separate hover and see_more responses', () => {
  const base = {
    symbol: { id: 'sym:v1:checkout', occurrenceId: 'occ:v1:checkout' },
    provider: 'test',
    providerInfo: { id: 'test', model: 'default' },
    viewHash: 'sha256:view',
    documentVersion: 2,
    policyVersion: 'hover.v1',
    promptVersion: 'hover-v2',
  };
  const hoverKey = app.explainCacheKey({ ...base, feature: 'hover' });
  const seeMoreKey = app.explainCacheKey({ ...base, feature: 'see_more', policyVersion: 'see_more.v1', promptVersion: 'see-more-v2' });
  const cache = app.createExplainCache(1);

  assert.notStrictEqual(hoverKey, seeMoreKey);
  cache.set(hoverKey, { feature: 'hover' });
  cache.set(seeMoreKey, { feature: 'see_more' });
  assert.strictEqual(cache.get(hoverKey), null);
  assert.deepStrictEqual(cache.get(seeMoreKey), { feature: 'see_more' });
  cache.setForRequest('request:see-more', seeMoreKey, { feature: 'see_more' });
  assert.deepStrictEqual(cache.getByRequest('request:see-more'), { feature: 'see_more' });
});

test('semantic-only explain cache keys include occurrence identity without minting symbol ids', () => {
  const base = {
    feature: 'hover',
    provider: 'test',
    snapshotId: 'sha256:snapshot',
    policyVersion: 'hover.v3',
    promptVersion: 'hover-v3',
  };
  const repository = app.explainCacheKey({
    ...base,
    symbol: {
      id: '', path: 'cmd/api/main.go', name: 'repository', kind: 'variable', signature: 'var repository *order.MemoryRepository',
      range: { start: { line: 11, column: 2 }, end: { line: 11, column: 12 } },
    },
  });
  const handleFunc = app.explainCacheKey({
    ...base,
    symbol: {
      id: '', path: 'cmd/api/main.go', name: 'HandleFunc', kind: 'method', signature: 'func (*http.ServeMux).HandleFunc(...)',
      range: { start: { line: 16, column: 6 }, end: { line: 16, column: 16 } },
    },
  });
  assert.notStrictEqual(repository, handleFunc);
});

test('see_more payload keeps the exact position and omits an empty persisted target', async () => {
  const payload = await app.buildExplainPayloadForTab(
    { path: 'cmd/api/main.go', overlay: null },
    { line: 16, column: 10 },
    'see_more',
    { symbolId: '', occurrenceId: '' },
  );
  assert.deepStrictEqual(payload, {
    feature: 'see_more',
    path: 'cmd/api/main.go',
    position: { line: 16, column: 10, encoding: 'utf-16' },
  });
});

test('safe resolution errors never enable See More', () => {
  const explanation = { symbol: { name: 'repository', kind: 'variable' } };
  assert.strictEqual(app.canOpenSeeMore(explanation), true);
  assert.strictEqual(app.canOpenSeeMore(explanation, { stale: true }), false);
  assert.strictEqual(app.canOpenSeeMore(explanation, { resolutionError: { code: 'SYMBOL_NOT_FOUND' } }), false);
  assert.strictEqual(app.canOpenSeeMore(explanation, { resolutionError: { code: 'SYMBOL_AMBIGUOUS' } }), false);
});

test('production normalizedExplainResult never falls back to free markdown', () => {
  const result = app.normalizedExplainResult({
    summary: 'legacy summary',
    markdown: '<img src=x onerror=alert(1)>',
    result: {
      summary: 'structured summary',
      codeEvidenceIds: ['ev:1'],
      observations: [{ text: 'fact', evidenceIds: ['ev:1'] }],
      inferences: [],
      uncertainties: [],
      changeImpact: [],
    },
  });

  assert.strictEqual(result.summary, 'structured summary');
  assert.deepStrictEqual(result.codeEvidenceIds, ['ev:1']);
  assert.deepStrictEqual(result.observations, [{ text: 'fact', evidenceIds: ['ev:1'] }]);
});

test('markdown code fences preserve embedded shorter backtick runs and language', () => {
  const code = 'const marker = "```";\nrun()';
  const html = app.markdownToHTML(`**src/app.ts:1-2**\n\n\`\`\`\`typescript\n${code}\n\`\`\`\``);
  assert.match(html, /<pre><code class="language-typescript">/);
  assert.ok(html.includes('const marker = &quot;```&quot;;\nrun()'));
});

test('production shouldApplyExplainResponse checks feature and overlay freshness', () => {
  const tab = { path: 'web/app.ts', overlay: { documentId: 'doc-1', serverVersion: 4 } };
  const response = {
    feature: 'hover',
    ephemeral: true,
    documentId: 'doc-1',
    documentVersion: 4,
    symbol: { path: 'web/app.ts' },
  };

  assert.strictEqual(app.shouldApplyExplainResponse(tab, response, 7, 7, 'hover'), true);
  assert.strictEqual(app.shouldApplyExplainResponse(tab, { ...response, feature: 'see_more' }, 7, 7, 'hover'), false);
  assert.strictEqual(app.shouldApplyExplainResponse(tab, { ...response, documentVersion: 3 }, 7, 7, 'hover'), false);
});

test('production shouldApplyExplainResponse accepts hover definitions from another file', () => {
  const tab = { path: 'cmd/api/main.go', overlay: { documentId: 'doc-main', serverVersion: 2 } };
  const response = {
    feature: 'hover',
    ephemeral: true,
    documentId: 'doc-main',
    documentVersion: 2,
    symbol: { name: 'NewHandler', path: 'internal/order/http.go' },
  };

  assert.strictEqual(app.shouldApplyExplainResponse(tab, response, 11, 11, 'hover'), true);
});

test('production navigation payload carries kind, target ids and acknowledged overlay version', () => {
  const payload = app.buildNavigationRequestPayload('incoming_calls', {
    path: 'web/app.ts',
    overlay: { documentId: 'doc-1', serverVersion: 8 },
  }, app.editorPosition(4, 12), {
    symbolId: 'sym:v1:submit',
    occurrenceId: 'occ:v1:submit',
  }, 250);

  assert.deepStrictEqual(payload, {
    kind: 'incoming_calls',
    path: 'web/app.ts',
    documentId: 'doc-1',
    documentVersion: 8,
    position: app.editorPosition(4, 12),
    symbolId: 'sym:v1:submit',
    occurrenceId: 'occ:v1:submit',
    limit: 250,
  });
});

test('production navigation history dedupes nearby cursor movement and clears forward', () => {
  const history = app.createNavigationHistory(3);
  app.pushNavigationLocation(history, { path: 'a.ts', range: pointRange(1, 1), snapshotId: 's1' });
  app.pushNavigationLocation(history, { path: 'a.ts', range: pointRange(4, 1), snapshotId: 's1' });
  app.navigateHistoryBack(history);
  app.pushNavigationLocation(history, { path: 'b.ts', range: pointRange(1, 1), snapshotId: 's1' });
  app.pushNavigationLocation(history, { path: 'b.ts', range: pointRange(2, 1), snapshotId: 's1' });

  assert.strictEqual(app.canNavigateForward(history), false);
  assert.deepStrictEqual(history.entries.map((item) => item.path), ['a.ts', 'b.ts']);
  assert.strictEqual(history.index, 1);
});

test('production shouldApplyNavigationResponse drops stale or wrong-kind navigation results', () => {
  const response = { kind: 'references', documentId: 'doc-1', documentVersion: 3 };
  assert.strictEqual(app.shouldApplyNavigationResponse(response, {
    sequence: 5, latestSequence: 5, kind: 'references', documentId: 'doc-1', documentVersion: 3,
  }), true);
  assert.strictEqual(app.shouldApplyNavigationResponse(response, {
    sequence: 4, latestSequence: 5, kind: 'references', documentId: 'doc-1', documentVersion: 3,
  }), false);
  assert.strictEqual(app.shouldApplyNavigationResponse(response, {
    sequence: 5, latestSequence: 5, kind: 'definition', documentId: 'doc-1', documentVersion: 3,
  }), false);
  assert.strictEqual(app.shouldApplyNavigationResponse(response, {
    sequence: 5, latestSequence: 5, kind: 'references', documentId: 'doc-1', documentVersion: 4,
  }), false);
});

test('production diagnostics helpers count, decorate and virtualize problems', () => {
  const response = {
    documentId: 'doc-1',
    documentVersion: 3,
    diagnostics: [
      diagnostic('diag:warn', 'warning', 9),
      diagnostic('diag:error', 'error', 2),
      diagnostic('diag:info', 'info', 5),
    ],
  };

  assert.deepStrictEqual(app.diagnosticSeverityCounts(response.diagnostics), { error: 1, warning: 1, info: 1, hint: 0 });
  const decorations = app.diagnosticsToDecorations({ diagnostics: [response.diagnostics[1]] });
  assert.strictEqual(decorations[0].className, 'diagnostic-marker diagnostic-error');
  assert.match(decorations[0].title, /tree-sitter/);
  const rows = app.visibleProblemRows(response, { offset: 0, limit: 2 });
  assert.deepStrictEqual(rows.map((row) => row.diagnostic.diagnosticId), ['diag:error', 'diag:warn']);
  const emptyRows = app.visibleProblemRows(response, { offset: 0, limit: 2, severity: 'hint' });
  assert.deepStrictEqual(emptyRows, []);
});

test('production semantic tokens require exact document/version/hash/session and use canonical classes', () => {
  const response = {
    legendVersion: 'codeatlas-semantic-tokens/v1',
    documentId: 'doc-1', documentVersion: 3, contentHash: 'sha256:abc', providerSession: 'typescript-lsp:2',
    semanticCoverage: { providerState: 'available', provider: 'typescript-lsp' },
    tokens: [{
      range: { start: { line: 2, column: 7, encoding: 'utf-16' }, end: { line: 2, column: 12, encoding: 'utf-16' } },
      tokenType: 'class', modifiers: ['declaration', 'readonly'],
    }],
  };
  const check = {
    sequence: 4, latestSequence: 4, documentId: 'doc-1', documentVersion: 3, contentHash: 'sha256:abc',
  };
  assert.strictEqual(app.shouldApplySemanticTokensResponse(response, check), true);
  assert.strictEqual(app.shouldApplySemanticTokensResponse({ ...response, legendVersion: 'future/v2' }, check), false);
  assert.strictEqual(app.shouldApplySemanticTokensResponse({ ...response, contentHash: 'sha256:stale' }, check), false);
  assert.strictEqual(app.shouldApplySemanticTokensResponse({ ...response, providerSession: '' }, check), false);
  assert.strictEqual(app.shouldApplySemanticTokensResponse(response, { ...check, latestSequence: 5 }), false);

  const decorations = app.semanticTokensToDecorations(response);
  assert.equal(decorations.length, 1);
  assert.match(decorations[0].className, /semantic-type-class/);
  assert.match(decorations[0].className, /semantic-modifier-readonly/);
});

test('successful semantic responses are ignored after document identity advances', () => {
  const tab = {
    documentId: 'tab-1',
    overlay: { documentId: 'doc-1', serverVersion: 4, contentHash: 'sha256:new' },
  };
  const sequences = new Map([['tab-1', 2]]);
  assert.strictEqual(app.isCurrentDocumentRequest(tab, sequences, {
    sequence: 2, documentId: 'doc-1', documentVersion: 4, contentHash: 'sha256:new',
  }), true);
  assert.strictEqual(app.isCurrentDocumentRequest(tab, sequences, {
    sequence: 1, documentId: 'doc-1', documentVersion: 3, contentHash: 'sha256:old',
  }), false);

  const staleCheck = {
    sequence: 2, latestSequence: 2, documentId: 'doc-old', documentVersion: 3, contentHash: 'sha256:old',
  };
  assert.strictEqual(app.shouldApplySemanticTokensResponse({
    legendVersion: 'codeatlas-semantic-tokens/v1',
    documentId: 'doc-old', documentVersion: 3, contentHash: 'sha256:old',
    semanticCoverage: { providerState: 'disabled' }, tokens: [],
  }, staleCheck), true, 'the response itself is internally consistent');
  assert.strictEqual(app.shouldApplyDiagnosticsResponse({
    documentId: 'doc-old', documentVersion: 3, contentHash: 'sha256:old',
    semanticCoverage: { lsp: 'disabled' }, diagnostics: [],
  }, staleCheck), true, 'the response itself is internally consistent');
  assert.strictEqual(app.isCurrentDocumentRequest(tab, sequences, staleCheck), false, 'the active overlay changed');
});

test('production openFile coordinator coalesces concurrent document/model creation', async () => {
  const pendingOpens = new Map();
  const tabs = new Map();
  let resolveOpen;
  const gate = new Promise((resolve) => { resolveOpen = resolve; });
  let createCalls = 0;
  let installCalls = 0;
  const options = {
    pendingOpens,
    tabs,
    path: 'main.go',
    createTab: async () => {
      createCalls += 1;
      await gate;
      return { path: 'main.go', documentId: 'doc-1' };
    },
    installTab: async () => { installCalls += 1; },
  };

  const first = app.ensureOpenTab(options);
  const second = app.ensureOpenTab(options);
  assert.strictEqual(first, second);
  assert.strictEqual(createCalls, 0, 'creation starts in the next microtask');
  resolveOpen();
  const [firstTab, secondTab] = await Promise.all([first, second]);

  assert.strictEqual(firstTab, secondTab);
  assert.strictEqual(tabs.get('main.go'), firstTab);
  assert.strictEqual(createCalls, 1);
  assert.strictEqual(installCalls, 1);
  assert.strictEqual(pendingOpens.size, 0);
  assert.strictEqual(await app.ensureOpenTab(options), firstTab);
  assert.strictEqual(createCalls, 1);
  assert.strictEqual(installCalls, 1);
});

test('production openFile coordinator clears failed opens for retry', async () => {
  const pendingOpens = new Map();
  const tabs = new Map();
  let attempts = 0;
  const options = {
    pendingOpens,
    tabs,
    path: 'retry.go',
    createTab: async () => {
      attempts += 1;
      if (attempts === 1) throw new Error('open failed');
      return { path: 'retry.go', documentId: 'doc-2' };
    },
    installTab: () => {},
  };

  await assert.rejects(app.ensureOpenTab(options), /open failed/);
  assert.strictEqual(pendingOpens.size, 0);
  const tab = await app.ensureOpenTab(options);
  assert.strictEqual(tab.documentId, 'doc-2');
  assert.strictEqual(attempts, 2);
});

test('flushSync waits for the active PUT and observes its acknowledged version', async () => {
  let resolveRequest;
  const requestGate = new Promise((resolve) => { resolveRequest = resolve; });
  let requestCalls = 0;
  const tab = {
    content: 'package main\n',
    overlay: {
      documentId: 'doc-1', leaseId: 'lease-1', serverVersion: 1,
      localEditRevision: 1, acknowledgedEditRevision: 0,
      syncState: 'idle', inFlight: false, syncPromise: null, debounce: null,
    },
  };
  const dependencies = {
    request: async () => {
      requestCalls += 1;
      return requestGate;
    },
    render: () => {},
    refresh: () => {},
    notify: () => {},
  };

  const active = app.runSyncPump(tab, dependencies);
  assert.strictEqual(app.runSyncPump(tab, dependencies), active, 'active PUT promise must be reused');
  let flushed = false;
  const flushing = app.flushSync(tab, (value) => app.runSyncPump(value, dependencies)).then(() => { flushed = true; });
  await Promise.resolve();
  assert.strictEqual(flushed, false, 'flush returned before network ACK');
  assert.strictEqual(requestCalls, 1);

  resolveRequest({ version: 2, contentHash: 'hash-2' });
  await flushing;
  assert.strictEqual(tab.overlay.serverVersion, 2);
  assert.strictEqual(tab.overlay.acknowledgedEditRevision, 1);
  assert.strictEqual(tab.overlay.syncState, 'idle');
  assert.strictEqual(tab.overlay.syncPromise, null);
});

test('sync promise chains an edit made while the first PUT is active', async () => {
  const resolvers = [];
  const bodies = [];
  const tab = {
    content: 'first',
    overlay: {
      documentId: 'doc-2', leaseId: 'lease-2', serverVersion: 3,
      localEditRevision: 1, acknowledgedEditRevision: 0,
      syncState: 'idle', inFlight: false, syncPromise: null, debounce: null,
    },
  };
  const dependencies = {
    request: async (_path, options) => {
      bodies.push(JSON.parse(options.body));
      return new Promise((resolve) => { resolvers.push(resolve); });
    },
    render: () => {},
    refresh: () => {},
    notify: () => {},
  };

  const chain = app.runSyncPump(tab, dependencies);
  await Promise.resolve();
  tab.content = 'second';
  tab.overlay.localEditRevision = 2;
  resolvers[0]({ version: 4, contentHash: 'hash-4' });
  await new Promise((resolve) => setImmediate(resolve));
  assert.strictEqual(resolvers.length, 2, 'newer edit did not start a sequential PUT');
  assert.deepStrictEqual(bodies.map((body) => [body.expectedVersion, body.newVersion, body.content]), [
    [3, 4, 'first'],
    [4, 5, 'second'],
  ]);
  resolvers[1]({ version: 5, contentHash: 'hash-5' });
  await chain;
  assert.strictEqual(tab.overlay.acknowledgedEditRevision, 2);
  assert.strictEqual(tab.overlay.serverVersion, 5);
});

test('external workspace change refreshes a clean open overlay tab', async () => {
  const tab = {
    documentId: 'doc-external', path: 'main.go', language: 'go',
    content: 'package old\n', original: 'package old\n', dirty: false, readOnly: false,
    overlay: {
      documentId: 'doc-external', leaseId: 'lease-external', serverVersion: 1,
      contentHash: 'hash-1', baseContentHash: 'hash-1', baseSnapshotId: 'snapshot-1',
      localEditRevision: 0, acknowledgedEditRevision: 0,
      syncState: 'idle', inFlight: false, syncPromise: null, debounce: null,
    },
  };
  let updatedModel;
  const result = await app.refreshOpenTabAfterExternalChange(tab, {
    isCurrent: () => true,
    request: async (path, options) => {
      assert.strictEqual(path, '/api/documents/doc-external');
      assert.strictEqual(options.headers['X-Document-Lease'], 'lease-external');
      return {
        documentId: 'doc-external', version: 2, content: 'package current\n',
        contentHash: 'hash-2', baseContentHash: 'hash-2', baseSnapshotId: 'snapshot-2',
        dirty: false, state: 'external_changed_clean',
      };
    },
    updateModel: (model) => { updatedModel = model; },
  });

  assert.deepStrictEqual(result, { status: 'refreshed' });
  assert.strictEqual(tab.content, 'package current\n');
  assert.strictEqual(tab.original, 'package current\n');
  assert.strictEqual(tab.dirty, false);
  assert.strictEqual(tab.overlay.serverVersion, 2);
  assert.strictEqual(tab.overlay.contentHash, 'hash-2');
  assert.strictEqual(tab.overlay.baseSnapshotId, 'snapshot-2');
  assert.strictEqual(updatedModel.content, 'package current\n');
  assert.strictEqual(updatedModel.version, 2);
});

test('external refresh never overwrites an edit made while GET is active', async () => {
  let resolveRequest;
  const gate = new Promise((resolve) => { resolveRequest = resolve; });
  const tab = {
    documentId: 'doc-race', path: 'race.go', language: 'go',
    content: 'old', original: 'old', dirty: false, readOnly: false,
    overlay: {
      documentId: 'doc-race', leaseId: 'lease-race', serverVersion: 4,
      localEditRevision: 2, acknowledgedEditRevision: 2,
      syncState: 'idle', inFlight: false, syncPromise: null, debounce: null,
    },
  };
  let modelUpdates = 0;
  const refreshing = app.refreshOpenTabAfterExternalChange(tab, {
    isCurrent: () => true,
    request: () => gate,
    updateModel: () => { modelUpdates += 1; },
  });
  tab.content = 'local edit';
  tab.dirty = true;
  tab.overlay.localEditRevision = 3;
  resolveRequest({
    version: 5, content: 'external edit', contentHash: 'hash-5',
    baseContentHash: 'hash-5', baseSnapshotId: 'snapshot-5',
    dirty: false, state: 'external_changed_clean',
  });

  assert.deepStrictEqual(await refreshing, { status: 'preserved', reason: 'local_changes' });
  assert.strictEqual(tab.content, 'local edit');
  assert.strictEqual(tab.overlay.serverVersion, 4);
  assert.strictEqual(modelUpdates, 0);
});

test('production shouldApplyDiagnosticsResponse drops stale diagnostics', () => {
	const response = { documentId: 'doc-1', documentVersion: 4, contentHash: 'sha256:4', diagnostics: [], semanticCoverage: { lsp: 'disabled' } };
  assert.strictEqual(app.shouldApplyDiagnosticsResponse(response, {
    sequence: 1, latestSequence: 1, documentId: 'doc-1', documentVersion: 4, contentHash: 'sha256:4',
  }), true);
  assert.strictEqual(app.shouldApplyDiagnosticsResponse(response, {
    sequence: 1, latestSequence: 2, documentId: 'doc-1', documentVersion: 4, contentHash: 'sha256:4',
  }), false);
  assert.strictEqual(app.shouldApplyDiagnosticsResponse(response, {
    sequence: 1, latestSequence: 1, documentId: 'doc-1', documentVersion: 5, contentHash: 'sha256:4',
  }), false);
  assert.strictEqual(app.shouldApplyDiagnosticsResponse(response, {
    sequence: 1, latestSequence: 1, documentId: 'doc-1', documentVersion: 4, contentHash: 'sha256:stale',
  }), false);
});

function diagnostic(id, severity, line) {
  return {
    diagnosticId: id,
    path: 'web/app.ts',
    range: pointRange(line, 1),
    severity,
    source: 'tree-sitter',
    message: 'Tree-sitter parse error',
    versionKnown: true,
  };
}

function pointRange(line, column) {
  const point = app.editorPosition(line, column);
  return { start: point, end: point };
}

function sampleCodemap(overrides = {}) {
  return {
    query: 'checkout handler',
    title: 'Checkout handler',
    overview: 'Factual overview.',
    summary: 'A grounded summary of the request lifecycle.',
    motivation: 'Why the order flow exists and why its boundaries matter.',
    details: 'How request data crosses the handler, service, and repository.',
    trace: ['Handler -calls-> Service'],
    flows: [{
      title: 'Request processing',
      entryNodeId: 'node:a',
      steps: [
        { label: '1a', nodeId: 'node:a', text: 'Receives the request', path: 'api/handler.go', line: 3, snippet: 'func Handler() {}' },
        { label: '1b', nodeId: 'node:b', text: 'Calls the service', path: 'domain/service.go', line: 8, snippet: 'service.Submit()' },
      ],
    }],
    nodes: [
      node('node:a', 'Handler', 'api'),
      node('node:b', 'Service', 'domain'),
    ],
    edges: [
      edge('edge:a-b', 'node:a', 'node:b', 'calls', 0.9),
    ],
    diagram: {
      version: 'mermaid/v1',
      kind: 'flowchart',
      source: 'graph TD\n  subgraph g0["api"]\n    direction TB\n    n0["Handler · handler.go"]\n  end',
      sources: [{ nodeId: 'node:a', label: 'Handler', path: 'api/handler.go', range: pointRange(3, 1) }],
    },
    provider: 'fake',
    generatedAt: '2026-06-25T10:00:00Z',
    snapshotId: 'snapshot:input',
    contextPackHash: 'sha256:pack',
    policyVersion: 'codemap.v2',
    outputSchemaVersion: 'codemap-narrative/v2',
    artifact: {
      artifactId: 'artifact:codemap',
      type: 'codemap',
      key: 'query/checkout#36',
      artifactRevision: 3,
      inputSnapshotId: 'snapshot:input',
      contextPackHash: 'sha256:pack',
      promptVersion: 'codemap-v2',
      outputSchema: 'codemap-narrative/v2',
      provider: 'fake',
      model: 'fake',
      status: 'stale',
      staleReasons: ['snapshot_changed'],
      createdAt: '2026-06-25T10:00:00Z',
    },
    ...overrides,
  };
}

function node(id, label, group) {
  return {
    id,
    label,
    kind: 'function',
    path: `${group}/${label.toLowerCase()}.go`,
    range: pointRange(3, 1),
    summary: `${label} summary`,
    snippet: `func ${label}() {}`,
    group,
    relevance: id.endsWith(':a') ? 0.9 : 0.7,
  };
}

function edge(id, source, target, type, confidence = 0.85) {
  return {
    id,
    source,
    target,
    type,
    label: type,
    path: 'api/handler.go',
    line: 3,
    confidence,
    snippet: `${source} ${type} ${target}`,
  };
}
