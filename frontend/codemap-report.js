'use strict';

// Codemap presentation layer. Renders the shareable Codemap artifact page
// (/codemaps/:artifactId) as a split reading surface: a narrative sidebar with
// numbered trace sections and per-section guides on the left, and a read-only
// source viewer with step glyphs on the right. The layout, tokens and
// interactions mirror the Devin Codemaps reference interface.
//
// Data ownership: everything rendered here comes from the backend-owned
// Codemap view model that app.js stores on #codemap-result. This module never
// derives new execution facts; it only presents them.

(function codemapPresentationModule(globalObject) {
  const PRESENTATION_CLASS = 'codeatlas-codemap-presentation';
  const THEME_STORAGE_KEY = 'codeatlas.codemap.report-theme.v1';
  const SIDEBAR_STORAGE_KEY = 'codeatlas.codemap.sidebar-width.v1';
  const STEP_LABEL_PATTERN = /^\d+[a-z](?:[a-z0-9-]*)?$/i;
  const REF_PATTERN = /\[((?:\d+[a-z][a-z0-9]*)(?:\s*,\s*\d+[a-z][a-z0-9]*)*)\]/gi;

  let observer = null;
  let presentation = null;
  let scheduled = false;

  // ---------------------------------------------------------------------------
  // Pure helpers (exported for tests)
  // ---------------------------------------------------------------------------

  function isCodemapRoute(pathname) {
    return /^\/codemaps\/[^/]+\/?$/.test(String(pathname || ''));
  }

  function alphabeticSuffix(index) {
    let value = Number(index) + 1;
    let suffix = '';
    while (value > 0) {
      value -= 1;
      suffix = String.fromCharCode(97 + (value % 26)) + suffix;
      value = Math.floor(value / 26);
    }
    return suffix || 'a';
  }

  function parseCanonicalHash(hash) {
    const raw = String(hash || '').replace(/^#/, '');
    if (!raw) return '';
    try {
      const decoded = decodeURIComponent(raw);
      return STEP_LABEL_PATTERN.test(decoded) ? decoded.toLowerCase() : '';
    } catch (_) {
      return '';
    }
  }

  // Splits prose into text and step-reference parts. "[1a]" and "[1b, 2c]"
  // become individual {type:'ref'} entries; everything else stays text.
  function parseStepRefs(text) {
    const value = String(text || '');
    const parts = [];
    let cursor = 0;
    REF_PATTERN.lastIndex = 0;
    let match = REF_PATTERN.exec(value);
    while (match) {
      if (match.index > cursor) parts.push({ type: 'text', text: value.slice(cursor, match.index) });
      const ids = match[1].split(',').map((id) => id.trim().toLowerCase()).filter(Boolean);
      ids.forEach((id, index) => {
        if (index > 0) parts.push({ type: 'text', text: ', ' });
        parts.push({ type: 'ref', id });
      });
      cursor = match.index + match[0].length;
      match = REF_PATTERN.exec(value);
    }
    if (cursor < value.length) parts.push({ type: 'text', text: value.slice(cursor) });
    return parts;
  }

  function sectionNumberForFlow(_flow, index) {
    return index + 1;
  }

  // Assigns canonical presentation labels: section number + letter sequence
  // ("1a", "1b", … "2a"). Labels are a stable function of step position, so
  // deep links stay valid across backend label conventions; the original
  // backend label is preserved on the step as sourceLabel.
  function ensureStepLabels(flows) {
    (flows || []).forEach((flow, flowIndex) => {
      const sectionNumber = sectionNumberForFlow(flow, flowIndex);
      (flow.steps || []).forEach((step, stepIndex) => {
        step.sourceLabel = String(step.label || '').trim().toLowerCase();
        step.label = `${sectionNumber}${alphabeticSuffix(stepIndex)}`;
      });
    });
    return flows;
  }

  function fileTabsForFlows(flows) {
    const seen = new Set();
    const files = [];
    (flows || []).forEach((flow) => {
      (flow.steps || []).forEach((step) => {
        const path = String(step.path || '').replaceAll('\\', '/');
        if (!path || seen.has(path)) return;
        seen.add(path);
        files.push(path);
      });
    });
    return files;
  }

  function breadcrumbSegments(path) {
    return String(path || '').replaceAll('\\', '/').split('/').filter(Boolean);
  }

  function fileName(path) {
    const segments = breadcrumbSegments(path);
    return segments.at(-1) || String(path || '');
  }

  function deriveSectionSummary(flow) {
    const declared = String(flow?.summary || '').trim();
    if (declared) return declared;
    const steps = flow?.steps || [];
    const paths = steps.map((step) => step.path).filter(Boolean);
    if (!steps.length) return 'No grounded steps in this section.';
    const count = `${steps.length} grounded step${steps.length === 1 ? '' : 's'}`;
    if (!paths.length) return `${count}.`;
    const first = fileName(paths[0]);
    const last = fileName(paths.at(-1));
    return first === last ? `${count} in ${first}.` : `${count}, from ${first} to ${last}.`;
  }

  // Builds the nesting hierarchy from the backend-derived step depths. A step
  // can never skip levels: its effective depth is clamped to one below its
  // nearest surviving ancestor, so a malformed depth sequence degrades to a
  // shallower — never broken — tree.
  function buildStepTree(steps) {
    const roots = [];
    const stack = [];
    (steps || []).forEach((step) => {
      const declared = Math.max(0, Number(step.depth) || 0);
      const depth = Math.min(declared, stack.length);
      const node = { step, depth, children: [] };
      stack.length = depth;
      if (depth === 0) roots.push(node);
      else stack[depth - 1].children.push(node);
      stack.push(node);
    });
    return roots;
  }

  // Groups consecutive sibling tree nodes that live in the same enclosing
  // symbol so each run can render a context label, like the reference UI.
  function groupTreeSiblings(nodes, nodesById) {
    const runs = [];
    (nodes || []).forEach((node) => {
      const symbolNode = nodesById?.get?.(node.step.nodeId) || null;
      const symbol = symbolNode && !symbolNode.external ? String(symbolNode.label || '') : '';
      const previous = runs.at(-1);
      if (previous && previous.symbol === symbol) previous.nodes.push(node);
      else runs.push({ symbol, nodes: [node] });
    });
    return runs;
  }

  // Minimal markdown block parser for backend narrative text. Supports
  // headings, unordered lists and paragraphs; inline handling is separate.
  function parseMarkdownBlocks(text) {
    const blocks = [];
    const lines = String(text || '').replace(/\r\n/g, '\n').split('\n');
    let paragraph = [];
    let list = null;
    const flushParagraph = () => {
      if (paragraph.length) {
        blocks.push({ type: 'paragraph', text: paragraph.join(' ').trim() });
        paragraph = [];
      }
    };
    const flushList = () => {
      if (list && list.items.length) blocks.push(list);
      list = null;
    };
    lines.forEach((line) => {
      const trimmed = line.trim();
      if (!trimmed) {
        flushParagraph();
        flushList();
        return;
      }
      const heading = trimmed.match(/^(#{1,4})\s+(.*)$/);
      if (heading) {
        flushParagraph();
        flushList();
        blocks.push({ type: 'heading', level: heading[1].length, text: heading[2].trim() });
        return;
      }
      const item = trimmed.match(/^[-*]\s+(.*)$/);
      if (item) {
        flushParagraph();
        if (!list) list = { type: 'list', items: [] };
        list.items.push(item[1].trim());
        return;
      }
      flushList();
      paragraph.push(trimmed);
    });
    flushParagraph();
    flushList();
    return blocks;
  }

  function availableStorage() {
    try {
      return globalObject.localStorage || null;
    } catch (_) {
      return null;
    }
  }

  function preferredTheme(storage) {
    try {
      return storage && storage.getItem(THEME_STORAGE_KEY) === 'light' ? 'light' : 'dark';
    } catch (_) {
      return 'dark';
    }
  }

  function rememberTheme(theme, storage) {
    try {
      storage?.setItem(THEME_STORAGE_KEY, theme === 'light' ? 'light' : 'dark');
    } catch (_) {
      // Presentation preferences must never block the report.
    }
  }

  function preferredSidebarWidth(storage) {
    try {
      const value = Number(storage?.getItem(SIDEBAR_STORAGE_KEY));
      return Number.isFinite(value) && value >= 280 && value <= 900 ? value : 0;
    } catch (_) {
      return 0;
    }
  }

  function rememberSidebarWidth(width, storage) {
    try {
      storage?.setItem(SIDEBAR_STORAGE_KEY, String(Math.round(width)));
    } catch (_) {
      // Ignored: resizing still works without persistence.
    }
  }

  // ---------------------------------------------------------------------------
  // DOM helpers (textContent only; repository strings are untrusted)
  // ---------------------------------------------------------------------------

  function element(tag, className, text) {
    const node = document.createElement(tag);
    if (className) node.className = className;
    if (text !== undefined) node.textContent = text;
    return node;
  }

  function button(className, onClick, attributes = {}) {
    const control = document.createElement('button');
    control.type = 'button';
    control.className = className;
    Object.entries(attributes).forEach(([name, value]) => control.setAttribute(name, String(value)));
    if (onClick) control.addEventListener('click', onClick);
    return control;
  }

  function svgIcon(pathData, size = 16) {
    const svg = document.createElementNS('http://www.w3.org/2000/svg', 'svg');
    svg.setAttribute('viewBox', '0 0 16 16');
    svg.setAttribute('width', String(size));
    svg.setAttribute('height', String(size));
    svg.setAttribute('aria-hidden', 'true');
    const path = document.createElementNS('http://www.w3.org/2000/svg', 'path');
    path.setAttribute('d', pathData);
    path.setAttribute('fill', 'currentColor');
    svg.appendChild(path);
    return svg;
  }

  const ICONS = {
    chevron: 'M5.7 3.3 10.4 8l-4.7 4.7-.9-.9L8.6 8 4.8 4.2z',
    copy: 'M5 2h7a1 1 0 0 1 1 1v8h-1.3V3.3H5V2Zm-2 3h7a1 1 0 0 1 1 1v7a1 1 0 0 1-1 1H3a1 1 0 0 1-1-1V6a1 1 0 0 1 1-1Zm.3 1.3v6.4h6.4V6.3H3.3Z',
    theme: 'M8 1.5a6.5 6.5 0 1 0 0 13V1.5Z',
    sparkle: 'M8 1.5 9.3 6 14 7.3 9.3 8.6 8 13.2 6.7 8.6 2 7.3 6.7 6 8 1.5Z',
    crumb: 'M6.1 4.2 9.9 8l-3.8 3.8-.8-.8L8.3 8 5.3 5Z',
  };

  function appendInlineText(container, text, onRef) {
    // Inline markdown: **bold** and `code`, with step refs linked inside plain
    // segments. Order: split code/bold first, then refs inside text pieces.
    const value = String(text || '');
    const pattern = /(\*\*[^*]+\*\*|`[^`]+`)/g;
    let cursor = 0;
    let match = pattern.exec(value);
    const emitText = (piece) => {
      parseStepRefs(piece).forEach((part) => {
        if (part.type === 'ref' && onRef && onRef.has(part.id)) {
          const ref = button('codemap-presentation-ref', () => onRef.get(part.id)(), { 'data-step-ref': part.id });
          ref.textContent = `[${part.id}]`;
          container.appendChild(ref);
        } else if (part.type === 'ref') {
          container.appendChild(document.createTextNode(`[${part.id}]`));
        } else {
          container.appendChild(document.createTextNode(part.text));
        }
      });
    };
    while (match) {
      if (match.index > cursor) emitText(value.slice(cursor, match.index));
      const token = match[0];
      if (token.startsWith('**')) container.appendChild(element('strong', '', token.slice(2, -2)));
      else container.appendChild(element('code', '', token.slice(1, -1)));
      cursor = match.index + token.length;
      match = pattern.exec(value);
    }
    if (cursor < value.length) emitText(value.slice(cursor));
  }

  function renderMarkdownBlocks(container, text, onRef) {
    parseMarkdownBlocks(text).forEach((block) => {
      if (block.type === 'heading') {
        const level = Math.min(4, Math.max(2, block.level + 1));
        const heading = element(`h${level}`, '');
        appendInlineText(heading, block.text, onRef);
        container.appendChild(heading);
      } else if (block.type === 'list') {
        const list = element('ul', '');
        block.items.forEach((item) => {
          const entry = element('li', '');
          appendInlineText(entry, item, onRef);
          list.appendChild(entry);
        });
        container.appendChild(list);
      } else {
        const paragraph = element('p', '');
        appendInlineText(paragraph, block.text, onRef);
        container.appendChild(paragraph);
      }
    });
  }

  async function copyText(text) {
    const value = String(text || '');
    if (!value) return false;
    if (globalObject.navigator?.clipboard?.writeText) {
      try {
        await globalObject.navigator.clipboard.writeText(value);
        return true;
      } catch (_) {
        // Fall through to the manual prompt.
      }
    }
    try {
      globalObject.prompt?.('Copy to clipboard:', value);
    } catch (_) {
      // Manual copy is best-effort without clipboard permission.
    }
    return false;
  }

  // ---------------------------------------------------------------------------
  // Presentation controller
  // ---------------------------------------------------------------------------

  function createPresentation() {
    const root = element('div', 'codemap-presentation');
    root.id = 'codemap-presentation';
    root.hidden = true;
    document.body.appendChild(root);
    return {
      root,
      active: false,
      artifact: null,
      snapshot: null,
      theme: preferredTheme(availableStorage()),
      view: 'traces',
      selectedStep: '',
      activeFile: '',
      stepsByLabel: new Map(),
      stepsByFile: new Map(),
      nodesById: new Map(),
      refHandlers: new Map(),
      fileCache: new Map(),
      viewer: null,
      viewerReady: null,
      diagramState: { scale: 1, x: 0, y: 0 },
      els: {},
      fileLoadToken: 0,
    };
  }

  function schedulePresentationUpdate() {
    if (scheduled) return;
    scheduled = true;
    const schedule = typeof requestAnimationFrame === 'function' ? requestAnimationFrame : setTimeout;
    schedule(() => {
      scheduled = false;
      updateFromStore();
    });
  }

  function storeSnapshot() {
    const source = document.getElementById('codemap-result');
    return source ? source.__codemapSnapshot || null : null;
  }

  function updateFromStore() {
    const snapshot = storeSnapshot();
    if (!presentation) presentation = createPresentation();
    const shouldShow = isCodemapRoute(globalObject.location?.pathname) || presentation.active;
    if (!shouldShow) return;
    enterPresentation(snapshot);
  }

  function enterPresentation(snapshot = storeSnapshot()) {
    if (!presentation) presentation = createPresentation();
    presentation.active = true;
    presentation.snapshot = snapshot;
    document.body.classList.add(PRESENTATION_CLASS);
    presentation.root.hidden = false;
    applyTheme(presentation.theme);
    const artifact = snapshot?.artifact || null;
    if (!artifact) {
      renderPlaceholder(snapshot);
      return;
    }
    if (presentation.artifact !== artifact) {
      presentation.artifact = artifact;
      buildLayout(artifact);
      const hashStep = parseCanonicalHash(globalObject.location?.hash);
      const initial = hashStep && presentation.stepsByLabel.has(hashStep)
        ? hashStep
        : presentation.stepsByLabel.keys().next().value || '';
      if (initial) selectStep(initial, { updateHash: false, scrollSidebar: false });
    }
  }

  function exitPresentation() {
    if (!presentation) return;
    presentation.active = false;
    presentation.root.hidden = true;
    document.body.classList.remove(PRESENTATION_CLASS);
  }

  function applyTheme(theme) {
    if (!presentation) return;
    presentation.theme = theme === 'light' ? 'light' : 'dark';
    presentation.root.dataset.theme = presentation.theme;
    document.body.classList.toggle('codeatlas-codemap-light', presentation.theme === 'light');
    presentation.viewer?.setTheme(presentation.theme);
    presentation.root.querySelectorAll('[data-theme-toggle]').forEach((control) => {
      control.setAttribute('aria-pressed', presentation.theme === 'light' ? 'true' : 'false');
      control.title = presentation.theme === 'light' ? 'Switch to dark theme' : 'Switch to light theme';
      control.setAttribute('aria-label', control.title);
    });
  }

  function renderPlaceholder(snapshot) {
    const root = presentation.root;
    root.replaceChildren();
    presentation.artifact = null;
    const shell = element('div', 'codemap-presentation-placeholder');
    const status = snapshot?.status || 'empty';
    const message = snapshot?.error
      ? String(snapshot.error)
      : status === 'generating' || status === 'pending'
        ? 'Loading codemap…'
        : 'This codemap artifact is not available.';
    shell.appendChild(element('p', '', message));
    root.appendChild(shell);
  }

  // ---------------------------------------------------------------------------
  // Layout
  // ---------------------------------------------------------------------------

  function buildLayout(artifact) {
    const root = presentation.root;
    root.replaceChildren();
    presentation.selectedStep = '';
    presentation.activeFile = '';
    presentation.view = 'traces';
    presentation.stepsByLabel = new Map();
    presentation.stepsByFile = new Map();
    presentation.refHandlers = new Map();
    presentation.fileLoadToken += 1;

    const flows = ensureStepLabels((artifact.flows || []).map((flow) => ({
      ...flow,
      steps: (flow.steps || []).map((step) => ({ ...step, path: String(step.path || '').replaceAll('\\', '/') })),
    })));
    presentation.flows = flows;
    presentation.nodesById = new Map((artifact.nodes || []).map((node) => [node.id, node]));
    flows.forEach((flow) => {
      (flow.steps || []).forEach((step) => {
        presentation.stepsByLabel.set(step.label, step);
        if (step.path) {
          if (!presentation.stepsByFile.has(step.path)) presentation.stepsByFile.set(step.path, []);
          presentation.stepsByFile.get(step.path).push(step);
        }
        presentation.refHandlers.set(step.label, () => selectStep(step.label));
      });
    });
    presentation.files = fileTabsForFlows(flows);

    root.append(
      buildHeader(artifact),
      buildBody(artifact),
    );
    applyTheme(presentation.theme);
    renderTracesView(artifact);
    if (presentation.files.length) void openFile(presentation.files[0], { fromStep: false });
    else renderCodePaneEmpty('This codemap has no grounded source files.');
  }

  function buildHeader(artifact) {
    const header = element('header', 'codemap-presentation-header');
    const title = element('span', 'codemap-presentation-title', artifact.title || 'Codemap');
    title.title = artifact.query || artifact.title || '';
    const actions = element('div', 'codemap-presentation-header-actions');

    const copyLink = button('codemap-presentation-icon-button', async () => {
      await copyText(globalObject.location?.href || '');
    }, { title: 'Copy link', 'aria-label': 'Copy link' });
    copyLink.appendChild(svgIcon(ICONS.copy));

    const theme = button('codemap-presentation-icon-button', () => {
      const next = presentation.theme === 'light' ? 'dark' : 'light';
      applyTheme(next);
      rememberTheme(next, availableStorage());
    }, { 'data-theme-toggle': 'true', title: 'Theme', 'aria-label': 'Theme' });
    theme.appendChild(svgIcon(ICONS.theme));

    const workspace = button('codemap-presentation-workspace-button', () => {
      exitPresentation();
    }, { title: 'Open this codemap inside the CodeAtlas workspace' });
    workspace.textContent = 'Open in workspace';

    actions.append(copyLink, theme, workspace);
    header.append(title, actions);
    return header;
  }

  function buildBody(artifact) {
    const body = element('div', 'codemap-presentation-body');
    const sidebar = element('div', 'codemap-presentation-sidebar');
    const storedWidth = preferredSidebarWidth(availableStorage());
    if (storedWidth) sidebar.style.width = `${storedWidth}px`;
    const resizer = buildResizer(sidebar);
    const codePane = element('div', 'codemap-presentation-code-pane');

    sidebar.append(buildSidebarToolbar(), element('div', 'codemap-presentation-sidebar-content'));
    codePane.append(
      element('div', 'codemap-presentation-tabs'),
      buildBreadcrumbBar(),
      element('div', 'codemap-presentation-editor-host'),
      element('div', 'codemap-presentation-editor-notice'),
    );
    body.append(sidebar, resizer, codePane);

    presentation.els = {
      sidebar,
      sidebarContent: sidebar.querySelector('.codemap-presentation-sidebar-content'),
      tabs: codePane.querySelector('.codemap-presentation-tabs'),
      breadcrumb: codePane.querySelector('.codemap-presentation-breadcrumb'),
      editorHost: codePane.querySelector('.codemap-presentation-editor-host'),
      editorNotice: codePane.querySelector('.codemap-presentation-editor-notice'),
    };
    presentation.els.editorNotice.hidden = true;
    return body;
  }

  function buildSidebarToolbar() {
    const toolbar = element('div', 'codemap-presentation-sidebar-toolbar');
    const group = element('div', 'codemap-presentation-view-switch');
    group.setAttribute('role', 'group');
    group.setAttribute('aria-label', 'Sidebar view');
    const traces = button('codemap-presentation-view-button active', () => setSidebarView('traces'), { 'data-view': 'traces', 'aria-pressed': 'true' });
    traces.textContent = 'Traces';
    const diagram = button('codemap-presentation-view-button', () => setSidebarView('diagram'), { 'data-view': 'diagram', 'aria-pressed': 'false' });
    diagram.textContent = 'Diagram';
    group.append(traces, diagram);
    toolbar.appendChild(group);
    return toolbar;
  }

  function buildBreadcrumbBar() {
    const bar = element('div', 'codemap-presentation-breadcrumb');
    bar.setAttribute('aria-label', 'Active file path');
    return bar;
  }

  function buildResizer(sidebar) {
    const resizer = element('div', 'codemap-presentation-resizer');
    resizer.setAttribute('role', 'separator');
    resizer.setAttribute('aria-orientation', 'vertical');
    resizer.setAttribute('aria-label', 'Resize sidebar');
    resizer.addEventListener('pointerdown', (event) => {
      event.preventDefault();
      resizer.setPointerCapture(event.pointerId);
      const startX = event.clientX;
      const startWidth = sidebar.getBoundingClientRect().width;
      const onMove = (moveEvent) => {
        const width = Math.min(Math.max(startWidth + (moveEvent.clientX - startX), 280), Math.max(320, globalObject.innerWidth * 0.6));
        sidebar.style.width = `${width}px`;
        presentation.viewer?.layout();
      };
      const onUp = () => {
        resizer.removeEventListener('pointermove', onMove);
        resizer.removeEventListener('pointerup', onUp);
        rememberSidebarWidth(sidebar.getBoundingClientRect().width, availableStorage());
      };
      resizer.addEventListener('pointermove', onMove);
      resizer.addEventListener('pointerup', onUp);
    });
    return resizer;
  }

  function setSidebarView(view) {
    presentation.view = view === 'diagram' ? 'diagram' : 'traces';
    presentation.root.querySelectorAll('.codemap-presentation-view-button').forEach((control) => {
      const active = control.dataset.view === presentation.view;
      control.setAttribute('aria-pressed', active ? 'true' : 'false');
      control.classList.toggle('active', active);
    });
    if (presentation.view === 'diagram') renderDiagramView(presentation.artifact);
    else renderTracesView(presentation.artifact);
  }

  // ---------------------------------------------------------------------------
  // Sidebar: traces view
  // ---------------------------------------------------------------------------

  function renderTracesView(artifact) {
    const container = presentation.els.sidebarContent;
    container.className = 'codemap-presentation-sidebar-content codemap-presentation-traces';
    container.replaceChildren();

    container.appendChild(element('div', 'codemap-presentation-map-title', artifact.title || 'Codemap'));

    const prose = element('div', 'codemap-presentation-prose');
    const summaryText = String(artifact.summary || artifact.overview || '').trim();
    if (summaryText) {
      const paragraph = element('p', '');
      appendInlineText(paragraph, summaryText, presentation.refHandlers);
      prose.appendChild(paragraph);
    }
    container.appendChild(prose);

    const mapGuide = buildGuidePanel(artifact.motivation, artifact.details);
    if (mapGuide) {
      const guideRow = element('div', 'codemap-presentation-map-guide');
      const toggle = button('codemap-presentation-see-guide', () => {
        const open = guideRow.classList.toggle('open');
        toggle.textContent = open ? 'Hide guide' : 'See guide';
        toggle.setAttribute('aria-expanded', open ? 'true' : 'false');
      }, { 'aria-expanded': 'false' });
      toggle.textContent = 'See guide';
      guideRow.append(toggle, mapGuide);
      container.appendChild(guideRow);
    }

    const sections = element('div', 'codemap-presentation-sections');
    (presentation.flows || []).forEach((flow, index) => {
      sections.appendChild(buildSection(flow, index));
    });
    container.appendChild(sections);
  }

  function buildGuidePanel(motivation, details, extraBlocks = []) {
    const motivationText = String(motivation || '').trim();
    const detailsText = String(details || '').trim();
    if (!motivationText && !detailsText && !extraBlocks.length) return null;
    const panel = element('div', 'codemap-presentation-guide');
    const kicker = element('div', 'codemap-presentation-guide-kicker');
    kicker.appendChild(svgIcon(ICONS.sparkle, 12));
    kicker.appendChild(document.createTextNode('AI generated guide'));
    panel.appendChild(kicker);
    if (motivationText) {
      panel.appendChild(element('h2', '', 'Motivation'));
      renderMarkdownBlocks(panel, motivationText, presentation.refHandlers);
    }
    if (detailsText) {
      panel.appendChild(element('h2', '', 'Details'));
      renderMarkdownBlocks(panel, detailsText, presentation.refHandlers);
    }
    return panel;
  }

  function buildSection(flow, index) {
    const section = element('div', 'codemap-presentation-section');
    const number = sectionNumberForFlow(flow, index);
    section.dataset.sectionNumber = String(number);

    const guide = buildGuidePanel(flow.motivation, flow.details);
    const summaryText = deriveSectionSummary(flow);

    const header = button('codemap-presentation-section-header', () => {
      const collapsed = section.classList.toggle('collapsed');
      header.setAttribute('aria-expanded', collapsed ? 'false' : 'true');
    }, { 'aria-expanded': 'true' });
    header.appendChild(element('span', 'codemap-presentation-section-number', String(number)));
    const heading = element('div', 'codemap-presentation-section-heading');
    heading.appendChild(element('span', 'codemap-presentation-section-title', flow.title || `Section ${number}`));
    const summary = element('p', 'codemap-presentation-section-summary');
    appendInlineText(summary, summaryText, presentation.refHandlers);
    if (guide) {
      summary.appendChild(document.createTextNode(' '));
      const seeGuide = element('a', 'codemap-presentation-see-guide-link', 'See guide');
      seeGuide.href = '#';
      seeGuide.addEventListener('click', (event) => {
        event.preventDefault();
        event.stopPropagation();
        const open = section.classList.toggle('guide-open');
        seeGuide.textContent = open ? 'Hide guide' : 'See guide';
      });
      summary.appendChild(seeGuide);
    }
    heading.appendChild(summary);
    header.appendChild(heading);
    const chevron = element('span', 'codemap-presentation-section-chevron');
    chevron.appendChild(svgIcon(ICONS.chevron, 14));
    header.appendChild(chevron);
    section.appendChild(header);

    if (guide) {
      const guideWrap = element('div', 'codemap-presentation-section-guide');
      // Unpadded middle wrapper: the padded guide cannot shrink below its
      // padding, which would leak a sliver through the collapsed 0fr row.
      const guideInner = element('div', 'codemap-presentation-section-guide-inner');
      guideInner.appendChild(guide);
      guideWrap.appendChild(guideInner);
      section.appendChild(guideWrap);
    }

    const treeWrap = element('div', 'codemap-presentation-section-tree');
    const tree = element('div', 'codemap-presentation-tree');
    renderTreeLevel(tree, buildStepTree(flow.steps), '');
    treeWrap.appendChild(tree);
    section.appendChild(treeWrap);
    return section;
  }

  function renderTreeLevel(container, nodes, parentSymbol) {
    groupTreeSiblings(nodes, presentation.nodesById).forEach((run) => {
      if (run.symbol && run.symbol !== parentSymbol) {
        const labelItem = element('div', 'codemap-presentation-tree-item');
        labelItem.appendChild(element('div', 'codemap-presentation-tree-label', run.symbol));
        container.appendChild(labelItem);
      }
      run.nodes.forEach((node) => {
        const item = element('div', 'codemap-presentation-tree-item');
        item.appendChild(buildStepCard(node.step));
        const notes = Array.isArray(node.step.notes) ? node.step.notes : [];
        if (notes.length || node.children.length) {
          const childWrap = element('div', 'codemap-presentation-tree-children');
          notes.forEach((note) => {
            const noteItem = element('div', 'codemap-presentation-tree-item');
            noteItem.appendChild(element('div', 'codemap-presentation-tree-label note', note));
            childWrap.appendChild(noteItem);
          });
          renderTreeLevel(childWrap, node.children, run.symbol || parentSymbol);
          item.appendChild(childWrap);
        }
        container.appendChild(item);
      });
    });
  }

  function buildStepCard(step) {
    const card = button('codemap-presentation-step', () => selectStep(step.label), {
      'data-step-id': step.label,
      'data-active': 'false',
      'aria-label': `Step ${step.label}: ${step.text || ''}${step.path ? `, ${step.path}:${step.line}` : ''}`,
    });
    card.appendChild(element('span', 'codemap-presentation-step-chip', step.label));
    const bodyEl = element('div', 'codemap-presentation-step-body');
    const row = element('div', 'codemap-presentation-step-row');
    row.appendChild(element('span', 'codemap-presentation-step-title', step.text || step.label));
    row.appendChild(element('span', 'codemap-presentation-step-location', step.path ? `${fileName(step.path)}:${step.line}` : 'external'));
    bodyEl.appendChild(row);
    if (step.snippet) bodyEl.appendChild(element('div', 'codemap-presentation-step-snippet', step.snippet));
    card.appendChild(bodyEl);
    return card;
  }

  // ---------------------------------------------------------------------------
  // Sidebar: diagram view
  // ---------------------------------------------------------------------------

  function renderDiagramView(artifact) {
    const container = presentation.els.sidebarContent;
    container.className = 'codemap-presentation-sidebar-content codemap-presentation-diagram';
    container.replaceChildren();
    const stage = element('div', 'codemap-presentation-diagram-stage');
    const world = element('div', 'codemap-presentation-diagram-world');
    stage.appendChild(world);
    container.appendChild(stage);

    const source = artifact.diagram?.source || '';
    if (!source) {
      world.appendChild(element('p', 'codemap-presentation-diagram-empty', 'No deterministic diagram is available for this codemap.'));
      return;
    }
    void import('./src/mermaid-lite.ts').then(({ renderMermaidSubset }) => {
      let svgText = '';
      try {
        svgText = renderMermaidSubset(source, 'codemap-presentation-diagram');
      } catch (_) {
        world.appendChild(element('p', 'codemap-presentation-diagram-empty', 'The diagram for this codemap could not be rendered.'));
        return;
      }
      const parsed = new DOMParser().parseFromString(svgText, 'image/svg+xml');
      const svg = parsed.documentElement;
      if (!svg || svg.nodeName.toLowerCase() !== 'svg' || parsed.querySelector('parsererror')) return;
      svg.querySelectorAll('script, foreignObject').forEach((node) => node.remove());
      world.appendChild(document.importNode(svg, true));
      const tones = ['#fcc2d7', '#a5d8ff', '#b2f2bb', '#ffec99', '#d0bfff', '#ffd8a8'];
      world.querySelectorAll('g.cluster').forEach((cluster, index) => {
        cluster.classList.add(`codemap-presentation-cluster-tone-${index % tones.length}`);
        const rect = cluster.querySelector('rect');
        if (rect) rect.style.fill = tones[index % tones.length];
      });
      bindDiagramSources(world, artifact);
      initializeDiagramInteractions(stage, world);
    });
  }

  function bindDiagramSources(world, artifact) {
    const sources = new Map((artifact.diagram?.sources || []).map((entry) => [entry.nodeId, entry]));
    world.querySelectorAll('g.node[data-node-id]').forEach((node) => {
      const entry = sources.get(node.dataset.nodeId);
      if (!entry || !entry.path) return;
      node.classList.add('has-source');
      node.addEventListener('click', () => {
        void openFile(String(entry.path).replaceAll('\\', '/'), { fromStep: false, revealLine: entry.range?.start?.line || 0 });
      });
    });
  }

  function initializeDiagramInteractions(stage, world) {
    const state = presentation.diagramState;
    state.scale = 1;
    state.x = 0;
    state.y = 0;
    const apply = () => {
      world.style.transform = `scale(${state.scale}) translate(${state.x}px, ${state.y}px)`;
    };
    apply();
    stage.addEventListener('wheel', (event) => {
      event.preventDefault();
      const factor = event.deltaY < 0 ? 1.11 : 1 / 1.11;
      state.scale = Math.min(3, Math.max(0.2, state.scale * factor));
      apply();
    }, { passive: false });
    stage.addEventListener('pointerdown', (event) => {
      if (event.target.closest('g.node')) return;
      stage.setPointerCapture(event.pointerId);
      stage.classList.add('is-panning');
      const startX = event.clientX;
      const startY = event.clientY;
      const originX = state.x;
      const originY = state.y;
      const onMove = (moveEvent) => {
        state.x = originX + (moveEvent.clientX - startX) / state.scale;
        state.y = originY + (moveEvent.clientY - startY) / state.scale;
        apply();
      };
      const onUp = () => {
        stage.classList.remove('is-panning');
        stage.removeEventListener('pointermove', onMove);
        stage.removeEventListener('pointerup', onUp);
      };
      stage.addEventListener('pointermove', onMove);
      stage.addEventListener('pointerup', onUp);
    });
    stage.addEventListener('dblclick', () => {
      state.scale = 1;
      state.x = 0;
      state.y = 0;
      apply();
    });
  }

  // ---------------------------------------------------------------------------
  // Code pane
  // ---------------------------------------------------------------------------

  async function ensureViewer() {
    if (presentation.viewer) return presentation.viewer;
    if (!presentation.viewerReady) {
      presentation.viewerReady = import('./src/codemap-presentation-editor.ts').then(({ createCodemapCodeViewer }) => {
        const viewer = createCodemapCodeViewer();
        viewer.mount(presentation.els.editorHost, { theme: presentation.theme });
        viewer.setTheme(presentation.theme);
        viewer.setGlyphClickListener((line) => {
          const marker = viewer.markerAtLine(line);
          if (marker) selectStep(marker.id, { openFile: false });
        });
        presentation.viewer = viewer;
        return viewer;
      });
    }
    return presentation.viewerReady;
  }

  function renderCodePaneEmpty(message) {
    const notice = presentation.els.editorNotice;
    notice.hidden = false;
    notice.textContent = message;
    presentation.els.tabs.replaceChildren();
    presentation.els.breadcrumb.replaceChildren();
  }

  function renderFileTabs() {
    const tabs = presentation.els.tabs;
    tabs.replaceChildren();
    (presentation.files || []).forEach((path) => {
      const tab = button('codemap-presentation-tab', () => { void openFile(path, { fromStep: false }); }, {
        'data-path': path,
        'data-active': path === presentation.activeFile ? 'true' : 'false',
        title: path,
      });
      tab.textContent = fileName(path);
      tabs.appendChild(tab);
    });
  }

  function renderBreadcrumb(path) {
    const bar = presentation.els.breadcrumb;
    bar.replaceChildren();
    const segments = breadcrumbSegments(path);
    segments.forEach((segment, index) => {
      if (index > 0) {
        const divider = element('span', 'codemap-presentation-breadcrumb-divider');
        divider.appendChild(svgIcon(ICONS.crumb, 12));
        bar.appendChild(divider);
      }
      bar.appendChild(element('span', 'codemap-presentation-breadcrumb-segment', segment));
    });
  }

  async function fetchFileContent(path) {
    if (presentation.fileCache.has(path)) return presentation.fileCache.get(path);
    const request = fetch(`/api/file?path=${encodeURIComponent(path)}`, { headers: { Accept: 'application/json' } })
      .then(async (response) => {
        if (!response.ok) throw new Error(`file request failed: ${response.status}`);
        const payload = await response.json();
        if (typeof payload?.content !== 'string') throw new Error('file response missing content');
        return payload.content;
      });
    presentation.fileCache.set(path, request);
    request.catch(() => presentation.fileCache.delete(path));
    return request;
  }

  async function openFile(path, options = {}) {
    if (!path) return;
    const token = ++presentation.fileLoadToken;
    presentation.activeFile = path;
    renderFileTabs();
    renderBreadcrumb(path);
    const notice = presentation.els.editorNotice;
    try {
      const [viewer, content] = await Promise.all([ensureViewer(), fetchFileContent(path)]);
      if (token !== presentation.fileLoadToken) return;
      notice.hidden = true;
      await viewer.showFile({ path, content });
      if (token !== presentation.fileLoadToken) return;
      const markers = (presentation.stepsByFile.get(path) || []).map((step) => ({
        id: step.label,
        line: Number(step.line) || 0,
        text: step.text || '',
      }));
      viewer.setMarkers(markers);
      const selected = presentation.stepsByLabel.get(presentation.selectedStep);
      if (selected && selected.path === path) {
        viewer.selectMarker({ id: selected.label, line: Number(selected.line) || 0, text: selected.text || '' });
      } else if (options.revealLine) {
        viewer.selectMarker({ id: '', line: Number(options.revealLine) || 1, text: '' });
      } else {
        viewer.selectMarker(null);
      }
    } catch (_) {
      if (token !== presentation.fileLoadToken) return;
      notice.hidden = false;
      notice.textContent = `The file ${path} is not available in the current working tree. The codemap snapshot may be older than the workspace.`;
    }
  }

  // ---------------------------------------------------------------------------
  // Step selection
  // ---------------------------------------------------------------------------

  function selectStep(label, options = {}) {
    const normalized = String(label || '').toLowerCase();
    const step = presentation.stepsByLabel.get(normalized);
    if (!step) return false;
    presentation.selectedStep = normalized;
    presentation.root.querySelectorAll('[data-step-id]').forEach((control) => {
      const active = control.dataset.stepId === normalized;
      control.dataset.active = active ? 'true' : 'false';
      if (active) control.setAttribute('aria-current', 'step');
      else control.removeAttribute('aria-current');
    });
    const card = presentation.root.querySelector(`[data-step-id="${normalized}"]`);
    if (card && options.scrollSidebar !== false) {
      const section = card.closest('.codemap-presentation-section');
      section?.classList.remove('collapsed');
      card.scrollIntoView({ behavior: reducedMotion() ? 'auto' : 'smooth', block: 'nearest' });
    }
    if (options.updateHash !== false && globalObject.history && globalObject.location) {
      const url = new URL(globalObject.location.href);
      url.hash = normalized;
      globalObject.history.replaceState(globalObject.history.state, '', `${url.pathname}${url.search}#${normalized}`);
    }
    if (step.path && options.openFile !== false) {
      void openFile(step.path, { fromStep: true });
    } else if (step.path && presentation.viewer && presentation.activeFile === step.path) {
      presentation.viewer.selectMarker({ id: step.label, line: Number(step.line) || 0, text: step.text || '' });
    }
    return true;
  }

  function reducedMotion() {
    return Boolean(globalObject.matchMedia?.('(prefers-reduced-motion: reduce)').matches);
  }

  // ---------------------------------------------------------------------------
  // Boot
  // ---------------------------------------------------------------------------

  function boot() {
    const source = document.getElementById('codemap-result');
    if (!source || observer) return;
    observer = new MutationObserver(schedulePresentationUpdate);
    observer.observe(source, { childList: true, subtree: false });
    document.addEventListener('keydown', (event) => {
      if (event.key === 'Escape' && presentation?.active) exitPresentation();
    });
    globalObject.addEventListener?.('hashchange', () => {
      const step = parseCanonicalHash(globalObject.location?.hash);
      if (step && presentation?.active) selectStep(step, { updateHash: false });
    });
    globalObject.CodeAtlasCodemapPresentation = {
      enter: () => enterPresentation(),
      exit: () => exitPresentation(),
      isActive: () => Boolean(presentation?.active),
    };
    schedulePresentationUpdate();
  }

  const exported = {
    isCodemapRoute,
    alphabeticSuffix,
    parseCanonicalHash,
    parseStepRefs,
    sectionNumberForFlow,
    ensureStepLabels,
    fileTabsForFlows,
    breadcrumbSegments,
    fileName,
    deriveSectionSummary,
    buildStepTree,
    groupTreeSiblings,
    parseMarkdownBlocks,
    preferredTheme,
    copyText,
  };

  if (typeof module !== 'undefined' && module.exports) module.exports = exported;
  if (typeof document !== 'undefined') {
    if (document.readyState === 'loading') document.addEventListener('DOMContentLoaded', boot, { once: true });
    else boot();
  }
}(typeof globalThis !== 'undefined' ? globalThis : this));
