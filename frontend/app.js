'use strict';

const NAV_HISTORY_LIMIT = 50;
const NAV_DEFAULT_LIMIT = 100;
const SEMANTIC_TOKEN_LEGEND_VERSION = 'codeatlas-semantic-tokens/v1';
const SEMANTIC_TOKEN_OWNER = 'semantic-tokens';
const LSP_DIAGNOSTICS_OWNER = 'lsp-diagnostics';
const PARSER_DIAGNOSTICS_OWNER = 'parser-diagnostics';
const PROBLEMS_VISIBLE_LIMIT = 100;
const JOB_VISIBLE_LIMIT = 50;
const HOVER_POINTER_INTERVAL_MS = 75;
const HOVER_CARD_WIDTH_PX = 420;
const HOVER_CARD_RESERVED_HEIGHT_PX = 420;
const HOVER_HIDE_DELAY_MS = 600;
const DOCUMENT_LEASE_STORAGE_KEY = 'codeatlas.open-document-leases.v1';
const DOCUMENT_LEASE_HANDOFF_STORAGE_KEY = 'codeatlas.document-lease-handoffs.v1';
const DOCUMENT_LEASE_HANDOFF_MAX_AGE_MS = 30 * 60 * 1000;
const DOCUMENT_LEASE_HEARTBEAT_MS = 5 * 60 * 1000;
const CODEMAP_CONFIG = Object.freeze({
  FULL_GRAPH_MAX_NODES: 150,
  FULL_GRAPH_MAX_EDGES: 300,
  MAX_DTO_NODES: 500,
  MAX_DTO_EDGES: 1000,
  WORKER_TIMEOUT_MS: 5000,
  FILTER_INPUT_DEBOUNCE_MS: 120,
  MIN_ZOOM: 0.45,
  MAX_ZOOM: 1.7,
});
const MERMAID_DIAGRAM_VERSION = 'mermaid/v1';
const MERMAID_MAX_SOURCE_BYTES = 64 * 1024;
const FIRST_FOCUSABLE_SELECTOR = 'button, [href], input, select, textarea, [tabindex]:not([tabindex="-1"])';
const SHORTCUTS = Object.freeze({
  focusSearch: { mac: '⌘K', other: 'Ctrl+K', aria: 'Control+K' },
  save: { mac: '⌘S', other: 'Ctrl+S', aria: 'Control+S' },
  definition: { mac: 'F12', other: 'F12', aria: 'F12' },
  references: { mac: '⇧F12', other: 'Shift+F12', aria: 'Shift+F12' },
  focusWorkspace: { mac: '⌘1', other: 'Alt+1', aria: 'Alt+1' },
  focusEditor: { mac: '⌘2', other: 'Alt+2', aria: 'Alt+2' },
  focusInspector: { mac: '⌘3', other: 'Alt+3', aria: 'Alt+3' },
  focusJobs: { mac: '⌘4', other: 'Alt+4', aria: 'Alt+4' },
});

const state = {
  stats: null,
  tree: null,
  tabs: new Map(),
  pendingOpens: new Map(),
  activePath: null,
  editorAdapter: null,
  wikiPages: [],
  wikiStatus: 'not_generated',
  wikiLastError: '',
  activeWikiSlug: null,
  hoverTimer: null,
  hoverHideTimer: null,
  hoverMoveThrottle: null,
  hoverPositionScheduler: null,
  hoverCardAnchor: null,
  hoverShellRect: null,
  hoverGeometryBound: false,
  hoverResizeObserver: null,
  hoverController: null,
  hoverKey: '',
  hoverTarget: null,
  hoverExplanation: null,
  hoverPinned: false,
  hoverStale: false,
  hoverCache: createExplainCache(80),
  seeMoreController: null,
  seeMoreCache: createExplainCache(32),
  explainChordUntil: 0,
  navHistory: createNavigationHistory(NAV_HISTORY_LIMIT),
  navController: null,
  navSeq: 0,
  lastNavigationResult: null,
  navResultItems: [],
  semanticTokensByDocument: new Map(),
  semanticTokenControllers: new Map(),
  semanticTokenSeq: new Map(),
  diagnosticControllers: new Map(),
  diagnosticSeq: new Map(),
  diagnosticsByDocument: new Map(),
  problemFilters: { severity: 'all', source: '', path: '' },
  problemItems: [],
  jobs: createJobStore(JOB_VISIBLE_LIMIT),
  codemap: createCodemapStore(),
  codemapLayoutWorker: null,
  codemapRenderedSnapshot: null,
  codemapSearchCommit: null,
  codemapFilterFrame: null,
  a11yAnnouncer: createA11yAnnouncerState(),
  latestCodemapJobId: '',
  // Per-panel request sequences for dropping stale overlay responses.
  hoverSeq: 0,
  seeMoreSeq: 0,
  searchTimer: null,
  searchController: null,
  searchActiveIndex: -1,
  searchHits: [],
  searchResultItems: [],
  treeItems: [],
  explainExplanation: null,
  // Readiness state machine.
  phase: 'connecting',
  appReady: false,
  bootstrapped: false,
  eventSource: null,
  lastEventSeq: 0,
  uiBound: false,
  pollTimer: null,
  pollGeneration: 0,
  capabilities: [],
  lastReadyBody: null,
  lastUpdate: null,
  documentLeaseHeartbeat: null,
  documentLeaseHeartbeatInFlight: false,
};

const POLL_INTERVAL_MS = 1200;

const elements = {};
const $ = (id) => document.getElementById(id);

function boot() {
  Object.assign(elements, {
    appShell: document.querySelector('.app-shell'),
    workspaceLabel: $('workspace-label'),
    searchInput: $('search-input'),
    searchResults: $('search-results'),
    indexStatus: $('index-status'),
    reindexButton: $('reindex-button'),
    refreshTreeButton: $('refresh-tree-button'),
    fileTree: $('file-tree'),
    sidebarStats: $('sidebar-stats'),
    editorTabs: $('editor-tabs'),
    emptyEditor: $('empty-editor'),
    editorShell: $('editor-shell'),
    editorMount: $('editor-mount'),
    hoverCard: $('hover-card'),
    hoverKind: $('hover-kind'),
    hoverName: $('hover-name'),
    hoverSignature: $('hover-signature'),
    hoverSummary: $('hover-summary'),
    hoverProvider: $('hover-provider'),
    hoverFreshness: $('hover-freshness'),
    hoverCoverage: $('hover-coverage'),
    hoverObservations: $('hover-observations'),
    hoverError: $('hover-error'),
    hoverStatus: $('hover-status'),
    hoverPinButton: $('hover-pin-button'),
    hoverCloseButton: $('hover-close-button'),
    seeMoreButton: $('see-more-button'),
    fileStatus: $('file-status'),
    cursorStatus: $('cursor-status'),
    saveStatus: $('save-status'),
    semanticStatus: $('semantic-status'),
    diagnosticsStatus: $('diagnostics-status'),
    refreshWikiButton: $('refresh-wiki-button'),
    wikiPageSelect: $('wiki-page-select'),
    wikiStatus: $('wiki-status'),
    wikiContent: $('wiki-content'),
    codemapForm: $('codemap-form'),
    codemapQuery: $('codemap-query'),
    codemapResult: $('codemap-result'),
    explainContent: $('explain-content'),
    evidenceList: $('evidence-list'),
    navDefinitionButton: $('nav-definition-button'),
    navReferencesButton: $('nav-references-button'),
    navImplementationButton: $('nav-implementation-button'),
    navIncomingButton: $('nav-incoming-button'),
    navOutgoingButton: $('nav-outgoing-button'),
    navSummary: $('navigation-summary'),
    navResults: $('navigation-results'),
    problemSeverityFilter: $('problem-severity-filter'),
    problemSourceFilter: $('problem-source-filter'),
    problemPathFilter: $('problem-path-filter'),
    problemRefreshButton: $('problem-refresh-button'),
    problemsSummary: $('problems-summary'),
    problemsResults: $('problems-results'),
    jobsRefreshButton: $('jobs-refresh-button'),
    jobsSummary: $('jobs-summary'),
    jobsList: $('jobs-list'),
    a11yPolite: $('a11y-polite'),
    a11yAssertive: $('a11y-assertive'),
    appDialog: $('app-dialog'),
    appDialogTitle: $('app-dialog-title'),
    appDialogDescription: $('app-dialog-description'),
    appDialogActions: $('app-dialog-actions'),
    toastRegion: $('toast-region'),
    overlay: $('bootstrap-overlay'),
    overlayPhase: $('bootstrap-phase'),
    overlayStage: $('bootstrap-stage'),
    overlayError: $('bootstrap-error'),
    overlayCapabilities: $('bootstrap-capabilities'),
    overlayInstruction: $('bootstrap-instruction'),
    overlayRetry: $('bootstrap-retry'),
    overlayUpdated: $('bootstrap-updated'),
  });
  elements.overlayRetry.addEventListener('click', retryReadiness);
  startReadinessLoop();
}

if (typeof window !== 'undefined') {
  window.addEventListener('DOMContentLoaded', boot);
}

// ---- Readiness state machine: pure, injectable helpers (also unit-tested) ----

// backendStateToPhase maps a /api/health/ready response to a frontend phase.
function backendStateToPhase(status, backendState) {
  if (status === 200 && backendState === 'READY') return 'ready';
  switch (backendState) {
    case 'BOOTING':
      return 'booting';
    case 'PROBING_CAPABILITIES':
      return 'probing';
    case 'INDEXING':
    case 'GENERATING_REQUIRED_ARTIFACTS':
      return 'indexing';
    case 'FAILED':
      return 'failed';
    case 'SHUTTING_DOWN':
      return 'unreachable';
    default:
      return 'booting';
  }
}

// shouldContinuePolling reports whether a phase is transient and worth re-polling.
function shouldContinuePolling(phase) {
  return phase === 'connecting' || phase === 'booting' || phase === 'probing'
    || phase === 'indexing' || phase === 'unreachable';
}

function isDiagnosticPhase(phase) {
  return phase !== 'ready';
}

// isAppNotReady detects the backend's stable 503 APP_NOT_READY envelope.
function isAppNotReady(status, payload) {
  return status === 503 && !!payload && typeof payload === 'object'
    && !!payload.error && payload.error.code === 'APP_NOT_READY';
}

// fetchReadiness queries /api/health/ready and derives the phase. A transport
// failure means the server is unreachable (distinct from a server still booting).
async function fetchReadiness(fetchFn) {
  try {
    const response = await fetchFn('/api/health/ready');
    let body = {};
    try {
      body = await response.json();
    } catch (_) {
      body = {};
    }
    return {
      reachable: true,
      status: response.status,
      phase: backendStateToPhase(response.status, body && body.state),
      body: body || {},
    };
  } catch (_) {
    return { reachable: false, status: 0, phase: 'unreachable', body: {} };
  }
}

// loadMandatoryResources fetches the resources required for a functional UI. It
// uses Promise.all (never allSettled): a single failure rejects the bootstrap.
async function loadMandatoryResources(apiFn) {
  const [stats, tree] = await Promise.all([apiFn('/api/stats'), apiFn('/api/tree')]);
  return { stats, tree };
}

function sanitizeMessage(message) {
  if (!message) return '';
  return String(message).replace(/\s+/g, ' ').trim().slice(0, 300);
}

function createA11yAnnouncerState() {
  return { last: { polite: '', assertive: '' }, timers: { polite: null, assertive: null } };
}

function nextA11yAnnouncement(announcer, message, priority = 'polite') {
  const target = priority === 'assertive' ? 'assertive' : 'polite';
  const cleaned = sanitizeMessage(message);
  if (!cleaned || !announcer) return null;
  if (announcer.last[target] === cleaned) return null;
  announcer.last[target] = cleaned;
  return { target, message: cleaned };
}

function announce(message, priority = 'polite') {
  const next = nextA11yAnnouncement(state.a11yAnnouncer, message, priority);
  if (!next) return;
  const region = next.target === 'assertive' ? elements.a11yAssertive : elements.a11yPolite;
  if (!region) return;
  region.textContent = next.message;
  const timer = state.a11yAnnouncer.timers[next.target];
  if (timer) clearTimeout(timer);
  state.a11yAnnouncer.timers[next.target] = setTimeout(() => {
    if (region.textContent === next.message) region.textContent = '';
  }, 2200);
}

function progressA11yAttributes(progress = {}) {
  if (progress.indeterminate || typeof progress.percent !== 'number') {
    return {
      role: 'progressbar',
      ariaValueNow: null,
      ariaValueText: `Em andamento${progress.unit ? `: ${progress.unit}` : ''}`,
    };
  }
  const percent = Math.max(0, Math.min(100, Math.round(progress.percent)));
  return {
    role: 'progressbar',
    ariaValueNow: percent,
    ariaValueText: `${percent}%${progress.unit ? ` ${progress.unit}` : ''}`,
  };
}

function editorTabA11yLabel(tab, counts = {}) {
  const parts = [String(tab.path || '').split('/').pop() || 'file'];
  if (tab.dirty) parts.push('unsaved changes');
  if (tab.readOnly) parts.push('read-only');
  if (tab.overlay && tab.overlay.syncState && tab.overlay.syncState !== 'idle') {
    parts.push(tab.overlay.syncState === 'conflict' ? 'sync conflict' : `sync ${tab.overlay.syncState}`);
  }
  if (counts.error) parts.push(`${counts.error} error${counts.error === 1 ? '' : 's'}`);
  if (counts.warning) parts.push(`${counts.warning} warning${counts.warning === 1 ? '' : 's'}`);
  if (counts.info) parts.push(`${counts.info} info`);
  if (counts.hint) parts.push(`${counts.hint} hint${counts.hint === 1 ? '' : 's'}`);
  return parts.join(', ');
}

function shouldHandleGlobalShortcut(event) {
  if (!event || event.isComposing) return false;
  const target = event.target || {};
  const tag = String(target.tagName || '').toUpperCase();
  if (tag === 'INPUT' || tag === 'TEXTAREA' || tag === 'SELECT') return false;
  if (target.isContentEditable) return false;
  return true;
}

function shortcutDisplayLabel(action, platform = '') {
  const shortcut = SHORTCUTS[action];
  if (!shortcut) return '';
  return String(platform).toLowerCase().includes('darwin') || String(platform).toLowerCase().includes('mac')
    ? shortcut.mac
    : shortcut.other;
}

function shortcutAriaLabel(action) {
  return SHORTCUTS[action] ? SHORTCUTS[action].aria : '';
}

function focusFallbackSelector(context = {}) {
  if (context.hasActiveEditor) return '#editor-mount';
  if (context.hasWorkspace) return '#file-tree';
  return FIRST_FOCUSABLE_SELECTOR;
}

const FocusManager = {
  saved: null,
  trap: null,
  saveFocus(fallbackSelector = '') {
    if (typeof document === 'undefined') return;
    this.saved = { element: document.activeElement, fallbackSelector };
  },
  restoreFocus() {
    if (typeof document === 'undefined') return false;
    const candidate = this.saved && isFocusableElement(this.saved.element)
      ? this.saved.element
      : document.querySelector((this.saved && this.saved.fallbackSelector) || focusFallbackSelector({
        hasActiveEditor: !!state.activePath,
        hasWorkspace: !!state.tree,
      }));
    return this.moveFocus(candidate);
  },
  moveFocus(element) {
    if (!isFocusableElement(element)) return false;
    try {
      element.focus({ preventScroll: true });
    } catch (_) {
      element.focus();
    }
    return true;
  },
  getFocusableElements(container) {
    if (!container) return [];
    return [...container.querySelectorAll(FIRST_FOCUSABLE_SELECTOR)].filter(isFocusableElement);
  },
  trapFocus(container) {
    this.releaseTrap();
    if (!container) return;
    const handler = (event) => {
      if (event.key !== 'Tab') return;
      const focusable = this.getFocusableElements(container);
      if (!focusable.length) return;
      const first = focusable[0];
      const last = focusable[focusable.length - 1];
      if (event.shiftKey && document.activeElement === first) {
        event.preventDefault();
        last.focus();
      } else if (!event.shiftKey && document.activeElement === last) {
        event.preventDefault();
        first.focus();
      }
    };
    container.addEventListener('keydown', handler);
    this.trap = { container, handler };
  },
  releaseTrap() {
    if (!this.trap) return;
    this.trap.container.removeEventListener('keydown', this.trap.handler);
    this.trap = null;
  },
};

function isFocusableElement(element) {
  if (!element || typeof element.focus !== 'function') return false;
  if (element.disabled || element.getAttribute('aria-hidden') === 'true') return false;
  if (element.offsetParent === null && element !== document.body && element.getAttribute('role') !== 'dialog') return false;
  return true;
}

function nextAnimationFrame(callback) {
  if (typeof requestAnimationFrame === 'function') requestAnimationFrame(callback);
  else setTimeout(callback, 0);
}

function createLatestThrottle(callback, intervalMs = HOVER_POINTER_INTERVAL_MS, timers = {}) {
  const now = timers.now || Date.now;
  const setTimer = timers.setTimeout || setTimeout;
  const clearTimer = timers.clearTimeout || clearTimeout;
  let pending = null;
  let timer = null;
  let lastRun = Number.NEGATIVE_INFINITY;

  const flush = () => {
    timer = null;
    if (pending === null) return;
    const value = pending;
    pending = null;
    lastRun = now();
    callback(value);
  };

  return {
    push(value) {
      pending = value;
      if (timer !== null) return;
      const elapsed = now() - lastRun;
      timer = setTimer(flush, Math.max(0, intervalMs - elapsed));
    },
    cancel() {
      pending = null;
      if (timer !== null) clearTimer(timer);
      timer = null;
    },
  };
}

function createFrameCoalescer(callback, schedule = nextAnimationFrame) {
  let pending = null;
  let scheduled = false;
  return {
    push(value) {
      pending = value;
      if (scheduled) return;
      scheduled = true;
      schedule(() => {
        scheduled = false;
        if (pending === null) return;
        const latest = pending;
        pending = null;
        callback(latest);
      });
    },
    cancel() {
      pending = null;
    },
  };
}

function createDebouncedCommit(callback, delayMs, timers = {}) {
  const setTimer = timers.setTimeout || setTimeout;
  const clearTimer = timers.clearTimeout || clearTimeout;
  let timer = null;
  let pending;
  const flush = () => {
    timer = null;
    const value = pending;
    pending = undefined;
    callback(value);
  };
  return {
    push(value) {
      pending = value;
      if (timer !== null) clearTimer(timer);
      timer = setTimer(flush, delayMs);
    },
    flush() {
      if (timer === null) return;
      clearTimer(timer);
      flush();
    },
    cancel() {
      pending = undefined;
      if (timer !== null) clearTimer(timer);
      timer = null;
    },
  };
}

function delegatedItem(event, container, selector) {
  if (!event?.target || typeof event.target.closest !== 'function' || !container) return null;
  const item = event.target.closest(selector);
  return item && container.contains(item) ? item : null;
}

function domId(prefix, value) {
  return `${prefix}-${safeClassName(stableHash(value))}`;
}

function showAppDialog({ title, description, actions }) {
  if (!elements.appDialog || !elements.appDialogActions) {
    return Promise.resolve(false);
  }
  const dialog = elements.appDialog.querySelector('[role="dialog"]');
  const normalizedActions = actions && actions.length ? actions : [
    { label: 'Cancel', value: false, variant: 'secondary' },
    { label: 'Confirm', value: true, variant: 'primary' },
  ];
  const previousAppShellAriaHidden = elements.appShell?.getAttribute('aria-hidden');
  FocusManager.saveFocus(focusFallbackSelector({ hasActiveEditor: !!state.activePath, hasWorkspace: !!state.tree }));
  elements.appDialogTitle.textContent = title || 'Confirm action';
  elements.appDialogDescription.textContent = description || '';
  elements.appDialogActions.replaceChildren();
  return new Promise((resolve) => {
    let resolved = false;
    const close = (value) => {
      if (resolved) return;
      resolved = true;
      elements.appDialog.classList.add('hidden');
      if (previousAppShellAriaHidden == null) elements.appShell?.removeAttribute('aria-hidden');
      else elements.appShell?.setAttribute('aria-hidden', previousAppShellAriaHidden);
      FocusManager.releaseTrap();
      elements.appDialog.removeEventListener('keydown', onKeydown);
      FocusManager.restoreFocus();
      resolve(value);
    };
    const onKeydown = (event) => {
      if (event.key === 'Escape') {
        event.preventDefault();
        close(false);
      }
    };
    normalizedActions.forEach((action) => {
      const button = document.createElement('button');
      button.type = 'button';
      button.className = `button ${action.variant === 'primary' ? 'primary' : 'secondary'}`;
      button.textContent = action.label;
      button.addEventListener('click', () => close(action.value));
      elements.appDialogActions.appendChild(button);
    });
    elements.appShell?.setAttribute('aria-hidden', 'true');
    elements.appDialog.classList.remove('hidden');
    elements.appDialog.addEventListener('keydown', onKeydown);
    FocusManager.trapFocus(elements.appDialog);
    nextAnimationFrame(() => FocusManager.moveFocus(
      elements.appDialogActions.querySelector('.button.primary')
      || elements.appDialogActions.querySelector('button')
      || dialog,
    ));
  });
}

function confirmDiscardChanges(path) {
  return showAppDialog({
    title: 'Discard changes?',
    description: `Local changes to ${path} will be lost.`,
    actions: [
      { label: 'Cancel', value: false, variant: 'secondary' },
      { label: 'Discard', value: true, variant: 'primary' },
    ],
  });
}

function editorPosition(line, column) {
  return { line, column, encoding: 'utf-16' };
}

function lineRanges(content) {
  const ranges = [];
  let start = 0;
  for (let index = 0; index < content.length; index += 1) {
    const char = content[index];
    if (char === '\r' || char === '\n') {
      ranges.push({ start, end: index });
      if (char === '\r' && content[index + 1] === '\n') index += 1;
      start = index + 1;
    }
  }
  ranges.push({ start, end: content.length });
  return ranges;
}

function lineStartOffsets(content) {
  const starts = [0];
  for (let index = 0; index < content.length; index += 1) {
    const char = content[index];
    if (char !== '\r' && char !== '\n') continue;
    if (char === '\r' && content[index + 1] === '\n') index += 1;
    starts.push(index + 1);
  }
  return starts;
}

function lineRangeAtLine(content, starts, rawLine) {
  if (!starts.length || rawLine < 1 || rawLine > starts.length) return null;
  const lineIndex = rawLine - 1;
  const start = starts[lineIndex];
  let end = lineIndex + 1 < starts.length ? starts[lineIndex + 1] : content.length;
  if (end > start && content[end - 1] === '\n') end -= 1;
  if (end > start && content[end - 1] === '\r') end -= 1;
  return { start, end };
}

function offsetToEditorPosition(content, rawOffset) {
  const offset = Math.max(0, Math.min(rawOffset, content.length));
  const ranges = lineRanges(content);
  let selected = 0;
  for (let index = 0; index < ranges.length; index += 1) {
    if (offset >= ranges[index].start && offset <= ranges[index].end) {
      selected = index;
      break;
    }
    if (offset > ranges[index].end) selected = index;
  }
  const range = ranges[selected] || { start: 0, end: 0 };
  return normalizeEditorPosition(content, editorPosition(selected + 1, offset - range.start + 1));
}

function editorPositionToOffset(content, position) {
  const normalized = normalizeEditorPosition(content, position);
  const ranges = lineRanges(content);
  const range = ranges[normalized.line - 1] || { start: 0, end: 0 };
  return Math.max(range.start, Math.min(range.start + normalized.column - 1, range.end));
}

function normalizeEditorPosition(content, position) {
  const ranges = lineRanges(content);
  const lineIndex = Math.max(0, Math.min(position.line - 1, ranges.length - 1));
  const range = ranges[lineIndex] || { start: 0, end: 0 };
  const line = content.slice(range.start, range.end);
  let column = Math.max(1, Math.min(position.column, line.length + 1));
  const offset = column - 1;
  if (offset > 0 && offset < line.length && isHighSurrogate(line.charCodeAt(offset - 1)) && isLowSurrogate(line.charCodeAt(offset))) {
    column += 1;
  }
  return editorPosition(lineIndex + 1, column);
}

function wordRangeAtPosition(content, position, starts = lineStartOffsets(content)) {
  const lineIndex = Math.max(0, Math.min(position.line - 1, starts.length - 1));
  const range = lineRangeAtLine(content, starts, lineIndex + 1) || { start: 0, end: 0 };
  const line = content.slice(range.start, range.end);
  if (!line) return null;
  let index = Math.max(0, Math.min(position.column - 1, line.length - 1));
  const valid = (char) => /[\p{L}\p{N}_$]/u.test(char || '');
  if (!valid(line[index])) return null;
  let start = index;
  let end = index + 1;
  while (start > 0 && valid(line[start - 1])) start -= 1;
  while (end < line.length && valid(line[end])) end += 1;
  return {
    start: editorPosition(lineIndex + 1, start + 1),
    end: editorPosition(lineIndex + 1, end + 1),
  };
}

function isHighSurrogate(value) {
  return value >= 0xd800 && value <= 0xdbff;
}

function isLowSurrogate(value) {
  return value >= 0xdc00 && value <= 0xdfff;
}

function phaseLabel(phase) {
  switch (phase) {
    case 'connecting':
      return 'Conectando…';
    case 'booting':
      return 'Initializing…';
    case 'probing':
      return 'Checking capabilities…';
    case 'indexing':
      return 'Indexing workspace…';
    case 'ready-loading':
      return 'Loading interface…';
    case 'ready':
      return 'Ready';
    case 'failed':
      return 'Initialization failed';
    case 'unreachable':
      return 'Server unreachable';
    default:
      return phase;
  }
}

function capabilityStateLabel(value) {
  switch (value) {
    case 'available':
      return 'OK';
    case 'unavailable':
      return 'unavailable';
    case 'disabled':
      return 'desabilitada (config)';
    case 'checking':
      return 'checking…';
    default:
      return value || 'unknown';
  }
}

// ---- Readiness orchestration (browser side) ----

function startReadinessLoop() {
  state.pollGeneration += 1;
  pollOnce(state.pollGeneration);
}

// retryReadiness re-runs only the HTTP readiness queries; it never restarts the
// backend. If the backend is now READY it (re)attempts the functional bootstrap.
function retryReadiness() {
  clearTimeout(state.pollTimer);
  startReadinessLoop();
}

async function pollOnce(generation) {
  if (generation !== state.pollGeneration) return;
  if (typeof fetch !== 'function') return;
  const result = await fetchReadiness((path) => fetch(path, { cache: 'no-store' }));
  if (generation !== state.pollGeneration) return; // a newer cycle replaced this one
  state.capabilities = await fetchCapabilities();
  if (generation !== state.pollGeneration) return;
  applyPhase(result.phase, result.body);
  if (result.phase === 'ready') {
    enterReady();
    return;
  }
  if (shouldContinuePolling(result.phase)) {
    scheduleNextPoll(generation);
  }
  // 'failed' stops automatic polling; recovery requires an explicit retry.
}

function scheduleNextPoll(generation) {
  clearTimeout(state.pollTimer);
  state.pollTimer = setTimeout(() => pollOnce(generation), POLL_INTERVAL_MS);
}

async function fetchCapabilities() {
  if (typeof fetch !== 'function') return state.capabilities;
  try {
    const response = await fetch('/api/capabilities', { cache: 'no-store' });
    if (!response.ok) return state.capabilities;
    const body = await response.json();
    return Array.isArray(body && body.capabilities) ? body.capabilities : [];
  } catch (_) {
    return state.capabilities;
  }
}

function applyPhase(phase, body) {
  state.phase = phase;
  state.lastReadyBody = body || null;
  state.lastUpdate = new Date();
  if (phase === 'ready') return; // overlay is hidden by the functional bootstrap
  state.appReady = false;
  showOverlay();
  renderDiagnostic(phase, body || {}, state.capabilities);
}

function enterReady() {
  state.appReady = true;
  if (state.bootstrapped) {
    state.phase = 'ready';
    hideOverlay();
    return;
  }
  runFunctionalBootstrap();
}

// runFunctionalBootstrap performs a single, atomic functional initialization. A
// failed mandatory request never reveals a partial UI.
async function runFunctionalBootstrap() {
  if (state.bootstrapped) {
    hideOverlay();
    return;
  }
  applyReadyLoading();
  try {
    const { stats, tree } = await loadMandatoryResources(api);
    await initializeEditorAdapter();
    if (!state.uiBound) {
      bindUI();
      state.uiBound = true;
    }
    applyStats(stats);
    applyTree(tree);
    await loadWiki(); // best-effort; never blocks readiness
    await loadJobs();
    if (!state.appReady) return; // readiness dropped mid-bootstrap
    await restoreCodemapFromLocation();
    if (!state.appReady) return;
    connectEventStream();
    state.bootstrapped = true;
    state.phase = 'ready';
    hideOverlay();
  } catch (error) {
    if (error && error.code === 'APP_NOT_READY') return; // handled; polling resumed
    showFunctionalError(error);
  }
}

async function initializeEditorAdapter() {
  if (state.editorAdapter) return state.editorAdapter;
  try {
    const { createMonacoEditorAdapter } = await import('./src/monaco-editor-adapter.ts');
    const adapter = createMonacoEditorAdapter();
    adapter.mount(elements.editorMount);
    state.editorAdapter = adapter;
    return adapter;
  } catch (cause) {
    const error = new Error('Failed to load Monaco Editor and its local workers.');
    error.code = 'EDITOR_RUNTIME_UNAVAILABLE';
    error.cause = cause;
    throw error;
  }
}

function applyReadyLoading() {
  state.phase = 'ready-loading';
  showOverlay();
  if (elements.overlayPhase) elements.overlayPhase.textContent = phaseLabel('ready-loading');
  if (elements.overlayStage) elements.overlayStage.textContent = 'Loading workspace…';
  if (elements.overlayError) elements.overlayError.classList.add('hidden');
  if (elements.overlayRetry) elements.overlayRetry.classList.add('hidden');
}

function showFunctionalError(error) {
  state.phase = 'failed';
  state.appReady = false;
  showOverlay();
  if (elements.overlayPhase) elements.overlayPhase.textContent = 'Functional initialization failed';
  if (elements.overlayStage) elements.overlayStage.textContent = '';
  if (elements.overlayError) {
    elements.overlayError.classList.remove('hidden');
    elements.overlayError.textContent = sanitizeMessage(error && error.message) || 'Failed to load required resources.';
  }
  if (elements.overlayInstruction) elements.overlayInstruction.textContent = 'Check the backend and try again.';
  if (elements.overlayRetry) elements.overlayRetry.classList.remove('hidden');
}

function renderDiagnostic(phase, body, capabilities) {
  if (!elements.overlay) return;
  elements.overlayPhase.textContent = phaseLabel(phase);
  const stage = body && body.stage ? body.stage : '';
  elements.overlayStage.textContent = stage ? `Stage: ${stage}` : '';

  const fatal = body && body.error;
  if (phase === 'failed' && fatal) {
    elements.overlayError.classList.remove('hidden');
    elements.overlayError.textContent = `${fatal.code || 'FAILURE'}: ${sanitizeMessage(fatal.message) || 'no cause reported'}`;
    elements.overlayInstruction.textContent = 'Correct the indicated configuration (for example, the AI endpoint) and restart the backend.';
  } else if (phase === 'unreachable') {
    elements.overlayError.classList.remove('hidden');
    elements.overlayError.textContent = 'Server unreachable.';
    elements.overlayInstruction.textContent = 'Check whether the backend is running.';
  } else {
    elements.overlayError.classList.add('hidden');
    elements.overlayInstruction.textContent = '';
  }

  if (state.bootstrapped && [...state.tabs.values()].some((tab) => tab.dirty)) {
    elements.overlayInstruction.textContent += ' Unsaved changes were kept in memory.';
  }

  const showRetry = phase === 'failed' || phase === 'unreachable';
  elements.overlayRetry.classList.toggle('hidden', !showRetry);
  renderCapabilities(capabilities);
  if (state.lastUpdate) {
    elements.overlayUpdated.textContent = `Updated at ${state.lastUpdate.toLocaleTimeString()}`;
  }
}

function renderCapabilities(capabilities) {
  elements.overlayCapabilities.replaceChildren();
  if (!capabilities || !capabilities.length) return;
  capabilities.forEach((capability) => {
    const row = document.createElement('div');
    row.className = `capability-row ${capability.state || ''}`;
    const name = document.createElement('span');
    name.className = 'capability-name';
    name.textContent = capability.id;
    const requirement = document.createElement('span');
    requirement.className = 'capability-req';
    requirement.textContent = capability.requirement === 'optional' ? 'optional' : 'required';
    const status = document.createElement('span');
    status.className = `capability-state ${capability.state || ''}`;
    status.textContent = capabilityStateLabel(capability.state);
    row.append(name, requirement, status);
    if (capability.errorCode) {
      const code = document.createElement('small');
      code.className = 'capability-error';
      code.textContent = capability.errorCode;
      row.appendChild(code);
    }
    elements.overlayCapabilities.appendChild(row);
  });
}

function showOverlay() {
  if (elements.overlay) elements.overlay.classList.remove('hidden');
  if (elements.appShell) elements.appShell.setAttribute('aria-hidden', 'true');
}

function hideOverlay() {
  if (elements.overlay) elements.overlay.classList.add('hidden');
  if (elements.appShell) elements.appShell.removeAttribute('aria-hidden');
}

// handleNotReady reacts to a 503 APP_NOT_READY received during normal operation:
// it blocks functional actions and returns to the diagnostic screen while keeping
// unsaved editor buffers in memory.
function handleNotReady(payload) {
  state.appReady = false;
  cancelExplainRequests();
  const backendState = payload && payload.error ? payload.error.state : '';
  applyPhase(backendStateToPhase(503, backendState), { state: backendState, stage: '', error: null });
  startReadinessLoop();
}

function ensureReady() {
  if (state.appReady) return true;
  toast('CodeAtlas is not ready.', true);
  return false;
}

