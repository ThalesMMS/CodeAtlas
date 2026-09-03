'use strict';

// DeepWiki presentation layer. Renders the generated wiki as a full-page
// documentation site at /wiki (and /wiki/:slug): a page-tree sidebar on the
// left, a centered prose column, and a minimized "on this page" rail on the
// right — mirroring the Devin DeepWiki reference interface.
//
// Data ownership: pages arrive fully rendered from the backend through the
// workspace (globalThis.CodeAtlasWorkspaceBridge). This module adds no facts;
// it lays the same markdown out as a reading surface.

(function deepwikiPresentationModule(globalObject) {
  const PRESENTATION_CLASS = 'codeatlas-deepwiki-presentation';
  const THEME_STORAGE_KEY = 'codeatlas.codemap.report-theme.v1';

  let presentation = null;

  // ---------------------------------------------------------------------------
  // Pure helpers (exported for tests)
  // ---------------------------------------------------------------------------

  function parseWikiRoute(pathname) {
    const match = String(pathname || '').match(/^\/wiki(?:\/([A-Za-z0-9][A-Za-z0-9.-]*))?\/?$/);
    if (!match) return null;
    return { slug: match[1] ? match[1].toLowerCase() : '' };
  }

  function wikiRouteFor(slug) {
    return slug ? `/wiki/${encodeURIComponent(slug)}` : '/wiki';
  }

  // Builds the sidebar hierarchy from parentSlug references, preserving the
  // backend's page order. A page with an unknown parent degrades to a root.
  function buildPageTree(pages) {
    const nodes = new Map();
    const roots = [];
    (pages || []).forEach((page) => {
      nodes.set(page.slug, { page, children: [] });
    });
    (pages || []).forEach((page) => {
      const node = nodes.get(page.slug);
      const parent = page.parentSlug ? nodes.get(page.parentSlug) : null;
      if (parent && parent !== node) parent.children.push(node);
      else roots.push(node);
    });
    return roots;
  }

  // Filters the tree by a case-insensitive title query, keeping ancestors of
  // any matching page so the hierarchy stays readable.
  function filterPageTree(tree, query) {
    const normalized = String(query || '').trim().toLowerCase();
    if (!normalized) return tree;
    const filterNodes = (nodes) => nodes
      .map((node) => {
        const children = filterNodes(node.children);
        const matches = String(node.page.title || '').toLowerCase().includes(normalized)
          || String(node.page.slug || '').toLowerCase().includes(normalized);
        if (!matches && !children.length) return null;
        return { page: node.page, children };
      })
      .filter(Boolean);
    return filterNodes(tree);
  }

  function formatRelativeTime(iso, now = Date.now()) {
    const time = new Date(iso).getTime();
    if (!Number.isFinite(time)) return '';
    const seconds = Math.max(0, Math.floor((now - time) / 1000));
    if (seconds < 60) return 'just now';
    const units = [
      [60 * 60 * 24 * 365, 'year'],
      [60 * 60 * 24 * 30, 'month'],
      [60 * 60 * 24 * 7, 'week'],
      [60 * 60 * 24, 'day'],
      [60 * 60, 'hour'],
      [60, 'minute'],
    ];
    for (const [size, name] of units) {
      if (seconds >= size) {
        const count = Math.floor(seconds / size);
        return `${count} ${name}${count === 1 ? '' : 's'} ago`;
      }
    }
    return 'just now';
  }

  function lastUpdatedAt(model) {
    const times = (model?.pages || [])
      .map((page) => new Date(page.updatedAt || 0).getTime())
      .filter((time) => Number.isFinite(time) && time > 0);
    const artifactTime = new Date(model?.artifact?.createdAt || 0).getTime();
    if (Number.isFinite(artifactTime) && artifactTime > 0) times.push(artifactTime);
    if (!times.length) return '';
    return new Date(Math.max(...times)).toISOString();
  }

  function statusLabel(status) {
    switch (status) {
      case 'ready': return '';
      case 'generating': return 'Generating…';
      case 'stale': return 'Out of date';
      case 'failed': return 'Generation failed';
      case 'not_generated': return 'Not generated yet';
      default: return String(status || '');
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
      // Presentation preferences must never block the wiki.
    }
  }

  function availableStorage() {
    try {
      return globalObject.localStorage || null;
    } catch (_) {
      return null;
    }
  }

  // ---------------------------------------------------------------------------
  // DOM helpers
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
    copy: 'M5 2h7a1 1 0 0 1 1 1v8h-1.3V3.3H5V2Zm-2 3h7a1 1 0 0 1 1 1v7a1 1 0 0 1-1 1H3a1 1 0 0 1-1-1V6a1 1 0 0 1 1-1Zm.3 1.3v6.4h6.4V6.3H3.3Z',
    theme: 'M8 1.5a6.5 6.5 0 1 0 0 13V1.5Z',
    refresh: 'M8 2.5A5.5 5.5 0 1 0 13.5 8h-1.3A4.2 4.2 0 1 1 8 3.8V6l3.2-2.4L8 1.2v1.3Z',
  };

  function bridge() {
    return globalObject.CodeAtlasWorkspaceBridge || null;
  }

  // ---------------------------------------------------------------------------
  // Presentation lifecycle
  // ---------------------------------------------------------------------------

  function createPresentation() {
    const root = element('div', 'deepwiki-presentation');
    root.id = 'deepwiki-presentation';
    root.hidden = true;
    document.body.appendChild(root);
    return {
      root,
      active: false,
      model: null,
      activeSlug: '',
      searchQuery: '',
      theme: preferredTheme(availableStorage()),
      headingObserver: null,
      els: {},
    };
  }

  function enterPresentation() {
    if (!presentation) presentation = createPresentation();
    presentation.active = true;
    document.body.classList.add(PRESENTATION_CLASS);
    presentation.root.hidden = false;
    applyTheme(presentation.theme);
    render();
  }

  function exitPresentation() {
    if (!presentation) return;
    presentation.active = false;
    presentation.root.hidden = true;
    document.body.classList.remove(PRESENTATION_CLASS);
  }

  function updateModel(model) {
    if (!presentation) presentation = createPresentation();
    presentation.model = model || null;
    if (presentation.active) render();
  }

  function applyTheme(theme) {
    if (!presentation) return;
    presentation.theme = theme === 'light' ? 'light' : 'dark';
    presentation.root.dataset.theme = presentation.theme;
    presentation.root.querySelectorAll('[data-theme-toggle]').forEach((control) => {
      control.setAttribute('aria-pressed', presentation.theme === 'light' ? 'true' : 'false');
      control.title = presentation.theme === 'light' ? 'Switch to dark theme' : 'Switch to light theme';
      control.setAttribute('aria-label', control.title);
    });
  }

  // ---------------------------------------------------------------------------
  // Rendering
  // ---------------------------------------------------------------------------

  function render() {
    const root = presentation.root;
    root.replaceChildren();
    const model = presentation.model;
    if (!model || !model.pages?.length) {
      const placeholder = element('div', 'deepwiki-presentation-placeholder');
      const message = model?.status === 'failed'
        ? `The last DeepWiki generation failed. ${model.lastError || ''}`.trim()
        : model?.status === 'generating'
          ? 'Generating DeepWiki…'
          : model?.status === 'not_generated'
            ? 'This workspace has no DeepWiki yet. Generate it from the workspace, or with the button below.'
            : 'Loading DeepWiki…';
      placeholder.appendChild(element('p', '', message));
      if (model && model.status !== 'generating') {
        const generate = button('deepwiki-presentation-refresh-button', () => bridge()?.refreshWiki(), {});
        generate.textContent = 'Generate DeepWiki';
        placeholder.appendChild(generate);
      }
      root.append(buildTopBar(model), placeholder);
      return;
    }
    if (!presentation.activeSlug || !model.pages.some((page) => page.slug === presentation.activeSlug)) {
      const routeSlug = parseWikiRoute(globalObject.location?.pathname)?.slug;
      presentation.activeSlug = routeSlug && model.pages.some((page) => page.slug === routeSlug)
        ? routeSlug
        : (model.pages.find((page) => page.slug === 'overview') || model.pages[0]).slug;
    }
    root.append(buildTopBar(model), buildBody(model));
    renderSidebar();
    renderPage(presentation.activeSlug, { updateHistory: false });
  }

  function buildTopBar(model) {
    const bar = element('header', 'deepwiki-presentation-topbar');
    const brand = element('div', 'deepwiki-presentation-brand');
    brand.appendChild(element('span', 'deepwiki-presentation-brand-name', 'DeepWiki'));
    brand.appendChild(element('span', 'deepwiki-presentation-brand-repo', model?.workspaceLabel || 'workspace'));
    const actions = element('div', 'deepwiki-presentation-topbar-actions');
    const updated = lastUpdatedAt(model);
    if (updated) actions.appendChild(element('span', 'deepwiki-presentation-updated', `Last updated ${formatRelativeTime(updated)}`));
    const status = statusLabel(model?.status);
    if (status) actions.appendChild(element('span', `deepwiki-presentation-status status-${model.status}`, status));

    const search = element('input', 'deepwiki-presentation-search');
    search.type = 'search';
    search.placeholder = 'Search';
    search.setAttribute('aria-label', 'Filter wiki pages');
    search.value = presentation.searchQuery;
    search.addEventListener('input', () => {
      presentation.searchQuery = search.value;
      renderSidebar();
    });
    actions.appendChild(search);

    const refresh = button('deepwiki-presentation-icon-button', () => bridge()?.refreshWiki(), { title: 'Regenerate DeepWiki', 'aria-label': 'Regenerate DeepWiki' });
    refresh.appendChild(svgIcon(ICONS.refresh));
    const copyLink = button('deepwiki-presentation-icon-button', async () => {
      try {
        await globalObject.navigator?.clipboard?.writeText(globalObject.location?.href || '');
      } catch (_) {
        globalObject.prompt?.('Copy to clipboard:', globalObject.location?.href || '');
      }
    }, { title: 'Copy link', 'aria-label': 'Copy link' });
    copyLink.appendChild(svgIcon(ICONS.copy));
    const theme = button('deepwiki-presentation-icon-button', () => {
      const next = presentation.theme === 'light' ? 'dark' : 'light';
      applyTheme(next);
      rememberTheme(next, availableStorage());
    }, { 'data-theme-toggle': 'true', title: 'Theme', 'aria-label': 'Theme' });
    theme.appendChild(svgIcon(ICONS.theme));
    const workspace = button('deepwiki-presentation-workspace-button', () => {
      exitPresentation();
      bridge()?.showWikiPage(presentation.activeSlug);
    }, { title: 'Open this wiki inside the CodeAtlas workspace' });
    workspace.textContent = 'Open in workspace';
    actions.append(refresh, copyLink, theme, workspace);
    bar.append(brand, actions);
    return bar;
  }

  function buildBody() {
    const body = element('div', 'deepwiki-presentation-body');
    const sidebar = element('nav', 'deepwiki-presentation-sidebar');
    sidebar.setAttribute('aria-label', 'Wiki pages');
    const content = element('div', 'deepwiki-presentation-content');
    const scroller = element('div', 'deepwiki-presentation-scroller');
    const article = element('article', 'deepwiki-presentation-article');
    const title = element('h1', 'deepwiki-presentation-page-title');
    const markdown = element('div', 'deepwiki-presentation-markdown markdown-content');
    article.append(title, markdown);
    scroller.appendChild(article);
    content.appendChild(scroller);
    const rail = element('aside', 'deepwiki-presentation-toc-rail');
    rail.setAttribute('aria-label', 'On this page');
    body.append(sidebar, content, rail);
    presentation.els = { sidebar, content, scroller, article, title, markdown, rail };

    markdown.addEventListener('click', (event) => {
      const wikiLink = event.target.closest('.wiki-reference[data-wiki-slug]');
      if (wikiLink) {
        event.preventDefault();
        renderPage(wikiLink.dataset.wikiSlug, { updateHistory: true });
        return;
      }
      const codeReference = event.target.closest('.code-reference[data-path]');
      if (codeReference) {
        event.preventDefault();
        exitPresentation();
        bridge()?.openSource(codeReference.dataset.path, codeReference.dataset.line, codeReference.dataset.endLine);
      }
    });
    return body;
  }

  function renderSidebar() {
    const sidebar = presentation.els.sidebar;
    if (!sidebar) return;
    sidebar.replaceChildren();
    const tree = filterPageTree(buildPageTree(presentation.model?.pages || []), presentation.searchQuery);
    const renderLevel = (nodes, depth) => {
      const list = element('ul', 'deepwiki-presentation-nav-list');
      nodes.forEach((node) => {
        const item = element('li', 'deepwiki-presentation-nav-item');
        const link = button('deepwiki-presentation-nav-link', () => renderPage(node.page.slug, { updateHistory: true }), {
          'data-wiki-nav-slug': node.page.slug,
          'data-active': node.page.slug === presentation.activeSlug ? 'true' : 'false',
        });
        link.style.paddingLeft = `${10 + depth * 14}px`;
        link.appendChild(element('span', 'deepwiki-presentation-nav-label', node.page.title || node.page.slug));
        item.appendChild(link);
        if (node.children.length) item.appendChild(renderLevel(node.children, depth + 1));
        list.appendChild(item);
      });
      return list;
    };
    sidebar.appendChild(renderLevel(tree, 0));
  }

  function renderPage(slug, options = {}) {
    const model = presentation.model;
    const page = model?.pages?.find((item) => item.slug === slug);
    if (!page) return false;
    presentation.activeSlug = slug;
    presentation.els.sidebar.querySelectorAll('[data-wiki-nav-slug]').forEach((link) => {
      const active = link.dataset.wikiNavSlug === slug;
      link.dataset.active = active ? 'true' : 'false';
      if (active) link.setAttribute('aria-current', 'page');
      else link.removeAttribute('aria-current');
    });
    presentation.els.title.textContent = page.title || slug;
    const renderer = bridge()?.renderMarkdownInto;
    if (renderer) renderer(presentation.els.markdown, page.markdown || '');
    else presentation.els.markdown.textContent = page.markdown || '';
    presentation.els.scroller.scrollTop = 0;
    if (options.updateHistory !== false && globalObject.history) {
      globalObject.history.pushState({ wikiSlug: slug }, '', wikiRouteFor(slug));
    }
    requestAnimationFrame(() => buildTableOfContents());
    return true;
  }

  // ---------------------------------------------------------------------------
  // "On this page" rail: minimized tick lines that expand to a heading card.
  // ---------------------------------------------------------------------------

  function buildTableOfContents() {
    const rail = presentation.els.rail;
    if (!rail) return;
    rail.replaceChildren();
    presentation.headingObserver?.disconnect();
    const headings = [...presentation.els.markdown.querySelectorAll('h2, h3')]
      .filter((heading) => heading.textContent.trim());
    if (!headings.length) return;
    headings.forEach((heading, index) => {
      if (!heading.id) heading.id = `wiki-section-${index}-${heading.textContent.trim().toLowerCase().replace(/[^a-z0-9]+/g, '-').replace(/^-+|-+$/g, '')}`;
    });
    const ticks = element('div', 'deepwiki-presentation-toc-ticks');
    const card = element('div', 'deepwiki-presentation-toc-card');
    card.appendChild(element('div', 'deepwiki-presentation-toc-heading', 'On this page'));
    const list = element('ul', '');
    headings.forEach((heading) => {
      ticks.appendChild(element('span', `deepwiki-presentation-toc-tick level-${heading.tagName.toLowerCase()}`));
      const item = element('li', '');
      const link = button(`deepwiki-presentation-toc-link level-${heading.tagName.toLowerCase()}`, () => {
        heading.scrollIntoView({ behavior: 'smooth', block: 'start' });
      }, { 'data-toc-target': heading.id });
      link.textContent = heading.textContent.trim();
      item.appendChild(link);
      list.appendChild(item);
    });
    card.appendChild(list);
    rail.append(ticks, card);

    if ('IntersectionObserver' in globalObject) {
      presentation.headingObserver = new IntersectionObserver((entries) => {
        const visible = entries.filter((entry) => entry.isIntersecting).sort((a, b) => b.intersectionRatio - a.intersectionRatio)[0];
        if (!visible) return;
        rail.querySelectorAll('[data-toc-target]').forEach((link) => {
          link.classList.toggle('active', link.dataset.tocTarget === visible.target.id);
        });
        const index = headings.indexOf(visible.target);
        [...ticks.children].forEach((tick, tickIndex) => tick.classList.toggle('active', tickIndex === index));
      }, { root: presentation.els.scroller, rootMargin: '-10% 0px -70% 0px', threshold: [0, 0.5] });
      headings.forEach((heading) => presentation.headingObserver.observe(heading));
    }
  }

  // ---------------------------------------------------------------------------
  // Boot
  // ---------------------------------------------------------------------------

  function boot() {
    globalObject.CodeAtlasDeepWikiPresentation = {
      enter: () => {
        if (!presentation?.model) {
          const model = bridge()?.wikiModel?.();
          if (model) updateModel(model);
        }
        enterPresentation();
      },
      exit: () => exitPresentation(),
      update: (model) => updateModel(model),
      isActive: () => Boolean(presentation?.active),
    };
    document.addEventListener('keydown', (event) => {
      if (event.key === 'Escape' && presentation?.active) exitPresentation();
    });
    globalObject.addEventListener?.('popstate', () => {
      const route = parseWikiRoute(globalObject.location?.pathname);
      if (!route) {
        if (presentation?.active) exitPresentation();
        return;
      }
      if (!presentation?.active) globalObject.CodeAtlasDeepWikiPresentation.enter();
      if (route.slug) renderPage(route.slug, { updateHistory: false });
    });
    const panelHeader = document.querySelector('#panel-deepwiki .panel-header');
    if (panelHeader && !panelHeader.querySelector('[data-deepwiki-full-page]')) {
      const fullPage = button('button secondary small', () => {
        globalObject.CodeAtlasDeepWikiPresentation.enter();
        if (globalObject.history && !parseWikiRoute(globalObject.location?.pathname)) {
          globalObject.history.pushState({ wikiSlug: '' }, '', wikiRouteFor(''));
        }
      }, { 'data-deepwiki-full-page': 'true' });
      fullPage.textContent = 'Full page';
      panelHeader.appendChild(fullPage);
    }
    if (parseWikiRoute(globalObject.location?.pathname)) {
      globalObject.CodeAtlasDeepWikiPresentation.enter();
    }
  }

  const exported = {
    parseWikiRoute,
    wikiRouteFor,
    buildPageTree,
    filterPageTree,
    formatRelativeTime,
    lastUpdatedAt,
    statusLabel,
    preferredTheme,
  };

  if (typeof module !== 'undefined' && module.exports) module.exports = exported;
  if (typeof document !== 'undefined') {
    if (document.readyState === 'loading') document.addEventListener('DOMContentLoaded', boot, { once: true });
    else boot();
  }
}(typeof globalThis !== 'undefined' ? globalThis : this));
