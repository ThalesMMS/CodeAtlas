import { generateCodemap, loadDeepWiki, loadJobCodemap, loadSource, refreshDeepWiki, waitForJob } from './knowledge-api';
import { escapeHTML } from './knowledge-links';
import {
  errorView, hydrateMarkdownDiagrams, loadingView, renderCodemap, renderCodemapWelcome,
  renderSidecar, renderWikiNav, renderWikiPage, workspaceTemplate,
} from './knowledge-view';
import type { ArtifactMetadata, Codemap, DeepWikiCollection, KnowledgeMode, OutlineItem, SourceReference } from './knowledge-types';

const DEFAULT_QUERY = 'How does a request move from the entry point through the main application layers?';
const STORAGE_KEY = 'codeatlas.knowledge-workspace.v2';

export function mountKnowledgeWorkspace(): void {
  if (document.querySelector('[data-action="open-explore"]')) return;
  const actions = document.querySelector('.topbar-actions');
  const button = document.createElement('button');
  button.type = 'button';
  button.className = 'button secondary knowledge-launch';
  button.dataset.action = 'open-explore';
  button.textContent = 'Explore';
  button.title = 'Open DeepWiki and Codemap (Command/Ctrl+Shift+E)';
  (actions ?? document.body).append(button);
  const controller = new KnowledgeWorkspace();
  button.addEventListener('click', () => controller.open());
  window.addEventListener('keydown', (event) => {
    if ((event.metaKey || event.ctrlKey) && event.shiftKey && event.key.toLowerCase() === 'e') {
      event.preventDefault();
      controller.toggle();
    }
  });
}

class KnowledgeWorkspace {
  private overlay: HTMLElement;
  private shell: HTMLElement;
  private wiki?: DeepWikiCollection;
  private codemap?: Codemap;
  private mode: KnowledgeMode = 'wiki';
  private side = 'sources';
  private selected = '';
  private query = DEFAULT_QUERY;
  private sources: SourceReference[] = [];
  private outline: OutlineItem[] = [];
  private artifact?: ArtifactMetadata;
  private abort?: AbortController;
  private sourceAbort?: AbortController;

  constructor() {
    const persisted = readState();
    this.mode = persisted.mode;
    this.selected = persisted.selected;
    this.query = persisted.query;
    this.overlay = document.createElement('div');
    this.overlay.className = 'knowledge-overlay hidden';
    this.overlay.innerHTML = workspaceTemplate();
    document.body.append(this.overlay);
    this.shell = this.overlay.querySelector('.knowledge-shell') as HTMLElement;
    this.shell.dataset.theme = persisted.theme;
    this.bind();
    this.switchMode(this.mode, false);
  }

  toggle(): void { this.overlay.classList.contains('hidden') ? this.open() : this.close(); }

  open(): void {
    this.overlay.classList.remove('hidden');
    document.body.classList.add('knowledge-open');
    if (!this.wiki) void this.loadWiki();
    this.persist();
  }

  private close(): void {
    this.overlay.classList.add('hidden');
    document.body.classList.remove('knowledge-open');
    this.abort?.abort();
    this.hideSource();
  }

  private bind(): void {
    this.overlay.addEventListener('click', (event) => {
      const target = (event.target as Element).closest<HTMLElement>('button, a');
      if (!target) return;
      const action = target.dataset.action;
      if (action === 'close' || action === 'home') return this.close();
      if (action === 'theme') return this.toggleTheme();
      if (action === 'refresh') return void this.refreshWiki();
      if (action === 'new-map') return this.showCodemapWelcome();
      if (action === 'close-source') return this.hideSource();
      if (action === 'nav') return this.shell.classList.toggle('show-nav');
      if (target.dataset.mode) return this.switchMode(target.dataset.mode as KnowledgeMode);
      if (target.dataset.side) return this.switchSide(target.dataset.side);
      if (target.dataset.wikiSlug || target.dataset.knowledgeWiki) {
        event.preventDefault();
        this.selectPage(target.dataset.wikiSlug || target.dataset.knowledgeWiki || '');
        return;
      }
      if (target.dataset.knowledgeSource) {
        event.preventDefault();
        void this.openSource(target.dataset.knowledgeSource, toInt(target.dataset.knowledgeStart), toInt(target.dataset.knowledgeEnd));
        return;
      }
      if (target.dataset.codemapExample) {
        this.query = target.dataset.codemapExample;
        const area = this.overlay.querySelector<HTMLTextAreaElement>('[data-codemap-form] textarea');
        if (area) area.value = this.query;
      }
    });
    this.overlay.addEventListener('input', (event) => {
      const input = event.target as HTMLInputElement;
      if (input.matches('[data-page-filter]') && this.wiki) this.pageTree().innerHTML = renderWikiNav(this.wiki, input.value, this.selected);
    });
    this.overlay.addEventListener('submit', (event) => {
      const form = (event.target as Element).closest<HTMLFormElement>('[data-codemap-form]');
      if (!form) return;
      event.preventDefault();
      const area = form.elements.namedItem('query') as HTMLTextAreaElement;
      this.query = area.value.trim();
      if (this.query) void this.runCodemap();
    });
    window.addEventListener('keydown', (event) => {
      if (event.key !== 'Escape' || this.overlay.classList.contains('hidden')) return;
      if (!this.sourceDrawer().classList.contains('hidden')) this.hideSource();
      else if (this.shell.classList.contains('show-nav')) this.shell.classList.remove('show-nav');
      else this.close();
    });
  }