function bindUI() {
  initializeAccessibility();
  elements.refreshTreeButton.addEventListener('click', loadTree);
  elements.reindexButton.addEventListener('click', reindexWorkspace);
  elements.refreshWikiButton.addEventListener('click', refreshWiki);
  elements.wikiPageSelect.addEventListener('change', () => showWikiPage(elements.wikiPageSelect.value));
  elements.codemapForm.addEventListener('submit', generateCodemap);
  state.codemap.subscribe(renderCodemapStore);
  elements.seeMoreButton.addEventListener('click', loadSeeMore);
  elements.hoverPinButton.addEventListener('click', toggleHoverPin);
  elements.hoverCloseButton.addEventListener('click', hideHover);
  elements.navDefinitionButton?.addEventListener('click', () => runNavigationCommand('definition'));
  elements.navReferencesButton?.addEventListener('click', () => runNavigationCommand('references', { forcePanel: true, limit: 500 }));
  elements.navImplementationButton?.addEventListener('click', () => runNavigationCommand('implementation', { forcePanel: true, limit: 50 }));
  elements.navIncomingButton?.addEventListener('click', () => runNavigationCommand('incoming_calls', { forcePanel: true, limit: 200 }));
  elements.navOutgoingButton?.addEventListener('click', () => runNavigationCommand('outgoing_calls', { forcePanel: true, limit: 200 }));
  elements.navResults?.addEventListener('click', handleNavigationResultClick);
  elements.navResults?.addEventListener('keydown', handleNavigationResultsKeydown);
  elements.problemSeverityFilter?.addEventListener('change', () => {
    state.problemFilters.severity = elements.problemSeverityFilter.value || 'all';
    renderProblemsPanel();
  });
  elements.problemSourceFilter?.addEventListener('input', () => {
    state.problemFilters.source = elements.problemSourceFilter.value.trim();
    renderProblemsPanel();
  });
  elements.problemPathFilter?.addEventListener('input', () => {
    state.problemFilters.path = elements.problemPathFilter.value.trim();
    renderProblemsPanel();
  });
  elements.problemRefreshButton?.addEventListener('click', refreshActiveDiagnostics);
  elements.problemsResults?.addEventListener('click', handleProblemClick);
  elements.problemsResults?.addEventListener('keydown', handleProblemsKeydown);
  elements.jobsRefreshButton?.addEventListener('click', loadJobs);
  elements.jobsList?.addEventListener('click', (event) => {
    const button = event.target.closest('[data-cancel-job-id]');
    if (button) cancelJob(button.dataset.cancelJobId);
  });
  elements.fileTree?.addEventListener('click', handleFileTreeClick);
  elements.fileTree?.addEventListener('toggle', handleFileTreeToggle, true);
  elements.fileTree?.addEventListener('keydown', handleFileTreeKeydown);
  elements.editorTabs?.addEventListener('keydown', handleEditorTabsKeydown);

  document.querySelectorAll('.inspector-tab').forEach((button) => {
    button.addEventListener('click', () => activateInspector(button.dataset.panel));
  });
  document.querySelector('.inspector-tabs')?.addEventListener('keydown', handleInspectorTabsKeydown);

  state.editorAdapter.onDidChangeContent(onEditorInput);
  state.editorAdapter.onDidChangeCursor(updateCursorStatus);
  state.editorAdapter.registerCommand({ id: 'save', key: 'mod+s', macKey: 'mod+s', run: saveActiveFile });
  state.editorAdapter.registerCommand({ id: 'navigation.definition', key: 'f12', macKey: 'f12', run: () => runNavigationCommand('definition') });
  state.editorAdapter.registerCommand({ id: 'navigation.peekDefinition', key: 'alt+f12', macKey: 'alt+f12', run: () => runNavigationCommand('definition', { forcePanel: true }) });
  state.editorAdapter.registerCommand({ id: 'navigation.references', key: 'shift+f12', macKey: 'shift+f12', run: () => runNavigationCommand('references', { forcePanel: true, limit: 500 }) });
  state.editorAdapter.registerCommand({ id: 'navigation.back', key: 'alt+arrowleft', macKey: 'alt+arrowleft', run: navigateBack });
  state.editorAdapter.registerCommand({ id: 'navigation.forward', key: 'alt+arrowright', macKey: 'alt+arrowright', run: navigateForward });
  initializeHoverScheduling();
  state.editorAdapter.onMouseMove(scheduleHover);
  state.editorAdapter.onMouseLeave(() => {
    state.hoverMoveThrottle?.cancel();
    scheduleHideHover();
  });
  elements.hoverCard.addEventListener('mouseenter', cancelHideHover);
  elements.hoverCard.addEventListener('pointerdown', cancelHideHover);
  elements.hoverCard.addEventListener('mouseleave', scheduleHideHover);
  elements.hoverCard.addEventListener('keydown', (event) => {
    if (event.key === 'Escape') hideHover();
  });

  elements.searchInput.addEventListener('input', scheduleSearch);
  elements.searchInput.addEventListener('keydown', (event) => {
    if (handleSearchKeydown(event)) return;
    if (event.key === 'Escape') hideSearchResults();
  });
  document.addEventListener('click', (event) => {
    if (!event.target.closest('.global-search')) hideSearchResults();
  });
  elements.searchResults.addEventListener('click', handleSearchResultClick);
  document.addEventListener('keydown', (event) => {
    if (handleExplainShortcut(event)) return;
    handleGlobalShortcut(event);
  });
  window.addEventListener('beforeunload', (event) => {
    const unsaved = [...state.tabs.values()].some(
      (tab) => tab.dirty || (tab.overlay && tab.overlay.syncState !== 'idle'),
    );
    if (unsaved) {
      event.preventDefault();
      event.returnValue = '';
    }
  });
  window.addEventListener('pagehide', (event) => {
    stopDocumentLeaseHeartbeat();
    if (!event.persisted) {
      const tabs = [...state.tabs.values()];
      // Persist the handoff synchronously before the best-effort keepalive
      // DELETE. A fresh page can rotate the lease even if browser shutdown
      // cancels that request; active pages never publish a handoff.
      prepareDocumentLeaseHandoff(tabs);
      releaseDocumentsOnPageHide(tabs);
    }
  });
  window.addEventListener('pageshow', startDocumentLeaseHeartbeat);
  startDocumentLeaseHeartbeat();

  elements.wikiContent.addEventListener('click', handleCodeReferenceClick);
  elements.codemapResult?.addEventListener('click', handleCodeReferenceClick);
  elements.hoverObservations.addEventListener('click', (event) => handleEvidenceClick(event, state.hoverExplanation));
  elements.explainContent.addEventListener('click', (event) => {
    handleCodeReferenceClick(event);
    handleEvidenceClick(event, state.explainExplanation);
  });
  elements.evidenceList.addEventListener('click', (event) => handleEvidenceClick(event, state.explainExplanation));
}

function initializeAccessibility() {
  if (elements.indexStatus) elements.indexStatus.setAttribute('role', 'status');
  if (elements.seeMoreButton) {
    elements.seeMoreButton.setAttribute('aria-label', 'Open the expanded explanation for the current symbol');
  }
  if (elements.searchInput) {
    elements.searchInput.setAttribute('aria-keyshortcuts', shortcutAriaLabel('focusSearch'));
  }
  if (elements.jobsList) elements.jobsList.setAttribute('role', 'list');
  activateInspector('deepwiki');
}

function handleGlobalShortcut(event) {
  if (!shouldHandleGlobalShortcut(event)) return false;
  const key = String(event.key || '').toLowerCase();
  const mod = event.metaKey || event.ctrlKey;
  const panelModifier = event.altKey || event.metaKey;
  if (mod && key === 'k') {
    event.preventDefault();
    elements.searchInput.focus();
    elements.searchInput.select();
    announce('Global search focused.');
    return true;
  }
  if (mod && key === 's') {
    event.preventDefault();
    saveActiveFile();
    return true;
  }
  if (panelModifier && key === '1') {
    event.preventDefault();
    focusWorkspace();
    return true;
  }
  if (panelModifier && key === '2') {
    event.preventDefault();
    focusEditor();
    return true;
  }
  if (panelModifier && key === '3') {
    event.preventDefault();
    focusInspector();
    return true;
  }
  if (panelModifier && key === '4') {
    event.preventDefault();
    activateInspector('jobs', { focus: true });
    return true;
  }
  return false;
}

function focusWorkspace() {
  const active = elements.fileTree?.querySelector('[role="treeitem"][aria-selected="true"]')
    || elements.fileTree?.querySelector('[role="treeitem"]')
    || elements.fileTree;
  FocusManager.moveFocus(active);
}

function focusEditor() {
  if (state.activePath) {
    const tab = state.tabs.get(state.activePath);
    if (tab) {
      state.editorAdapter.activateModel(tab.documentId);
      return;
    }
  }
  FocusManager.moveFocus(document.querySelector(focusFallbackSelector({
    hasActiveEditor: !!state.activePath,
    hasWorkspace: !!state.tree,
  })));
}

function focusInspector() {
  const activeTab = document.querySelector('.inspector-tab[aria-selected="true"]');
  if (activeTab) FocusManager.moveFocus(activeTab);
}

// parseErrorEnvelope reads the stable error envelope
// ({ error: { code, message, retryable, details, requestId } }) and falls back to
// the legacy string form. Call sites branch on `code`, never on message text.
function parseErrorEnvelope(payload, status) {
  if (payload && typeof payload === 'object' && payload.error && typeof payload.error === 'object') {
    const envelope = payload.error;
    return {
      code: envelope.code || 'INTERNAL_ERROR',
      message: envelope.message || `HTTP ${status}`,
      retryable: !!envelope.retryable,
      details: envelope.details || {},
      requestId: envelope.requestId || '',
    };
  }
  const legacy = payload && typeof payload === 'object' ? payload.error : payload;
  return {
    code: '',
    message: typeof legacy === 'string' && legacy ? legacy : `HTTP ${status}`,
    retryable: false,
    details: {},
    requestId: '',
  };
}

async function api(path, options = {}) {
  const { expectedErrorCodes = [], ...requestOptions } = options;
  const response = await fetch(path, {
    ...requestOptions,
    headers: {
      ...(requestOptions.body ? { 'Content-Type': 'application/json' } : {}),
      ...(requestOptions.headers || {}),
    },
  });
  const contentType = response.headers.get('content-type') || '';
  const payload = contentType.includes('application/json') ? await response.json() : await response.text();
  if (isAppNotReady(response.status, payload)) {
    handleNotReady(payload);
    const error = new Error('CodeAtlas is not ready.');
    error.code = 'APP_NOT_READY';
    throw error;
  }
  if (!response.ok) {
    const envelope = parseErrorEnvelope(payload, response.status);
    if (envelope.requestId && !expectedErrorCodes.includes(envelope.code)) {
      console.warn(`API ${path} failed [${envelope.code || response.status}] requestId=${envelope.requestId}`);
    }
    const error = new Error(envelope.message);
    error.code = envelope.code;
    error.retryable = envelope.retryable;
    error.details = envelope.details;
    error.requestId = envelope.requestId;
    throw error;
  }
  return payload;
}

function applyStats(stats) {
  state.stats = stats;
  elements.workspaceLabel.textContent = stats.workspace;
  elements.sidebarStats.innerHTML = '';
  const lines = [
    `${stats.files} files · ${stats.symbols} symbols`,
    `${stats.edges} relationships · ${stats.wikiPages} wiki pages`,
    `IA: ${stats.llmProvider}`,
  ];
  lines.forEach((line) => {
    const div = document.createElement('div');
    div.textContent = line;
    elements.sidebarStats.appendChild(div);
  });
  setIndexStatus(stats.indexing ? 'indexing' : 'index updated', stats.indexing ? 'active' : 'ok');
  if (stats.lastError) setIndexStatus('indexing error', 'error');
}

async function loadStats() {
  try {
    applyStats(await api('/api/stats'));
  } catch (error) {
    if (error.code === 'APP_NOT_READY') return;
    setIndexStatus('backend unavailable', 'error');
    toast(error.message, true);
  }
}

function applyTree(tree) {
  state.tree = tree;
  const first = !state.activePath && state.tabs.size === 0 ? findFirstFile(tree) : null;
  renderFileTree(first?.path || state.activePath);
  if (first) openFile(first.path);
}

async function loadTree() {
  try {
    applyTree(await api('/api/tree'));
  } catch (error) {
    if (error.code === 'APP_NOT_READY') return;
    toast(error.message, true);
  }
}

function renderFileTree(revealPath = state.activePath) {
  elements.fileTree.replaceChildren();
  state.treeItems = [];
  if (!state.tree || !state.tree.children?.length) {
    const empty = document.createElement('p');
    empty.className = 'empty-state';
    empty.textContent = 'No indexed Go/JS/TS files.';
    elements.fileTree.appendChild(empty);
    return;
  }
  const siblingCount = state.tree.children.length;
  const fragment = document.createDocumentFragment();
  state.tree.children.forEach((node, index) => fragment.appendChild(renderTreeNode(node, 1, index + 1, siblingCount, revealPath)));
  elements.fileTree.appendChild(fragment);
  refreshTreeRovingTabindex();
}

function renderTreeNode(node, depth, posInSet = 1, setSize = 1, revealPath = state.activePath) {
  if (node.type === 'directory') {
    const details = document.createElement('details');
    if (depth <= 1 || revealPath?.startsWith(`${node.path}/`)) details.open = true;
    const summary = document.createElement('summary');
    summary.textContent = node.name;
    summary.setAttribute('role', 'treeitem');
    summary.setAttribute('aria-expanded', details.open ? 'true' : 'false');
    summary.setAttribute('aria-level', String(depth));
    summary.setAttribute('aria-posinset', String(posInSet));
    summary.setAttribute('aria-setsize', String(setSize));
    summary.dataset.path = node.path || node.name;
    summary.dataset.name = node.name || '';
    summary.dataset.kind = 'directory';
    summary.title = node.path || node.name;
    details.appendChild(summary);
    const children = document.createElement('div');
    children.className = 'file-tree-children';
    children.setAttribute('role', 'group');
    const childCount = (node.children || []).length;
    (node.children || []).forEach((child, index) => children.appendChild(renderTreeNode(child, depth + 1, index + 1, childCount, revealPath)));
    details.appendChild(children);
    return details;
  }

  const button = document.createElement('button');
  button.type = 'button';
  button.className = `file-button${node.path === state.activePath ? ' active' : ''}`;
  button.title = node.path;
  button.setAttribute('role', 'treeitem');
  button.setAttribute('aria-level', String(depth));
  button.setAttribute('aria-posinset', String(posInSet));
  button.setAttribute('aria-setsize', String(setSize));
  button.setAttribute('aria-selected', node.path === state.activePath ? 'true' : 'false');
  button.dataset.path = node.path;
  button.dataset.name = node.name || '';
  button.dataset.kind = 'file';
  const dot = document.createElement('span');
  dot.className = `file-dot ${node.language || ''}`;
  dot.setAttribute('aria-hidden', 'true');
  const label = document.createElement('span');
  label.className = 'file-label';
  label.textContent = node.name;
  button.append(dot, label);
  return button;
}

function fileTreeItemForPath(root, path) {
  if (!root || !path) return null;
  return root.querySelector(`[role="treeitem"][data-kind="file"][data-path="${cssEscape(path)}"]`);
}

// A tab activation changes only selection. Keep the existing tree DOM so
// manually toggled <details>, listeners and the cached keyboard order survive.
function updateFileTreeSelection(root, previousPath, nextPath, dependencies = {}) {
  if (!root) return { previous: null, next: null };
  const findItem = dependencies.findItem || fileTreeItemForPath;
  const isVisible = dependencies.isVisible || isVisibleTreeItem;
  const previous = findItem(root, previousPath);
  const next = findItem(root, nextPath);
  if (previous && previous !== next) {
    previous.classList.remove('active');
    previous.setAttribute('aria-selected', 'false');
  }
  if (next) {
    next.classList.add('active');
    next.setAttribute('aria-selected', 'true');
    if (isVisible(next)) {
      const current = dependencies.currentRoving
        ? dependencies.currentRoving(root)
        : root.querySelector('[role="treeitem"][tabindex="0"]');
      if (current && current !== next) current.tabIndex = -1;
      next.tabIndex = 0;
    } else {
      next.tabIndex = -1;
    }
  }
  return { previous, next };
}

function visibleTreeItems() {
  return [...elements.fileTree.querySelectorAll('[role="treeitem"]')]
    .filter(isVisibleTreeItem);
}

function isVisibleTreeItem(item) {
  if (!item) return false;
  let ancestor = item.tagName === 'SUMMARY'
    ? item.closest('details')?.parentElement?.closest('details')
    : item.closest('details');
  while (ancestor) {
    if (!ancestor.open) return false;
    ancestor = ancestor.parentElement?.closest('details');
  }
  return true;
}

function refreshTreeRovingTabindex(preferred = null) {
  const items = visibleTreeItems();
  state.treeItems.forEach((item) => { item.tabIndex = -1; });
  state.treeItems = items;
  if (!items.length) return;
  const active = (preferred && items.includes(preferred) ? preferred : null)
    || items.find((item) => item.dataset.path === state.activePath)
    || items[0];
  items.forEach((item) => {
    item.tabIndex = item === active ? 0 : -1;
  });
}

function handleFileTreeKeydown(event) {
  const item = event.target.closest('[role="treeitem"]');
  const items = state.treeItems;
  if (!items.length) return;
  const current = item || items[0];
  const index = Math.max(0, items.indexOf(current));
  const focusAt = (nextIndex) => {
    event.preventDefault();
    const target = items[Math.max(0, Math.min(items.length - 1, nextIndex))];
    items.forEach((entry) => { entry.tabIndex = entry === target ? 0 : -1; });
    FocusManager.moveFocus(target);
  };
  if (event.key === 'ArrowDown') focusAt(index + 1);
  else if (event.key === 'ArrowUp') focusAt(index - 1);
  else if (event.key === 'Home') focusAt(0);
  else if (event.key === 'End') focusAt(items.length - 1);
  else if (event.key === 'ArrowRight') {
    const details = treeDetailsForItem(current);
    if (details && !details.open) {
      event.preventDefault();
      details.open = true;
      current.setAttribute('aria-expanded', 'true');
      refreshTreeRovingTabindex(current);
    } else if (details) {
      focusAt(index + 1);
    }
  } else if (event.key === 'ArrowLeft') {
    const details = treeDetailsForItem(current);
    if (details && details.open) {
      event.preventDefault();
      details.open = false;
      current.setAttribute('aria-expanded', 'false');
      refreshTreeRovingTabindex(current);
    } else {
      const parent = parentTreeItem(current);
      if (parent) {
        event.preventDefault();
        items.forEach((entry) => { entry.tabIndex = entry === parent ? 0 : -1; });
        FocusManager.moveFocus(parent);
      }
    }
  } else if (event.key === 'Enter' || event.key === ' ') {
    event.preventDefault();
    if (current.dataset.kind === 'directory') {
      const details = treeDetailsForItem(current);
      if (details) {
        details.open = !details.open;
        refreshTreeRovingTabindex(current);
      }
    } else if (current.dataset.path) {
      openFile(current.dataset.path);
    }
  }
}

function handleFileTreeClick(event) {
  const item = delegatedItem(event, elements.fileTree, '[role="treeitem"][data-kind="file"]');
  if (!item || !item.dataset.path) return;
  openFile(item.dataset.path);
}

function handleFileTreeToggle(event) {
  const details = event.target;
  if (!details || details.tagName !== 'DETAILS' || !elements.fileTree.contains(details)) return;
  const summary = details.querySelector(':scope > summary[role="treeitem"]');
  summary?.setAttribute('aria-expanded', details.open ? 'true' : 'false');
  refreshTreeRovingTabindex(summary);
}

function treeDetailsForItem(item) {
  return item && item.tagName === 'SUMMARY' ? item.closest('details') : null;
}

function parentTreeItem(item) {
  const parentDetails = item?.closest('details')?.parentElement?.closest('details');
  return parentDetails ? parentDetails.querySelector(':scope > summary[role="treeitem"]') : null;
}

function findFirstFile(node) {
  if (!node) return null;
  if (node.type === 'file') return node;
  for (const child of node.children || []) {
    const found = findFirstFile(child);
    if (found) return found;
  }
  return null;
}

async function openFile(path, line = 1, column = 1) {
  persistActiveEditorState();
  const tab = await ensureOpenTab({
    pendingOpens: state.pendingOpens,
    tabs: state.tabs,
    path,
    createTab: async () => {
      try {
        // Editable files open through the versioned document API (overlay buffer);
        // a failed open never silently falls back to direct editing.
        const doc = await reclaimTrackedDocument(path)
          || await api('/api/documents/open', { method: 'POST', body: JSON.stringify({ path }) });
        const opened = {
          documentId: doc.documentId, path, language: doc.language, content: doc.content,
          original: typeof doc.baseContent === 'string' ? doc.baseContent : doc.content,
          dirty: Boolean(doc.dirty), readOnly: false, viewState: null,
          overlay: {
            documentId: doc.documentId, leaseId: doc.leaseId, serverVersion: doc.version,
            contentHash: doc.contentHash, baseContentHash: doc.baseContentHash, baseSnapshotId: doc.baseSnapshotId,
            syncState: ['conflicted_dirty', 'external_deleted', 'external_renamed', 'resolving'].includes(doc.state)
              ? 'conflict'
              : 'idle',
            localEditRevision: 0, acknowledgedEditRevision: 0,
            inFlight: false, syncPromise: null, debounce: null,
          },
        };
        trackDocumentLease(opened);
        return opened;
      } catch (error) {
        if (error.code === 'UNSUPPORTED_LANGUAGE') {
          // Non-editable file: read-only navigation via the legacy read endpoint.
          try {
            const payload = await api(`/api/file?path=${encodeURIComponent(path)}`);
            return {
              documentId: `readonly:${path}`, path, language: payload.language || 'text', content: payload.content,
              original: payload.content, dirty: false, readOnly: true, viewState: null, overlay: null,
            };
          } catch (readError) {
            toast(readError.message, true);
            return null;
          }
        }
        toast(error.message, true);
        return null;
      }
    },
    installTab: async (opened) => {
      if (state.tabs.size > 10) {
        const removable = [...state.tabs.values()].find((item) => item.path !== path && item.path !== state.activePath && !item.dirty);
        if (removable) {
          await closeDocument(removable);
          untrackDocumentLease(removable);
          state.editorAdapter.closeModel(removable.documentId);
          state.tabs.delete(removable.path);
        }
      }
      await state.editorAdapter.openModel({
        documentId: opened.documentId,
        path: opened.path,
        language: opened.language || 'text',
        version: opened.overlay ? opened.overlay.serverVersion : 0,
        content: opened.content,
        readOnly: !!opened.readOnly,
      });
    },
  });
  if (!tab) return;

  const previousPath = state.activePath;
  state.activePath = path;
  state.editorAdapter.activateModel(tab.documentId);
  state.editorAdapter.setReadOnly(tab.documentId, !!tab.readOnly, tab.readOnly ? 'File opened in read-only mode' : '');
  elements.emptyEditor.classList.add('hidden');
  elements.editorShell.classList.remove('hidden');
  elements.fileStatus.textContent = `${path}${tab.dirty ? ' · modified' : ''}`;
  renderTabs();
  updateFileTreeSelection(elements.fileTree, previousPath, path);
  hideHover();
  const target = editorPosition(line, column);
  state.editorAdapter.revealRange(tab.documentId, { start: target, end: target });
  refreshSemanticStateForTab(tab);
  announce(`File opened: ${path}.`);
}

function ensureOpenTab({ pendingOpens, tabs, path, createTab, installTab }) {
  const existing = tabs.get(path);
  if (existing) return Promise.resolve(existing);
  const pending = pendingOpens.get(path);
  if (pending) return pending;
  const opening = Promise.resolve().then(async () => {
    const raced = tabs.get(path);
    if (raced) return raced;
    const tab = await createTab();
    if (!tab) return null;
    tabs.set(path, tab);
    try {
      await installTab(tab);
    } catch (error) {
      if (tabs.get(path) === tab) tabs.delete(path);
      throw error;
    }
    return tab;
  });
  pendingOpens.set(path, opening);
  opening.then(
    () => { if (pendingOpens.get(path) === opening) pendingOpens.delete(path); },
    () => { if (pendingOpens.get(path) === opening) pendingOpens.delete(path); },
  );
  return opening;
}

function persistActiveEditorState() {
  if (!state.activePath) return;
  const tab = state.tabs.get(state.activePath);
  if (!tab) return;
  tab.content = state.editorAdapter.getContent(tab.documentId);
  tab.viewState = state.editorAdapter.saveViewState(tab.documentId);
  tab.dirty = tab.content !== tab.original;
}

function renderTabs() {
  elements.editorTabs.replaceChildren();
  for (const tab of state.tabs.values()) {
    const active = tab.path === state.activePath;
    const tabId = domId('editor-tab', tab.path);
    const diagnostics = state.diagnosticsByDocument.get(tab.documentId);
    const button = document.createElement('button');
    button.type = 'button';
    button.className = `editor-tab${active ? ' active' : ''}`;
    button.id = tabId;
    button.title = tab.path;
    button.dataset.tabPath = tab.path;
    button.setAttribute('role', 'tab');
    button.setAttribute('aria-selected', active ? 'true' : 'false');
    button.setAttribute('aria-controls', 'editor-shell');
    button.setAttribute('aria-label', `${editorTabA11yLabel(tab, diagnosticSeverityCounts(diagnostics?.diagnostics || []))}. Delete closes the tab.`);
    button.tabIndex = active ? 0 : -1;
    const label = document.createElement('span');
    label.className = 'editor-tab-label';
    label.textContent = tab.path.split('/').pop();
    button.appendChild(label);
    if (tab.dirty) {
      const dirty = document.createElement('span');
      dirty.className = 'dirty-dot';
      dirty.title = 'Unsaved changes';
      button.appendChild(dirty);
    }
    if (tab.overlay && tab.overlay.syncState && tab.overlay.syncState !== 'idle') {
      const sync = document.createElement('span');
      sync.className = `tab-badge ${tab.overlay.syncState}`;
      sync.textContent = tab.overlay.syncState === 'conflict' ? 'conflict' : tab.overlay.syncState;
      sync.title = `Sync state: ${tab.overlay.syncState}`;
      button.appendChild(sync);
    } else if (diagnosticBadgeText(tab)) {
      const diagnostics = document.createElement('span');
      diagnostics.className = `tab-badge diagnostics ${diagnosticBadgeClass(tab)}`;
      diagnostics.textContent = diagnosticBadgeText(tab);
      diagnostics.title = 'Document diagnostics';
      button.appendChild(diagnostics);
    } else if (tab.readOnly) {
      const readOnly = document.createElement('span');
      readOnly.className = 'tab-badge readonly';
      readOnly.textContent = 'read-only';
      readOnly.title = 'File in read-only mode';
      button.appendChild(readOnly);
    }
    const close = document.createElement('span');
    close.className = 'tab-close';
    close.textContent = '×';
    close.title = 'Close';
    close.setAttribute('aria-hidden', 'true');
    button.appendChild(close);
    button.addEventListener('click', (event) => {
      if (event.target === close) closeTab(tab.path);
      else openFile(tab.path);
    });
    elements.editorTabs.appendChild(button);
  }
  if (state.activePath) {
    elements.editorShell.setAttribute('aria-labelledby', domId('editor-tab', state.activePath));
    elements.editorShell.removeAttribute('aria-label');
  } else {
    elements.editorShell.removeAttribute('aria-labelledby');
    elements.editorShell.setAttribute('aria-label', 'Open file');
  }
}

async function closeTab(path) {
  const tab = state.tabs.get(path);
  if (!tab) return;
  if (tab.dirty && !(await confirmDiscardChanges(path))) return;
  try {
    await closeDocument(tab, tab.dirty);
    untrackDocumentLease(tab);
  } catch (error) {
    toast(error.message || `Could not close ${path}.`, true);
    return;
  }
  const paths = [...state.tabs.keys()];
  const index = paths.indexOf(path);
  state.tabs.delete(path);
  clearSemanticStateForTab(tab, { removeResults: true });
  state.editorAdapter.closeModel(tab.documentId);
  if (state.activePath === path) {
    const nextPath = paths[index + 1] || paths[index - 1];
    state.activePath = null;
    updateFileTreeSelection(elements.fileTree, path, null);
    if (nextPath && state.tabs.has(nextPath)) {
      openFile(nextPath);
    } else {
      elements.editorShell.classList.add('hidden');
      elements.emptyEditor.classList.remove('hidden');
      elements.fileStatus.textContent = 'No file open';
      renderTabs();
    }
  } else {
    renderTabs();
  }
  announce(`Tab closed: ${path}.`);
}

function handleEditorTabsKeydown(event) {
  const tabs = [...elements.editorTabs.querySelectorAll('[role="tab"]')];
  if (!tabs.length) return;
  const currentIndex = Math.max(0, tabs.indexOf(document.activeElement));
  const move = (nextIndex) => {
    event.preventDefault();
    const target = tabs[(nextIndex + tabs.length) % tabs.length];
    openFile(target.dataset.tabPath);
    nextAnimationFrame(() => FocusManager.moveFocus(document.getElementById(target.id)));
  };
  if (event.key === 'ArrowRight') move(currentIndex + 1);
  else if (event.key === 'ArrowLeft') move(currentIndex - 1);
  else if (event.key === 'Home') move(0);
  else if (event.key === 'End') move(tabs.length - 1);
  else if (event.key === 'Delete' || event.key === 'Backspace') {
    const tab = tabs[currentIndex];
    if (!tab?.dataset.tabPath) return;
    event.preventDefault();
    closeTab(tab.dataset.tabPath);
  }
}

function onEditorInput(event) {
  const tab = tabForDocument(event.documentId);
  if (!tab) return;
  if (tab.readOnly) {
    return;
  }
  tab.content = event.content;
  tab.dirty = tab.content !== tab.original;
  if (tab.path === state.activePath) elements.fileStatus.textContent = `${state.activePath}${tab.dirty ? ' · modified' : ''}`;
  elements.saveStatus.textContent = tab.dirty ? 'unsaved' : '';
  elements.saveStatus.className = '';
  // Drive the per-document sync pump: local content/dirty update immediately,
  // the PUT is debounced and sequential.
  scheduleSync(tab);
  markHoverStaleForDocument(tab.documentId);
  clearSemanticStateForTab(tab, { stale: true });
  renderTabs();
}

function tabForDocument(documentId) {
  for (const tab of state.tabs.values()) {
    if (tab.documentId === documentId) return tab;
  }
  return null;
}

function updateCursorStatus(event) {
  const tab = state.activePath ? state.tabs.get(state.activePath) : null;
  if (!tab) return;
  const position = event && event.documentId === tab.documentId
    ? event.position
    : state.editorAdapter.getPosition(tab.documentId);
  elements.cursorStatus.textContent = `Ln ${position.line}, Col ${position.column}`;
}

// shouldApplyOverlayResponse decides whether a completed Hover/See More response
// still belongs to the current document, version and panel request. A stale
// response (the user kept typing, switched files, or a newer request started) is
// dropped silently. Pure and exported for testing.
function shouldApplyOverlayResponse(tab, response, requestSeq, latestSeq) {
  if (!tab || !response) return false;
  if (requestSeq !== latestSeq) return false; // a newer request superseded this one
  const overlay = tab.overlay;
  if (!overlay) return !response.ephemeral; // read-only tab: only accept non-ephemeral
  if (!response.ephemeral) return false; // an open document expects an ephemeral answer
  if (response.documentId && response.documentId !== overlay.documentId) return false;
  if (typeof response.documentVersion === 'number' && response.documentVersion !== overlay.serverVersion) return false;
  return true;
}

// scheduleSync drives the per-document sync pump from an editor edit: it bumps the
// local edit revision, marks dirty immediately, and debounces a sequential PUT.
function scheduleSync(tab) {
  if (!tab || !tab.overlay) return;
  const overlay = tab.overlay;
  overlay.localEditRevision += 1;
  clearTimeout(overlay.debounce);
  overlay.debounce = setTimeout(() => {
    overlay.debounce = null;
    runSyncPump(tab);
  }, 320);
}

// runSyncPump sends the latest content once, coalescing edits made during a
// request: a single PUT is in flight at a time, and any newer revision is sent in
// a fresh sequential version after the ack.
function runSyncPump(tab, dependencies = {}) {
  const overlay = tab.overlay;
  if (!overlay) return Promise.resolve();
  if (overlay.syncPromise) return overlay.syncPromise;
  if (overlay.acknowledgedEditRevision >= overlay.localEditRevision) return Promise.resolve();
  if (overlay.syncState === 'conflict') return Promise.resolve();
  const request = dependencies.request || api;
  const render = dependencies.render || renderTabs;
  const refresh = dependencies.refresh || refreshSemanticStateForTab;
  const notify = dependencies.notify || toast;
  overlay.inFlight = true;
  overlay.syncState = 'syncing';
  render();
  const sendingRevision = overlay.localEditRevision;
  const content = tab.content;
  let syncPromise;
  syncPromise = (async () => {
    try {
      const result = await request(`/api/documents/${overlay.documentId}/content`, {
        method: 'PUT',
        body: JSON.stringify({
          leaseId: overlay.leaseId,
          expectedVersion: overlay.serverVersion,
          newVersion: overlay.serverVersion + 1,
          content,
        }),
      });
      overlay.serverVersion = result.version;
      overlay.contentHash = result.contentHash;
      overlay.acknowledgedEditRevision = sendingRevision;
      overlay.syncState = 'idle';
      refresh(tab);
      render();
    } catch (error) {
      if (error.code === 'DOCUMENT_VERSION_CONFLICT') {
        overlay.syncState = 'conflict';
        notify('Document version conflict. Reload to continue.', true);
      } else {
        overlay.syncState = 'failed'; // keep local content; allow an explicit retry
      }
      render();
    } finally {
      overlay.inFlight = false;
      if (overlay.syncPromise === syncPromise) overlay.syncPromise = null;
    }
    // Coalesce: if newer edits arrived during the request, send them now and
    // keep the returned chain pending until the latest revision is acknowledged.
    if (overlay.syncState === 'idle' && overlay.acknowledgedEditRevision < overlay.localEditRevision) {
      return runSyncPump(tab, dependencies);
    }
  })();
  overlay.syncPromise = syncPromise;
  return syncPromise;
}

// flushSync waits for the document to be fully acknowledged before a save.
async function flushSync(tab, syncPump = runSyncPump) {
  const overlay = tab.overlay;
  if (!overlay) return;
  clearTimeout(overlay.debounce);
  overlay.debounce = null;
  while (overlay.acknowledgedEditRevision < overlay.localEditRevision) {
    await syncPump(tab);
    if (overlay.syncState === 'conflict' || overlay.syncState === 'failed') break;
  }
}

function documentLeaseForTab(tab) {
  const overlay = tab?.overlay;
  if (!overlay?.documentId || !overlay?.leaseId) return null;
  return { documentId: overlay.documentId, leaseId: overlay.leaseId, path: tab.path || '' };
}

function browserSessionStorage() {
  if (typeof sessionStorage === 'undefined') return null;
  try {
    return sessionStorage;
  } catch (_) {
    return null;
  }
}

function browserLocalStorage() {
  if (typeof window === 'undefined' || typeof window.localStorage === 'undefined') return null;
  try {
    return window.localStorage;
  } catch (_) {
    return null;
  }
}

function readTrackedDocumentLeases(storage = browserSessionStorage()) {
  if (!storage) return [];
  try {
    const parsed = JSON.parse(storage.getItem(DOCUMENT_LEASE_STORAGE_KEY) || '[]');
    if (!Array.isArray(parsed)) return [];
    return parsed.filter((item) => item && typeof item.documentId === 'string' && item.documentId
      && typeof item.leaseId === 'string' && item.leaseId);
  } catch (_) {
    return [];
  }
}

function writeTrackedDocumentLeases(leases, storage = browserSessionStorage()) {
  if (!storage) return;
  try {
    if (leases.length) storage.setItem(DOCUMENT_LEASE_STORAGE_KEY, JSON.stringify(leases));
    else storage.removeItem(DOCUMENT_LEASE_STORAGE_KEY);
  } catch (_) {
    // pagehide keepalive remains the fallback when session storage is blocked.
  }
}

function readDocumentLeaseHandoffs(storage = browserLocalStorage(), now = Date.now()) {
  if (!storage) return [];
  try {
    const parsed = JSON.parse(storage.getItem(DOCUMENT_LEASE_HANDOFF_STORAGE_KEY) || '[]');
    if (!Array.isArray(parsed)) return [];
    return parsed.filter((item) => item && typeof item.documentId === 'string' && item.documentId
      && typeof item.leaseId === 'string' && item.leaseId
      && typeof item.path === 'string' && item.path
      && Number.isFinite(item.handedOffAt)
      && now >= item.handedOffAt
      && now - item.handedOffAt <= DOCUMENT_LEASE_HANDOFF_MAX_AGE_MS);
  } catch (_) {
    return [];
  }
}

function writeDocumentLeaseHandoffs(handoffs, storage = browserLocalStorage()) {
  if (!storage) return;
  try {
    if (handoffs.length) storage.setItem(DOCUMENT_LEASE_HANDOFF_STORAGE_KEY, JSON.stringify(handoffs));
    else storage.removeItem(DOCUMENT_LEASE_HANDOFF_STORAGE_KEY);
  } catch (_) {
    // Lease TTL remains the bounded fallback when local storage is blocked.
  }
}

function removeDocumentLeaseHandoff(lease, storage = browserLocalStorage(), now = Date.now()) {
  if (!lease || !storage) return;
  writeDocumentLeaseHandoffs(
    readDocumentLeaseHandoffs(storage, now).filter((item) => (
      item.documentId !== lease.documentId && item.path !== lease.path
    )),
    storage,
  );
}

function prepareDocumentLeaseHandoff(tabs, {
  storage = browserLocalStorage(),
  now = Date.now(),
} = {}) {
  if (!storage) return [];
  const next = readDocumentLeaseHandoffs(storage, now);
  for (const tab of tabs) {
    const lease = documentLeaseForTab(tab);
    if (!lease) continue;
    const filtered = next.filter((item) => item.documentId !== lease.documentId && item.path !== lease.path);
    next.splice(0, next.length, ...filtered, { ...lease, handedOffAt: now });
  }
  writeDocumentLeaseHandoffs(next, storage);
  return next;
}

function trackDocumentLease(tab, storage = browserSessionStorage(), handoffStorage = browserLocalStorage()) {
  const lease = documentLeaseForTab(tab);
  if (!lease) return;
  const leases = readTrackedDocumentLeases(storage).filter((item) => item.documentId !== lease.documentId);
  leases.push(lease);
  writeTrackedDocumentLeases(leases, storage);
  removeDocumentLeaseHandoff(lease, handoffStorage);
}

function untrackDocumentLease(tab, storage = browserSessionStorage(), handoffStorage = browserLocalStorage()) {
  const lease = documentLeaseForTab(tab);
  if (!lease) return;
  writeTrackedDocumentLeases(
    readTrackedDocumentLeases(storage).filter((item) => item.documentId !== lease.documentId),
    storage,
  );
  removeDocumentLeaseHandoff(lease, handoffStorage);
}

function untrackLease(lease, storage = browserSessionStorage()) {
  if (!lease?.documentId) return;
  writeTrackedDocumentLeases(
    readTrackedDocumentLeases(storage).filter((item) => item.documentId !== lease.documentId),
    storage,
  );
}

// A reload in the same browser session owns the lease stored in sessionStorage.
// Transfer it atomically before trying to create a second writer; dirty server
// content is restored together with its base so dirty tracking remains exact.
async function reclaimTrackedDocument(path, {
  storage = browserSessionStorage(),
  handoffStorage = browserLocalStorage(),
  now = Date.now(),
  request = api,
} = {}) {
  const tracked = readTrackedDocumentLeases(storage).find((item) => item.path === path);
  const handedOff = readDocumentLeaseHandoffs(handoffStorage, now).find((item) => item.path === path);
  const lease = tracked || handedOff;
  if (!lease) return null;
  try {
    const snapshot = await request(`/api/documents/${encodeURIComponent(lease.documentId)}/reclaim`, {
      method: 'POST', headers: { 'X-Document-Lease': lease.leaseId }, expectedErrorCodes: ['DOCUMENT_NOT_FOUND'],
    });
    if (!snapshot || snapshot.path !== path || snapshot.documentId !== lease.documentId) {
      const error = new Error('The stored lease does not match the requested document.');
      error.code = 'DOCUMENT_NOT_FOUND';
      throw error;
    }
    if (typeof snapshot.leaseId !== 'string' || !snapshot.leaseId) {
      const error = new Error('The server did not transfer the document lease.');
      error.code = 'DOCUMENT_NOT_FOUND';
      throw error;
    }
    if (handedOff) removeDocumentLeaseHandoff(handedOff, handoffStorage, now);
    return { ...snapshot, reclaimed: true };
  } catch (error) {
    if (error?.code === 'DOCUMENT_NOT_FOUND') {
      untrackLease(lease, storage);
      removeDocumentLeaseHandoff(lease, handoffStorage, now);
      return null;
    }
    throw error;
  }
}

async function releaseDocumentLease(lease, {
  discard = false,
  keepalive = false,
  ignoreMissing = false,
  fetchFn = typeof fetch === 'function' ? fetch : null,
} = {}) {
  if (!lease?.documentId || !lease?.leaseId || !fetchFn) return false;
  const response = await fetchFn(
    `/api/documents/${encodeURIComponent(lease.documentId)}${discard ? '?discard=true' : ''}`,
    {
      method: 'DELETE',
      headers: { 'X-Document-Lease': lease.leaseId },
      keepalive,
    },
  );
  if (response.ok) return true;
  let payload = null;
  try {
    const contentType = response.headers?.get?.('content-type') || '';
    payload = contentType.includes('application/json') ? await response.json() : await response.text();
  } catch (_) {
    payload = null;
  }
  const envelope = parseErrorEnvelope(payload, response.status);
  if (ignoreMissing && (response.status === 404 || envelope.code === 'DOCUMENT_NOT_FOUND')) return true;
  const error = new Error(envelope.message);
  error.code = envelope.code;
  error.requestId = envelope.requestId;
  throw error;
}

// closeDocument resolves only after the backend has released the writable path.
function closeDocument(tab, discard = false, options = {}) {
  const lease = documentLeaseForTab(tab);
  if (!lease) return Promise.resolve(false);
  return releaseDocumentLease(lease, { ...options, discard });
}

function releaseDocumentsOnPageHide(tabs, fetchFn = typeof fetch === 'function' ? fetch : null) {
  return tabs.map((tab) => {
    const lease = documentLeaseForTab(tab);
    if (!lease) return Promise.resolve(false);
    // Clean documents close; dirty documents deliberately receive DOCUMENT_DIRTY
    // and remain reclaimable by this browser session after reload.
    return releaseDocumentLease(lease, { discard: false, keepalive: true, fetchFn }).catch(() => false);
  });
}

async function renewDocumentLeases(tabs, request = api) {
  const renewals = tabs.flatMap((tab) => {
    const lease = documentLeaseForTab(tab);
    return lease ? [{ tab, documentId: lease.documentId, leaseId: lease.leaseId }] : [];
  });
  const settled = await Promise.allSettled(renewals.map(({ documentId, leaseId }) => (
    request(
      `/api/documents/${encodeURIComponent(documentId)}/renew`,
      { method: 'POST', headers: { 'X-Document-Lease': leaseId } },
    )
  )));
  return renewals.map((renewal, index) => ({ ...renewal, settled: settled[index] }));
}

function applyDocumentLeaseRenewalResults(tabs, renewals) {
  const applied = [];
  renewals.forEach(({ tab: originalTab, documentId, leaseId, settled }) => {
    const tab = tabs.find((candidate) => candidate === originalTab);
    const currentLease = documentLeaseForTab(tab);
    if (!tab?.overlay || currentLease?.documentId !== documentId || currentLease?.leaseId !== leaseId) return;
    applied.push(tab);
    if (settled.status === 'fulfilled') {
      if (tab.overlay.syncState === 'disconnected') tab.overlay.syncState = 'idle';
      delete tab.overlay.leaseError;
      return;
    }
    tab.overlay.syncState = 'disconnected';
    tab.overlay.leaseError = settled.reason?.message || 'The editing session lost its connection.';
  });
  return applied;
}

async function recoverDisconnectedDocumentLease(tab, {
  reclaim = reclaimTrackedDocument,
  open = (path) => api('/api/documents/open', { method: 'POST', body: JSON.stringify({ path }) }),
  track = trackDocumentLease,
  flush = flushSync,
} = {}) {
  if (!tab?.overlay || tab.overlay.syncState !== 'disconnected') return true;
  const localContent = tab.content;
  const doc = await reclaim(tab.path) || await open(tab.path);
  tab.overlay = {
    documentId: doc.documentId, leaseId: doc.leaseId, serverVersion: doc.version,
    contentHash: doc.contentHash, baseContentHash: doc.baseContentHash, baseSnapshotId: doc.baseSnapshotId,
    syncState: ['conflicted_dirty', 'external_deleted', 'external_renamed', 'resolving'].includes(doc.state) ? 'conflict' : 'idle',
    localEditRevision: 0, acknowledgedEditRevision: 0,
    inFlight: false, syncPromise: null, debounce: null,
  };
  track(tab);
  if (localContent !== doc.content) {
    tab.dirty = true;
    tab.overlay.localEditRevision = 1;
    await flush(tab);
  }
  return tab.overlay.syncState !== 'disconnected';
}

async function runDocumentLeaseHeartbeat() {
  if (state.documentLeaseHeartbeatInFlight) return;
  state.documentLeaseHeartbeatInFlight = true;
  const tabs = [...state.tabs.values()];
  try {
	const priorStates = new Map(tabs.map((tab) => [tab, tab.overlay?.syncState]));
    const results = await renewDocumentLeases(tabs);
	const writable = applyDocumentLeaseRenewalResults([...state.tabs.values()], results);
	if (writable.some((tab) => priorStates.get(tab) !== tab.overlay?.syncState)) renderTabs();
  } finally {
    state.documentLeaseHeartbeatInFlight = false;
  }
}

function stopDocumentLeaseHeartbeat() {
  if (state.documentLeaseHeartbeat == null) return;
  clearInterval(state.documentLeaseHeartbeat);
  state.documentLeaseHeartbeat = null;
}

function startDocumentLeaseHeartbeat() {
  if (state.documentLeaseHeartbeat != null) return;
  void runDocumentLeaseHeartbeat();
  state.documentLeaseHeartbeat = setInterval(() => {
    void runDocumentLeaseHeartbeat();
  }, DOCUMENT_LEASE_HEARTBEAT_MS);
}

async function saveActiveFile() {
  if (!state.activePath) return;
  if (!ensureReady()) return;
  const tab = state.tabs.get(state.activePath);
  persistActiveEditorState();
  if (!tab.overlay) return; // read-only navigation tab
  elements.saveStatus.textContent = 'saving…';
  elements.saveStatus.className = '';
  try {
    if (tab.overlay.syncState === 'disconnected') {
      await recoverDisconnectedDocumentLease(tab);
    }
    await flushSync(tab);
    if (tab.overlay.syncState === 'conflict' || tab.overlay.syncState === 'failed') {
      const conflict = tab.overlay.syncState === 'conflict';
      throw Object.assign(new Error(conflict ? 'version conflict' : 'document not synchronized'), {
        code: conflict ? 'DOCUMENT_VERSION_CONFLICT' : 'DOCUMENT_SYNC_FAILED',
      });
    }
    const saved = await api(`/api/documents/${tab.overlay.documentId}/save`, {
      method: 'POST',
      body: JSON.stringify({ leaseId: tab.overlay.leaseId, version: tab.overlay.serverVersion }),
    });
    tab.original = tab.content;
    tab.dirty = false;
    tab.overlay.baseContentHash = saved.contentHash;
    tab.overlay.baseSnapshotId = saved.baseSnapshotId || tab.overlay.baseSnapshotId;
    tab.overlay.syncState = 'idle';
    elements.saveStatus.textContent = 'saved and reindexed';
    elements.saveStatus.className = 'success';
    elements.fileStatus.textContent = tab.path;
    renderTabs();
    announce(`File saved: ${tab.path}.`);
    await loadStats();
  } catch (error) {
    elements.saveStatus.textContent = 'save failed';
    elements.saveStatus.className = 'error';
    // Branch on the stable code, not the message text.
    if (isSaveConflictCode(error.code)) {
      toast('The file changed outside the editor. Reload before saving; local content was preserved.', true);
    } else {
      toast(error.message, true);
    }
    announce(`Failed to save ${tab.path}.`, 'assertive');
  }
}

function createExplainCache(maxEntries = 80) {
  const entries = new Map();
  const requestIndex = new Map();
  return {
    get(key) {
      const hit = entries.get(key);
      if (!hit) return null;
      entries.delete(key);
      entries.set(key, hit);
      return hit;
    },
    getByRequest(requestKey) {
      const responseKey = requestIndex.get(requestKey);
      return responseKey ? this.get(responseKey) : null;
    },
    set(key, value) {
      if (!key || !value) return;
      entries.delete(key);
      entries.set(key, value);
      while (entries.size > maxEntries) {
        const oldest = entries.keys().next().value;
        entries.delete(oldest);
        for (const [requestKey, responseKey] of [...requestIndex.entries()]) {
          if (responseKey === oldest) requestIndex.delete(requestKey);
        }
      }
    },
    setForRequest(requestKey, responseKey, value) {
      this.set(responseKey, value);
      if (entries.has(responseKey)) requestIndex.set(requestKey, responseKey);
    },
    invalidate(predicate) {
      for (const [key, value] of [...entries.entries()]) {
        if (predicate(value, key)) {
          entries.delete(key);
          for (const [requestKey, responseKey] of [...requestIndex.entries()]) {
            if (responseKey === key) requestIndex.delete(requestKey);
          }
        }
      }
    },
    size() {
      return entries.size;
    },
  };
}

function explainCacheKey(explanation) {
  if (!explanation || !explanation.symbol) return '';
  const provider = explanation.providerInfo?.id || explanation.provider || '';
  const model = explanation.providerInfo?.model || '';
  const symbol = explanation.symbol;
  const range = symbol.range || {};
  const semanticTarget = symbol.id ? '' : [
    symbol.path || '',
    symbol.name || symbol.qualifiedName || '',
    symbol.kind || '',
    symbol.signature || '',
    `${range.start?.line || 0}:${range.start?.column || 0}-${range.end?.line || 0}:${range.end?.column || 0}`,
  ].join('\x1e');
  return [
    explanation.feature || 'hover',
    explanation.viewHash || explanation.snapshotId || '',
    String(explanation.documentVersion || 0),
    symbol.id || semanticTarget,
    symbol.occurrenceId || explanation.occurrenceId || '',
    explanation.policyVersion || '',
    explanation.promptVersion || '',
    provider,
    model,
  ].join('\x1f');
}

function explainRequestKey(payload) {
  if (!payload) return '';
  return [
    payload.feature || '',
    payload.documentId || '',
    String(payload.documentVersion || 0),
    payload.path || '',
    payload.position ? `${payload.position.line}:${payload.position.column}:${payload.position.encoding}` : '',
    payload.target?.symbolId || '',
    payload.target?.occurrenceId || '',
  ].join('\x1f');
}

function rememberExplainResponse(explanation, requestKey = '') {
  const key = explainCacheKey(explanation);
  if (!key) return;
  const cache = explanation.feature === 'see_more' ? state.seeMoreCache : state.hoverCache;
  if (requestKey) cache.setForRequest(requestKey, key, explanation);
  else cache.set(key, explanation);
}

function cachedExplainResponse(feature, requestKey) {
  const cache = feature === 'see_more' ? state.seeMoreCache : state.hoverCache;
  return requestKey ? cache.getByRequest(requestKey) : null;
}

async function buildExplainPayloadForTab(tab, target, feature, targetIds = null) {
  if (!tab || !target) return null;
  const overlay = tab.overlay;
  const capturedRevision = overlay ? overlay.localEditRevision : 0;
  if (overlay && overlay.acknowledgedEditRevision < capturedRevision) {
    await flushSync(tab);
    if (overlay.localEditRevision !== capturedRevision || overlay.acknowledgedEditRevision < capturedRevision) {
      return null;
    }
  }
  if (overlay && (overlay.syncState === 'conflict' || overlay.syncState === 'failed')) {
    const error = new Error(overlay.syncState === 'conflict' ? 'Document version conflict.' : 'Document not synchronized.');
    error.code = overlay.syncState === 'conflict' ? 'DOCUMENT_VERSION_CONFLICT' : 'DOCUMENT_SYNC_FAILED';
    throw error;
  }
  const payload = {
    feature,
    path: tab.path,
    position: editorPosition(target.line, target.column),
  };
  if (overlay) {
    payload.documentId = overlay.documentId;
    payload.documentVersion = overlay.serverVersion;
  }
  if (feature === 'see_more') {
    const symbolId = targetIds?.symbolId || '';
    const occurrenceId = targetIds?.occurrenceId || '';
    if (symbolId || occurrenceId) payload.target = { symbolId, occurrenceId };
  }
  return payload;
}

function shouldApplyExplainResponse(tab, response, requestSeq, latestSeq, feature) {
  if (!shouldApplyOverlayResponse(tab, response, requestSeq, latestSeq)) return false;
  if (response.feature && response.feature !== feature) return false;
  return true;
}

function createNavigationHistory(limit = NAV_HISTORY_LIMIT) {
  return { entries: [], index: -1, limit };
}

function pushNavigationLocation(history, location) {
  if (!history || !location || !location.path || !location.range) return;
  const current = history.entries[history.index];
  const next = { ...location, recordedAt: location.recordedAt || Date.now() };
  if (current && equivalentNavigationLocation(current, next)) return;
  if (history.index < history.entries.length - 1) {
    history.entries.splice(history.index + 1);
  }
  history.entries.push(next);
  while (history.entries.length > history.limit) history.entries.shift();
  history.index = history.entries.length - 1;
}

function canNavigateBack(history = state.navHistory) {
  return !!history && history.index > 0;
}

function canNavigateForward(history = state.navHistory) {
  return !!history && history.index >= 0 && history.index < history.entries.length - 1;
}

function navigateHistoryBack(history = state.navHistory) {
  if (!canNavigateBack(history)) return null;
  history.index -= 1;
  return history.entries[history.index];
}

function navigateHistoryForward(history = state.navHistory) {
  if (!canNavigateForward(history)) return null;
  history.index += 1;
  return history.entries[history.index];
}

function equivalentNavigationLocation(left, right) {
  return left.path === right.path
    && (left.snapshotId || '') === (right.snapshotId || '')
    && (left.viewHash || '') === (right.viewHash || '')
    && Math.abs((left.range?.start?.line || 0) - (right.range?.start?.line || 0)) <= 1
    && Math.abs((left.range?.start?.column || 0) - (right.range?.start?.column || 0)) <= 1;
}

function buildNavigationRequestPayload(kind, tab, position, targetIds = null, limit = NAV_DEFAULT_LIMIT) {
  const payload = { kind, path: tab.path };
  if (tab.overlay) {
    payload.documentId = tab.overlay.documentId;
    payload.documentVersion = tab.overlay.serverVersion;
  }
  if (position) payload.position = editorPosition(position.line, position.column);
  if (targetIds?.symbolId) payload.symbolId = targetIds.symbolId;
  if (targetIds?.occurrenceId) payload.occurrenceId = targetIds.occurrenceId;
  if (limit) payload.limit = limit;
  return payload;
}

function shouldApplyNavigationResponse(response, check) {
  if (!response || !check) return false;
  if (check.sequence !== check.latestSequence) return false;
  if (response.kind !== check.kind) return false;
  if (check.documentId && response.documentId && response.documentId !== check.documentId) return false;
  if (typeof check.documentVersion === 'number' && typeof response.documentVersion === 'number'
    && response.documentVersion !== check.documentVersion) return false;
  return true;
}

async function buildNavigationPayloadForTab(kind, tab, position, targetIds = null, limit = NAV_DEFAULT_LIMIT) {
  if (!tab) return null;
  const overlay = tab.overlay;
  const capturedRevision = overlay ? overlay.localEditRevision : 0;
  if (overlay && overlay.acknowledgedEditRevision < capturedRevision) {
    await flushSync(tab);
    if (overlay.localEditRevision !== capturedRevision || overlay.acknowledgedEditRevision < capturedRevision) {
      return null;
    }
  }
  if (overlay && (overlay.syncState === 'conflict' || overlay.syncState === 'failed')) {
    const error = new Error(overlay.syncState === 'conflict' ? 'Document version conflict.' : 'Document not synchronized.');
    error.code = overlay.syncState === 'conflict' ? 'DOCUMENT_VERSION_CONFLICT' : 'DOCUMENT_SYNC_FAILED';
    throw error;
  }
  return buildNavigationRequestPayload(kind, tab, position, targetIds, limit);
}

function currentNavigationLocation() {
  if (!state.activePath) return null;
  const tab = state.tabs.get(state.activePath);
  if (!tab) return null;
  const position = state.editorAdapter.getPosition(tab.documentId);
  const range = { start: position, end: position };
  const snapshotId = tab.overlay?.baseSnapshotId || state.stats?.snapshotId || '';
  return {
    documentId: tab.overlay?.documentId || tab.documentId,
    path: tab.path,
    range,
    documentVersion: tab.overlay?.serverVersion,
    snapshotId,
    viewState: state.editorAdapter.saveViewState(tab.documentId),
    recordedAt: Date.now(),
  };
}

async function runNavigationCommand(kind, options = {}) {
  if (!state.activePath || !ensureReady()) return;
  const tab = state.tabs.get(state.activePath);
  if (!tab) return;
  const position = state.editorAdapter.getPosition(tab.documentId);
  const wordRange = state.editorAdapter.wordRangeAtPosition(tab.documentId, position);
  if (!wordRange) {
    toast('No symbol under the cursor.', true);
    return;
  }
  const fromLocation = currentNavigationLocation();
  state.navController?.abort();
  state.navController = new AbortController();
  const seq = (state.navSeq += 1);
  try {
    const payload = await buildNavigationPayloadForTab(kind, tab, position, options.targetIds || null, options.limit || NAV_DEFAULT_LIMIT);
    if (!payload || seq !== state.navSeq) return;
    const response = await api('/api/navigation/query', {
      method: 'POST',
      body: JSON.stringify(payload),
      signal: state.navController.signal,
    });
    if (!shouldApplyNavigationResponse(response, {
      sequence: seq,
      latestSequence: state.navSeq,
      kind,
      documentId: payload.documentId,
      documentVersion: payload.documentVersion,
    })) return;
    handleNavigationResult(response, fromLocation, { forcePanel: !!options.forcePanel });
  } catch (error) {
    if (error.name !== 'AbortError') toast(error.message || 'Navigation failed.', true);
  }
}

function handleNavigationResult(result, fromLocation, options = {}) {
  state.lastNavigationResult = result;
  const targets = Array.isArray(result.targets) ? result.targets : [];
  if (!targets.length) {
    toast('No results.');
    renderNavigationResults(result);
    return;
  }
  const internalTargets = targets.filter((target) => target && !target.external && target.path);
  if (!options.forcePanel && targets.length === 1 && internalTargets.length === 1) {
    navigateToNavigationTarget(result, internalTargets[0], fromLocation);
    return;
  }
  renderNavigationResults(result);
}

function renderNavigationResults(result) {
  activateInspector('navigation');
  if (!elements.navResults || !elements.navSummary) return;
  const targets = Array.isArray(result?.targets) ? result.targets : [];
  state.navResultItems = [];
  elements.navSummary.textContent = navigationSummaryText(result);
  if (!targets.length) {
    const empty = document.createElement('p');
    empty.className = 'empty-state';
    empty.textContent = 'No results.';
    elements.navResults.replaceChildren(empty);
    return;
  }
  const fragment = document.createDocumentFragment();
  const groups = new Map();
  targets.forEach((target) => {
    const key = target.external ? 'External' : (target.path || 'Unknown');
    if (!groups.has(key)) groups.set(key, []);
    groups.get(key).push(target);
  });
  let firstButton = null;
  for (const [path, groupTargets] of groups.entries()) {
    const header = document.createElement('div');
    header.className = 'nav-result-group';
    header.textContent = path;
    fragment.appendChild(header);
    groupTargets.forEach((target) => {
      const button = document.createElement('button');
      button.type = 'button';
      button.className = `nav-result-item${target.external ? ' external' : ''}`;
      button.dataset.targetId = target.targetId || '';
      button.setAttribute('role', 'option');
      button.setAttribute('aria-label', `${target.label || target.symbolId || 'target'}, ${navigationTargetDetail(target)}`);
      const badge = document.createElement('span');
      badge.className = 'symbol-kind';
      badge.textContent = target.relationship || result.kind;
      const body = document.createElement('span');
      const label = document.createElement('strong');
      label.textContent = target.label || target.symbolId || 'target';
      const detail = document.createElement('small');
      detail.textContent = navigationTargetDetail(target);
      body.append(label, detail);
      const confidence = document.createElement('span');
      confidence.className = 'nav-confidence';
      confidence.textContent = typeof target.confidence === 'number' ? `${Math.round(target.confidence * 100)}%` : '';
      button.append(badge, body, confidence);
      fragment.appendChild(button);
      state.navResultItems.push(button);
      if (!firstButton) firstButton = button;
    });
  }
  elements.navResults.replaceChildren(fragment);
  firstButton?.focus({ preventScroll: true });
  announce(navigationSummaryText(result));
}

function navigationSummaryText(result) {
  if (!result) return '';
  const total = Number(result.total || 0);
  const shown = Array.isArray(result.targets) ? result.targets.length : 0;
  const coverage = result.semanticCoverage?.coverage || '';
  const truncated = result.truncated ? ' · truncated' : '';
  return `${result.kind}: ${shown}/${total} result(s)${coverage ? ` · ${coverage}` : ''}${truncated}`;
}

function navigationTargetDetail(target) {
  if (target.external) return 'External boundary';
  const line = target.selectionRange?.start?.line || target.range?.start?.line || 1;
  return `${target.path}:${line}`;
}

function handleNavigationResultsKeydown(event) {
  const items = state.navResultItems;
  if (!items.length) return;
  const currentIndex = Math.max(0, items.indexOf(document.activeElement));
  if (event.key === 'ArrowDown' || event.key === 'ArrowUp') {
    event.preventDefault();
    const delta = event.key === 'ArrowDown' ? 1 : -1;
    const next = (currentIndex + delta + items.length) % items.length;
    items[next].focus();
  } else if (event.key === 'Enter') {
    event.preventDefault();
    document.activeElement.click();
  } else if (event.key === 'Escape') {
    event.preventDefault();
    const tab = state.activePath ? state.tabs.get(state.activePath) : null;
    if (tab) state.editorAdapter.activateModel(tab.documentId);
  }
}

function handleNavigationResultClick(event) {
  const item = delegatedItem(event, elements.navResults, '.nav-result-item[data-target-id]');
  if (!item) return;
  selectNavigationTarget(item.dataset.targetId);
}

function selectNavigationTarget(targetId) {
  const result = state.lastNavigationResult;
  const target = (result?.targets || []).find((item) => item.targetId === targetId);
  if (!target) return;
  navigateToNavigationTarget(result, target, currentNavigationLocation());
}

async function navigateToNavigationTarget(result, target, fromLocation = null) {
  if (!target || target.external || !target.path) {
    toast('External targets are not opened automatically.', true);
    return;
  }
  if (fromLocation) pushNavigationLocation(state.navHistory, fromLocation);
  const range = target.selectionRange || target.range || { start: editorPosition(1, 1), end: editorPosition(1, 1) };
  await openFile(target.path, range.start.line, range.start.column);
  const tab = state.tabs.get(target.path);
  if (tab) {
    state.editorAdapter.setDecorations(tab.documentId, 'navigation', [{ range, className: 'navigation-highlight', title: target.label }]);
    setTimeout(() => state.editorAdapter.setDecorations(tab.documentId, 'navigation', []), 1500);
  }
  pushNavigationLocation(state.navHistory, {
    documentId: result.documentId || tab?.documentId,
    path: target.path,
    symbolId: target.symbolId,
    occurrenceId: target.occurrenceId,
    range,
    documentVersion: result.documentVersion,
    snapshotId: result.snapshotId || '',
    viewHash: result.viewHash || '',
    recordedAt: Date.now(),
  });
}

async function navigateToKnownLocation(location) {
  if (!location || !location.path || !location.range) return;
  const fromLocation = currentNavigationLocation();
  if (fromLocation) pushNavigationLocation(state.navHistory, fromLocation);
  await openFile(location.path, location.range.start.line, location.range.start.column);
  pushNavigationLocation(state.navHistory, {
    documentId: location.documentId,
    path: location.path,
    symbolId: location.symbolId,
    occurrenceId: location.occurrenceId,
    range: location.range,
    documentVersion: location.documentVersion,
    snapshotId: location.snapshotId || state.stats?.snapshotId || '',
    viewHash: location.viewHash || '',
    recordedAt: Date.now(),
  });
}

async function navigateBack() {
  const location = navigateHistoryBack(state.navHistory);
  if (!location) {
    toast('No previous navigation entry.');
    return;
  }
  await goToNavigationLocation(location);
}

async function navigateForward() {
  const location = navigateHistoryForward(state.navHistory);
  if (!location) {
    toast('No next navigation entry.');
    return;
  }
  await goToNavigationLocation(location);
}

async function goToNavigationLocation(location) {
  if (!location || !location.path || !location.range) return;
  await openFile(location.path, location.range.start.line, location.range.start.column);
  const tab = state.tabs.get(location.path);
  if (tab && location.viewState) {
    state.editorAdapter.restoreViewState(tab.documentId, location.viewState);
  }
}

function shouldApplyDiagnosticsResponse(response, check) {
  if (!response || !check) return false;
  if (check.sequence !== check.latestSequence) return false;
  if (response.documentId !== check.documentId) return false;
  if (response.documentVersion !== check.documentVersion) return false;
  if (response.contentHash !== check.contentHash) return false;
  if (response.semanticCoverage?.lsp === 'available' && !response.providerSession) return false;
  return true;
}

function refreshSemanticStateForTab(tab) {
  if (!tab?.overlay || tab.readOnly) {
    clearSemanticStateForTab(tab, { removeResults: true });
    updateSemanticStatus();
    return;
  }
  requestSemanticTokens(tab);
  requestDiagnostics(tab);
}

function clearSemanticStateForTab(tab, options = {}) {
  if (!tab) return;
  state.semanticTokenControllers.get(tab.documentId)?.abort();
  state.diagnosticControllers.get(tab.documentId)?.abort();
  state.editorAdapter.setDecorations(tab.documentId, SEMANTIC_TOKEN_OWNER, []);
  state.editorAdapter.setDecorations(tab.documentId, LSP_DIAGNOSTICS_OWNER, []);
  state.editorAdapter.setDecorations(tab.documentId, PARSER_DIAGNOSTICS_OWNER, []);
  if (options.removeResults) {
    state.semanticTokensByDocument.delete(tab.documentId);
    state.semanticTokenSeq.delete(tab.documentId);
    state.diagnosticsByDocument.delete(tab.documentId);
    state.diagnosticSeq.delete(tab.documentId);
  } else if (options.stale) {
    const prior = state.diagnosticsByDocument.get(tab.documentId);
    if (prior) state.diagnosticsByDocument.set(tab.documentId, { ...prior, stale: true });
    const semantic = state.semanticTokensByDocument.get(tab.documentId);
    if (semantic) state.semanticTokensByDocument.set(tab.documentId, { ...semantic, stale: true });
  }
  updateSemanticStatus();
  renderProblemsPanel();
}

function shouldApplySemanticTokensResponse(response, check) {
  if (!response || !check) return false;
  if (response.legendVersion !== SEMANTIC_TOKEN_LEGEND_VERSION) return false;
  if (check.sequence !== check.latestSequence) return false;
  if (response.documentId !== check.documentId || response.documentVersion !== check.documentVersion) return false;
  if (response.contentHash !== check.contentHash) return false;
  if (response.semanticCoverage?.providerState === 'available' && !response.providerSession) return false;
	return true;
}

function isCurrentDocumentRequest(tab, sequenceMap, check) {
  if (!tab?.overlay || !check) return false;
  if (sequenceMap.get(tab.documentId) !== check.sequence) return false;
  return tab.overlay.documentId === check.documentId
    && tab.overlay.serverVersion === check.documentVersion
    && tab.overlay.contentHash === check.contentHash;
}

async function requestSemanticTokens(tab) {
  if (!tab?.overlay) return;
  state.semanticTokenControllers.get(tab.documentId)?.abort();
  const controller = new AbortController();
  state.semanticTokenControllers.set(tab.documentId, controller);
  const sequence = (state.semanticTokenSeq.get(tab.documentId) || 0) + 1;
  state.semanticTokenSeq.set(tab.documentId, sequence);
  const documentId = tab.overlay.documentId;
  const documentVersion = tab.overlay.serverVersion;
  const contentHash = tab.overlay.contentHash;
  try {
    const response = await api(`/api/documents/${encodeURIComponent(documentId)}/semantic-tokens?version=${encodeURIComponent(documentVersion)}`, {
      signal: controller.signal,
    });
    const check = {
      sequence,
      latestSequence: state.semanticTokenSeq.get(tab.documentId),
      documentId,
      documentVersion,
      contentHash,
    };
    if (!isCurrentDocumentRequest(tab, state.semanticTokenSeq, check)
      || !shouldApplySemanticTokensResponse(response, check)) return;
    state.semanticTokensByDocument.set(tab.documentId, response);
    state.editorAdapter.setDecorations(tab.documentId, SEMANTIC_TOKEN_OWNER, semanticTokensToDecorations(response));
    updateSemanticStatus();
  } catch (error) {
    if (error.name === 'AbortError') return;
    if (!isCurrentDocumentRequest(tab, state.semanticTokenSeq, { sequence, documentId, documentVersion, contentHash })) return;
    state.semanticTokensByDocument.set(tab.documentId, {
      legendVersion: SEMANTIC_TOKEN_LEGEND_VERSION, documentId, documentVersion, contentHash, tokens: [],
      semanticCoverage: { coverage: 'none', providerState: 'unavailable', provider: 'none', llm: false },
      error: error.message || 'Semantic tokens unavailable.',
    });
    state.editorAdapter.setDecorations(tab.documentId, SEMANTIC_TOKEN_OWNER, []);
    updateSemanticStatus();
  }
}

function semanticTokensToDecorations(response) {
  return (response?.tokens || []).map((token, index) => ({
    id: `semantic:${index}`,
    range: token.range,
    className: [
      'semantic-token',
      `semantic-type-${safeClassName(token.tokenType)}`,
      ...(token.modifiers || []).map((modifier) => `semantic-modifier-${safeClassName(modifier)}`),
    ].join(' '),
    title: `${token.tokenType}${token.modifiers?.length ? ` (${token.modifiers.join(', ')})` : ''}`,
  }));
}