  private async loadWiki(): Promise<void> {
    this.begin();
    this.wikiView().innerHTML = loadingView('Loading DeepWiki', 'Reading the current grounded documentation artifact.');
    try {
      this.wiki = await loadDeepWiki(this.abort?.signal);
      this.renderWiki();
    } catch (error) {
      if (!aborted(error)) this.wikiView().innerHTML = errorView('DeepWiki unavailable', message(error));
    } finally { this.finish(); }
  }

  private renderWiki(): void {
    if (!this.wiki) return;
    if (!this.wiki.pages.length) {
      this.pageTree().innerHTML = '';
      this.wikiView().innerHTML = `${errorView('Wiki not generated', this.wiki.lastError || 'Generate the first repository wiki from this workspace.')}<div class="knowledge-empty-action"><button class="knowledge-primary" data-action="refresh" type="button">Generate DeepWiki</button></div>`;
      this.sources = []; this.outline = []; this.artifact = this.wiki.artifact; this.renderSide();
      return;
    }
    if (!this.wiki.pages.some((page) => page.slug === this.selected)) this.selected = this.wiki.pages.find((page) => page.slug === 'overview')?.slug ?? this.wiki.pages[0]?.slug ?? '';
    this.pageTree().innerHTML = renderWikiNav(this.wiki, this.pageFilter().value, this.selected);
    const page = this.wiki.pages.find((candidate) => candidate.slug === this.selected) ?? this.wiki.pages[0];
    if (!page) return;
    const rendered = renderWikiPage(page, this.wiki);
    this.wikiView().innerHTML = rendered.html;
    hydrateMarkdownDiagrams(this.wikiView(), rendered.diagrams);
    this.sources = rendered.sources; this.outline = rendered.outline; this.artifact = rendered.artifact;
    this.overlay.querySelector<HTMLElement>('[data-snapshot]')!.textContent = `snapshot ${this.wiki.snapshotId.slice(0, 10) || '—'}`;
    this.renderSide(); this.persist();
  }

  private selectPage(slug: string): void {
    if (!this.wiki?.pages.some((page) => page.slug === slug)) return;
    this.selected = slug; this.shell.classList.remove('show-nav'); this.renderWiki();
    this.wikiView().scrollTo({ top: 0, behavior: 'smooth' });
  }

  private async refreshWiki(): Promise<void> {
    this.begin();
    try {
      const job = await refreshDeepWiki(this.abort?.signal);
      await waitForJob(job, this.abort?.signal, (current) => this.progress(current.message || current.stage, current.progress));
      await this.loadWiki();
    } catch (error) {
      if (!aborted(error)) this.wikiView().insertAdjacentHTML('afterbegin', `<div class="knowledge-alert">${escapeHTML(message(error))}</div>`);
    } finally { this.finish(); }
  }

  private async runCodemap(): Promise<void> {
    this.begin();
    this.codemapView().innerHTML = loadingView('Building Codemap', 'Retrieving evidence and expanding the factual symbol graph.');
    try {
      const job = await generateCodemap(this.query, this.abort?.signal);
      const completed = await waitForJob(job, this.abort?.signal, (current) => this.progress(current.message || current.stage, current.progress));
      this.codemap = await loadJobCodemap(completed, this.abort?.signal);
      const rendered = renderCodemap(this.codemap);
      this.codemapView().innerHTML = rendered.html;
      this.sources = rendered.sources; this.outline = rendered.outline; this.artifact = rendered.artifact;
      this.renderSide(); this.persist();
    } catch (error) {
      if (!aborted(error)) this.codemapView().innerHTML = errorView('Codemap generation failed', message(error));
    } finally { this.finish(); }
  }

  private showCodemapWelcome(): void { this.codemap = undefined; this.codemapView().innerHTML = renderCodemapWelcome(this.query); this.sources = []; this.outline = []; this.artifact = undefined; this.renderSide(); }

  private switchMode(mode: KnowledgeMode, persist = true): void {
    this.mode = mode === 'codemap' ? 'codemap' : 'wiki';
    this.overlay.querySelectorAll<HTMLElement>('[data-mode]').forEach((button) => button.classList.toggle('active', button.dataset.mode === this.mode));
    this.wikiView().classList.toggle('hidden', this.mode !== 'wiki');
    this.codemapView().classList.toggle('hidden', this.mode !== 'codemap');
    this.pageFilter().closest('label')?.classList.toggle('hidden', this.mode !== 'wiki');
    this.pageTree().classList.toggle('hidden', this.mode !== 'wiki');
    this.overlay.querySelector<HTMLElement>('[data-nav-title]')!.textContent = this.mode === 'wiki' ? 'Documentation' : 'Guided tour';
    if (this.mode === 'codemap' && !this.codemap) this.showCodemapWelcome();
    if (this.mode === 'wiki' && this.wiki) this.renderWiki();
    if (persist) this.persist();
  }