async function requestDiagnostics(tab) {
  if (!tab?.overlay) return;
  state.diagnosticControllers.get(tab.documentId)?.abort();
  const controller = new AbortController();
  state.diagnosticControllers.set(tab.documentId, controller);
  const sequence = (state.diagnosticSeq.get(tab.documentId) || 0) + 1;
  state.diagnosticSeq.set(tab.documentId, sequence);
  const documentId = tab.overlay.documentId;
  const version = tab.overlay.serverVersion;
	const contentHash = tab.overlay.contentHash;
  try {
    const response = await api(`/api/documents/${encodeURIComponent(documentId)}/diagnostics?version=${encodeURIComponent(version)}`, {
      signal: controller.signal,
    });
    const check = {
      sequence,
      latestSequence: state.diagnosticSeq.get(tab.documentId),
      documentId,
      documentVersion: version,
      contentHash,
    };
    if (!isCurrentDocumentRequest(tab, state.diagnosticSeq, check)
      || !shouldApplyDiagnosticsResponse(response, check)) return;
    applyDiagnostics(tab, response);
  } catch (error) {
	if (error.name !== 'AbortError' && isCurrentDocumentRequest(tab, state.diagnosticSeq, {
		sequence, documentId, documentVersion: version, contentHash,
	})) {
      state.diagnosticsByDocument.set(tab.documentId, {
        documentId,
        documentVersion: version,
		contentHash,
        diagnostics: [],
        semanticCoverage: { parser: 'unavailable', lsp: 'disabled' },
        truncated: false,
        error: error.message || 'Diagnostics unavailable.',
      });
      updateSemanticStatus();
      renderProblemsPanel();
    }
  }
}

function applyDiagnostics(tab, response) {
  state.diagnosticsByDocument.set(tab.documentId, response);
  const parserDiagnostics = response.diagnostics.filter((diagnostic) => diagnostic.source === 'tree-sitter');
  const lspDiagnostics = response.diagnostics.filter((diagnostic) => diagnostic.source !== 'tree-sitter');
  state.editorAdapter.setDecorations(tab.documentId, PARSER_DIAGNOSTICS_OWNER, diagnosticsToDecorations({ ...response, diagnostics: parserDiagnostics }));
  state.editorAdapter.setDecorations(tab.documentId, LSP_DIAGNOSTICS_OWNER, diagnosticsToDecorations({ ...response, diagnostics: lspDiagnostics }));
  updateSemanticStatus();
  renderTabs();
  renderProblemsPanel();
}

function diagnosticsToDecorations(response) {
  return (response?.diagnostics || []).map((diagnostic) => ({
    id: diagnostic.diagnosticId,
    range: diagnostic.range,
    className: `diagnostic-marker diagnostic-${diagnostic.severity || 'info'}`,
    title: diagnosticTitle(diagnostic),
  }));
}

function diagnosticTitle(diagnostic) {
  const code = diagnostic.code ? ` ${diagnostic.code}` : '';
  const stale = diagnostic.versionKnown === false ? ' (uncertain version)' : '';
  return `${diagnostic.source || 'diagnostic'}${code}: ${diagnostic.message || ''}${stale}`;
}

function diagnosticSeverityCounts(diagnostics) {
  const counts = { error: 0, warning: 0, info: 0, hint: 0 };
  for (const diagnostic of diagnostics || []) {
    if (Object.prototype.hasOwnProperty.call(counts, diagnostic.severity)) counts[diagnostic.severity] += 1;
  }
  return counts;
}

function diagnosticBadgeText(tab) {
  const response = state.diagnosticsByDocument.get(tab.documentId);
  if (!response || response.stale || !Array.isArray(response.diagnostics)) return '';
  const counts = diagnosticSeverityCounts(response.diagnostics);
  if (counts.error > 0) return String(counts.error);
  if (counts.warning > 0) return String(counts.warning);
  return '';
}

function diagnosticBadgeClass(tab) {
  const response = state.diagnosticsByDocument.get(tab.documentId);
  const counts = diagnosticSeverityCounts(response?.diagnostics || []);
  if (counts.error > 0) return 'error';
  if (counts.warning > 0) return 'warning';
  return 'info';
}

function aggregateDiagnosticsResponse() {
  const diagnostics = [];
  let truncated = false;
  for (const response of state.diagnosticsByDocument.values()) {
    if (response?.stale || !Array.isArray(response.diagnostics)) continue;
    diagnostics.push(...response.diagnostics);
    truncated = truncated || !!response.truncated;
  }
  return {
    documentId: 'all',
    documentVersion: 0,
    snapshotId: state.stats?.snapshotId || '',
    viewHash: '',
    diagnostics,
    semanticCoverage: { parser: 'available', lsp: 'disabled' },
    truncated,
  };
}

function sortedDiagnostics(diagnostics) {
  return [...(diagnostics || [])].sort((left, right) => {
    const severity = diagnosticSeverityRank(left.severity) - diagnosticSeverityRank(right.severity);
    if (severity !== 0) return severity;
    if ((left.path || '') !== (right.path || '')) return (left.path || '').localeCompare(right.path || '');
    if ((left.range?.start?.line || 0) !== (right.range?.start?.line || 0)) return (left.range?.start?.line || 0) - (right.range?.start?.line || 0);
    if ((left.range?.start?.column || 0) !== (right.range?.start?.column || 0)) return (left.range?.start?.column || 0) - (right.range?.start?.column || 0);
    return (left.diagnosticId || '').localeCompare(right.diagnosticId || '');
  });
}

function visibleProblemRows(response, window = {}) {
  const filtered = filteredProblemDiagnostics(response, window);
  const offset = Math.max(0, Math.trunc(window.offset || 0));
  const limit = Math.max(0, Math.trunc(window.limit || PROBLEMS_VISIBLE_LIMIT));
  return filtered.slice(offset, offset + limit).map((diagnostic, index) => ({ rowIndex: offset + index, diagnostic, total: filtered.length }));
}

function filteredProblemDiagnostics(response, window = {}) {
  return sortedDiagnostics(response?.diagnostics || []).filter((diagnostic) => {
    const severity = window.severity || 'all';
    if (severity !== 'all' && diagnostic.severity !== severity) return false;
    if (window.source && !(diagnostic.source || '').includes(window.source)) return false;
    if (window.path && !(diagnostic.path || '').includes(window.path)) return false;
    return true;
  });
}

function renderProblemsPanel() {
  if (!elements.problemsResults || !elements.problemsSummary) return;
  const aggregate = aggregateDiagnosticsResponse();
  const window = {
    offset: 0,
    limit: PROBLEMS_VISIBLE_LIMIT,
    severity: state.problemFilters.severity,
    source: state.problemFilters.source,
    path: state.problemFilters.path,
  };
  const rows = visibleProblemRows(aggregate, window);
  const counts = diagnosticSeverityCounts(aggregate.diagnostics);
  const total = filteredProblemDiagnostics(aggregate, window).length;
  elements.problemsSummary.textContent = `${rows.length}/${total} problem(s) · ${counts.error} error(s), ${counts.warning} warning(s)${aggregate.truncated ? ' · truncated' : ''}`;
  state.problemItems = [];
  if (!rows.length) {
    const empty = document.createElement('p');
    empty.className = 'empty-state';
    empty.textContent = 'No problems match the current filters.';
    elements.problemsResults.replaceChildren(empty);
    return;
  }
  const fragment = document.createDocumentFragment();
  rows.forEach(({ diagnostic }) => {
    const button = document.createElement('button');
    button.type = 'button';
    button.className = `problem-row ${diagnostic.severity || 'info'}${diagnostic.versionKnown === false ? ' unknown-version' : ''}`;
    button.dataset.diagnosticId = diagnostic.diagnosticId || '';
    button.setAttribute('role', 'option');
    button.setAttribute('aria-label', `${diagnostic.severity || 'info'}: ${diagnostic.message || 'Diagnostic'} at ${diagnostic.path || ''}:${diagnostic.range?.start?.line || 1}`);
    const severity = document.createElement('span');
    severity.className = 'problem-severity';
    severity.textContent = diagnostic.severity || 'info';
    const body = document.createElement('span');
    const message = document.createElement('strong');
    message.textContent = diagnostic.message || 'Diagnostic';
    const detail = document.createElement('small');
    const line = diagnostic.range?.start?.line || 1;
    detail.textContent = `${diagnostic.path || ''}:${line} · ${diagnostic.source || 'unknown'}${diagnostic.versionKnown === false ? ' · uncertain version' : ''}`;
    body.append(message, detail);
    button.append(severity, body);
    fragment.appendChild(button);
    state.problemItems.push(button);
  });
  elements.problemsResults.replaceChildren(fragment);
}

function handleProblemClick(event) {
  const item = delegatedItem(event, elements.problemsResults, '.problem-row[data-diagnostic-id]');
  if (!item) return;
  selectProblemDiagnostic(item.dataset.diagnosticId);
}

function selectProblemDiagnostic(diagnosticId) {
  const aggregate = aggregateDiagnosticsResponse();
  const diagnostic = aggregate.diagnostics.find((item) => item.diagnosticId === diagnosticId);
  if (!diagnostic || !diagnostic.path || !diagnostic.range) return;
  navigateToKnownLocation({
    path: diagnostic.path,
    range: diagnostic.range,
    snapshotId: aggregate.snapshotId || state.stats?.snapshotId || '',
  });
}

function handleProblemsKeydown(event) {
  const items = state.problemItems;
  if (!items.length) return;
  const currentIndex = Math.max(0, items.indexOf(document.activeElement));
  if (event.key === 'ArrowDown' || event.key === 'ArrowUp') {
    event.preventDefault();
    const delta = event.key === 'ArrowDown' ? 1 : -1;
    const next = (currentIndex + delta + items.length) % items.length;
    items[next].focus();
  } else if (event.key === 'Enter') {
    event.preventDefault();
    document.activeElement.click();
  }
}

function refreshActiveDiagnostics() {
  const tab = state.activePath ? state.tabs.get(state.activePath) : null;
  if (tab) refreshSemanticStateForTab(tab);
}

function updateSemanticStatus() {
  const tab = state.activePath ? state.tabs.get(state.activePath) : null;
  if (!tab) {
    if (elements.semanticStatus) elements.semanticStatus.textContent = '';
    if (elements.diagnosticsStatus) elements.diagnosticsStatus.textContent = '';
    return;
  }
  const semantic = state.semanticTokensByDocument.get(tab.documentId);
  if (elements.semanticStatus) {
    if (tab.readOnly || !tab.overlay) elements.semanticStatus.textContent = 'semantic: n/a';
    else if (!semantic || semantic.stale) elements.semanticStatus.textContent = 'semantic: aguardando';
    else if (semantic.error) elements.semanticStatus.textContent = 'semantic: error';
    else if (semantic.semanticCoverage?.providerState !== 'available') elements.semanticStatus.textContent = `semantic: ${semantic.semanticCoverage?.providerState || 'unavailable'}`;
    else elements.semanticStatus.textContent = `semantic: ${semantic.tokens?.length || 0}`;
  }
  const diagnostics = state.diagnosticsByDocument.get(tab.documentId);
  if (elements.diagnosticsStatus) {
    if (tab.readOnly || !tab.overlay) elements.diagnosticsStatus.textContent = 'diagnostics: n/a';
    else if (!diagnostics || diagnostics.stale) elements.diagnosticsStatus.textContent = 'diagnostics: waiting';
    else if (diagnostics.error) elements.diagnosticsStatus.textContent = 'diagnostics: error';
    else {
      const counts = diagnosticSeverityCounts(diagnostics.diagnostics || []);
      elements.diagnosticsStatus.textContent = `diagnostics: ${counts.error}/${counts.warning}`;
    }
  }
}

function diagnosticSeverityRank(severity) {
  switch (severity) {
    case 'error':
      return 0;
    case 'warning':
      return 1;
    case 'info':
      return 2;
    case 'hint':
      return 3;
    default:
      return 4;
  }
}

function safeClassName(value) {
  return String(value || 'unknown').replace(/[^a-z0-9_-]/gi, '-').toLowerCase();
}

function cssEscape(value) {
  if (typeof CSS !== 'undefined' && typeof CSS.escape === 'function') return CSS.escape(String(value));
  return String(value).replace(/["\\\]\[]/g, '\\$&');
}

function renderHoverLoading(target) {
  updateHoverA11y();
  elements.hoverCard.setAttribute('aria-busy', 'true');
  elements.hoverKind.textContent = '...';
  elements.hoverName.textContent = 'Generating';
  elements.hoverSignature.textContent = '';
  elements.hoverSummary.textContent = 'Querying context and provider...';
  elements.hoverProvider.textContent = '';
  elements.hoverFreshness.textContent = '';
  elements.hoverCoverage.textContent = '';
  elements.hoverObservations.replaceChildren();
  elements.hoverError.classList.add('hidden');
  elements.hoverStatus.textContent = 'loading';
  elements.seeMoreButton.disabled = true;
  elements.hoverCard.classList.remove('hidden');
  positionHoverCard(target.clientX, target.clientY);
  announce('Generating quick explanation.');
}

function renderHoverExplanation(explanation) {
  const result = normalizedExplainResult(explanation);
  updateHoverA11y();
  elements.hoverCard.setAttribute('aria-busy', 'false');
  elements.hoverKind.textContent = explanation.symbol?.kind || '';
  elements.hoverName.textContent = explanation.symbol?.name || explanation.symbol?.qualifiedName || '';
  elements.hoverSignature.textContent = explanation.symbol?.signature || explanation.symbol?.qualifiedName || '';
  elements.hoverSummary.textContent = result.summary || explanation.summary || '';
  elements.hoverProvider.textContent = explanation.providerInfo?.model
    ? `${explanation.providerInfo.id}:${explanation.providerInfo.model}`
    : explanation.provider || explanation.providerInfo?.id || '';
  elements.hoverFreshness.textContent = freshnessLabel(explanation);
  elements.hoverCoverage.textContent = coverageLabel(explanation);
  elements.hoverError.classList.add('hidden');
  elements.hoverStatus.textContent = state.hoverStale ? 'stale' : 'ready';
  elements.hoverObservations.replaceChildren();
  appendClaims(elements.hoverObservations, result.observations.slice(0, 3), explanation);
  if (result.inferences.length || result.uncertainties.length) {
    const meta = document.createElement('small');
    meta.className = 'hover-meta-line';
    meta.textContent = `${result.inferences.length} inference(s), ${result.uncertainties.length} uncertainty(ies)`;
    elements.hoverObservations.appendChild(meta);
  }
  elements.seeMoreButton.disabled = !canOpenSeeMore(explanation, { stale: state.hoverStale });
  elements.seeMoreButton.setAttribute('aria-label', `Open the expanded explanation for ${elements.hoverName.textContent || 'the current symbol'}`);
  elements.hoverCard.classList.toggle('stale', state.hoverStale);
  elements.hoverCard.classList.remove('hidden');
  updateHoverPinButton();
}

function renderHoverError(error, target) {
  updateHoverA11y();
  elements.hoverCard.setAttribute('aria-busy', 'false');
  elements.hoverKind.textContent = 'error';
  elements.hoverName.textContent = error.code || 'Failure';
  elements.hoverSignature.textContent = '';
  elements.hoverSummary.textContent = error.message || 'Could not generate Hover.';
  elements.hoverProvider.textContent = error.requestId ? `request ${error.requestId}` : '';
  elements.hoverFreshness.textContent = '';
  elements.hoverCoverage.textContent = '';
  elements.hoverObservations.replaceChildren();
  elements.hoverError.textContent = `${error.code || 'INTERNAL_ERROR'}${error.requestId ? ` · ${error.requestId}` : ''}`;
  elements.hoverError.classList.remove('hidden');
  elements.hoverStatus.textContent = 'failed';
  elements.seeMoreButton.disabled = !canOpenSeeMore(state.hoverExplanation, { resolutionError: error });
  elements.hoverCard.classList.remove('hidden');
  if (target) positionHoverCard(target.clientX, target.clientY);
  announce('Failed to generate quick explanation.', 'assertive');
}

function canOpenSeeMore(explanation, { stale = false, resolutionError = null } = {}) {
  return Boolean(explanation?.symbol && !stale && !resolutionError);
}

function renderStructuredExplanation(container, explanation, feature) {
  if (container === elements.explainContent) state.explainExplanation = explanation;
  const result = normalizedExplainResult(explanation);
  container.replaceChildren();
  container.classList.remove('empty-state');

  const header = document.createElement('div');
  header.className = 'structured-explain-header';
  const title = document.createElement('h3');
  title.textContent = feature === 'see_more' ? 'See More' : 'Hover Explain';
  const meta = document.createElement('small');
  meta.textContent = [
    explanation.symbol?.qualifiedName || explanation.symbol?.name || '',
    freshnessLabel(explanation),
    explanation.policyVersion || '',
    explanation.promptVersion || '',
  ].filter(Boolean).join(' · ');
  header.append(title, meta);
  container.appendChild(header);

  if (feature === 'see_more') appendRelevantSourceFiles(container, explanation);

  if (result.summary) {
    const summary = document.createElement('p');
    summary.textContent = result.summary;
    container.appendChild(summary);
  }
  if (feature === 'see_more') {
    const selected = partitionExplainCodeEvidence(explanation, result.codeEvidenceIds);
    appendSelectedCodeBlocks(container, selected.definitions, explanation, 'Definition');
    appendSection(container, 'Key Components and Tests', result.observations, explanation);
    appendSelectedCodeBlocks(container, selected.usages, explanation, 'Example Usages');
    appendInferences(container, result.inferences, explanation, 'Notes');
    appendUncertainties(container, result.uncertainties, explanation);
    appendSection(container, 'Change impact', result.changeImpact, explanation);
    appendSeeAlso(container, explanation);
  } else {
    appendSelectedCodeBlocks(container, result.codeEvidenceIds, explanation);
    appendSection(container, 'Observations', result.observations, explanation);
    appendSection(container, 'Change impact', result.changeImpact, explanation);
    appendInferences(container, result.inferences, explanation);
    appendUncertainties(container, result.uncertainties, explanation);
  }
  if (!result.observations.length && !result.changeImpact.length && !result.inferences.length && !result.uncertainties.length) {
    const empty = document.createElement('p');
    empty.className = 'empty-state';
    empty.textContent = 'The provider returned only the structured summary.';
    container.appendChild(empty);
  }
}

function renderExplainLoading(message) {
  elements.explainContent.replaceChildren();
  elements.evidenceList.replaceChildren();
  elements.explainContent.setAttribute('aria-busy', 'true');
  const item = document.createElement('p');
  item.textContent = message;
  elements.explainContent.appendChild(item);
  elements.explainContent.classList.add('loading');
  announce(message);
}

function renderExplainStale() {
  elements.explainContent.replaceChildren();
  elements.evidenceList.replaceChildren();
  elements.explainContent.setAttribute('aria-busy', 'false');
  const item = document.createElement('p');
  item.className = 'empty-state';
  item.textContent = 'Content changed; generate it again from the current version.';
  elements.explainContent.appendChild(item);
}

function renderExplainError(error) {
  elements.explainContent.classList.remove('loading');
  elements.explainContent.replaceChildren();
  elements.explainContent.setAttribute('aria-busy', 'false');
  const box = document.createElement('div');
  box.className = 'explain-error';
  const strong = document.createElement('strong');
  strong.textContent = error.code || 'INTERNAL_ERROR';
  const message = document.createElement('p');
  message.textContent = error.message || 'Failed to generate See More.';
  box.append(strong, message);
  if (error.requestId) {
    const request = document.createElement('small');
    request.textContent = `request ${error.requestId}`;
    box.appendChild(request);
  }
  elements.explainContent.appendChild(box);
  announce('Failed to generate See More.', 'assertive');
}

function normalizedExplainResult(explanation) {
  const result = explanation?.result || {};
  return {
    summary: result.summary || explanation?.summary || '',
    codeEvidenceIds: Array.isArray(result.codeEvidenceIds) ? result.codeEvidenceIds : [],
    observations: Array.isArray(result.observations) ? result.observations : [],
    inferences: Array.isArray(result.inferences) ? result.inferences : [],
    uncertainties: Array.isArray(result.uncertainties) ? result.uncertainties : [],
    changeImpact: Array.isArray(result.changeImpact) ? result.changeImpact : [],
  };
}

function partitionExplainCodeEvidence(explanation, ids) {
  const evidenceByID = evidenceMap(explanation);
  return ids.reduce((parts, id) => {
    const evidence = evidenceByID.get(id);
    if (evidence?.relation === 'usage_site') parts.usages.push(id);
    else parts.definitions.push(id);
    return parts;
  }, { definitions: [], usages: [] });
}

function appendSelectedCodeBlocks(container, ids, explanation, title = 'Selected code') {
  if (!ids.length) return;
  const evidenceByID = evidenceMap(explanation);
  const selected = ids.map((id) => evidenceByID.get(id)).filter((evidence) => evidence?.code);
  if (!selected.length) return;
  const heading = document.createElement('h3');
  heading.textContent = title;
  container.appendChild(heading);
  selected.forEach((evidence) => {
    const block = document.createElement('section');
    block.className = 'selected-code-block';
    block.appendChild(createSourceAnchor(evidence.path, evidenceLabel(evidence), evidence.range));
    if (evidence.codeTruncated) {
      const truncated = document.createElement('small');
      truncated.textContent = 'truncated to complete lines';
      block.appendChild(truncated);
    }
    const pre = document.createElement('pre');
    const code = document.createElement('code');
    if (evidence.language) code.className = `language-${safeClassName(evidence.language)}`;
    code.textContent = evidence.code;
    pre.appendChild(code);
    block.appendChild(pre);
    container.appendChild(block);
  });
}

function appendSection(container, title, claims, explanation) {
  if (!claims.length) return;
  const heading = document.createElement('h3');
  heading.textContent = title;
  container.appendChild(heading);
  const list = document.createElement('ul');
  appendClaims(list, claims, explanation);
  container.appendChild(list);
  appendSources(container, claims, explanation);
}

function appendClaims(list, claims, explanation) {
  const evidenceByID = evidenceMap(explanation);
  claims.forEach((claim) => {
    const item = document.createElement('li');
    const text = document.createElement('span');
    text.textContent = claim.text || '';
    item.appendChild(text);
    appendEvidenceButtons(item, claim.evidenceIds || [], evidenceByID);
    list.appendChild(item);
  });
}

function appendInferences(container, inferences, explanation, title = 'Inferences') {
  if (!inferences.length) return;
  const heading = document.createElement('h3');
  heading.textContent = title;
  container.appendChild(heading);
  const list = document.createElement('ul');
  const evidenceByID = evidenceMap(explanation);
  inferences.forEach((inference) => {
    const item = document.createElement('li');
    item.textContent = inference.text || '';
    if (typeof inference.confidence === 'number') {
      const confidence = document.createElement('small');
      confidence.textContent = ` confidence ${Math.round(inference.confidence * 100)}%`;
      item.appendChild(confidence);
    }
    appendEvidenceButtons(item, inference.evidenceIds || [], evidenceByID);
    list.appendChild(item);
  });
  container.appendChild(list);
  appendSources(container, inferences, explanation);
}

function seeAlsoEvidence(explanation, limit = 5) {
  const seen = new Set();
  return (explanation?.evidence || [])
    .filter((evidence) => evidence?.relation === 'definition' && evidence.kind !== 'test' && evidence.symbolId)
    .filter((evidence) => {
      if (seen.has(evidence.symbolId)) return false;
      seen.add(evidence.symbolId);
      return true;
    })
    .sort((left, right) => {
      const priority = seeAlsoPriority(left) - seeAlsoPriority(right);
      if (priority) return priority;
      const relevance = Number(right.relevance || 0) - Number(left.relevance || 0);
      if (relevance) return relevance;
      return String(left.title || '').localeCompare(String(right.title || ''));
    })
    .slice(0, Math.max(0, limit));
}

function seeAlsoPriority(evidence) {
  const content = `${evidence?.title || ''}\n${evidence?.content || ''}`;
  if (/\binterface\b/u.test(content)) return 0;
  if (/\bstruct\b/u.test(content)) return 1;
  const name = String(evidence?.title || '').split(' — ')[0].split(/::|\./u).pop() || '';
  if (name.startsWith('New')) return 2;
  return 3;
}

function appendSeeAlso(container, explanation) {
  const related = seeAlsoEvidence(explanation);
  if (!related.length) return;
  const heading = document.createElement('h3');
  heading.textContent = 'See Also';
  container.appendChild(heading);
  const list = document.createElement('ul');
  related.forEach((evidence) => {
    const item = document.createElement('li');
    const name = String(evidence.title || evidenceLabel(evidence)).split(' — ')[0].split(/::|\./u).pop();
    item.appendChild(createSourceAnchor(evidence.path, name || evidenceLabel(evidence), evidence.range));
    list.appendChild(item);
  });
  container.appendChild(list);
}

function appendUncertainties(container, uncertainties, explanation) {
  if (!uncertainties.length) return;
  const heading = document.createElement('h3');
  heading.textContent = 'Uncertainties and omissions';
  container.appendChild(heading);
  const list = document.createElement('ul');
  const evidenceByID = evidenceMap(explanation);
  uncertainties.forEach((uncertainty) => {
    const item = document.createElement('li');
    item.textContent = [uncertainty.text, uncertainty.reason].filter(Boolean).join(' — ');
    appendEvidenceButtons(item, uncertainty.evidenceIds || [], evidenceByID);
    list.appendChild(item);
  });
  container.appendChild(list);
  appendSources(container, uncertainties, explanation);
}

function appendEvidenceButtons(container, ids, evidenceByID) {
  let appended = 0;
  ids.forEach((id) => {
    const evidence = evidenceByID.get(id);
    if (!evidence) return;
    let control;
    if (evidence.path) {
      control = createSourceAnchor(evidence.path, evidenceLabel(evidence), evidence.range);
    } else {
      control = document.createElement('button');
      control.type = 'button';
      control.textContent = evidenceLabel(evidence);
    }
    control.className = 'evidence-link';
    control.dataset.evidenceId = id;
    container.appendChild(control);
    appended += 1;
  });
  return appended;
}

function handleEvidenceClick(event, explanation) {
  const button = delegatedItem(event, event.currentTarget, '[data-evidence-id]');
  if (!button || !explanation) return;
  event.preventDefault();
  navigateEvidence(explanation, button.dataset.evidenceId);
}

function evidenceMap(explanation) {
  const map = new Map();
  (explanation?.evidence || []).forEach((item) => {
    if (item.id) map.set(item.id, item);
  });
  return map;
}

function evidenceLabel(evidence) {
  if (!evidence.path) return evidence.title || 'external boundary';
  const startLine = evidence.range?.start?.line || 1;
  const endLine = evidence.range?.end?.line || startLine;
  return `${evidence.path}:${startLine}${endLine > startLine ? `-${endLine}` : ''}`;
}

function appendSources(container, items, explanation) {
  const ids = [];
  const seen = new Set();
  items.forEach((item) => (item.evidenceIds || []).forEach((id) => {
    if (!seen.has(id)) {
      seen.add(id);
      ids.push(id);
    }
  }));
  if (!ids.length) return;
  const line = document.createElement('p');
  line.className = 'sources-line';
  const label = document.createElement('strong');
  label.textContent = 'Sources:';
  line.appendChild(label);
  if (appendEvidenceButtons(line, ids, evidenceMap(explanation))) container.appendChild(line);
}

function appendRelevantSourceFiles(container, explanation) {
  const byPath = new Map();
  (explanation?.evidence || []).forEach((evidence) => {
    if (evidence.path && !byPath.has(evidence.path)) byPath.set(evidence.path, evidence);
  });
  if (!byPath.size) return;
  const details = document.createElement('details');
  details.className = 'relevant-source-files';
  const summary = document.createElement('summary');
  summary.textContent = 'Relevant source files';
  const intro = document.createElement('p');
  intro.textContent = 'The following files contributed evidence to this response:';
  const list = document.createElement('ul');
  [...byPath.keys()].sort().forEach((path) => {
    const item = document.createElement('li');
    item.appendChild(createSourceAnchor(path, path));
    list.appendChild(item);
  });
  details.append(summary, intro, list);
  container.appendChild(details);
}

function createSourceAnchor(path, label, range = null) {
  const anchor = document.createElement('a');
  const startLine = range?.start?.line || 1;
  const endLine = range?.end?.line || startLine;
  anchor.href = sourceHref(path, range);
  anchor.className = 'code-reference';
  anchor.dataset.path = path;
  anchor.dataset.line = String(startLine);
  anchor.dataset.endLine = String(endLine);
  anchor.title = 'Open in editor';
  anchor.textContent = label;
  return anchor;
}

function sourceHref(path, range = null) {
  const encodedPath = String(path).split('/').map((part) => encodeURIComponent(part)).join('/');
  const startLine = range?.start?.line || 0;
  if (!startLine) return encodedPath;
  const endLine = range?.end?.line || startLine;
  return `${encodedPath}#L${startLine}${endLine > startLine ? `-L${endLine}` : ''}`;
}

function freshnessLabel(explanation) {
  const parts = [];
  if (typeof explanation.documentVersion === 'number') parts.push(`doc v${explanation.documentVersion}`);
  if (explanation.viewHash) parts.push(shortHash(explanation.viewHash));
  else if (explanation.snapshotId) parts.push(shortHash(explanation.snapshotId));
  return parts.join(' · ');
}

function coverageLabel(explanation) {
  const coverage = explanation.semanticCoverage || {};
  const evidenceCount = coverage.evidenceCount ?? (explanation.evidence || []).length;
  const omissionCount = coverage.omissionCount ?? 0;
  return `${evidenceCount} evidence item(s)${omissionCount ? ` · ${omissionCount} omission(s)` : ''}`;
}

function shortHash(value) {
  const text = String(value || '');
  if (text.length <= 18) return text;
  return `${text.slice(0, 12)}…`;
}

function navigateEvidence(explanation, evidenceId) {
  const evidence = evidenceMap(explanation).get(evidenceId);
  if (!evidence || !evidence.path || !evidence.range) return;
  navigateToKnownLocation({
    path: evidence.path,
    symbolId: evidence.symbolId || '',
    occurrenceId: evidence.occurrenceId || '',
    range: evidence.range,
    snapshotId: explanation.snapshotId || '',
    viewHash: explanation.viewHash || '',
    documentId: explanation.documentId || '',
    documentVersion: explanation.documentVersion,
  });
}

function toggleHoverPin() {
  state.hoverPinned = !state.hoverPinned;
  updateHoverPinButton();
  updateHoverA11y();
  if (!state.hoverPinned && state.hoverStale) hideHover();
}

function updateHoverPinButton() {
  if (!elements.hoverPinButton) return;
  elements.hoverPinButton.textContent = state.hoverPinned ? 'Pinned' : 'Pin';
  elements.hoverPinButton.setAttribute('aria-pressed', state.hoverPinned ? 'true' : 'false');
  elements.hoverPinButton.setAttribute('aria-label', state.hoverPinned ? 'Unpin quick explanation' : 'Pin quick explanation');
}

function updateHoverA11y() {
  if (!elements.hoverCard) return;
  elements.hoverCard.setAttribute('role', state.hoverPinned ? 'dialog' : 'tooltip');
  if (state.hoverPinned) elements.hoverCard.setAttribute('aria-modal', 'false');
  else elements.hoverCard.removeAttribute('aria-modal');
}

function markHoverStaleForDocument(documentId) {
  state.hoverCache.invalidate((explanation) => explanation.documentId === documentId);
  state.seeMoreCache.invalidate((explanation) => explanation.documentId === documentId);
  if (!state.hoverExplanation || state.hoverExplanation.documentId !== documentId) return;
  if (state.hoverPinned) {
    state.hoverStale = true;
    elements.hoverCard.classList.add('stale');
    elements.hoverStatus.textContent = 'stale';
    elements.seeMoreButton.disabled = true;
  } else {
    hideHover();
  }
}

function cancelExplainRequests() {
  clearTimeout(state.hoverTimer);
  state.hoverMoveThrottle?.cancel();
  state.hoverPositionScheduler?.cancel();
  state.hoverController?.abort();
  state.seeMoreController?.abort();
}

function handleExplainShortcut(event) {
  if (!(event.metaKey || event.ctrlKey) || event.isComposing) return false;
  if (!elements.editorShell.contains(document.activeElement)) return false;
  const key = event.key.toLowerCase();
  const now = Date.now();
  if (key === 'k') {
    state.explainChordUntil = now + 1200;
    event.preventDefault();
    return true;
  }
  if (key === 'i' && state.explainChordUntil > now) {
    state.explainChordUntil = 0;
    event.preventDefault();
    triggerKeyboardHover();
    return true;
  }
  return false;
}

function triggerKeyboardHover() {
  if (!state.activePath || !state.appReady) return;
  const tab = state.tabs.get(state.activePath);
  if (!tab) return;
  const position = state.editorAdapter.getPosition(tab.documentId);
  const wordRange = state.editorAdapter.wordRangeAtPosition(tab.documentId, position);
  if (!wordRange) {
    toast('No symbol under the cursor.', true);
    return;
  }
  const shell = editorShellRect();
  const key = `${state.activePath}:${wordRange.start.line}:${wordRange.start.column}:${wordRange.end.column}`;
  state.hoverCardAnchor = null;
  state.hoverTarget = {
    ...position,
    key,
    path: state.activePath,
    documentId: tab.documentId,
    clientX: shell.left + Math.min(shell.width - 30, 120),
    clientY: shell.top + Math.min(shell.height - 30, 80),
  };
  cancelHideHover();
  requestHover(key);
}

function scheduleHover(event) {
  if (!state.activePath) return;
  if (state.hoverPinned) return;
  cancelHideHover();
  const pointer = { clientX: event.clientX, clientY: event.clientY };
  state.hoverMoveThrottle?.push(pointer);
}

function hoverAnchorForPointer(currentTarget, pointer, cardVisible, sameKey) {
  if (currentTarget && cardVisible && sameKey) {
    return { clientX: currentTarget.clientX, clientY: currentTarget.clientY };
  }
  return { clientX: pointer.clientX, clientY: pointer.clientY };
}

function resolveHoverPointer(pointer) {
  if (!state.activePath || state.hoverPinned) return;
  const tab = state.tabs.get(state.activePath);
  if (!tab || state.editorAdapter.getContent(tab.documentId).length === 0) return;
  const target = state.editorAdapter.positionFromMouseEvent(pointer);
  const wordRange = state.editorAdapter.wordRangeAtPosition(tab.documentId, target);
  const cardVisible = !elements.hoverCard.classList.contains('hidden');
  if (!wordRange) {
    clearTimeout(state.hoverTimer);
    state.hoverTarget = hoverTargetAfterPointerMiss(state.hoverTarget, cardVisible);
    scheduleHideHover();
    return;
  }
  const key = `${state.activePath}:${wordRange.start.line}:${wordRange.start.column}:${wordRange.end.column}`;
  const previousKey = state.hoverTarget?.key;
  if (key !== previousKey) state.hoverCardAnchor = null;
  const anchor = hoverAnchorForPointer(state.hoverTarget, pointer, cardVisible, key === previousKey);
  state.hoverTarget = { ...target, key, path: state.activePath, documentId: tab.documentId, ...anchor };
  if (key === state.hoverKey && cardVisible) {
    return;
  }
  if (key === previousKey) return;
  clearTimeout(state.hoverTimer);
  state.hoverTimer = setTimeout(() => requestHover(key), 520);
}

async function requestHover(key) {
  const target = state.hoverTarget;
  if (!target || !state.activePath) return;
  if (!state.appReady) return;
  const tab = state.tabs.get(state.activePath);
  if (!tab || tab.documentId !== target.documentId) return;
  state.hoverController?.abort();
  state.hoverController = new AbortController();
  const seq = (state.hoverSeq += 1);
  try {
    const payload = await buildExplainPayloadForTab(tab, target, 'hover');
    if (!payload || seq !== state.hoverSeq) return;
    const requestKey = explainRequestKey(payload);
    const cached = cachedExplainResponse('hover', requestKey);
    if (cached && shouldApplyExplainResponse(tab, cached, seq, state.hoverSeq, 'hover')) {
      state.hoverKey = key;
      state.hoverExplanation = cached;
      state.hoverStale = false;
      renderHoverExplanation(cached);
      positionHoverCard(target.clientX, target.clientY);
      return;
    }
    renderHoverLoading(target);
    const explanation = await api('/api/explain', {
      method: 'POST',
      body: JSON.stringify(payload),
      signal: state.hoverController.signal,
    });
    // Drop a response that no longer matches the document/version/sequence.
    if (!shouldApplyExplainResponse(tab, explanation, seq, state.hoverSeq, 'hover')) return;
    if (!state.hoverTarget || key !== state.hoverTarget.key) return;
    state.hoverKey = key;
    state.hoverExplanation = explanation;
    state.hoverStale = false;
    rememberExplainResponse(explanation, requestKey);
    renderHoverExplanation(explanation);
    positionHoverCard(target.clientX, target.clientY);
  } catch (error) {
    if (error.name !== 'AbortError') renderHoverError(error, target);
  }
}

function positionHoverCard(clientX, clientY) {
  if (!state.hoverCardAnchor) {
    positionHoverCardNow(clientX, clientY);
    return;
  }
  state.hoverPositionScheduler?.push({ clientX, clientY });
}

function positionHoverCardNow(clientX, clientY) {
  const shell = editorShellRect();
  const width = HOVER_CARD_WIDTH_PX;
  const renderedHeight = elements.hoverCard.getBoundingClientRect().height;
  const height = renderedHeight || 190;
  const position = stableHoverCardPosition(state.hoverCardAnchor, clientX, clientY, shell, { width, height });
  state.hoverCardAnchor = position;
  elements.hoverCard.style.left = `${position.left}px`;
  elements.hoverCard.style.top = `${position.top}px`;
  elements.hoverCard.style.maxHeight = `${position.maxHeight}px`;
}

function hoverCardPosition(clientX, clientY, shell, { width, height }) {
  let left = clientX - shell.left + 14;
  let top = clientY - shell.top + 14;
  if (left + width > shell.width - 10) left = Math.max(10, shell.width - width - 10);
  if (top + height > shell.height - 10) top = Math.max(10, top - height - 28);
  return { left, top };
}

function stableHoverCardPosition(previous, clientX, clientY, shell, { width, height }) {
  if (previous) {
    const left = Math.min(Math.max(10, previous.left), Math.max(10, shell.width - width - 10));
    const top = Math.min(Math.max(10, previous.top), Math.max(10, shell.height - 110));
    return { left, top, maxHeight: Math.max(1, shell.height - top - 10) };
  }
  const reservedHeight = Math.min(HOVER_CARD_RESERVED_HEIGHT_PX, Math.max(1, shell.height - 20));
  const position = hoverCardPosition(clientX, clientY, shell, {
    width,
    height: Math.max(height, reservedHeight),
  });
  return { ...position, maxHeight: Math.max(1, shell.height - position.top - 10) };
}

function hoverTargetAfterPointerMiss(currentTarget, cardVisible) {
  return cardVisible ? currentTarget : null;
}

function editorShellRect() {
  if (!state.hoverShellRect) {
    const rect = elements.editorShell.getBoundingClientRect();
    state.hoverShellRect = { left: rect.left, top: rect.top, width: rect.width, height: rect.height };
  }
  return state.hoverShellRect;
}

function invalidateHoverShellRect() {
  state.hoverShellRect = null;
  state.editorAdapter?.invalidatePointerMetrics();
}

function initializeHoverScheduling() {
  if (!state.hoverMoveThrottle) {
    state.hoverMoveThrottle = createLatestThrottle(resolveHoverPointer, HOVER_POINTER_INTERVAL_MS);
  }
  if (!state.hoverPositionScheduler) {
    state.hoverPositionScheduler = createFrameCoalescer(({ clientX, clientY }) => positionHoverCardNow(clientX, clientY));
  }
  if (state.hoverGeometryBound) return;
  state.hoverGeometryBound = true;
  if (typeof ResizeObserver === 'function') {
    state.hoverResizeObserver = new ResizeObserver(invalidateHoverShellRect);
    state.hoverResizeObserver.observe(elements.editorShell);
  }
  window.addEventListener('resize', invalidateHoverShellRect);
  window.addEventListener('scroll', invalidateHoverShellRect, true);
}

function scheduleHideHover() {
  if (state.hoverPinned) return;
  clearTimeout(state.hoverHideTimer);
  state.hoverHideTimer = setTimeout(() => {
    if (state.hoverPinned) return;
    if (!elements.hoverCard.classList.contains('hidden') && elements.hoverCard.matches?.(':hover')) return;
    hideHover();
  }, HOVER_HIDE_DELAY_MS);
}

function cancelHideHover() {
  clearTimeout(state.hoverHideTimer);
}

function hideHover() {
  clearTimeout(state.hoverTimer);
  state.hoverMoveThrottle?.cancel();
  state.hoverPositionScheduler?.cancel();
  state.hoverController?.abort();
  elements.hoverCard.classList.add('hidden');
  state.hoverPinned = false;
  state.hoverStale = false;
  state.hoverKey = '';
  state.hoverTarget = null;
  state.hoverExplanation = null;
  state.hoverCardAnchor = null;
  setSeeMoreButtonBusy(false);
  updateHoverPinButton();
}

function setSeeMoreButtonBusy(busy) {
  if (!elements.seeMoreButton) return;
  elements.seeMoreButton.textContent = busy ? 'Opening…' : 'See more';
  elements.seeMoreButton.disabled = busy || !canOpenSeeMore(state.hoverExplanation, { stale: state.hoverStale });
  elements.seeMoreButton.setAttribute('aria-busy', busy ? 'true' : 'false');
}

async function loadSeeMore() {
  const target = state.hoverTarget;
  const hover = state.hoverExplanation;
  if (!target || !canOpenSeeMore(hover, { stale: state.hoverStale })) return;
  if (!ensureReady()) return;
  const tab = state.tabs.get(state.activePath);
  if (!tab || tab.documentId !== target.documentId) return;
  setSeeMoreButtonBusy(true);
  activateInspector('explain');
  renderExplainLoading('Generating See More…');
  announce('Opening expanded explanation.');
  // The explicit expanded action owns the inspector from this point onward;
  // dismiss the transient card while preserving the captured target/response.
  hideHover();
  state.seeMoreController?.abort();
  state.seeMoreController = new AbortController();
  const seq = (state.seeMoreSeq += 1);
  try {
    const payload = await buildExplainPayloadForTab(tab, target, 'see_more', {
      symbolId: hover.symbol?.id || '',
      occurrenceId: hover.symbol?.occurrenceId || hover.occurrenceId || '',
    });
    if (!payload || seq !== state.seeMoreSeq) {
      renderExplainStale();
      return;
    }
    const requestKey = explainRequestKey(payload);
    const cached = cachedExplainResponse('see_more', requestKey);
    if (cached && shouldApplyExplainResponse(tab, cached, seq, state.seeMoreSeq, 'see_more')) {
      renderStructuredExplanation(elements.explainContent, cached, 'see_more');
      renderEvidence(cached);
      hideHover();
      return;
    }
    const explanation = await api('/api/explain', {
      method: 'POST',
      body: JSON.stringify(payload),
      signal: state.seeMoreController.signal,
    });
    // Drop a stale See More result (newer request, or document/version changed).
    if (!shouldApplyExplainResponse(tab, explanation, seq, state.seeMoreSeq, 'see_more')) {
      renderExplainStale();
      return;
    }
    rememberExplainResponse(explanation, requestKey);
    renderStructuredExplanation(elements.explainContent, explanation, 'see_more');
    renderEvidence(explanation);
    hideHover();
  } catch (error) {
    if (error.name !== 'AbortError') renderExplainError(error);
  } finally {
    if (!elements.hoverCard.classList.contains('hidden')) setSeeMoreButtonBusy(false);
    elements.explainContent.classList.remove('loading');
    elements.explainContent.setAttribute('aria-busy', 'false');
  }
}

function renderEvidence(response) {
  const evidence = Array.isArray(response?.evidence) ? response.evidence : [];
  state.explainExplanation = response || null;
  elements.evidenceList.replaceChildren();
  if (!evidence.length) return;
  const fragment = document.createDocumentFragment();
  const heading = document.createElement('h3');
  heading.textContent = 'Evidence used';
  fragment.appendChild(heading);
  evidence.forEach((item) => {
    const card = document.createElement('button');
    card.type = 'button';
    card.className = 'evidence-card';
    card.dataset.evidenceId = item.id || '';
    const title = document.createElement('strong');
    title.textContent = item.title;
    const detail = document.createElement('small');
    detail.textContent = item.path
      ? `${item.path}:${item.range.start.line} · ${item.relation || item.kind || 'selection'}`
      : `External boundary · ${item.relation || item.kind || 'evidence'}`;
    card.append(title, detail);
    card.disabled = !item.path || !item.range || !item.id;
    fragment.appendChild(card);
  });
  elements.evidenceList.appendChild(fragment);
}

function scheduleSearch() {
  clearTimeout(state.searchTimer);
  const query = elements.searchInput.value.trim();
  if (query.length < 2) {
    hideSearchResults();
    return;
  }
  state.searchTimer = setTimeout(() => runSearch(query), 260);
}

async function runSearch(query) {
  state.searchController?.abort();
  state.searchController = new AbortController();
  try {
    const hits = await api(`/api/search?q=${encodeURIComponent(query)}`, { signal: state.searchController.signal });
    renderSearchResults(hits);
  } catch (error) {
    if (error.name !== 'AbortError') toast(error.message, true);
  }
}

function renderSearchResults(hits) {
  const visibleHits = Array.isArray(hits) ? hits.slice(0, 18) : [];
  state.searchHits = visibleHits;
  state.searchResultItems = [];
  if (!visibleHits.length) {
    const empty = document.createElement('div');
    empty.className = 'search-result';
    empty.setAttribute('role', 'status');
    empty.textContent = 'No results.';
    elements.searchResults.replaceChildren(empty);
    setSearchActiveIndex(-1);
    announce('Search returned no results.');
  }
  const fragment = document.createDocumentFragment();
  visibleHits.forEach((hit, index) => {
    const button = document.createElement('button');
    button.type = 'button';
    button.className = 'search-result';
    button.id = `search-result-${index}`;
    button.setAttribute('role', 'option');
    button.setAttribute('aria-selected', 'false');
    button.tabIndex = -1;
    // SymbolID is the logical selection; navigation always uses the current
    // occurrence's path/range from this response (never a cached range as identity).
    button.dataset.symbolId = hit.symbol.id || '';
    button.dataset.occurrenceId = hit.symbol.occurrenceId || '';
    button.dataset.searchIndex = String(index);
    const kind = document.createElement('span');
    kind.className = 'symbol-kind';
    kind.textContent = hit.symbol.kind;
    const body = document.createElement('span');
    const name = document.createElement('strong');
    name.textContent = hit.symbol.qualifiedName;
    const path = document.createElement('small');
    path.textContent = `${hit.symbol.path}:${hit.symbol.range.start.line} · ${hit.source}`;
    body.append(name, path);
    button.append(kind, body);
    fragment.appendChild(button);
    state.searchResultItems.push(button);
  });
  if (visibleHits.length) elements.searchResults.replaceChildren(fragment);
  elements.searchResults.classList.remove('hidden');
  elements.searchInput.setAttribute('aria-expanded', 'true');
  setSearchActiveIndex(visibleHits.length ? 0 : -1);
  if (visibleHits.length) announce(`${visibleHits.length} search result(s).`);
}

function hideSearchResults() {
  elements.searchResults.classList.add('hidden');
  elements.searchInput.setAttribute('aria-expanded', 'false');
  elements.searchInput.removeAttribute('aria-activedescendant');
  state.searchActiveIndex = -1;
  state.searchHits = [];
  state.searchResultItems = [];
}

function handleSearchResultClick(event) {
  const item = delegatedItem(event, elements.searchResults, '.search-result[data-search-index]');
  if (!item) return;
  const hit = state.searchHits[Number(item.dataset.searchIndex)];
  if (!hit?.symbol) return;
  navigateToKnownLocation({
    path: hit.symbol.path,
    symbolId: hit.symbol.id || '',
    occurrenceId: hit.symbol.occurrenceId || '',
    range: hit.symbol.range,
    snapshotId: hit.snapshotId || state.stats?.snapshotId || '',
  });
  hideSearchResults();
}

function handleSearchKeydown(event) {
  if (elements.searchResults.classList.contains('hidden')) return false;
  const options = state.searchResultItems;
  if (event.key === 'Escape') {
    event.preventDefault();
    hideSearchResults();
    return true;
  }
  if (!options.length) return false;
  if (event.key === 'ArrowDown' || event.key === 'ArrowUp') {
    event.preventDefault();
    const delta = event.key === 'ArrowDown' ? 1 : -1;
    setSearchActiveIndex((state.searchActiveIndex + delta + options.length) % options.length);
    return true;
  }
  if (event.key === 'Home') {
    event.preventDefault();
    setSearchActiveIndex(0);
    return true;
  }
  if (event.key === 'End') {
    event.preventDefault();
    setSearchActiveIndex(options.length - 1);
    return true;
  }
  if (event.key === 'Enter') {
    const active = options[state.searchActiveIndex] || options[0];
    if (active) {
      event.preventDefault();
      active.click();
      return true;
    }
  }
  return false;
}

function setSearchActiveIndex(index) {
  const options = state.searchResultItems;
  state.searchActiveIndex = options.length ? Math.max(0, Math.min(options.length - 1, index)) : -1;
  options.forEach((option, optionIndex) => {
    const selected = optionIndex === state.searchActiveIndex;
    option.setAttribute('aria-selected', selected ? 'true' : 'false');
    if (selected) option.scrollIntoView({ block: 'nearest' });
  });
  const active = options[state.searchActiveIndex];
  if (active) elements.searchInput.setAttribute('aria-activedescendant', active.id);
  else elements.searchInput.removeAttribute('aria-activedescendant');
}

function activateInspector(panel, options = {}) {
  document.querySelectorAll('.inspector-tab').forEach((button) => {
    const active = button.dataset.panel === panel;
    button.classList.toggle('active', active);
    button.setAttribute('aria-selected', active ? 'true' : 'false');
    button.tabIndex = active ? 0 : -1;
    if (active && options.focus) FocusManager.moveFocus(button);
  });
  document.querySelectorAll('.inspector-panel').forEach((section) => {
    const active = section.id === `panel-${panel}`;
    section.classList.toggle('active', active);
    section.hidden = !active;
    section.setAttribute('aria-hidden', active ? 'false' : 'true');
  });
  invalidateHoverShellRect();
}

function handleInspectorTabsKeydown(event) {
  const tabs = [...document.querySelectorAll('.inspector-tab')];
  if (!tabs.length) return;
  const currentIndex = Math.max(0, tabs.indexOf(document.activeElement));
  const activateAt = (nextIndex) => {
    event.preventDefault();
    const tab = tabs[(nextIndex + tabs.length) % tabs.length];
    activateInspector(tab.dataset.panel, { focus: true });
  };
  if (event.key === 'ArrowRight') activateAt(currentIndex + 1);
  else if (event.key === 'ArrowLeft') activateAt(currentIndex - 1);
  else if (event.key === 'Home') activateAt(0);
  else if (event.key === 'End') activateAt(tabs.length - 1);
}

async function loadWiki() {
  try {
    applyWikiCollection(await api('/api/deepwiki'));
  } catch (error) {
    if (error.code === 'APP_NOT_READY') return;
    toast(error.message, true);
  }
}

// applyWikiCollection consumes the DeepWiki envelope. An empty page list is a
// valid state (not_generated), never a silent error.
function applyWikiCollection(collection) {
  state.wikiPages = (collection && Array.isArray(collection.pages) ? collection.pages : []);
  state.wikiStatus = (collection && collection.status) || 'not_generated';
  state.wikiLastError = (collection && collection.lastError) || '';
  // Artifact provenance: input snapshot + sanitized stale reasons (current/stale/generating/failed).
  state.wikiArtifact = (collection && collection.artifact) || null;
  renderWikiSelector();
}

function wikiStatusLabel(status) {
  switch (status) {
    case 'not_generated':
      return 'DeepWiki has not been generated yet';
    case 'generating':
      return 'Generating DeepWiki…';
    case 'stale':
      return 'DeepWiki is out of date';
    case 'failed':
      return 'Failed to generate DeepWiki';
    case 'ready':
      return 'DeepWiki is up to date';
    default:
      return status || '';
  }
}

function renderWikiSelector() {
  elements.wikiPageSelect.replaceChildren();
  renderWikiStatus();
  if (!state.wikiPages.length) {
    const option = document.createElement('option');
    option.textContent = wikiStatusLabel(state.wikiStatus);
    option.value = '';
    elements.wikiPageSelect.appendChild(option);
    const help = state.wikiStatus === 'failed' && state.wikiLastError
      ? sanitizeMessage(state.wikiLastError)
      : 'Use “Refresh” to generate pages from the current index.';
    elements.wikiContent.innerHTML = `<p class="empty-state">${escapeHTML(help)}</p>`;
    return;
  }
  state.wikiPages.forEach((page) => {
    const option = document.createElement('option');
    option.value = page.slug;
    option.textContent = `${page.parentSlug ? '↳ ' : ''}${page.title}`;
    elements.wikiPageSelect.appendChild(option);
  });
  const desired = state.activeWikiSlug && state.wikiPages.some((page) => page.slug === state.activeWikiSlug)
    ? state.activeWikiSlug
    : (state.wikiPages.find((page) => page.slug === 'overview') || state.wikiPages[0]).slug;
  elements.wikiPageSelect.value = desired;
  showWikiPage(desired);
}

// renderWikiStatus surfaces a banner for stale/failed collections that still
// have viewable pages, so the UI never presents outdated content as current.
function renderWikiStatus() {
  if (!elements.wikiStatus) return;
  if ((state.wikiStatus === 'stale' || state.wikiStatus === 'failed') && state.wikiPages.length) {
    elements.wikiStatus.className = `wiki-status ${state.wikiStatus}`;
    const reasons = state.wikiArtifact && Array.isArray(state.wikiArtifact.staleReasons)
      ? state.wikiArtifact.staleReasons.map(sanitizeMessage).join('; ')
      : '';
    elements.wikiStatus.textContent = state.wikiStatus === 'stale'
      ? `⚠ Pages are out of date — the index changed since the last generation.${reasons ? ' (' + reasons + ')' : ''}`
      : `⚠ The last generation failed — showing the previous version. ${sanitizeMessage(state.wikiLastError)}`;
  } else {
    elements.wikiStatus.className = 'wiki-status hidden';
    elements.wikiStatus.textContent = '';
  }
}

function showWikiPage(slug) {
  state.activeWikiSlug = slug;
  const page = state.wikiPages.find((item) => item.slug === slug);
  if (!page) return;
  renderMarkdownInto(elements.wikiContent, page.markdown);
  const navigation = buildWikiPageNavigation(page);
  if (navigation) elements.wikiContent.appendChild(navigation);
}

function normalizedWikiLinks(page, pages = state.wikiPages) {
  const known = new Map((Array.isArray(pages) ? pages : []).map((candidate) => [candidate.slug, candidate]));
  const seen = new Set([page && page.slug]);
  const result = [];
  const raw = page && Array.isArray(page.relatedPages) ? page.relatedPages : [];
  raw.slice(0, 24).forEach((link) => {
    const slug = cleanID(link && link.slug);
    const target = known.get(slug);
    if (!target || seen.has(slug)) return;
    seen.add(slug);
    result.push({
      slug,
      title: cleanText((link && link.title) || target.title || slug),
      relation: cleanText((link && link.relation) || 'related'),
    });
  });
  return result;
}

function buildWikiPageNavigation(page) {
  const links = normalizedWikiLinks(page);
  if (!links.length) return null;
  const nav = document.createElement('nav');
  nav.className = 'wiki-page-navigation';
  const list = document.createElement('ul');
  links.forEach((link) => {
    const item = document.createElement('li');
    const button = document.createElement('button');
    button.type = 'button';
    button.className = 'wiki-page-link';
    button.dataset.wikiSlug = link.slug;
    button.textContent = `${link.title} · ${link.relation}`;
    button.addEventListener('click', () => {
      elements.wikiPageSelect.value = link.slug;
      showWikiPage(link.slug);
    });
    item.appendChild(button);
    list.appendChild(item);
  });
  nav.append(list);
  return nav;
}

async function refreshWiki() {
  if (!ensureReady()) return;
  elements.refreshWikiButton.disabled = true;
  elements.wikiContent.classList.add('loading');
  try {
    const ref = await api('/api/deepwiki/refresh', { method: 'POST', body: '{}' });
    applyJobSnapshot(ref.job);
    activateInspector('jobs');
    toast('Job de DeepWiki iniciado.');
  } catch (error) {
    if (error.code === 'APP_NOT_READY') return;
    toast(error.message, true);
  } finally {
    elements.refreshWikiButton.disabled = false;
    elements.wikiContent.classList.remove('loading');
  }
}

function createCodemapStore() {
  const listeners = new Set();
  const storeState = {
    status: 'idle',
    artifact: null,
    previousArtifact: null,
    layout: null,
    layoutError: '',
    activeLayout: null,
    job: null,
    filters: defaultCodemapFilterState(),
    viewMode: 'graph',
    selectedNodeId: '',
    selectedEdgeId: '',
    selectedTraceId: '',
    selectedTraceStep: 0,
    collapsedGroups: new Set(),
    viewport: { zoom: 1, panX: 0, panY: 0 },
    warnings: [],
    error: null,
  };

  function emit() {
    const snapshot = api.snapshot();
    listeners.forEach((listener) => listener(snapshot));
  }

  const api = {
    subscribe(listener) {
      if (typeof listener !== 'function') return () => {};
      listeners.add(listener);
      return () => listeners.delete(listener);
    },
    snapshot() {
      return {
        ...storeState,
        filters: { ...storeState.filters },
        collapsedGroups: new Set(storeState.collapsedGroups),
        viewport: { ...storeState.viewport },
        warnings: [...storeState.warnings],
      };
    },
    startGeneration(layout = {}) {
      storeState.status = 'loading';
      storeState.error = null;
      storeState.layout = null;
      storeState.layoutError = '';
      storeState.activeLayout = {
        requestId: String(layout.requestId || ''),
        inputHash: String(layout.inputHash || ''),
      };
      emit();
    },
    onJobQueued(job) {
      storeState.status = 'job_queued';
      storeState.job = job || null;
      emit();
    },
    onJobProgress(job) {
      storeState.status = job && job.state === 'queued' ? 'job_queued' : 'job_running';
      storeState.job = job || null;
      emit();
    },
    onReady(artifact, selection = {}) {
      if (storeState.artifact && storeState.artifact !== artifact) {
        storeState.previousArtifact = storeState.artifact;
      }
      storeState.artifact = artifact;
      storeState.status = artifact && artifact.status === 'stale' ? 'stale' : 'ready';
      storeState.error = null;
      storeState.selectedNodeId = String(selection.selectedNodeId || '');
      storeState.selectedEdgeId = '';
      storeState.selectedTraceId = String(selection.selectedTraceId || artifact?.selectedTraceId || '');
      storeState.selectedTraceStep = 0;
      storeState.warnings = [...new Set([...storeState.warnings, ...(selection.warnings || [])])];
      emit();
    },
    startLayout(layout = {}) {
      storeState.layout = null;
      storeState.layoutError = '';
      storeState.activeLayout = {
        requestId: String(layout.requestId || ''),
        inputHash: String(layout.inputHash || ''),
      };
      emit();
    },
    onStale(reason) {
      storeState.status = 'stale';
      if (reason) storeState.warnings = [...new Set([...storeState.warnings, String(reason)])];
      emit();
    },
    addWarning(reason) {
      if (reason) storeState.warnings = [...new Set([...storeState.warnings, String(reason)])];
      emit();
    },
    onFailed(error) {
      storeState.status = 'failed';
      storeState.error = error || null;
      emit();
    },
    cancel(job) {
      storeState.status = 'canceled';
      storeState.job = job || storeState.job;
      emit();
    },
    applyLayoutResult(result) {
      if (!isValidLayoutResult(result)) return false;
      const active = storeState.activeLayout || {};
      if (result.requestId !== active.requestId || result.inputHash !== active.inputHash) {
        return false;
      }
      storeState.layout = result;
      storeState.layoutError = '';
      storeState.warnings = [...new Set([...(storeState.warnings || []), ...(result.warnings || [])])];
      emit();
      return true;
    },
    onLayoutFailure(error) {
      storeState.layoutError = sanitizeMessage(error && error.message ? error.message : String(error || 'layout failed'));
      if (storeState.viewMode === 'graph') storeState.viewMode = 'text';
      emit();
    },
    setViewMode(mode) {
      storeState.viewMode = mode === 'text' ? 'text' : 'graph';
      emit();
    },
    updateFilters(filters) {
      storeState.filters = { ...storeState.filters, ...(filters || {}) };
      emit();
    },
    clearFilters() {
      storeState.filters = defaultCodemapFilterState();
      emit();
    },
    selectNode(id) {
      storeState.selectedNodeId = String(id || '');
      storeState.selectedEdgeId = '';
      emit();
    },
    selectEdge(id) {
      storeState.selectedEdgeId = String(id || '');
      storeState.selectedNodeId = '';
      emit();
    },
    selectTrace(id, stepIndex = 0) {
      storeState.selectedTraceId = String(id || '');
      storeState.selectedTraceStep = Math.max(0, Number(stepIndex) || 0);
      storeState.filters.traceId = storeState.selectedTraceId;
      emit();
    },
    toggleGroup(groupId) {
      const id = String(groupId || '');
      if (!id) return;
      if (storeState.collapsedGroups.has(id)) storeState.collapsedGroups.delete(id);
      else storeState.collapsedGroups.add(id);
      emit();
    },
    rememberViewport(viewport) {
      storeState.viewport = {
        zoom: clampNumber(viewport && viewport.zoom, CODEMAP_CONFIG.MIN_ZOOM, CODEMAP_CONFIG.MAX_ZOOM, 1),
        panX: Number(viewport && viewport.panX) || 0,
        panY: Number(viewport && viewport.panY) || 0,
      };
    },
    reset() {
      storeState.status = 'idle';
      storeState.artifact = null;
      storeState.previousArtifact = null;
      storeState.layout = null;
      storeState.activeLayout = null;
      storeState.job = null;
      storeState.filters = defaultCodemapFilterState();
      storeState.viewMode = 'graph';
      storeState.selectedNodeId = '';
      storeState.selectedEdgeId = '';
      storeState.selectedTraceId = '';
      storeState.selectedTraceStep = 0;
      storeState.collapsedGroups = new Set();
      storeState.viewport = { zoom: 1, panX: 0, panY: 0 };
      storeState.warnings = [];
      storeState.error = null;
      emit();
    },
  };
  return api;
}

function defaultCodemapFilterState() {
  return {
    groups: [],
    kinds: [],
    edgeTypes: [],
    provenanceSources: [],
    minConfidence: 0,
    internalOnly: false,
    traceId: '',
    searchText: '',
  };
}

function normalizeCodemapViewModel(input, config = CODEMAP_CONFIG) {
  const errors = [];
  if (!input || typeof input !== 'object') {
    return { ok: false, errors: ['codemap payload must be an object'], viewModel: null };
  }
  if (!Array.isArray(input.nodes)) errors.push('nodes must be an array');
  if (!Array.isArray(input.edges)) errors.push('edges must be an array');
  if (errors.length) return { ok: false, errors, viewModel: null };
  if (input.nodes.length > config.MAX_DTO_NODES) errors.push(`node count ${input.nodes.length} exceeds ${config.MAX_DTO_NODES}`);
  if (input.edges.length > config.MAX_DTO_EDGES) errors.push(`edge count ${input.edges.length} exceeds ${config.MAX_DTO_EDGES}`);

  const nodeIds = new Set();
  const nodes = [];
  input.nodes.forEach((node, index) => {
    if (!node || typeof node !== 'object') {
      errors.push(`node ${index} must be an object`);
      return;
    }
    const id = cleanID(node.id);
    if (!id) errors.push(`node ${index} missing id`);
    if (nodeIds.has(id)) errors.push(`duplicate node id ${id}`);
    nodeIds.add(id);
    nodes.push({
      id,
      label: cleanText(node.label || node.name || id),
      kind: cleanText(node.kind || 'symbol'),
      path: cleanText(node.path || ''),
      range: normalizeRange(node.range),
      summary: cleanText(node.summary || ''),
      snippet: truncateText(node.snippet || '', 400),
      group: cleanText(node.group || 'Other'),
      relevance: clampNumber(node.relevance, 0, 1, 0),
      confidence: clampNumber(node.confidence, 0, 1, null),
      provenance: normalizeStringList(node.provenance),
      evidenceIds: normalizeStringList(node.evidenceIds),
      external: Boolean(node.external) || node.kind === 'external' || node.kind === 'unresolved' || !node.path,
      raw: node,
    });
  });

  const edgeIds = new Set();
  const edges = [];
  input.edges.forEach((edge, index) => {
    if (!edge || typeof edge !== 'object') {
      errors.push(`edge ${index} must be an object`);
      return;
    }
    const id = cleanID(edge.id || `edge:${index}`);
    if (!id) errors.push(`edge ${index} missing id`);
    if (edgeIds.has(id)) errors.push(`duplicate edge id ${id}`);
    edgeIds.add(id);
    const source = cleanID(edge.source);
    const target = cleanID(edge.target);
    if (!nodeIds.has(source)) errors.push(`edge ${id} references unknown source ${source}`);
    if (!nodeIds.has(target)) errors.push(`edge ${id} references unknown target ${target}`);
    edges.push({
      id,
      source,
      target,
      type: cleanText(edge.type || 'references'),
      label: cleanText(edge.label || edge.type || 'references'),
      path: cleanText(edge.path || ''),
      line: Math.max(0, Number(edge.line) || 0),
      confidence: clampNumber(edge.confidence, 0, 1, null),
      snippet: truncateText(edge.snippet || '', 260),
      provenance: normalizeStringList(edge.provenance),
      evidenceIds: normalizeStringList(edge.evidenceIds),
      raw: edge,
    });
  });

  const groups = normalizeCodemapGroups(input, nodes, errors);
  const flows = normalizeCodemapFlows(input.flows, nodes, errors);
  const traceCandidates = normalizeCodemapTraces(input, nodes, edges, errors);
  const diagram = normalizeMermaidDiagram(input.diagram, errors);
  validateCodemapEvidence(nodes, edges, traceCandidates, errors);
  if (errors.length) return { ok: false, errors, viewModel: null };

  const artifact = input.artifact && typeof input.artifact === 'object' ? input.artifact : {};
  const status = normalizeCodemapStatus(input.status || artifact.status || input.freshness, artifact.artifactId || input.artifactId || input.id);
  const viewModel = {
    artifactId: cleanText(artifact.artifactId || input.artifactId || input.id || ''),
    artifactRevision: Number(artifact.artifactRevision || input.artifactRevision || 0) || 0,
    inputSnapshotId: cleanText(artifact.inputSnapshotId || input.inputSnapshotId || input.snapshotId || ''),
    contextPackHash: cleanText(artifact.contextPackHash || input.contextPackHash || ''),
    status,
    staleReasons: normalizeStringList(artifact.staleReasons || input.staleReasons),
    query: cleanText(input.query || ''),
    title: cleanText(input.title || input.query || 'Codemap'),
    overview: cleanText(input.overview || input.narrative || ''),
    nodes,
    edges,
    groups,
    flows,
    traceCandidates,
    diagram,
    selectedTraceId: traceCandidates[0] ? traceCandidates[0].id : '',
    omissions: Array.isArray(input.omissions) ? input.omissions.slice(0, 50) : [],
    semanticCoverage: input.semanticCoverage && typeof input.semanticCoverage === 'object' ? { ...input.semanticCoverage } : {},
    provider: cleanText(input.provider || artifact.provider || ''),
    generatedAt: cleanText(input.generatedAt || artifact.createdAt || ''),
    policyVersion: cleanText(input.policyVersion || ''),
    outputSchemaVersion: cleanText(input.outputSchemaVersion || artifact.outputSchema || ''),
    artifact,
    raw: input,
  };
  return { ok: true, errors: [], viewModel };
}

function normalizeCodemapFlows(rawFlows, nodes, errors) {
  if (rawFlows == null) return [];
  if (!Array.isArray(rawFlows)) {
    errors.push('flows must be an array');
    return [];
  }
  const nodeIds = new Set(nodes.map((node) => node.id));
  const seenLabels = new Set();
  return rawFlows.slice(0, 8).map((flow, flowIndex) => {
    if (!flow || typeof flow !== 'object') {
      errors.push(`flow ${flowIndex} must be an object`);
      return null;
    }
    const entryNodeId = cleanID(flow.entryNodeId);
    if (!nodeIds.has(entryNodeId)) errors.push(`flow ${flowIndex} references unknown entry node ${entryNodeId}`);
    const steps = Array.isArray(flow.steps) ? flow.steps.slice(0, 16).map((step, stepIndex) => {
      const nodeId = cleanID(step && step.nodeId);
      if (!nodeIds.has(nodeId)) errors.push(`flow ${flowIndex} step ${stepIndex} references unknown node ${nodeId}`);
      const label = cleanText((step && step.label) || `${flowIndex + 1}${String.fromCharCode(97 + stepIndex)}`);
      const labelKey = `${flowIndex}:${label}`;
      if (seenLabels.has(labelKey)) errors.push(`flow ${flowIndex} has duplicate step label ${label}`);
      seenLabels.add(labelKey);
      return {
        label,
        nodeId,
        text: cleanText((step && step.text) || ''),
        path: cleanText((step && step.path) || ''),
        line: Math.max(0, Number(step && step.line) || 0),
        snippet: truncateText((step && step.snippet) || '', 400),
      };
    }) : [];
    return {
      title: cleanText(flow.title || `Flow ${flowIndex + 1}`),
      entryNodeId,
      steps,
    };
  }).filter(Boolean);
}

function normalizeCodemapGroups(input, nodes, errors) {
  const byNode = new Set(nodes.map((node) => node.id));
  const rawGroups = Array.isArray(input.groups) ? input.groups : Array.isArray(input.sections) ? input.sections : null;
  if (rawGroups) {
    const seen = new Set();
    return rawGroups.map((group, index) => {
      const id = cleanID(group.id || group.title || `group:${index}`);
      if (!id) errors.push(`group ${index} missing id`);
      if (seen.has(id)) errors.push(`duplicate group id ${id}`);
      seen.add(id);
      const nodeIds = normalizeStringList(group.nodeIds || group.nodes).filter((idValue) => {
        const found = byNode.has(idValue);
        if (!found) errors.push(`group ${id} references unknown node ${idValue}`);
        return found;
      });
      return {
        id,
        title: cleanText(group.title || group.label || id),
        nodeIds,
        collapsed: false,
        raw: group,
      };
    }).filter((group) => group.nodeIds.length > 0);
  }
  const grouped = new Map();
  nodes.forEach((node) => {
    const id = cleanID(node.group || 'Other') || 'Other';
    if (!grouped.has(id)) grouped.set(id, { id, title: node.group || id, nodeIds: [] });
    grouped.get(id).nodeIds.push(node.id);
  });
  return [...grouped.values()].sort((a, b) => {
    if (a.title === 'External') return 1;
    if (b.title === 'External') return -1;
    return a.title.localeCompare(b.title);
  });
}

function normalizeCodemapTraces(input, nodes, edges, errors) {
  const nodeIds = new Set(nodes.map((node) => node.id));
  const edgeIds = new Set(edges.map((edge) => edge.id));
  const raw = Array.isArray(input.traceCandidates) ? input.traceCandidates : Array.isArray(input.traces) ? input.traces : [];
  const traces = raw.slice(0, 3).map((trace, index) => normalizeStructuredTrace(trace, index, nodeIds, edgeIds, errors)).filter(Boolean);
  if (traces.length) return traces;
  const legacyTrace = Array.isArray(input.trace) ? input.trace : [];
  if (!legacyTrace.length) return [];
  return [{
    id: 'trace:legacy:0',
    title: 'Trace guide',
    summary: '',
    confidence: null,
    provenance: [],
    steps: legacyTrace.slice(0, 16).map((step, index) => {
      const edge = edges[index] || null;
      return {
        id: `trace:legacy:0:${index}`,
        label: String(index + 1),
        summary: cleanText(step),
        nodeIds: edge ? [edge.source, edge.target] : [],
        edgeIds: edge ? [edge.id] : [],
        evidenceIds: [],
        confidence: edge ? edge.confidence : null,
        provenance: [],
      };
    }),
    omissions: [],
  }];
}

function normalizeStructuredTrace(trace, index, nodeIds, edgeIds, errors) {
  if (!trace || typeof trace !== 'object') return null;
  const id = cleanID(trace.id || `trace:${index}`);
  const steps = Array.isArray(trace.steps) ? trace.steps.slice(0, 16).map((step, stepIndex) => {
    const nodeList = normalizeStringList(step.nodeIds || step.nodes);
    const edgeList = normalizeStringList(step.edgeIds || step.edges);
    nodeList.forEach((nodeId) => {
      if (!nodeIds.has(nodeId)) errors.push(`trace ${id} step ${stepIndex} references unknown node ${nodeId}`);
    });
    edgeList.forEach((edgeId) => {
      if (!edgeIds.has(edgeId)) errors.push(`trace ${id} step ${stepIndex} references unknown edge ${edgeId}`);
    });
    return {
      id: cleanID(step.id || `${id}:${stepIndex}`),
      label: cleanText(step.label || String(stepIndex + 1)),
      summary: cleanText(step.summary || step.title || ''),
      nodeIds: nodeList,
      edgeIds: edgeList,
      evidenceIds: normalizeStringList(step.evidenceIds),
      confidence: clampNumber(step.confidence, 0, 1, null),
      provenance: normalizeStringList(step.provenance),
    };
  }) : [];
  return {
    id,
    title: cleanText(trace.title || `Trace ${index + 1}`),
    summary: cleanText(trace.summary || ''),
    confidence: clampNumber(trace.confidence, 0, 1, null),
    provenance: normalizeStringList(trace.provenance),
    steps,
    omissions: Array.isArray(trace.omissions) ? trace.omissions.slice(0, 20) : [],
  };
}

function validateCodemapEvidence(nodes, edges, traces, errors) {
  const allowed = new Set([
    ...nodes.map((node) => node.id),
    ...edges.map((edge) => edge.id),
  ]);
  nodes.forEach((node) => node.evidenceIds.forEach((id) => {
    if (!allowed.has(id)) errors.push(`node ${node.id} references unknown evidence ${id}`);
  }));
  edges.forEach((edge) => edge.evidenceIds.forEach((id) => {
    if (!allowed.has(id)) errors.push(`edge ${edge.id} references unknown evidence ${id}`);
  }));
  traces.forEach((trace) => trace.steps.forEach((step) => step.evidenceIds.forEach((id) => {
    if (!allowed.has(id)) errors.push(`trace ${trace.id} references unknown evidence ${id}`);
  })));
}

function codemapRenderDecision(viewModel, config = CODEMAP_CONFIG) {
  const nodeCount = viewModel && Array.isArray(viewModel.nodes) ? viewModel.nodes.length : 0;
  const edgeCount = viewModel && Array.isArray(viewModel.edges) ? viewModel.edges.length : 0;
  if (nodeCount > config.FULL_GRAPH_MAX_NODES || edgeCount > config.FULL_GRAPH_MAX_EDGES) {
    return {
      mode: 'text',
      reason: `${nodeCount} nodes / ${edgeCount} edges exceeds graph threshold ${config.FULL_GRAPH_MAX_NODES}/${config.FULL_GRAPH_MAX_EDGES}`,
    };
  }
  return { mode: 'graph', reason: '' };
}

function applyCodemapFilters(viewModel, filters = defaultCodemapFilterState()) {
  const effective = { ...defaultCodemapFilterState(), ...(filters || {}) };
  const groups = new Set(effective.groups || []);
  const kinds = new Set(effective.kinds || []);
  const edgeTypes = new Set(effective.edgeTypes || []);
  const provenanceSources = new Set(effective.provenanceSources || []);
  const trace = effective.traceId ? (viewModel.traceCandidates || []).find((item) => item.id === effective.traceId) : null;
  const traceNodes = new Set();
  const traceEdges = new Set();
  if (trace) {
    trace.steps.forEach((step) => {
      step.nodeIds.forEach((id) => traceNodes.add(id));
      step.edgeIds.forEach((id) => traceEdges.add(id));
    });
  }
  const search = String(effective.searchText || '').trim().toLowerCase();
  const visibleNodeIds = new Set();
  (viewModel.nodes || []).forEach((node) => {
    if (groups.size && !groups.has(node.group)) return;
    if (kinds.size && !kinds.has(node.kind)) return;
    if (effective.internalOnly && isExternalCodemapNode(node)) return;
    if (trace && !traceNodes.has(node.id)) return;
    if (search && !(`${node.label} ${node.summary} ${node.path}`.toLowerCase().includes(search))) return;
    visibleNodeIds.add(node.id);
  });
  const visibleEdgeIds = new Set();
  (viewModel.edges || []).forEach((edge) => {
    if (!visibleNodeIds.has(edge.source) || !visibleNodeIds.has(edge.target)) return;
    if (edgeTypes.size && !edgeTypes.has(edge.type)) return;
    if (Number(edge.confidence || 0) < Number(effective.minConfidence || 0)) return;
    if (provenanceSources.size && !edge.provenance.some((source) => provenanceSources.has(source))) return;
    if (trace && !traceEdges.has(edge.id)) return;
    visibleEdgeIds.add(edge.id);
  });
  return {
    visibleNodeIds,
    visibleEdgeIds,
    nodeCount: visibleNodeIds.size,
    edgeCount: visibleEdgeIds.size,
    totalNodeCount: (viewModel.nodes || []).length,
    totalEdgeCount: (viewModel.edges || []).length,
  };
}

function prepareCodemapLayoutInput(viewModel, options = {}) {
  const traceRanks = codemapTraceRanks(viewModel, options.selectedTraceId || viewModel.selectedTraceId);
  const request = {
    requestId: String(options.requestId || `layout:${Date.now()}:${Math.random().toString(16).slice(2)}`),
    artifactId: viewModel.artifactId || '',
    inputHash: '',
    nodes: (viewModel.nodes || []).map((node, index) => ({
      id: node.id,
      kind: node.kind,
      groupId: node.group || 'Other',
      labelLength: Math.min(80, String(node.label || '').length),
      relevance: clampNumber(node.relevance, 0, 1, 0),
      confidence: clampNumber(node.confidence, 0, 1, null),
      external: isExternalCodemapNode(node),
      traceRank: traceRanks.nodes.has(node.id) ? traceRanks.nodes.get(node.id) : 10000 + index,
    })),
    edges: (viewModel.edges || []).map((edge, index) => ({
      id: edge.id,
      source: edge.source,
      target: edge.target,
      type: edge.type,
      confidence: clampNumber(edge.confidence, 0, 1, null),
      traceRank: traceRanks.edges.has(edge.id) ? traceRanks.edges.get(edge.id) : 10000 + index,
    })),
    groups: (viewModel.groups || []).map((group, index) => ({
      id: group.id,
      titleLength: Math.min(80, String(group.title || group.id || '').length),
      nodeIds: [...group.nodeIds],
      order: index,
    })),
    options: {
      orientation: options.orientation === 'TTB' ? 'TTB' : 'LTR',
      columnSpacing: 290,
      rowSpacing: 118,
      nodeWidth: 230,
      nodeHeight: 78,
    },
  };
  request.inputHash = stableHash({
    artifactId: request.artifactId,
    nodes: request.nodes,
    edges: request.edges,
    groups: request.groups,
    options: request.options,
  });
  return request;
}

function codemapTraceRanks(viewModel, traceId) {
  const nodes = new Map();
  const edges = new Map();
  const trace = traceId ? (viewModel.traceCandidates || []).find((item) => item.id === traceId) : null;
  if (!trace) return { nodes, edges };
  let rank = 0;
  trace.steps.forEach((step) => {
    step.nodeIds.forEach((id) => {
      if (!nodes.has(id)) nodes.set(id, rank++);
    });
    step.edgeIds.forEach((id) => {
      if (!edges.has(id)) edges.set(id, rank++);
    });
  });
  return { nodes, edges };
}

function parseCodemapRoute(route) {
  let url;
  try {
    url = new URL(route, 'http://codeatlas.local');
  } catch (_) {
    return { artifactId: '', nodeId: '', traceId: '', warning: 'invalid route' };
  }
  const match = /^\/codemaps\/([^/]+)$/.exec(url.pathname);
  if (!match) return { artifactId: '', nodeId: '', traceId: '', warning: '' };
  let artifactId;
  try {
    artifactId = decodeURIComponent(match[1]);
  } catch (_) {
    return { artifactId: '', nodeId: '', traceId: '', warning: 'invalid route' };
  }
  const nodeId = url.searchParams.get('node') || '';
  const traceId = url.searchParams.get('trace') || '';
  const safeArtifact = isSafeCodemapID(artifactId) ? artifactId : '';
  const safeNode = isSafeCodemapID(nodeId) ? nodeId : '';
  const safeTrace = isSafeCodemapID(traceId) ? traceId : '';
  return {
    artifactId: safeArtifact,
    nodeId: safeNode,
    traceId: safeTrace,
    warning: safeArtifact === artifactId && safeNode === nodeId && safeTrace === traceId ? '' : 'invalid id ignored',
  };
}

function codemapDeepLinkRequest(route) {
  const parsed = parseCodemapRoute(route);
  return {
    route: parsed,
    path: parsed.artifactId ? `/api/codemaps/${encodeURIComponent(parsed.artifactId)}` : '',
  };
}

function codemapRouteSelection(viewModel, route = {}) {
  const warnings = route.warning ? [route.warning] : [];
  const selectedNodeId = route.nodeId && viewModel.nodes.some((node) => node.id === route.nodeId)
    ? route.nodeId
    : '';
  const traceExists = route.traceId && viewModel.traceCandidates.some((trace) => trace.id === route.traceId);
  if (route.nodeId && !selectedNodeId) warnings.push(`deep link node ${route.nodeId} not found`);
  if (route.traceId && !traceExists) warnings.push(`deep link trace ${route.traceId} not found`);
  return {
    selectedNodeId,
    selectedTraceId: traceExists ? route.traceId : viewModel.selectedTraceId,
    warnings,
  };
}

function codemapRouteFor(stateLike) {
  if (!stateLike || !stateLike.artifactId || !isSafeCodemapID(stateLike.artifactId)) return '';
  const params = new URLSearchParams();
  if (stateLike.selectedNodeId && isSafeCodemapID(stateLike.selectedNodeId)) params.set('node', stateLike.selectedNodeId);
  if (stateLike.selectedTraceId && isSafeCodemapID(stateLike.selectedTraceId)) params.set('trace', stateLike.selectedTraceId);
  const suffix = params.toString() ? `?${params.toString()}` : '';
  return `/codemaps/${encodeURIComponent(stateLike.artifactId)}${suffix}`;
}

function isSafeCodemapID(id) {
  return !id || /^[A-Za-z0-9:._@~+-]{1,180}$/.test(id);
}

function isValidLayoutResult(result) {
  return Boolean(result
    && typeof result.requestId === 'string'
    && typeof result.inputHash === 'string'
    && Array.isArray(result.nodes)
    && Array.isArray(result.edgeRoutes)
    && result.bounds
    && typeof result.bounds === 'object');
}

function normalizeCodemapStatus(status, artifactId) {
  const value = String(status || '').toLowerCase();
  if (value === 'stale' || value === 'partially-stale') return 'stale';
  if (value === 'transient' || !artifactId) return 'transient';
  if (value === 'failed' || value === 'canceled') return value;
  return 'current';
}

function normalizeStringList(value) {
  if (!Array.isArray(value)) return [];
  return value.map((item) => cleanText(item)).filter(Boolean).slice(0, 100);
}

function cleanID(value) {
  return cleanText(value).slice(0, 180);
}

function cleanText(value) {
  return String(value == null ? '' : value).replace(/[\u0000-\u001f\u007f]/g, ' ').trim();
}

function normalizeMermaidDiagram(input, errors = []) {
  if (input == null) return null;
  if (typeof input !== 'object' || Array.isArray(input)) {
    errors.push('diagram must be an object');
    return null;
  }
  const version = cleanText(input.version);
  const kind = cleanText(input.kind);
  const source = String(input.source == null ? '' : input.source).replace(/\r\n/g, '\n').trim();
  if (version !== MERMAID_DIAGRAM_VERSION) errors.push(`diagram version ${version || '(empty)'} is unsupported`);
  if (kind !== 'flowchart' && kind !== 'sequence') errors.push(`diagram kind ${kind || '(empty)'} is unsupported`);
  if (!isSafeMermaidSource(source)) errors.push('diagram source is outside the deterministic Mermaid subset');

  const sources = [];
  if (!Array.isArray(input.sources)) {
    errors.push('diagram sources must be an array');
  } else if (input.sources.length > 20) {
    errors.push(`diagram source count ${input.sources.length} exceeds 20`);
  } else {
    input.sources.forEach((item, index) => {
      if (!item || typeof item !== 'object') {
        errors.push(`diagram source ${index} must be an object`);
        return;
      }
      const range = normalizeRange(item.range);
      const href = sourceHref(item.path || '', range);
      const parsed = parseSourceTarget(href);
      if (!parsed || !range) {
        errors.push(`diagram source ${index} has an invalid path/range`);
        return;
      }
      sources.push({
        nodeId: cleanID(item.nodeId),
        label: cleanText(item.label || item.nodeId || parsed.path),
        path: parsed.path,
        range,
      });
    });
  }
  return { version, kind, source, sources };
}

function isSafeMermaidSource(source) {
  if (!source || new TextEncoder().encode(source).length > MERMAID_MAX_SOURCE_BYTES) return false;
  if (/%%\{|javascript:|<\/?(?:script|foreignObject|iframe|object|embed)\b/i.test(source)) return false;
  const lines = source.split('\n');
  if (lines[0] === 'graph TD') {
    return lines.length > 1 && lines.slice(1).every((line) => (
      /^  subgraph g\d+\["[^"<>]{1,200}"\]$/u.test(line)
      || /^    direction TB$/u.test(line)
      || /^    n\d+\["[^"<>]{1,240}"\]$/u.test(line)
      || /^  end$/u.test(line)
      || /^  n\d+ -->\|[\p{L}\p{N}\s._/()#-]{1,80}\| n\d+$/u.test(line)
      || /^  n\d+ -\. [\p{L}\p{N}\s._/()#-]{1,80} \.-> n\d+$/u.test(line)
    ));
  }
  if (lines[0] === 'sequenceDiagram') {
    return lines.length > 2 && lines.slice(1).every((line) => (
      /^  participant p\d+ as [\p{L}\p{N}\s._/()#-]{1,240}$/u.test(line)
      || /^  p\d+->>p\d+: calls [\p{L}\p{N}\s._/()#-]{1,160}$/u.test(line)
    ));
  }
  return false;
}

function truncateText(value, limit) {
  const text = cleanText(value);
  return text.length > limit ? `${text.slice(0, limit - 1)}…` : text;
}

function normalizeRange(range) {
  if (!range || typeof range !== 'object') return null;
  const start = normalizePoint(range.start);
  const end = normalizePoint(range.end);
  if (!start || !end) return null;
  return { start, end };
}

function normalizePoint(point) {
  if (!point || typeof point !== 'object') return null;
  const line = Math.max(1, Number(point.line) || 0);
  const column = Math.max(1, Number(point.column) || 0);
  if (!line || !column) return null;
  return { line, column };
}

function clampNumber(value, min, max, fallback) {
  const number = Number(value);
  if (!Number.isFinite(number)) return fallback;
  return Math.min(max, Math.max(min, number));
}

function isExternalCodemapNode(node) {
  return Boolean(node && (node.external || node.kind === 'external' || node.kind === 'unresolved' || !node.path));
}

function stableHash(value) {
  const text = stableStringify(value);
  let hash = 2166136261;
  for (let index = 0; index < text.length; index += 1) {
    hash ^= text.charCodeAt(index);
    hash = Math.imul(hash, 16777619);
  }
  return `fnv1a:${(hash >>> 0).toString(16).padStart(8, '0')}`;
}

function stableStringify(value) {
  if (value == null || typeof value !== 'object') return JSON.stringify(value);
  if (Array.isArray(value)) return `[${value.map((item) => stableStringify(item)).join(',')}]`;
  return `{${Object.keys(value).sort().map((key) => `${JSON.stringify(key)}:${stableStringify(value[key])}`).join(',')}}`;
}

function createCodemapLayoutWorker(url = '/codemap-layout-worker.js') {
  let worker = null;
  let pending = null;

  function ensureWorker() {
    if (typeof Worker === 'undefined') throw new Error('Web Worker unavailable');
    if (worker) return worker;
    worker = new Worker(url);
    worker.onmessage = (event) => {
      const message = event.data || {};
      if (!pending) return;
      if (message.type === 'layout-result') {
        const result = message.result || {};
        if (result.requestId !== pending.requestId || result.inputHash !== pending.inputHash) return;
        if (!isValidLayoutResult(result)) {
          pending.reject(new Error('Invalid layout response.'));
          clearPending();
          return;
        }
        pending.resolve(result);
        clearPending();
      } else if (message.type === 'layout-error') {
        if (message.requestId && message.requestId !== pending.requestId) return;
        pending.reject(new Error(sanitizeMessage(message.error) || 'Layout failed.'));
        clearPending();
      }
    };
    worker.onerror = () => {
      if (pending) pending.reject(new Error('Codemap worker failed.'));
      clearPending();
      dispose();
    };
    return worker;
  }

  function clearPending() {
    if (pending && pending.timer) clearTimeout(pending.timer);
    pending = null;
  }

  function cancel() {
    if (pending && worker) {
      worker.postMessage({ type: 'cancel', requestId: pending.requestId });
    }
    clearPending();
  }

  function dispose() {
    clearPending();
    if (worker) worker.terminate();
    worker = null;
  }

  return {
    requestLayout(layoutInput) {
      cancel();
      return new Promise((resolve, reject) => {
        const activeWorker = ensureWorker();
        pending = {
          requestId: layoutInput.requestId,
          inputHash: layoutInput.inputHash,
          resolve,
          reject,
          timer: setTimeout(() => {
            reject(new Error('Layout timed out.'));
            dispose();
          }, CODEMAP_CONFIG.WORKER_TIMEOUT_MS),
        };
        activeWorker.postMessage({ type: 'layout', request: layoutInput });
      });
    },
    cancel,
    dispose,
  };
}

async function generateCodemap(event) {
  event.preventDefault();
  const query = elements.codemapQuery.value.trim();
  if (!query) return;
  if (!ensureReady()) return;
  const button = elements.codemapForm.querySelector('button[type="submit"]');
  button.disabled = true;
  state.codemap.startGeneration({ requestId: `job:${Date.now()}`, inputHash: stableHash({ query }) });
  try {
    const ref = await api('/api/codemaps', {
      method: 'POST',
      body: JSON.stringify({ query, maxNodes: 36 }),
    });
    state.latestCodemapJobId = ref.job && ref.job.id ? ref.job.id : '';
    applyJobSnapshot(ref.job);
    state.codemap.onJobQueued(ref.job);
    activateInspector('jobs');
  } catch (error) {
    state.codemap.onFailed(error);
    toast(error.message, true);
  } finally {
    button.disabled = false;
  }
}

function renderCodemap(codemap, route = {}) {
  cancelPendingCodemapFilterCommits();
  const normalized = normalizeCodemapViewModel(codemap);
  if (!normalized.ok) {
    state.codemap.onFailed(new Error(`Invalid Codemap: ${normalized.errors.join('; ')}`));
    return;
  }
  const viewModel = normalized.viewModel;
  restoreCodemapFilters(viewModel);
  const selection = codemapRouteSelection(viewModel, route);
  const decision = codemapRenderDecision(viewModel);
  if (decision.mode === 'text') {
    viewModel.selectedTraceId = viewModel.selectedTraceId || '';
    state.codemap.onReady(viewModel, selection);
    state.codemap.setViewMode('text');
    state.codemap.addWarning(decision.reason);
    return;
  }
  state.codemap.onReady(viewModel, selection);
  state.codemap.setViewMode(preferredCodemapViewMode());
  startCodemapLayout(viewModel);
}

function startCodemapLayout(viewModel) {
  const input = prepareCodemapLayoutInput(viewModel, { selectedTraceId: state.codemap.snapshot().selectedTraceId || viewModel.selectedTraceId });
  state.codemap.startLayout(input);
  if (!state.codemapLayoutWorker) state.codemapLayoutWorker = createCodemapLayoutWorker();
  state.codemapLayoutWorker.requestLayout(input)
    .then((result) => state.codemap.applyLayoutResult(result))
    .catch((error) => {
      state.codemap.onLayoutFailure(error);
      toast(error.message, true);
    });
}

function sameStringValues(left, right) {
  const a = Array.isArray(left) ? left : [];
  const b = Array.isArray(right) ? right : [];
  return a.length === b.length && a.every((value, index) => value === b[index]);
}

function sameStringSet(left, right) {
  const a = left instanceof Set ? left : new Set();
  const b = right instanceof Set ? right : new Set();
  return a.size === b.size && [...a].every((value) => b.has(value));
}

// A filter-only emit may patch the existing explorer. Everything that changes
// structure, selection, trace/details, warnings or layout still takes the full
// renderer so the incremental path cannot leave mixed snapshot state behind.
function isCodemapFilterOnlyUpdate(previous, next) {
  if (!previous?.artifact || !next?.artifact) return false;
  return previous.artifact === next.artifact
    && previous.layout === next.layout
    && previous.status === next.status
    && previous.viewMode === next.viewMode
    && previous.job === next.job
    && previous.error === next.error
    && previous.layoutError === next.layoutError
    && previous.selectedNodeId === next.selectedNodeId
    && previous.selectedEdgeId === next.selectedEdgeId
    && previous.selectedTraceId === next.selectedTraceId
    && previous.selectedTraceStep === next.selectedTraceStep
    && sameStringSet(previous.collapsedGroups, next.collapsedGroups)
    && sameStringValues(previous.warnings, next.warnings);
}

function setCodemapFilterVisibility(element, visible) {
  element.hidden = !visible;
  if (typeof element.setAttribute === 'function') {
    if (visible) element.removeAttribute?.('hidden');
    else element.setAttribute('hidden', '');
    element.setAttribute('aria-hidden', visible ? 'false' : 'true');
    if (element.dataset?.codemapFilterInteractive === 'true') {
      element.setAttribute('tabindex', visible ? '0' : '-1');
    }
  }
}

// Applies one filter result without replacing nodes, SVG paths, controls or
// listeners. Returns false when the expected explorer shell is absent so the
// caller can safely fall back to the full renderer.
function applyCodemapFilterDOM(root, snapshot, filtered) {
  if (!root || !snapshot?.artifact || !filtered) return false;
  if (!root.querySelector('.codemap-main') || !root.querySelector('.codemap-controls')) return false;
  const visibleNodes = filtered.visibleNodeIds instanceof Set ? filtered.visibleNodeIds : new Set();
  const visibleEdges = filtered.visibleEdgeIds instanceof Set ? filtered.visibleEdgeIds : new Set();

  root.querySelectorAll('[data-codemap-filter-node-id]').forEach((element) => {
    setCodemapFilterVisibility(element, visibleNodes.has(element.dataset.codemapFilterNodeId));
  });
  root.querySelectorAll('[data-codemap-filter-edge-id]').forEach((element) => {
    setCodemapFilterVisibility(element, visibleEdges.has(element.dataset.codemapFilterEdgeId));
  });

  const groups = new Map((snapshot.artifact.groups || []).map((group) => [group.id, group]));
  root.querySelectorAll('[data-codemap-filter-group-id]').forEach((section) => {
    const group = groups.get(section.dataset.codemapFilterGroupId);
    const count = group ? group.nodeIds.filter((id) => visibleNodes.has(id)).length : 0;
    section.hidden = count === 0;
    const heading = section.querySelector?.('.codemap-group-visible-count');
    if (heading) heading.textContent = `${heading.dataset.groupTitle || group?.title || ''} (${count})`;
  });

  const visibleMeta = root.querySelector('.codemap-visible-meta');
  if (visibleMeta) visibleMeta.textContent = `${filtered.nodeCount}/${filtered.totalNodeCount} visible`;
  const count = root.querySelector('.codemap-count');
  if (count) count.textContent = `${filtered.nodeCount} nodes · ${filtered.edgeCount} edges`;
  const live = root.querySelector('.codemap-filter-live');
  if (live) live.textContent = `${filtered.nodeCount} of ${filtered.totalNodeCount} nodes visible. ${snapshot.viewMode} mode.`;
  const warning = root.querySelector('.codemap-filter-selection-warning');
  if (warning) warning.hidden = visibleNodes.has(warning.dataset.nodeId);

  const filters = snapshot.filters || defaultCodemapFilterState();
  const slider = root.querySelector('.codemap-confidence');
  if (slider) {
    slider.value = String(filters.minConfidence || 0);
    slider.setAttribute('aria-valuetext', `${Math.round((filters.minConfidence || 0) * 100)}% minimum confidence`);
  }
  const internal = root.querySelector('.codemap-internal-only');
  if (internal) internal.checked = Boolean(filters.internalOnly);
  root.querySelectorAll('[data-codemap-filter-key]').forEach((select) => {
    const selected = filters[select.dataset.codemapFilterKey] || [];
    select.value = selected[0] || '';
  });
  const search = root.querySelector('.codemap-search');
  if (search && (typeof document === 'undefined' || document.activeElement !== search)) {
    search.value = filters.searchText || '';
  }
  return true;
}

function renderCodemapStore(snapshot = state.codemap.snapshot()) {
  if (!elements.codemapResult) return;
  const artifact = snapshot.artifact;
  const previous = state.codemapRenderedSnapshot;
  if (isCodemapFilterOnlyUpdate(previous, snapshot)) {
    const filtered = applyCodemapFilters(artifact, snapshot.filters);
    if (applyCodemapFilterDOM(elements.codemapResult, snapshot, filtered)) {
      state.codemapRenderedSnapshot = snapshot;
      return;
    }
  }
  if (previous?.artifact !== artifact) cancelPendingCodemapFilterCommits();
  const focusState = captureCodemapFocus();
  elements.codemapResult.className = `codemap-result codemap-state-${snapshot.status}`;
  elements.codemapResult.replaceChildren();
  if (!artifact) {
    elements.codemapResult.classList.add('empty-state');
    const message = document.createElement('p');
    if (snapshot.status === 'job_queued' || snapshot.status === 'job_running' || snapshot.status === 'loading') {
      message.textContent = codemapJobStatusText(snapshot.job, snapshot.status);
    } else if (snapshot.status === 'failed') {
      message.textContent = sanitizeMessage(snapshot.error && snapshot.error.message) || 'Failed to generate Codemap.';
    } else {
      message.textContent = 'Describe a flow, bug, or change. The backend retrieves seeds through BM25/embeddings and expands the AST graph.';
    }
    elements.codemapResult.appendChild(message);
    state.codemapRenderedSnapshot = snapshot;
    restoreCodemapFocus(focusState);
    return;
  }

  const filtered = applyCodemapFilters(artifact, snapshot.filters);
  const header = buildCodemapHeader(snapshot, filtered);
  const controls = buildCodemapControls(snapshot, filtered);
  const body = document.createElement('div');
  body.className = 'codemap-explorer';
  const main = document.createElement('div');
  main.className = 'codemap-main';
  if (snapshot.viewMode === 'text') {
    main.appendChild(buildCodemapTextView(snapshot, filtered));
  } else if (snapshot.layout) {
    main.appendChild(buildCodemapGraph(snapshot, filtered));
  } else {
    main.appendChild(buildCodemapLayoutPending(snapshot));
  }
  const side = document.createElement('aside');
  side.className = 'codemap-side';
  side.append(buildCodemapTracePanel(snapshot), buildCodemapDetailsPanel(snapshot, filtered));
  body.append(main, side);
  elements.codemapResult.append(header, controls, body, buildCodemapLiveRegion(snapshot, filtered));
  void renderMermaidDiagrams(elements.codemapResult);
  updateCodemapRoute(snapshot);
  state.codemapRenderedSnapshot = snapshot;
  restoreCodemapFocus(focusState);
}

function captureCodemapFocus() {
  if (typeof document === 'undefined' || !elements.codemapResult) return null;
  const active = document.activeElement;
  if (!active || !elements.codemapResult.contains(active)) return null;
  if (active.classList.contains('codemap-search')) {
    return { kind: 'search', start: active.selectionStart || 0, end: active.selectionEnd || active.selectionStart || 0 };
  }
  if (active.classList.contains('codemap-confidence')) return { kind: 'confidence' };
  if (active.classList.contains('codemap-graph-wrap')) return { kind: 'graph' };
  const node = active.closest && active.closest('.graph-node[data-node-id]');
  if (node) return { kind: 'node', id: node.dataset.nodeId || '' };
  const edge = active.closest && active.closest('.graph-edge[data-edge-id]');
  if (edge) return { kind: 'edge', id: edge.dataset.edgeId || '' };
  return null;
}

function restoreCodemapFocus(focusState) {
  if (!focusState || typeof document === 'undefined' || !elements.codemapResult) return;
  let target = null;
  if (focusState.kind === 'search') {
    target = elements.codemapResult.querySelector('.codemap-search');
  } else if (focusState.kind === 'confidence') {
    target = elements.codemapResult.querySelector('.codemap-confidence');
  } else if (focusState.kind === 'graph') {
    target = elements.codemapResult.querySelector('.codemap-graph-wrap');
  } else if (focusState.kind === 'node') {
    target = [...elements.codemapResult.querySelectorAll('.graph-node[data-node-id]')].find((item) => item.dataset.nodeId === focusState.id);
  } else if (focusState.kind === 'edge') {
    target = [...elements.codemapResult.querySelectorAll('.graph-edge[data-edge-id]')].find((item) => item.dataset.edgeId === focusState.id);
  }
  if (!target || typeof target.focus !== 'function') return;
  target.focus({ preventScroll: true });
  if (focusState.kind === 'search' && typeof target.setSelectionRange === 'function') {
    target.setSelectionRange(focusState.start, focusState.end);
  }
}

function buildCodemapHeader(snapshot, filtered) {
  const artifact = snapshot.artifact;
  const header = document.createElement('div');
  header.className = 'codemap-header';
  const title = document.createElement('h3');
  title.textContent = artifact.title;
  const meta = document.createElement('div');
  meta.className = 'codemap-meta';
  [
    `${artifact.nodes.length} nodes`,
    `${artifact.edges.length} edges`,
    `${filtered.nodeCount}/${filtered.totalNodeCount} visible`,
    artifact.status,
    artifact.provider || 'provider n/a',
  ].filter(Boolean).forEach((text, index) => {
    const pill = document.createElement('span');
    if (index === 2) pill.className = 'codemap-visible-meta';
    pill.textContent = text;
    meta.appendChild(pill);
  });
  header.append(title, meta);
  if (artifact.overview) {
    const narrative = document.createElement('div');
    narrative.className = 'codemap-overview markdown-content';
    renderMarkdownInto(narrative, artifact.overview);
    header.appendChild(narrative);
  }
  if (artifact.flows.length) header.appendChild(buildCodemapFlowWalkthrough(snapshot));
  if (artifact.diagram) header.appendChild(buildMermaidDiagramBlock(artifact.diagram, 'Deterministic diagram'));
  const warnings = [
    ...(artifact.status === 'stale' ? artifact.staleReasons : []),
    ...(artifact.status === 'transient' ? ['transient artifact not persisted'] : []),
    ...(snapshot.layoutError ? [snapshot.layoutError] : []),
    ...(snapshot.warnings || []),
  ].filter(Boolean);
  if (warnings.length) {
    const warning = document.createElement('div');
    warning.className = 'codemap-warning';
    warning.setAttribute('role', 'status');
    warning.textContent = warnings.join(' · ');
    header.appendChild(warning);
  }
  return header;
}

function buildCodemapFlowWalkthrough(snapshot) {
  const artifact = snapshot.artifact;
  const section = document.createElement('section');
  section.className = 'codemap-flow-walkthrough';
  const heading = document.createElement('h4');
  heading.textContent = 'Anchored flows';
  section.appendChild(heading);
  artifact.flows.forEach((flow) => {
    const flowBlock = document.createElement('article');
    flowBlock.className = 'codemap-flow';
    const title = document.createElement('h5');
    title.textContent = flow.title;
    const list = document.createElement('ol');
    flow.steps.forEach((step, index) => {
      const item = document.createElement('li');
      item.className = 'codemap-flow-step';
      const stepTitle = document.createElement('h6');
      const location = step.path && step.line ? ` — ${step.path}:${step.line}` : '';
      stepTitle.textContent = `[${step.label || index + 1}] ${step.text || 'Flow step'}${location}`;
      item.appendChild(stepTitle);
      if (step.snippet) {
        const pre = document.createElement('pre');
        const code = document.createElement('code');
        code.textContent = step.snippet;
        pre.appendChild(code);
        item.appendChild(pre);
      }
      const node = artifact.nodes.find((candidate) => candidate.id === step.nodeId);
      if (node && !isExternalCodemapNode(node)) {
        const open = document.createElement('button');
        open.type = 'button';
        open.className = 'button secondary small';
        open.textContent = 'Open step';
        open.addEventListener('click', () => {
          state.codemap.selectNode(node.id);
          openCodemapNode(node, artifact);
        });
        item.appendChild(open);
      }
      list.appendChild(item);
    });
    flowBlock.append(title, list);
    section.appendChild(flowBlock);
  });
  return section;
}

function buildCodemapControls(snapshot, filtered) {
  const artifact = snapshot.artifact;
  const controls = document.createElement('div');
  controls.className = 'codemap-controls';
  const mode = document.createElement('div');
  mode.className = 'codemap-mode';
  mode.setAttribute('role', 'group');
  mode.setAttribute('aria-label', 'Codemap view mode');
  const graphButton = document.createElement('button');
  graphButton.type = 'button';
  graphButton.className = `button small ${snapshot.viewMode === 'graph' ? 'primary' : 'secondary'}`;
  graphButton.textContent = 'Graph';
  graphButton.disabled = codemapRenderDecision(artifact).mode === 'text';
  graphButton.setAttribute('aria-pressed', snapshot.viewMode === 'graph' ? 'true' : 'false');
  graphButton.addEventListener('click', () => {
    state.codemap.setViewMode('graph');
    if (!state.codemap.snapshot().layout) startCodemapLayout(artifact);
  });
  const textButton = document.createElement('button');
  textButton.type = 'button';
  textButton.className = `button small ${snapshot.viewMode === 'text' ? 'primary' : 'secondary'}`;
  textButton.textContent = 'Text';
  textButton.setAttribute('aria-pressed', snapshot.viewMode === 'text' ? 'true' : 'false');
  textButton.addEventListener('click', () => state.codemap.setViewMode('text'));
  const retryButton = document.createElement('button');
  retryButton.type = 'button';
  retryButton.className = 'button secondary small';
  retryButton.textContent = 'Recalculate layout';
  retryButton.addEventListener('click', () => startCodemapLayout(artifact));
  mode.append(graphButton, textButton, retryButton);

  const filters = document.createElement('div');
  filters.className = 'codemap-filters';
  filters.id = 'codemap-filters';
  filters.append(
    codemapSelect('Group', 'groups', artifact.groups.map((group) => [group.id, group.title]), snapshot.filters.groups),
    codemapSelect('Kind', 'kinds', uniqueValues(artifact.nodes.map((node) => node.kind)).map((value) => [value, value]), snapshot.filters.kinds),
    codemapSelect('Relationship', 'edgeTypes', uniqueValues(artifact.edges.map((edge) => edge.type)).map((value) => [value, value]), snapshot.filters.edgeTypes),
  );
  const confidence = document.createElement('label');
  confidence.textContent = 'Confidence';
  const slider = document.createElement('input');
  slider.type = 'range';
  slider.className = 'codemap-confidence';
  slider.min = '0';
  slider.max = '1';
  slider.step = '0.05';
  slider.value = String(snapshot.filters.minConfidence || 0);
  slider.setAttribute('aria-valuetext', `${Math.round((snapshot.filters.minConfidence || 0) * 100)}% minimum confidence`);
  slider.addEventListener('input', () => scheduleCodemapFrameFilter({ minConfidence: Number(slider.value) || 0 }));
  confidence.appendChild(slider);
  const internal = document.createElement('label');
  const checkbox = document.createElement('input');
  checkbox.type = 'checkbox';
  checkbox.className = 'codemap-internal-only';
  checkbox.checked = Boolean(snapshot.filters.internalOnly);
  checkbox.addEventListener('change', () => updateCodemapFilters({ internalOnly: checkbox.checked }));
  internal.append(checkbox, document.createTextNode(' Internal'));
  const search = document.createElement('input');
  search.className = 'text-control codemap-search';
  search.type = 'search';
  search.placeholder = 'filter labels';
  search.value = snapshot.filters.searchText || '';
  search.addEventListener('input', () => scheduleCodemapSearchFilter(search.value));
  const clear = document.createElement('button');
  clear.type = 'button';
  clear.className = 'button secondary small';
  clear.textContent = 'Clear';
  clear.addEventListener('click', () => {
    cancelPendingCodemapFilterCommits();
    search.value = '';
    state.codemap.clearFilters();
    persistCodemapFilters(artifact, defaultCodemapFilterState());
  });
  const count = document.createElement('div');
  count.className = 'codemap-count';
  count.textContent = `${filtered.nodeCount} nodes · ${filtered.edgeCount} edges`;
  filters.append(confidence, internal, search, clear, count);
  controls.append(mode, filters);
  return controls;
}

function codemapSelect(labelText, key, options, selectedValues) {
  const label = document.createElement('label');
  label.textContent = labelText;
  const select = document.createElement('select');
  select.className = 'select-control';
  select.dataset.codemapFilterKey = key;
  select.setAttribute('aria-label', labelText);
  const all = document.createElement('option');
  all.value = '';
  all.textContent = 'All';
  select.appendChild(all);
  options.forEach(([value, labelValue]) => {
    const option = document.createElement('option');
    option.value = value;
    option.textContent = labelValue;
    option.selected = (selectedValues || []).includes(value);
    select.appendChild(option);
  });
  select.addEventListener('change', () => updateCodemapFilters({ [key]: select.value ? [select.value] : [] }));
  label.appendChild(select);
  return label;
}

function buildCodemapLayoutPending(snapshot) {
  const wrap = document.createElement('div');
  wrap.className = 'codemap-layout-pending';
  const text = document.createElement('p');
  text.textContent = snapshot.layoutError ? 'Visual layout unavailable. Text mode remains active.' : 'Calculating layout off the main thread.';
  const button = document.createElement('button');
  button.type = 'button';
  button.className = 'button secondary small';
  button.textContent = 'Open text mode';
  button.addEventListener('click', () => state.codemap.setViewMode('text'));
  wrap.append(text, button, buildCodemapTextView({ ...snapshot, viewMode: 'text' }, applyCodemapFilters(snapshot.artifact, snapshot.filters)));
  return wrap;
}

function buildCodemapGraph(snapshot, filtered) {
  const artifact = snapshot.artifact;
  const layout = snapshot.layout;
  const routeById = new Map(layout.edgeRoutes.map((route) => [route.id, route]));
  const nodeLayout = new Map(layout.nodes.map((node) => [node.id, node]));
  const visibleNodes = new Set(filtered.visibleNodeIds);
  const visibleEdges = new Set(filtered.visibleEdgeIds);
  const trace = activeCodemapTrace(snapshot);
  const traceNodeIds = new Set();
  const traceEdgeIds = new Set();
  if (trace) trace.steps.forEach((step) => {
    step.nodeIds.forEach((id) => traceNodeIds.add(id));
    step.edgeIds.forEach((id) => traceEdgeIds.add(id));
  });

  const wrap = document.createElement('div');
  wrap.className = 'codemap-graph-wrap';
  wrap.setAttribute('role', 'application');
  wrap.setAttribute('aria-label', `Codemap ${artifact.title}. Use Tab to navigate through nodes and filters.`);
  wrap.setAttribute('aria-keyshortcuts', 'ArrowUp ArrowDown ArrowLeft ArrowRight / [ ]');
  wrap.tabIndex = 0;

  const toolbar = document.createElement('div');
  toolbar.className = 'codemap-graph-tools';
  const zoomOut = document.createElement('button');
  zoomOut.type = 'button';
  zoomOut.className = 'icon-button';
  zoomOut.title = 'Reduzir zoom';
  zoomOut.setAttribute('aria-label', 'Reduzir zoom do Codemap');
  zoomOut.textContent = '−';
  const zoomIn = document.createElement('button');
  zoomIn.type = 'button';
  zoomIn.className = 'icon-button';
  zoomIn.title = 'Aumentar zoom';
  zoomIn.setAttribute('aria-label', 'Aumentar zoom do Codemap');
  zoomIn.textContent = '+';
  const skip = document.createElement('a');
  skip.href = '#codemap-filters';
  skip.className = 'codemap-skip';
  skip.textContent = 'Skip to filters';
  toolbar.append(zoomOut, zoomIn, skip, codemapLegend());

  const viewport = document.createElement('div');
  viewport.className = 'codemap-viewport';
  const canvas = document.createElement('div');
  canvas.className = 'codemap-canvas';
  canvas.style.width = `${layout.bounds.width}px`;
  canvas.style.height = `${layout.bounds.height}px`;
  viewport.appendChild(canvas);

  const svg = document.createElementNS('http://www.w3.org/2000/svg', 'svg');
  svg.setAttribute('width', String(layout.bounds.width));
  svg.setAttribute('height', String(layout.bounds.height));
  svg.appendChild(codemapArrowDefs());
  artifact.edges.forEach((edge) => {
    const route = routeById.get(edge.id);
    if (!route) return;
    const path = document.createElementNS('http://www.w3.org/2000/svg', 'path');
    path.setAttribute('d', `M ${route.x1} ${route.y1} C ${route.c1x} ${route.c1y}, ${route.c2x} ${route.c2y}, ${route.x2} ${route.y2}`);
    path.setAttribute('class', `graph-edge edge-${edge.type}${snapshot.selectedEdgeId === edge.id ? ' selected' : ''}${trace && !traceEdgeIds.has(edge.id) ? ' dimmed' : ''}`);
    path.setAttribute('marker-end', 'url(#codemap-arrowhead)');
    path.setAttribute('tabindex', '0');
    path.dataset.edgeId = edge.id;
    path.dataset.codemapFilterEdgeId = edge.id;
    path.dataset.codemapFilterInteractive = 'true';
    setCodemapFilterVisibility(path, visibleEdges.has(edge.id));
    path.setAttribute('role', 'img');
    path.setAttribute('aria-label', edgeAriaLabel(edge, artifact));
    path.addEventListener('click', () => state.codemap.selectEdge(edge.id));
    path.addEventListener('keydown', (event) => {
      if (event.key === 'Enter' || event.key === ' ') {
        event.preventDefault();
        state.codemap.selectEdge(edge.id);
      }
    });
    svg.appendChild(path);
  });
  canvas.appendChild(svg);

  artifact.groups.forEach((group) => {
    const members = group.nodeIds.map((id) => nodeLayout.get(id)).filter(Boolean);
    if (!members.length) return;
    const groupButton = document.createElement('button');
    groupButton.type = 'button';
    groupButton.className = 'graph-group-label';
    groupButton.style.left = `${Math.min(...members.map((node) => node.x))}px`;
    groupButton.style.top = '18px';
    groupButton.textContent = `${group.title} (${group.nodeIds.length})`;
    groupButton.setAttribute('aria-pressed', snapshot.collapsedGroups.has(group.id) ? 'true' : 'false');
    groupButton.setAttribute('aria-expanded', snapshot.collapsedGroups.has(group.id) ? 'false' : 'true');
    groupButton.addEventListener('click', () => state.codemap.toggleGroup(group.id));
    canvas.appendChild(groupButton);
  });

  artifact.nodes.forEach((node) => {
    const position = nodeLayout.get(node.id);
    if (!position) return;
    const collapsed = snapshot.collapsedGroups.has(node.group);
    if (collapsed) return;
    const button = document.createElement('button');
    button.type = 'button';
    button.className = `graph-node${isExternalCodemapNode(node) ? ' external' : ''}${snapshot.selectedNodeId === node.id ? ' selected' : ''}${trace && !traceNodeIds.has(node.id) ? ' dimmed' : ''}${Number(node.confidence || 1) < 0.55 ? ' low-confidence' : ''}`;
    button.style.left = `${position.x}px`;
    button.style.top = `${position.y}px`;
    button.setAttribute('role', 'button');
    button.dataset.nodeId = node.id;
    button.dataset.codemapFilterNodeId = node.id;
    button.dataset.codemapFilterInteractive = 'true';
    setCodemapFilterVisibility(button, visibleNodes.has(node.id));
    button.setAttribute('aria-selected', snapshot.selectedNodeId === node.id ? 'true' : 'false');
    button.setAttribute('aria-label', `${node.label}, ${node.kind}, group ${node.group || 'Other'}`);
    button.title = node.path && node.range ? `${node.path}:${node.range.start.line}` : node.summary;
    const kind = document.createElement('span');
    kind.className = 'node-kind';
    kind.textContent = node.kind;
    const label = document.createElement('strong');
    label.textContent = node.label;
    const summary = document.createElement('small');
    summary.textContent = node.path && node.range ? `${node.path}:${node.range.start.line}` : node.summary || 'boundary';
    button.append(kind, label, summary);
    button.addEventListener('click', () => state.codemap.selectNode(node.id));
    button.addEventListener('dblclick', () => openCodemapNode(node, artifact));
    button.addEventListener('keydown', (event) => {
      if (event.key === 'Enter') openCodemapNode(node, artifact);
    });
    canvas.appendChild(button);
  });

  let zoom = snapshot.viewport.zoom || 1;
  let panX = snapshot.viewport.panX || 0;
  let panY = snapshot.viewport.panY || 0;
  const applyTransform = () => {
    canvas.style.transform = `translate(${panX}px, ${panY}px) scale(${zoom})`;
    state.codemap.rememberViewport({ zoom, panX, panY });
  };
  applyTransform();
  zoomOut.addEventListener('click', () => {
    zoom = Math.max(CODEMAP_CONFIG.MIN_ZOOM, zoom - 0.1);
    applyTransform();
  });
  zoomIn.addEventListener('click', () => {
    zoom = Math.min(CODEMAP_CONFIG.MAX_ZOOM, zoom + 0.1);
    applyTransform();
  });
  viewport.addEventListener('wheel', (event) => {
    if (!event.ctrlKey && !event.metaKey) return;
    event.preventDefault();
    zoom = Math.min(CODEMAP_CONFIG.MAX_ZOOM, Math.max(CODEMAP_CONFIG.MIN_ZOOM, zoom + (event.deltaY < 0 ? 0.08 : -0.08)));
    applyTransform();
  }, { passive: false });
  let drag = null;
  viewport.addEventListener('pointerdown', (event) => {
    if (event.target.closest('button, path, a')) return;
    drag = { x: event.clientX, y: event.clientY, panX, panY };
    viewport.setPointerCapture(event.pointerId);
  });
  viewport.addEventListener('pointermove', (event) => {
    if (!drag) return;
    panX = Math.max(-layout.bounds.width, Math.min(layout.bounds.width, drag.panX + event.clientX - drag.x));
    panY = Math.max(-layout.bounds.height, Math.min(layout.bounds.height, drag.panY + event.clientY - drag.y));
    applyTransform();
  });
  viewport.addEventListener('pointerup', () => { drag = null; });
  wrap.addEventListener('keydown', (event) => handleCodemapGraphKeydown(event, artifact, layout));

  wrap.append(toolbar, viewport);
  return wrap;
}

function buildCodemapTextView(snapshot, filtered) {
  const artifact = snapshot.artifact;
  const visibleNodes = new Set(filtered.visibleNodeIds);
  const visibleEdges = new Set(filtered.visibleEdgeIds);
  const article = document.createElement('article');
  article.className = 'codemap-text-view';
  const title = document.createElement('h3');
  title.textContent = artifact.title;
  const query = document.createElement('p');
  query.className = 'codemap-query-line';
  query.textContent = `Query: ${artifact.query || 'n/a'}`;
  article.append(title, query);

  artifact.groups.forEach((group) => {
    const nodes = group.nodeIds.map((id) => artifact.nodes.find((node) => node.id === id)).filter(Boolean);
    if (!nodes.length) return;
    const visibleCount = nodes.filter((node) => visibleNodes.has(node.id)).length;
    const section = document.createElement('details');
    section.className = 'codemap-node-appendix';
    section.dataset.codemapFilterGroupId = group.id;
    section.hidden = visibleCount === 0;
    const heading = document.createElement('summary');
    heading.className = 'codemap-group-visible-count';
    heading.dataset.groupTitle = group.title;
    heading.textContent = `${group.title} (${visibleCount})`;
    const list = document.createElement('ul');
    list.className = 'codemap-node-list';
    nodes.forEach((node) => {
      const item = document.createElement('li');
      item.dataset.codemapFilterNodeId = node.id;
      setCodemapFilterVisibility(item, visibleNodes.has(node.id));
      const button = document.createElement('button');
      button.type = 'button';
      button.className = 'codemap-text-node';
      button.textContent = `${node.label} · ${node.kind}${node.path && node.range ? ` · ${node.path}:${node.range.start.line}` : ' · external'}`;
      button.addEventListener('click', () => state.codemap.selectNode(node.id));
      const open = document.createElement('button');
      open.type = 'button';
      open.className = 'button secondary small';
      open.textContent = 'Open';
      open.disabled = isExternalCodemapNode(node);
      open.addEventListener('click', () => openCodemapNode(node, artifact));
      item.append(button, open);
      list.appendChild(item);
    });
    section.append(heading, list);
    article.appendChild(section);
  });

  const edgeAppendix = document.createElement('details');
  edgeAppendix.className = 'codemap-edge-appendix';
  const edgesTitle = document.createElement('summary');
  edgesTitle.textContent = 'Relationships';
  const edgeList = document.createElement('ul');
  edgeList.className = 'codemap-edge-list';
  artifact.edges.forEach((edge) => {
    const source = artifact.nodes.find((node) => node.id === edge.source);
    const target = artifact.nodes.find((node) => node.id === edge.target);
    const item = document.createElement('li');
    item.dataset.codemapFilterEdgeId = edge.id;
    setCodemapFilterVisibility(item, visibleEdges.has(edge.id));
    const button = document.createElement('button');
    button.type = 'button';
    button.className = 'codemap-text-edge';
    button.textContent = `${source ? source.label : edge.source} -> ${target ? target.label : edge.target} (${edge.type}${edge.confidence == null ? '' : `, ${edge.confidence.toFixed(2)}`})`;
    button.addEventListener('click', () => state.codemap.selectEdge(edge.id));
    item.appendChild(button);
    edgeList.appendChild(item);
  });
  edgeAppendix.append(edgesTitle, edgeList);
  article.appendChild(edgeAppendix);

  const traceAppendix = document.createElement('details');
  traceAppendix.className = 'codemap-trace-appendix';
  const traceTitle = document.createElement('summary');
  traceTitle.textContent = 'Structural trace';
  traceAppendix.appendChild(traceTitle);
  artifact.traceCandidates.forEach((trace) => {
    const details = document.createElement('details');
    details.open = trace.id === snapshot.selectedTraceId;
    const summary = document.createElement('summary');
    summary.textContent = `${trace.title} (${trace.steps.length} steps)`;
    const list = document.createElement('ol');
    trace.steps.forEach((step, index) => {
      const item = document.createElement('li');
      const button = document.createElement('button');
      button.type = 'button';
      button.className = 'codemap-trace-step';
      button.textContent = step.summary || step.label || `Step ${index + 1}`;
      button.addEventListener('click', () => {
        state.codemap.selectTrace(trace.id, index);
        openTraceStep(step, artifact);
      });
      item.appendChild(button);
      list.appendChild(item);
    });
    details.append(summary, list);
    traceAppendix.appendChild(details);
  });
  article.appendChild(traceAppendix);
  return article;
}

function buildCodemapTracePanel(snapshot) {
  const panel = document.createElement('section');
  panel.className = 'codemap-trace-panel';
  const heading = document.createElement('h4');
  heading.textContent = 'Trace candidates';
  panel.appendChild(heading);
  if (!snapshot.artifact.traceCandidates.length) {
    const empty = document.createElement('p');
    empty.className = 'empty-state';
    empty.textContent = 'No validated traces.';
    panel.appendChild(empty);
    return panel;
  }
  snapshot.artifact.traceCandidates.slice(0, 3).forEach((trace) => {
    const button = document.createElement('button');
    button.type = 'button';
    button.className = `trace-card${trace.id === snapshot.selectedTraceId ? ' selected' : ''}`;
    button.textContent = `${trace.title} · ${trace.steps.length} steps`;
    button.addEventListener('click', () => state.codemap.selectTrace(trace.id, 0));
    panel.appendChild(button);
  });
  const active = activeCodemapTrace(snapshot);
  if (active) {
    const nav = document.createElement('div');
    nav.className = 'trace-step-nav';
    const prev = document.createElement('button');
    prev.type = 'button';
    prev.className = 'button secondary small';
    prev.textContent = 'Previous';
    prev.disabled = snapshot.selectedTraceStep <= 0;
    prev.addEventListener('click', () => state.codemap.selectTrace(active.id, Math.max(0, snapshot.selectedTraceStep - 1)));
    const next = document.createElement('button');
    next.type = 'button';
    next.className = 'button secondary small';
    next.textContent = 'Next';
    next.disabled = snapshot.selectedTraceStep >= active.steps.length - 1;
    next.addEventListener('click', () => state.codemap.selectTrace(active.id, Math.min(active.steps.length - 1, snapshot.selectedTraceStep + 1)));
    const only = document.createElement('button');
    only.type = 'button';
    only.className = 'button secondary small';
    only.textContent = 'Trace only';
    only.addEventListener('click', () => updateCodemapFilters({ traceId: active.id }));
    nav.append(prev, next, only);
    panel.appendChild(nav);
  }
  return panel;
}

function buildCodemapDetailsPanel(snapshot, filtered) {
  const panel = document.createElement('section');
  panel.className = 'codemap-details-panel';
  const heading = document.createElement('h4');
  heading.textContent = 'Details';
  panel.appendChild(heading);
  const artifact = snapshot.artifact;
  const node = snapshot.selectedNodeId ? artifact.nodes.find((item) => item.id === snapshot.selectedNodeId) : null;
  const edge = snapshot.selectedEdgeId ? artifact.edges.find((item) => item.id === snapshot.selectedEdgeId) : null;
  const trace = activeCodemapTrace(snapshot);
  if (node) {
    const warning = document.createElement('div');
    warning.className = 'codemap-warning codemap-filter-selection-warning';
    warning.dataset.nodeId = node.id;
    warning.hidden = filtered.visibleNodeIds.has(node.id);
    warning.textContent = 'The current selection is outside the filters.';
    panel.appendChild(warning);
  }
  if (node) {
    panel.appendChild(detailRows([
      ['ID', node.id],
      ['Label', node.label],
      ['Kind', node.kind],
      ['Group', node.group || 'Other'],
      ['Location', node.path && node.range ? `${node.path}:${node.range.start.line}` : 'external/boundary'],
      ['Confidence', node.confidence == null ? 'n/a' : node.confidence.toFixed(2)],
      ['Summary', node.summary || 'n/a'],
    ]));
    const actions = document.createElement('div');
    actions.className = 'codemap-detail-actions';
    const open = document.createElement('button');
    open.type = 'button';
    open.className = 'button primary small';
    open.textContent = 'Open code';
    open.disabled = isExternalCodemapNode(node);
    open.addEventListener('click', () => openCodemapNode(node, artifact));
    const copy = document.createElement('button');
    copy.type = 'button';
    copy.className = 'button secondary small';
    copy.textContent = 'Copy reference';
    copy.addEventListener('click', () => copyCodemapReference({ type: 'node', id: node.id, path: node.path || '' }));
    const filter = document.createElement('button');
    filter.type = 'button';
    filter.className = 'button secondary small';
    filter.textContent = 'Filter by group';
    filter.addEventListener('click', () => updateCodemapFilters({ groups: [node.group || 'Other'] }));
    actions.append(open, copy, filter);
    panel.appendChild(actions);
  } else if (edge) {
    const source = artifact.nodes.find((item) => item.id === edge.source);
    const target = artifact.nodes.find((item) => item.id === edge.target);
    panel.appendChild(detailRows([
      ['ID', edge.id],
      ['Direction', `${source ? source.label : edge.source} -> ${target ? target.label : edge.target}`],
      ['Type', edge.type],
      ['Confidence', edge.confidence == null ? 'n/a' : edge.confidence.toFixed(2)],
      ['Evidence', edge.evidenceIds.join(', ') || 'n/a'],
      ['Snippet', edge.snippet || 'n/a'],
    ]));
    const filter = document.createElement('button');
    filter.type = 'button';
    filter.className = 'button secondary small';
    filter.textContent = 'Filter by relationship';
    filter.addEventListener('click', () => updateCodemapFilters({ edgeTypes: [edge.type] }));
    panel.appendChild(filter);
  } else if (trace) {
    panel.appendChild(detailRows([
      ['ID', trace.id],
      ['Title', trace.title],
      ['Steps', String(trace.steps.length)],
      ['Confidence', trace.confidence == null ? 'n/a' : trace.confidence.toFixed(2)],
      ['Provenance', trace.provenance.join(', ') || 'n/a'],
    ]));
  } else {
    const empty = document.createElement('p');
    empty.className = 'empty-state';
    empty.textContent = 'Select a node, edge, or trace.';
    panel.appendChild(empty);
  }
  return panel;
}

function detailRows(rows) {
  const dl = document.createElement('dl');
  dl.className = 'codemap-details';
  rows.forEach(([label, value]) => {
    const dt = document.createElement('dt');
    dt.textContent = label;
    const dd = document.createElement('dd');
    dd.textContent = value == null || value === '' ? 'n/a' : String(value);
    dl.append(dt, dd);
  });
  return dl;
}

function buildCodemapLiveRegion(snapshot, filtered) {
  const live = document.createElement('div');
  live.className = 'sr-only codemap-filter-live';
  live.setAttribute('aria-live', 'polite');
  live.textContent = `${filtered.nodeCount} of ${filtered.totalNodeCount} nodes visible. ${snapshot.viewMode} mode.`;
  return live;
}

function codemapLegend() {
  const legend = document.createElement('div');
  legend.className = 'codemap-legend';
  [['calls', 'call'], ['imports', 'import'], ['contains', 'contains'], ['references', 'ref'], ['tests', 'test']].forEach(([type, label]) => {
    const item = document.createElement('span');
    item.className = `legend-edge edge-${type}`;
    item.textContent = label;
    legend.appendChild(item);
  });
  return legend;
}

function codemapArrowDefs() {
  const defs = document.createElementNS('http://www.w3.org/2000/svg', 'defs');
  const marker = document.createElementNS('http://www.w3.org/2000/svg', 'marker');
  marker.setAttribute('id', 'codemap-arrowhead');
  marker.setAttribute('markerWidth', '8');
  marker.setAttribute('markerHeight', '6');
  marker.setAttribute('refX', '7');
  marker.setAttribute('refY', '3');
  marker.setAttribute('orient', 'auto');
  const arrow = document.createElementNS('http://www.w3.org/2000/svg', 'path');
  arrow.setAttribute('d', 'M 0 0 L 8 3 L 0 6 z');
  arrow.setAttribute('fill', '#a9bed9');
  marker.appendChild(arrow);
  defs.appendChild(marker);
  return defs;
}

function edgeAriaLabel(edge, artifact) {
  const source = artifact.nodes.find((node) => node.id === edge.source);
  const target = artifact.nodes.find((node) => node.id === edge.target);
  return `${source ? source.label : edge.source} to ${target ? target.label : edge.target}, relationship ${edge.type}`;
}

function activeCodemapTrace(snapshot) {
  if (!snapshot || !snapshot.artifact || !snapshot.selectedTraceId) return null;
  return snapshot.artifact.traceCandidates.find((trace) => trace.id === snapshot.selectedTraceId) || null;
}

function openCodemapNode(node, artifact) {
  if (!node || isExternalCodemapNode(node)) {
    toast('External boundary without a local source.', true);
    return;
  }
  if (artifact.status === 'stale') {
    toast('Stale map: refresh the Codemap before navigating to old ranges.', true);
    return;
  }
  navigateToKnownLocation({
    path: node.path,
    symbolId: node.id || '',
    range: node.range,
    snapshotId: artifact.inputSnapshotId || state.stats?.snapshotId || '',
  });
}

function openTraceStep(step, artifact) {
  const nodeId = step.nodeIds.find(Boolean);
  if (!nodeId) return;
  const node = artifact.nodes.find((item) => item.id === nodeId);
  if (node) openCodemapNode(node, artifact);
}

function copyCodemapReference(reference) {
  const text = JSON.stringify({
    type: reference.type,
    id: cleanID(reference.id),
    path: cleanText(reference.path),
  });
  if (navigator.clipboard && navigator.clipboard.writeText) {
    navigator.clipboard.writeText(text).then(() => toast('Reference copied.'));
  }
}

function handleCodemapGraphKeydown(event, artifact, layout) {
  if (event.key === 'Escape') {
    state.codemap.selectNode('');
    return;
  }
  if (event.key === '/' || (event.key.toLowerCase() === 'f' && (event.ctrlKey || event.metaKey))) {
    const search = document.querySelector('.codemap-search');
    if (search) {
      event.preventDefault();
      search.focus();
    }
    return;
  }
  if (event.key === '[' || event.key === ']') {
    const snapshot = state.codemap.snapshot();
    const trace = activeCodemapTrace(snapshot);
    if (!trace) return;
    const next = event.key === '[' ? snapshot.selectedTraceStep - 1 : snapshot.selectedTraceStep + 1;
    state.codemap.selectTrace(trace.id, Math.min(trace.steps.length - 1, Math.max(0, next)));
    return;
  }
  if (!['ArrowUp', 'ArrowDown', 'ArrowLeft', 'ArrowRight'].includes(event.key)) return;
  const snapshot = state.codemap.snapshot();
  const currentId = snapshot.selectedNodeId || (artifact.nodes[0] && artifact.nodes[0].id);
  const current = layout.nodes.find((node) => node.id === currentId);
  if (!current) return;
  const candidates = layout.nodes.filter((node) => node.id !== current.id);
  const next = candidates.sort((a, b) => directionalScore(event.key, current, a) - directionalScore(event.key, current, b))[0];
  if (next) {
    event.preventDefault();
    state.codemap.selectNode(next.id);
    focusCodemapNode(next.id);
  }
}

function focusCodemapNode(nodeId) {
  nextAnimationFrame(() => {
    const node = document.querySelector(`.graph-node[data-node-id="${cssEscape(nodeId)}"]`);
    if (node) FocusManager.moveFocus(node);
  });
}

function directionalScore(key, current, candidate) {
  const dx = candidate.x - current.x;
  const dy = candidate.y - current.y;
  if (key === 'ArrowRight' && dx <= 0) return Number.MAX_SAFE_INTEGER;
  if (key === 'ArrowLeft' && dx >= 0) return Number.MAX_SAFE_INTEGER;
  if (key === 'ArrowDown' && dy <= 0) return Number.MAX_SAFE_INTEGER;
  if (key === 'ArrowUp' && dy >= 0) return Number.MAX_SAFE_INTEGER;
  return Math.abs(dx) + Math.abs(dy);
}

function codemapJobStatusText(job, fallbackStatus) {
  if (!job) return fallbackStatus === 'loading' ? 'Submitting Codemap job.' : 'Generating Codemap.';
  const progress = job.progress && Number.isFinite(Number(job.progress.percent)) ? ` ${Number(job.progress.percent).toFixed(0)}%` : '';
  return `${job.state || fallbackStatus}: ${job.stage || job.type || 'codemap'}${progress}`;
}

function updateCodemapFilters(partial) {
  const snapshot = state.codemap.snapshot();
  const filters = { ...snapshot.filters, ...(partial || {}) };
  state.codemap.updateFilters(partial);
  persistCodemapFilters(snapshot.artifact, filters);
}

function scheduleCodemapSearchFilter(searchText) {
  if (!state.codemapSearchCommit) {
    state.codemapSearchCommit = createDebouncedCommit(
      (value) => updateCodemapFilters({ searchText: value }),
      CODEMAP_CONFIG.FILTER_INPUT_DEBOUNCE_MS,
    );
  }
  state.codemapSearchCommit.push(searchText);
}

function scheduleCodemapFrameFilter(partial) {
  if (!state.codemapFilterFrame) {
    state.codemapFilterFrame = createFrameCoalescer((value) => updateCodemapFilters(value));
  }
  state.codemapFilterFrame.push(partial);
}

function cancelPendingCodemapFilterCommits() {
  state.codemapSearchCommit?.cancel();
  state.codemapFilterFrame?.cancel();
}

function restoreCodemapFilters(viewModel) {
  if (!viewModel || viewModel.status === 'transient' || !viewModel.artifactId || typeof sessionStorage === 'undefined') return;
  try {
    const stored = sessionStorage.getItem(`codemap:filters:${viewModel.artifactId}`);
    if (!stored) return;
    state.codemap.updateFilters({ ...defaultCodemapFilterState(), ...JSON.parse(stored) });
  } catch (_) {
    // Ignore invalid session state; filters are convenience only.
  }
}

function persistCodemapFilters(viewModel, filters) {
  if (!viewModel || viewModel.status === 'transient' || !viewModel.artifactId || typeof sessionStorage === 'undefined') return;
  try {
    sessionStorage.setItem(`codemap:filters:${viewModel.artifactId}`, JSON.stringify(filters));
  } catch (_) {
    // Storage quota or privacy mode should not affect Codemap rendering.
  }
}

function preferredCodemapViewMode() {
  if (typeof window !== 'undefined' && window.matchMedia && window.matchMedia('(prefers-reduced-motion: reduce)').matches) return 'text';
  return 'graph';
}

function uniqueValues(values) {
  return [...new Set(values.filter(Boolean))].sort((a, b) => a.localeCompare(b));
}

function updateCodemapRoute(snapshot) {
  if (typeof window === 'undefined' || !snapshot.artifact || snapshot.artifact.status === 'transient') return;
  const route = codemapRouteFor({
    artifactId: snapshot.artifact.artifactId,
    selectedNodeId: snapshot.selectedNodeId,
    selectedTraceId: snapshot.selectedTraceId,
  });
  if (!route || `${window.location.pathname}${window.location.search}` === route) return;
  window.history.replaceState(window.history.state, '', route);
}

async function restoreCodemapFromLocation() {
  if (typeof window === 'undefined') return false;
  const request = codemapDeepLinkRequest(`${window.location.pathname}${window.location.search}`);
  if (!request.path) return false;
  activateInspector('codemap');
  state.codemap.startGeneration({ requestId: `route:${request.route.artifactId}`, inputHash: request.route.artifactId });
  try {
    const codemap = await api(request.path);
    renderCodemap(codemap, request.route);
    return true;
  } catch (error) {
    if (error.code === 'APP_NOT_READY') return false;
    state.codemap.onFailed(error);
    toast(error.message, true);
    return false;
  }
}

async function reindexWorkspace() {
  if (!ensureReady()) return;
  elements.reindexButton.disabled = true;
  setIndexStatus('checking for changes', 'active');
  try {
    const ref = await api('/api/reindex', { method: 'POST', body: '{}' });
    applyJobSnapshot(ref.job);
    toast('Index verification started.');
  } catch (error) {
    setIndexStatus('indexing error', 'error');
    toast(error.message, true);
  } finally {
    elements.reindexButton.disabled = false;
  }
}

function connectEventStream() {
  if (state.eventSource) return; // install the stream only once
  const events = new EventSource('/api/events');
  state.eventSource = events;
  state.lastEventSeq = 0;
  events.addEventListener('index', (event) => {
    let payload;
    try {
      payload = JSON.parse(event.data);
    } catch (_) {
      return;
    }
    // A gap in the monotonic event id means dropped events: reload atomically.
    if (eventGap(state.lastEventSeq, payload.id)) {
      reloadAfterCommit([...state.tabs.keys()]);
      loadJobs();
    }
    state.lastEventSeq = Math.max(state.lastEventSeq, eventSeq(payload.id) || 0);
    handleIndexEvent(payload);
  });
  events.addEventListener('job', (event) => {
    let payload;
    try {
      payload = JSON.parse(event.data);
    } catch (_) {
      return;
    }
    if (eventGap(state.lastEventSeq, payload.id)) {
      reloadAfterCommit([...state.tabs.keys()]);
      loadJobs();
    }
    state.lastEventSeq = Math.max(state.lastEventSeq, eventSeq(payload.id) || 0);
    handleJobEvent(payload);
  });
  events.addEventListener('resync', () => {
    state.lastEventSeq = 0;
    reloadAfterCommit([...state.tabs.keys()]);
    loadJobs();
  });
  events.onerror = () => setIndexStatus('reconnecting events', 'active');
}

// eventSeq extracts the monotonic sequence number from an event id ("evt-N").
function eventSeq(id) {
  const match = /^evt-(\d+)$/.exec(id || '');
  return match ? Number(match[1]) : null;
}

// eventGap reports whether an event id skips past the expected next sequence.
function eventGap(lastSeq, id) {
  const seq = eventSeq(id);
  return seq !== null && lastSeq > 0 && seq > lastSeq + 1;
}

function isSaveConflictCode(code) {
  return code === 'FILE_CHANGED_ON_DISK'
    || code === 'DOCUMENT_VERSION_CONFLICT'
    || code === 'DOCUMENT_CONFLICT_CHANGED';
}

// handleIndexEvent updates the UI from a commit-aware event. The file tree and
// stats are only reloaded on commit events, never on preparation progress.
function handleIndexEvent(payload) {
  switch (payload.type) {
    case 'index.prepare.started':
      setIndexStatus('checking for changes', 'active');
      break;
    case 'index.idle':
      setIndexStatus('no changes', 'ok');
      break;
    case 'watch.self_write_confirmed':
      setIndexStatus('index updated', 'ok');
      break;
    case 'workspace.files.changed':
      setIndexStatus('index updated', 'ok');
      reloadAfterCommit(payload.counts?.truncated ? [...state.tabs.keys()] : (payload.paths || []));
      break;
    case 'save.commit.succeeded':
      setIndexStatus('index updated', 'ok');
      reloadAfterCommit();
      break;
    case 'document.diagnostics.updated':
      handleDiagnosticsUpdatedEvent(payload);
      break;
    case 'index.prepare.failed':
    case 'index.commit.failed':
      setIndexStatus('indexing error', 'error');
      if (payload.error) toast(sanitizeMessage(payload.error.message), true);
      break;
    default:
      break;
  }
}

function createJobStore(limit = JOB_VISIBLE_LIMIT) {
  return { byId: new Map(), order: [], limit, gap: false };
}

function applyJobEvent(store, event) {
  if (!event || !event.job) return null;
  return applyJobSnapshotToStore(store, event.job);
}

function applyJobSnapshotToStore(store, job) {
  if (!store || !job || !job.id) return null;
  const existing = store.byId.get(job.id);
  if (existing && Number(existing.revision || 0) > Number(job.revision || 0)) {
    return existing;
  }
  const snapshot = { ...job, progress: { ...(job.progress || {}) }, error: job.error ? { ...job.error } : null };
  store.byId.set(snapshot.id, snapshot);
  store.order = [snapshot.id, ...store.order.filter((id) => id !== snapshot.id)];
  trimJobStore(store);
  return snapshot;
}

function trimJobStore(store) {
  const terminalIds = store.order.filter((id) => isTerminalJobState(store.byId.get(id)?.state));
  if (terminalIds.length <= store.limit) return;
  const remove = new Set(terminalIds.slice(store.limit));
  store.order = store.order.filter((id) => !remove.has(id));
  remove.forEach((id) => store.byId.delete(id));
}

function isTerminalJobState(stateName) {
  return stateName === 'succeeded' || stateName === 'failed' || stateName === 'stale' || stateName === 'canceled';
}

function activeJobs(store) {
  return store.order.map((id) => store.byId.get(id)).filter((job) => job && !isTerminalJobState(job.state));
}

function recentJobs(store) {
  return store.order.map((id) => store.byId.get(id)).filter((job) => job && isTerminalJobState(job.state));
}

function jobSummaryLabel(store) {
  const active = activeJobs(store).length;
  const recent = recentJobs(store).length;
  if (active && recent) return `${active} active job${active === 1 ? '' : 's'} · ${recent} recent job${recent === 1 ? '' : 's'}`;
  if (active) return `${active} active job${active === 1 ? '' : 's'}`;
  if (recent) return `${recent} recent job${recent === 1 ? '' : 's'}`;
  return 'No jobs';
}

function applyJobSnapshot(job) {
  const snapshot = applyJobSnapshotToStore(state.jobs, job);
  renderJobCenter();
  return snapshot;
}

async function loadJobs() {
  if (!state.appReady) return;
  try {
    const response = await api('/api/jobs?limit=50');
    (response.jobs || []).forEach((job) => applyJobSnapshotToStore(state.jobs, job));
    renderJobCenter();
  } catch (error) {
    if (error.code !== 'APP_NOT_READY') toast(error.message, true);
  }
}

function handleJobEvent(payload) {
  const job = applyJobEvent(state.jobs, payload);
  renderJobCenter();
  if (!job) return;
  if (isTerminalJobState(job.state)) {
    announce(`${jobTypeLabel(job.type)} ${jobStateLabel(job.state)}.`, job.state === 'failed' || job.state === 'stale' ? 'assertive' : 'polite');
  }
  if (job.type === 'repository.reindex') {
    handleReindexJob(job);
  } else if (job.type === 'deepwiki.refresh') {
    handleDeepWikiJob(job);
  } else if (job.type === 'codemap.generate') {
    handleCodemapJob(job);
  }
}

function handleReindexJob(job) {
  if (job.state === 'running' || job.state === 'queued') {
    setIndexStatus(job.message || job.stage || 'checking for changes', 'active');
  } else if (job.state === 'succeeded') {
    const finalStatus = job.stage === 'no_changes' ? 'no changes' : 'index updated';
    setIndexStatus(finalStatus, 'ok');
    Promise.all([loadStats(), loadTree()]).finally(() => setIndexStatus(finalStatus, 'ok'));
  } else if (job.state === 'failed' || job.state === 'stale') {
    setIndexStatus('indexing error', 'error');
    if (job.error) toast(sanitizeMessage(job.error.message), true);
  }
}

function handleDeepWikiJob(job) {
  if (job.state === 'running' || job.state === 'queued') {
    state.wikiStatus = 'generating';
    renderWikiSelector();
  } else if (job.state === 'succeeded') {
    loadWiki();
    loadStats();
    toast('DeepWiki updated.');
  } else if (job.state === 'failed' || job.state === 'stale') {
    loadWiki();
    if (job.error) toast(sanitizeMessage(job.error.message), true);
  }
}

async function handleCodemapJob(job) {
  if (job.id !== state.latestCodemapJobId) return;
  if (job.state === 'failed' || job.state === 'stale' || job.state === 'canceled') {
    if (job.state === 'canceled') state.codemap.cancel(job);
    else if (job.state === 'stale') state.codemap.onStale(sanitizeMessage(job.error && job.error.message) || 'job stale');
    else state.codemap.onFailed(job.error || new Error(`Codemap ${job.state}.`));
    if (job.error) toast(sanitizeMessage(job.error.message), true);
    return;
  }
  if (job.state === 'queued' || job.state === 'running') {
    state.codemap.onJobProgress(job);
    return;
  }
  if (job.state !== 'succeeded') return;
  try {
    const codemap = await api(`/api/jobs/${encodeURIComponent(job.id)}/result`);
    renderCodemap(codemap);
  } catch (error) {
    state.codemap.onFailed(error);
    toast(error.message, true);
  }
}

async function cancelJob(jobId) {
  const job = state.jobs.byId.get(jobId);
  if (!job || isTerminalJobState(job.state)) return;
  try {
    const response = await api(`/api/jobs/${encodeURIComponent(jobId)}`, {
      method: 'DELETE',
      headers: { 'If-Match': String(job.revision || 0) },
    });
    applyJobSnapshot(response.job);
  } catch (error) {
    toast(error.message, true);
  }
}

function renderJobCenter() {
  if (!elements.jobsSummary || !elements.jobsList) return;
  elements.jobsSummary.textContent = jobSummaryLabel(state.jobs);
  elements.jobsList.replaceChildren();
  const jobs = state.jobs.order.map((id) => state.jobs.byId.get(id)).filter(Boolean).slice(0, JOB_VISIBLE_LIMIT);
  if (!jobs.length) {
    const empty = document.createElement('p');
    empty.className = 'empty-state';
    empty.textContent = 'No recent jobs.';
    elements.jobsList.appendChild(empty);
    return;
  }
  jobs.forEach((job) => elements.jobsList.appendChild(jobRow(job)));
}

function jobRow(job) {
  const presentation = jobPresentation(job);
  const row = document.createElement('div');
  row.className = `job-row ${job.state || ''}`;
  row.setAttribute('role', 'listitem');
  const body = document.createElement('div');
  const title = document.createElement('strong');
  title.textContent = `${presentation.typeLabel} · ${jobStateLabel(job.state)}`;
  const detail = document.createElement('small');
  detail.textContent = presentation.detail;
  const bar = document.createElement('div');
  bar.className = `job-progress${presentation.noChanges ? ' no-changes' : ''}`;
  const attrs = presentation.progressAttributes;
  bar.setAttribute('role', attrs.role);
  if (attrs.role === 'progressbar') {
    bar.setAttribute('aria-valuemin', '0');
    bar.setAttribute('aria-valuemax', '100');
  }
  if (attrs.ariaValueNow === null) bar.removeAttribute('aria-valuenow');
  else bar.setAttribute('aria-valuenow', String(attrs.ariaValueNow));
  bar.setAttribute('aria-valuetext', attrs.ariaValueText);
  bar.setAttribute('aria-label', `${jobTypeLabel(job.type)} ${jobStateLabel(job.state)}`);
  const fill = document.createElement('span');
  fill.style.transform = `scaleX(${presentation.fillPercent / 100})`;
  bar.appendChild(fill);
  body.append(title, detail, bar);
  row.appendChild(body);
  if (!isTerminalJobState(job.state)) {
    const cancel = document.createElement('button');
    cancel.type = 'button';
    cancel.className = 'button secondary small';
    cancel.dataset.cancelJobId = job.id;
    cancel.textContent = 'Cancel';
    cancel.setAttribute('aria-label', `Cancel ${jobTypeLabel(job.type)} ${job.id}`);
    row.appendChild(cancel);
  }
  return row;
}

function jobPresentation(job) {
  const progress = job.progress || {};
  const noChanges = job.state === 'succeeded' && job.stage === 'no_changes';
  const progressAttributes = noChanges
    ? { role: 'status', ariaValueNow: null, ariaValueText: 'No changes' }
    : (job.state === 'succeeded'
      ? progressA11yAttributes({ percent: 100 })
      : (isTerminalJobState(job.state)
        ? { role: 'progressbar', ariaValueNow: null, ariaValueText: jobStateLabel(job.state) }
        : progressA11yAttributes(progress.indeterminate || typeof progress.percent === 'number'
          ? progress
          : { indeterminate: true, unit: job.stage || jobStateLabel(job.state) })));
  const fillPercent = noChanges
    ? 0
    : (typeof progress.percent === 'number'
      ? Math.max(0, Math.min(100, progress.percent))
      : (isTerminalJobState(job.state) ? 100 : 35));
  return {
    typeLabel: jobTypeLabel(job.type),
    detail: noChanges
      ? (job.message || 'no changes')
      : [job.message || job.stage, progressLabel(progress), job.error && job.error.code].filter(Boolean).join(' · '),
    noChanges,
    fillPercent,
    progressAttributes,
  };
}

function progressLabel(progress) {
  if (progress.indeterminate) return 'in progress';
  if (typeof progress.percent === 'number') return `${Math.round(progress.percent)}%`;
  if (progress.total > 0) return `${progress.completed || 0}/${progress.total} ${progress.unit || ''}`.trim();
  return '';
}

function jobTypeLabel(type) {
  switch (type) {
    case 'deepwiki.refresh': return 'DeepWiki';
    case 'codemap.generate': return 'Codemap';
    case 'repository.reindex': return 'Index verification';
    case 'embeddings.rebuild': return 'Embeddings';
    case 'fts.rebuild': return 'FTS';
    default: return type || 'Job';
  }
}

function jobStateLabel(stateName) {
  switch (stateName) {
    case 'queued': return 'queued';
    case 'running': return 'running';
    case 'canceling': return 'canceling';
    case 'canceled': return 'canceled';
    case 'succeeded': return 'completed';
    case 'failed': return 'failed';
    case 'stale': return 'stale';
    default: return stateName || '';
  }
}

function handleDiagnosticsUpdatedEvent(payload) {
  const documentId = payload.documentId || payload.documentID;
  if (!documentId) return;
  const tab = tabForDocument(documentId);
  if (!tab?.overlay) return;
  if (typeof payload.documentVersion === 'number' && payload.documentVersion !== tab.overlay.serverVersion) return;
  requestDiagnostics(tab);
}

function reloadAfterCommit(changedOpenPaths = null) {
  if (!state.appReady) return;
  loadStats();
  loadTree();
  if (Array.isArray(changedOpenPaths)) void refreshOpenFilesAfterExternalChange(changedOpenPaths);
}

function tabHasPendingLocalChanges(tab) {
  if (!tab) return false;
  if (tab.dirty) return true;
  const overlay = tab.overlay;
  return Boolean(overlay && (
    overlay.inFlight
    || overlay.syncPromise
    || overlay.debounce
    || overlay.acknowledgedEditRevision < overlay.localEditRevision
    || (overlay.syncState && overlay.syncState !== 'idle')
  ));
}

// refreshOpenTabAfterExternalChange reads the backend's post-commit document
// snapshot and applies it only if the same tab is still clean and unchanged.
// The second guard is essential: a user may type while the GET is in flight.
async function refreshOpenTabAfterExternalChange(tab, dependencies = {}) {
  if (!tab || tabHasPendingLocalChanges(tab)) return { status: 'preserved', reason: 'local_changes' };
  const request = dependencies.request || api;
  const updateModel = dependencies.updateModel || ((model) => {
    const viewState = state.editorAdapter.saveViewState(tab.documentId);
    state.editorAdapter.updateModel(model, 'external');
    state.editorAdapter.restoreViewState(tab.documentId, viewState);
    tab.viewState = viewState;
  });
  const isCurrent = dependencies.isCurrent || (() => state.tabs.get(tab.path) === tab);
  const overlay = tab.overlay;
  const baseline = {
    content: tab.content,
    overlay,
    serverVersion: overlay?.serverVersion || 0,
    localEditRevision: overlay?.localEditRevision || 0,
    acknowledgedEditRevision: overlay?.acknowledgedEditRevision || 0,
  };
  const snapshot = overlay
    ? await request(`/api/documents/${overlay.documentId}`, {
      headers: { 'X-Document-Lease': overlay.leaseId },
    })
    : await request(`/api/file?path=${encodeURIComponent(tab.path)}`);

  if (!isCurrent()) return { status: 'superseded' };
  if (tabHasPendingLocalChanges(tab)) return { status: 'preserved', reason: 'local_changes' };
  if (tab.content !== baseline.content) return { status: 'superseded' };
  if (tab.overlay !== baseline.overlay) return { status: 'superseded' };

  if (overlay) {
    if (
      overlay.serverVersion !== baseline.serverVersion
      || overlay.localEditRevision !== baseline.localEditRevision
      || overlay.acknowledgedEditRevision !== baseline.acknowledgedEditRevision
    ) {
      return { status: 'superseded' };
    }
    const nextVersion = Number(snapshot.version);
    if (!Number.isSafeInteger(nextVersion) || nextVersion <= baseline.serverVersion) {
      return { status: 'preserved', reason: 'version_not_advanced' };
    }
    if (snapshot.dirty || !['clean', 'external_changed_clean'].includes(snapshot.state)) {
      return { status: 'preserved', reason: snapshot.state || 'external_state' };
    }
    overlay.serverVersion = nextVersion;
    overlay.contentHash = snapshot.contentHash;
    overlay.baseContentHash = snapshot.baseContentHash;
    overlay.baseSnapshotId = snapshot.baseSnapshotId;
    overlay.syncState = 'idle';
  } else if (snapshot.content === baseline.content) {
    return { status: 'unchanged' };
  }

  tab.content = snapshot.content;
  tab.original = snapshot.content;
  tab.dirty = false;
  if (snapshot.language) tab.language = snapshot.language;
  updateModel({
    documentId: tab.documentId,
    path: tab.path,
    language: tab.language || 'text',
    version: overlay ? overlay.serverVersion : 0,
    content: tab.content,
    readOnly: !!tab.readOnly,
  });
  return { status: 'refreshed' };
}

async function refreshOpenFilesAfterExternalChange(paths) {
  const changed = new Set(paths);
  const tabs = [...state.tabs.values()].filter((tab) => changed.has(tab.path));
  const refreshed = [];
  await Promise.all(tabs.map(async (tab) => {
    try {
      const result = await refreshOpenTabAfterExternalChange(tab);
      if (result.status === 'refreshed') {
        refreshed.push(tab);
        markHoverStaleForDocument(tab.documentId);
        clearSemanticStateForTab(tab, { removeResults: true });
        refreshSemanticStateForTab(tab);
      } else if (result.status === 'preserved') {
        toast(`The file ${tab.path} changed externally; its open content was preserved.`, true);
      }
    } catch (error) {
      toast(`Could not refresh ${tab.path}: ${sanitizeMessage(error.message)}`, true);
    }
  }));
  if (!refreshed.length) return;
  const active = state.activePath ? state.tabs.get(state.activePath) : null;
  if (active && refreshed.includes(active)) {
    elements.fileStatus.textContent = active.path;
    elements.saveStatus.textContent = '';
    elements.saveStatus.className = '';
  }
  renderTabs();
  announce(refreshed.length === 1
    ? `File refreshed after an external change: ${refreshed[0].path}.`
    : `${refreshed.length} open files were refreshed after external changes.`);
}

function setIndexStatus(text, className = '') {
  elements.indexStatus.textContent = text;
  elements.indexStatus.className = `status-pill ${className}`.trim();
  announce(`Index: ${text}.`, className === 'error' ? 'assertive' : 'polite');
}

function renderMarkdownInto(element, markdown) {
  const version = (markdownRenderVersions.get(element) || 0) + 1;
  markdownRenderVersions.set(element, version);
  element.replaceChildren();
  const outputBlocks = markdownToHTMLBlocks(markdown || '');
  let remaining = outputBlocks.length;
  scheduleBatches(outputBlocks, 16, (blocks) => {
    if (markdownRenderVersions.get(element) !== version) return false;
    const template = document.createElement('template');
    template.innerHTML = blocks.join('\n');
    markCodeReferences(template.content);
    element.appendChild(template.content);
    remaining -= blocks.length;
    if (remaining === 0) void renderMermaidDiagrams(element, version);
    return true;
  });
}

function markdownToHTML(markdown) {
  return markdownToHTMLBlocks(markdown).join('\n');
}

const markdownRenderVersions = new WeakMap();

let mermaidLibraryPromise = null;
let mermaidRenderID = 0;

function buildMermaidDiagramBlock(diagram, title) {
  const section = document.createElement('section');
  section.className = 'mermaid-diagram-block';
  const heading = document.createElement('h4');
  heading.textContent = title;
  const pre = document.createElement('pre');
  const code = document.createElement('code');
  code.className = 'language-mermaid';
  code.textContent = diagram.source;
  pre.appendChild(code);
  section.append(heading, pre);
  if (diagram.sources.length) {
    const label = document.createElement('strong');
    label.textContent = 'Diagram sources';
    const list = document.createElement('ul');
    diagram.sources.forEach((source) => {
      const item = document.createElement('li');
      const sourceLabel = source.label ? `${source.label} — ${source.path}:${source.range.start.line}` : source.path;
      item.appendChild(createSourceAnchor(source.path, sourceLabel, source.range));
      list.appendChild(item);
    });
    section.append(label, list);
  }
  return section;
}

async function loadMermaidLibrary() {
  if (!mermaidLibraryPromise) {
    mermaidLibraryPromise = import('./src/mermaid-lite.ts');
  }
  return mermaidLibraryPromise;
}

async function renderMermaidDiagrams(root, markdownVersion = null) {
  if (!root || (markdownVersion !== null && markdownRenderVersions.get(root) !== markdownVersion)) return;
  const codes = [...root.querySelectorAll('pre > code.language-mermaid:not([data-mermaid-state])')];
  if (!codes.length) return;
  const pending = codes.filter((code) => {
    const source = code.textContent || '';
    if (!isSafeMermaidSource(source)) {
      code.dataset.mermaidState = 'invalid';
      code.parentElement?.classList.add('mermaid-source-invalid');
      return false;
    }
    code.dataset.mermaidState = 'loading';
    return true;
  });
  if (!pending.length) return;

  let mermaid;
  try {
    mermaid = await loadMermaidLibrary();
  } catch (_) {
    pending.forEach((code) => { code.dataset.mermaidState = 'failed'; });
    return;
  }
  for (const code of pending) {
    if (!root.contains(code) || (markdownVersion !== null && markdownRenderVersions.get(root) !== markdownVersion)) continue;
    try {
      const renderID = `codeatlas-mermaid-${++mermaidRenderID}`;
      const rendered = mermaid.renderMermaidSubset(code.textContent || '', renderID);
      const svg = safeMermaidSVG(rendered);
      if (!svg) throw new Error('invalid Mermaid SVG');
      const figure = document.createElement('figure');
      figure.className = 'mermaid-diagram';
      figure.setAttribute('role', 'img');
      figure.setAttribute('aria-label', 'Deterministic repository diagram');
      figure.appendChild(svg);
      const sourceDetails = document.createElement('details');
      const summary = document.createElement('summary');
      summary.textContent = 'Mermaid source';
      const sourcePre = document.createElement('pre');
      const sourceCode = document.createElement('code');
      sourceCode.className = 'language-mermaid-source';
      sourceCode.textContent = code.textContent || '';
      sourcePre.appendChild(sourceCode);
      sourceDetails.append(summary, sourcePre);
      figure.appendChild(sourceDetails);
      code.parentElement?.replaceWith(figure);
    } catch (_) {
      code.dataset.mermaidState = 'failed';
      code.parentElement?.classList.add('mermaid-render-failed');
    }
  }
}

function safeMermaidSVG(source) {
  const template = document.createElement('template');
  template.innerHTML = String(source || '');
  const svg = template.content.querySelector('svg');
  if (!svg || template.content.querySelector('script, foreignObject, iframe, object, embed')) return null;
  svg.querySelectorAll('style').forEach((node) => node.remove());
  svg.querySelectorAll('*').forEach((node) => {
    [...node.attributes].forEach((attribute) => {
      const name = attribute.name.toLowerCase();
      const value = attribute.value.trim();
      if (name === 'style' || name.startsWith('on')) node.removeAttribute(attribute.name);
      if ((name === 'href' || name === 'xlink:href') && !value.startsWith('#')) node.removeAttribute(attribute.name);
    });
  });
  svg.classList.add('mermaid-svg');
  svg.removeAttribute('style');
  return svg;
}

function scheduleBatches(items, batchSize, renderBatch, schedule = nextAnimationFrame) {
  let cursor = 0;
  let cancelled = false;
  const size = Math.max(1, batchSize);
  const step = () => {
    if (cancelled || cursor >= items.length) return;
    const batch = items.slice(cursor, cursor + size);
    cursor += batch.length;
    if (renderBatch(batch) === false) {
      cancelled = true;
      return;
    }
    if (cursor < items.length) schedule(step);
  };
  step();
  return () => { cancelled = true; };
}

function markdownToHTMLBlocks(markdown) {
  const lines = String(markdown).replace(/\r\n/g, '\n').split('\n');
  const output = [];
  let codeFence = '';
  let codeLanguage = '';
  let codeLines = [];
  let listType = null;
  let listItems = [];
  let paragraph = [];

  const closeParagraph = () => {
    if (paragraph.length) {
      output.push(`<p>${inlineMarkdown(paragraph.join(' '))}</p>`);
      paragraph = [];
    }
  };
  const closeList = () => {
    if (listType) output.push(`<${listType}>\n${listItems.join('\n')}\n</${listType}>`);
    listType = null;
    listItems = [];
  };

  for (let lineIndex = 0; lineIndex < lines.length; lineIndex += 1) {
    const line = lines[lineIndex];
    if (line.trim() === '<details>') {
      closeParagraph();
      closeList();
      const closeIndex = lines.indexOf('</details>', lineIndex + 1);
      const summaryMatch = lines[lineIndex + 1]?.trim().match(/^<summary>(.*)<\/summary>$/);
      if (closeIndex > lineIndex + 1 && summaryMatch) {
        const body = markdownToHTMLBlocks(lines.slice(lineIndex + 2, closeIndex).join('\n')).join('\n');
        output.push(`<details><summary>${inlineMarkdown(summaryMatch[1])}</summary>\n${body}\n</details>`);
        lineIndex = closeIndex;
        continue;
      }
    }
    const trimmedLine = line.trim();
    const openingFence = !codeFence && trimmedLine.match(/^(`{3,})([A-Za-z0-9_+#-]*)$/);
    if (openingFence) {
      closeParagraph();
      closeList();
      codeFence = openingFence[1];
      codeLanguage = openingFence[2];
      continue;
    }
    if (codeFence && trimmedLine === codeFence) {
      const languageClass = codeLanguage ? ` class="language-${escapeHTML(codeLanguage)}"` : '';
      output.push(`<pre><code${languageClass}>${escapeHTML(codeLines.join('\n'))}</code></pre>`);
      codeLines = [];
      codeFence = '';
      codeLanguage = '';
      continue;
    }
    if (codeFence) {
      codeLines.push(line);
      continue;
    }
    if (!line.trim()) {
      closeParagraph();
      closeList();
      continue;
    }
    const heading = line.match(/^(#{1,4})\s+(.+)$/);
    if (heading) {
      closeParagraph();
      closeList();
      const level = heading[1].length;
      output.push(`<h${level}>${inlineMarkdown(heading[2])}</h${level}>`);
      continue;
    }
    if (line[0] === '|' && line.at(-1) === '|' && (lines[lineIndex + 1] || '').includes('---')) {
      closeParagraph();
      closeList();
      let table = '<table>';
      let rowIndex = lineIndex;
      for (; rowIndex < lines.length; rowIndex += 1) {
        if (rowIndex === lineIndex + 1) continue;
        const row = lines[rowIndex];
        if (row[0] !== '|' || row.at(-1) !== '|') break;
        const tag = rowIndex === lineIndex ? 'th' : 'td';
        table += `<tr>${row.slice(1, -1).split('|').map((cell) => `<${tag}>${inlineMarkdown(cell.trim())}</${tag}>`).join('')}</tr>`;
      }
      output.push(`${table}</table>`);
      lineIndex = rowIndex - 1;
      continue;
    }
    const unordered = line.match(/^\s*[-*]\s+(.+)$/);
    if (unordered) {
      closeParagraph();
      if (listType !== 'ul') {
        closeList();
        listType = 'ul';
      }
      listItems.push(`<li>${inlineMarkdown(unordered[1])}</li>`);
      continue;
    }
    const ordered = line.match(/^\s*\d+\.\s+(.+)$/);
    if (ordered) {
      closeParagraph();
      if (listType !== 'ol') {
        closeList();
        listType = 'ol';
      }
      listItems.push(`<li>${inlineMarkdown(ordered[1])}</li>`);
      continue;
    }
    const quote = line.match(/^>\s?(.*)$/);
    if (quote) {
      closeParagraph();
      closeList();
      output.push(`<blockquote>${inlineMarkdown(quote[1])}</blockquote>`);
      continue;
    }
    closeList();
    paragraph.push(line.trim());
  }
  if (codeFence) {
    const languageClass = codeLanguage ? ` class="language-${escapeHTML(codeLanguage)}"` : '';
    output.push(`<pre><code${languageClass}>${escapeHTML(codeLines.join('\n'))}</code></pre>`);
  }
  closeParagraph();
  closeList();
  return output;
}

function inlineMarkdown(value) {
  let raw = String(value);
  const sourceTokens = [];
  raw = raw.replace(/\[([^\]\n]+)\]\(([^)\s]+)\)/g, (whole, label, target) => {
    const parsed = parseSourceTarget(target);
    if (!parsed) return whole;
    const token = `@@SOURCE${sourceTokens.length}@@`;
    sourceTokens.push(
      `<a class="code-reference" href="${escapeHTML(target)}" data-path="${escapeHTML(parsed.path)}" data-line="${parsed.line}" data-end-line="${parsed.endLine}" title="Open in editor">${escapeHTML(label)}</a>`,
    );
    return token;
  });
  let safe = escapeHTML(raw);
  const codeTokens = [];
  safe = safe.replace(/`([^`]+)`/g, (_, code) => {
    const token = `@@CODE${codeTokens.length}@@`;
    codeTokens.push(`<code>${code}</code>`);
    return token;
  });
  safe = safe.replace(/\*\*([^*]+)\*\*/g, '<strong>$1</strong>');
  safe = safe.replace(/\*([^*]+)\*/g, '<em>$1</em>');
  safe = safe.replace(/@@CODE(\d+)@@/g, (_, index) => codeTokens[Number(index)]);
  safe = safe.replace(/@@SOURCE(\d+)@@/g, (_, index) => sourceTokens[Number(index)]);
  return safe;
}

function parseSourceTarget(target) {
  const match = String(target).match(/^([^#]+?)(?:#L(\d+)(?:-L?(\d+))?)?$/);
  if (!match) return null;
  let path;
  try {
    path = decodeURIComponent(match[1]);
  } catch (_) {
    return null;
  }
  if (!path || path.startsWith('/') || path.includes('\\') || path.includes('\0') || path.includes(':') || path.includes('?')) return null;
  if (path.split('/').some((part) => !part || part === '.' || part === '..')) return null;
  const line = Number(match[2]) || 1;
  const endLine = Math.max(line, Number(match[3]) || line);
  return { path, line, endLine };
}

function escapeHTML(value) {
  return String(value)
    .replaceAll('&', '&amp;')
    .replaceAll('<', '&lt;')
    .replaceAll('>', '&gt;')
    .replaceAll('"', '&quot;')
    .replaceAll("'", '&#039;');
}

function markCodeReferences(container) {
  container.querySelectorAll('code').forEach((code) => {
    if (code.closest('pre')) return;
    const match = code.textContent.match(/^(.+\.(?:go|js|jsx|ts|tsx)):(\d+)(?:-(\d+))?$/i);
    if (!match) return;
    code.classList.add('code-reference');
    code.dataset.path = match[1];
    code.dataset.line = match[2];
    code.title = 'Open in editor';
  });
}

function handleCodeReferenceClick(event) {
  const reference = event.target.closest('.code-reference[data-path]');
  if (!reference || reference.dataset.evidenceId) return;
  event.preventDefault();
  const line = Number(reference.dataset.line) || 1;
  const point = editorPosition(line, 1);
  navigateToKnownLocation({
    path: reference.dataset.path,
    range: { start: point, end: point },
    snapshotId: state.stats?.snapshotId || '',
  });
}

function toast(message, isError = false) {
  const item = document.createElement('div');
  item.className = `toast${isError ? ' error' : ''}`;
  item.textContent = message;
  elements.toastRegion.appendChild(item);
  setTimeout(() => item.remove(), 4200);
}

// Expose the pure readiness helpers for Node-based unit tests. In the browser
// `module` is undefined, so this block is skipped and has no effect.
if (typeof module !== 'undefined' && module.exports) {
  module.exports = {
    backendStateToPhase,
    shouldContinuePolling,
    isDiagnosticPhase,
    isAppNotReady,
    shouldApplyOverlayResponse,
    fetchReadiness,
    loadMandatoryResources,
    phaseLabel,
    capabilityStateLabel,
    sanitizeMessage,
    wikiStatusLabel,
    normalizedWikiLinks,
    eventSeq,
    eventGap,
    createA11yAnnouncerState,
    nextA11yAnnouncement,
    progressA11yAttributes,
    editorTabA11yLabel,
    shouldHandleGlobalShortcut,
    shortcutDisplayLabel,
    focusFallbackSelector,
    CODEMAP_CONFIG,
    MERMAID_DIAGRAM_VERSION,
    normalizeCodemapViewModel,
    normalizeCodemapFlows,
    normalizeMermaidDiagram,
    isSafeMermaidSource,
    codemapRenderDecision,
    createCodemapStore,
    prepareCodemapLayoutInput,
    applyCodemapFilters,
    isCodemapFilterOnlyUpdate,
    applyCodemapFilterDOM,
    parseCodemapRoute,
    codemapRouteFor,
    codemapDeepLinkRequest,
    codemapRouteSelection,
    createJobStore,
    applyJobEvent,
    activeJobs,
    recentJobs,
    jobSummaryLabel,
    jobPresentation,
    isSaveConflictCode,
    editorPosition,
    offsetToEditorPosition,
    editorPositionToOffset,
    normalizeEditorPosition,
    wordRangeAtPosition,
    lineStartOffsets,
    lineRangeAtLine,
    createLatestThrottle,
    createFrameCoalescer,
    createDebouncedCommit,
    delegatedItem,
    updateFileTreeSelection,
    scheduleBatches,
    markdownToHTML,
    markdownToHTMLBlocks,
    parseSourceTarget,
    sourceHref,
    createExplainCache,
    explainCacheKey,
    buildExplainPayloadForTab,
    canOpenSeeMore,
    normalizedExplainResult,
    partitionExplainCodeEvidence,
    seeAlsoEvidence,
    shouldApplyExplainResponse,
    createNavigationHistory,
    pushNavigationLocation,
    canNavigateForward,
    navigateHistoryBack,
    buildNavigationRequestPayload,
    shouldApplyNavigationResponse,
    shouldApplyDiagnosticsResponse,
    shouldApplySemanticTokensResponse,
	isCurrentDocumentRequest,
    semanticTokensToDecorations,
    diagnosticSeverityCounts,
    diagnosticsToDecorations,
    visibleProblemRows,
    ensureOpenTab,
    runSyncPump,
    flushSync,
    documentLeaseForTab,
    readTrackedDocumentLeases,
    trackDocumentLease,
    untrackDocumentLease,
    releaseDocumentLease,
    releaseDocumentsOnPageHide,
    reclaimTrackedDocument,
    renewDocumentLeases,
	applyDocumentLeaseRenewalResults,
	recoverDisconnectedDocumentLease,
    readDocumentLeaseHandoffs,
    prepareDocumentLeaseHandoff,
    hoverAnchorForPointer,
    hoverCardPosition,
    stableHoverCardPosition,
    hoverTargetAfterPointerMiss,
    tabHasPendingLocalChanges,
    refreshOpenTabAfterExternalChange,
  };
}