  private switchSide(side: string): void { this.side = ['sources', 'outline', 'artifact'].includes(side) ? side : 'sources'; this.renderSide(); }
  private renderSide(): void {
    this.overlay.querySelectorAll<HTMLElement>('[data-side]').forEach((button) => button.classList.toggle('active', button.dataset.side === this.side));
    this.overlay.querySelector<HTMLElement>('[data-side-content]')!.innerHTML = renderSidecar(this.side, this.sources, this.outline, this.artifact);
  }

  private async openSource(path: string, start: number, end: number): Promise<void> {
    this.sourceAbort?.abort();
    const request = new AbortController();
    this.sourceAbort = request;
    const drawer = this.sourceDrawer(); drawer.classList.remove('hidden');
    this.overlay.querySelector<HTMLElement>('[data-source-title]')!.textContent = path;
    this.overlay.querySelector<HTMLElement>('[data-source-range]')!.textContent = start ? `Lines ${start}${end > start ? `–${end}` : ''}` : '';
    const code = this.overlay.querySelector<HTMLElement>('[data-source-code]')!; code.textContent = 'Loading source…';
    try {
      const source = await loadSource(path, request.signal);
      if (this.sourceAbort !== request || request.signal.aborted) return;
      const fragment = document.createDocumentFragment();
      source.content.split('\n').forEach((line, index, lines) => {
        const n = index + 1;
        const row = document.createElement('span');
        row.classList.toggle('selected', start > 0 && n >= start && n <= Math.max(start, end));
        row.dataset.line = String(n);
        const lineNumber = document.createElement('i');
        lineNumber.textContent = String(n);
        row.append(lineNumber, document.createTextNode(line || ' '));
        fragment.append(row);
        if (index + 1 < lines.length) fragment.append(document.createTextNode('\n'));
      });
      code.replaceChildren(fragment);
      code.querySelector('.selected')?.scrollIntoView({ block: 'center' });
    } catch (error) {
      if (this.sourceAbort === request && !aborted(error)) code.textContent = message(error);
    } finally {
      if (this.sourceAbort === request) this.sourceAbort = undefined;
    }
  }

  private hideSource(): void { this.sourceAbort?.abort(); this.sourceAbort = undefined; this.sourceDrawer().classList.add('hidden'); }
  private toggleTheme(): void { this.shell.dataset.theme = this.shell.dataset.theme === 'light' ? 'dark' : 'light'; this.persist(); }
  private begin(): void { this.abort?.abort(); this.abort = new AbortController(); this.progress('Starting…', 0); }
  private finish(): void { this.overlay.querySelector('.knowledge-progress')?.classList.add('hidden'); }
  private progress(text: string, value?: number): void {
    const bar = this.overlay.querySelector<HTMLElement>('.knowledge-progress')!; bar.classList.remove('hidden');
    bar.querySelector('span')!.textContent = text || 'Working…';
    (bar.querySelector('i') as HTMLElement).style.width = `${Math.max(8, Math.min(100, value ?? 0))}%`;
  }
  private persist(): void { localStorage.setItem(STORAGE_KEY, JSON.stringify({ mode: this.mode, selected: this.selected, query: this.query, theme: this.shell.dataset.theme })); }
  private wikiView(): HTMLElement { return this.overlay.querySelector('[data-wiki-view]') as HTMLElement; }
  private codemapView(): HTMLElement { return this.overlay.querySelector('[data-codemap-view]') as HTMLElement; }
  private pageTree(): HTMLElement { return this.overlay.querySelector('[data-page-tree]') as HTMLElement; }
  private pageFilter(): HTMLInputElement { return this.overlay.querySelector('[data-page-filter]') as HTMLInputElement; }
  private sourceDrawer(): HTMLElement { return this.overlay.querySelector('.knowledge-source-drawer') as HTMLElement; }
}

function readState(): { mode: KnowledgeMode; selected: string; query: string; theme: string } {
  try {
    const value = JSON.parse(localStorage.getItem(STORAGE_KEY) || '{}') as Record<string, unknown>;
    return { mode: value.mode === 'codemap' ? 'codemap' : 'wiki', selected: typeof value.selected === 'string' ? value.selected : '', query: typeof value.query === 'string' ? value.query : DEFAULT_QUERY, theme: value.theme === 'light' ? 'light' : 'dark' };
  } catch { return { mode: 'wiki', selected: '', query: DEFAULT_QUERY, theme: 'dark' }; }
}
function toInt(value?: string): number { const n = Number(value); return Number.isFinite(n) && n > 0 ? Math.floor(n) : 0; }
function message(error: unknown): string { return error instanceof Error ? error.message : String(error); }
function aborted(error: unknown): boolean { return error instanceof DOMException && error.name === 'AbortError'; }
