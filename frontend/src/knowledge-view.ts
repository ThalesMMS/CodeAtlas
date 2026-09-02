import { renderMermaidSubset } from './mermaid-lite';
import { escapeAttribute, escapeHTML, sourceLabel } from './knowledge-links';
import { buildWikiTree, filterWikiTree, formatCompactHash, renderKnowledgeMarkdown, statusLabel } from './knowledge-model';
import type { ArtifactMetadata, Codemap, DeepWikiCollection, OutlineItem, SourceReference, WikiPage, WikiTreeNode } from './knowledge-types';

export function workspaceTemplate(): string {
  return `<section class="knowledge-shell" data-theme="dark" aria-label="CodeAtlas Explore">
    <header class="knowledge-header">
      <button class="knowledge-brand" data-action="home" type="button"><span class="knowledge-logo">◇</span><span>CodeAtlas</span></button>
      <div class="knowledge-tabs" role="tablist">
        <button class="active" data-mode="wiki" role="tab" type="button">DeepWiki</button>
        <button data-mode="codemap" role="tab" type="button">Codemap</button>
      </div>
      <div class="knowledge-header-actions">
        <button class="knowledge-mobile-nav" data-action="nav" type="button" aria-label="Toggle navigation">☰</button>
        <button data-action="theme" type="button" aria-label="Toggle theme">◐</button>
        <button data-action="close" type="button" aria-label="Close Explore">×</button>
      </div>
    </header>
    <div class="knowledge-progress hidden" aria-live="polite"><span></span><div><i></i></div></div>
    <div class="knowledge-layout">
      <aside class="knowledge-nav">
        <div class="knowledge-nav-head"><strong data-nav-title>Documentation</strong><button data-action="nav" type="button">☰</button></div>
        <label class="knowledge-search"><span>⌕</span><input data-page-filter placeholder="Filter pages…" autocomplete="off"></label>
        <nav data-page-tree></nav>
        <div class="knowledge-nav-foot"><span>Grounded by CodeAtlas</span><small data-snapshot>snapshot —</small></div>
      </aside>
      <main class="knowledge-main">
        <div data-wiki-view></div>
        <div data-codemap-view class="hidden"></div>
      </main>
      <aside class="knowledge-sidecar">
        <div class="knowledge-side-tabs">
          <button class="active" data-side="sources" type="button">Sources</button>
          <button data-side="outline" type="button">Outline</button>
          <button data-side="artifact" type="button">Artifact</button>
        </div>
        <div data-side-content></div>
      </aside>
    </div>
    <section class="knowledge-source-drawer hidden" aria-label="Source preview">
      <header><div><strong data-source-title>Source</strong><small data-source-range></small></div><button data-action="close-source" type="button">×</button></header>
      <pre><code data-source-code></code></pre>
    </section>
  </section>`;
}

export function renderWikiNav(collection: DeepWikiCollection, query: string, selected: string): string {
  const tree = filterWikiTree(buildWikiTree(collection.pages), query);
  const items = (nodes: WikiTreeNode[]): string => nodes.map((node) => {
    const page = node.page;
    return `<button class="knowledge-page-link${page.slug === selected ? ' active' : ''}" style="--depth:${node.depth}" data-wiki-slug="${escapeAttribute(page.slug)}" type="button"><span>${pageIcon(page.archetype)}</span><span>${escapeHTML(page.title)}</span></button>${items(node.children)}`;
  }).join('');
  return items(tree) || '<p class="knowledge-muted">No matching pages.</p>';
}

export function renderWikiPage(page: WikiPage, collection: DeepWikiCollection): {
  html: string; sources: SourceReference[]; outline: OutlineItem[]; diagrams: Array<{ id: string; source: string }>; artifact?: ArtifactMetadata;
} {
  const rendered = renderKnowledgeMarkdown(page.markdown);
  const diagram = page.diagram ? safeDiagram(page.diagram.source, `wiki-${page.slug}`) : '';
  const related = page.relatedPages.length ? `<section class="knowledge-related"><h2>Continue exploring</h2><div>${page.relatedPages.map((link) => `<button data-wiki-slug="${escapeAttribute(link.slug)}" type="button"><span>${escapeHTML(link.title)}</span><small>${escapeHTML(link.relation || 'Related page')}</small></button>`).join('')}</div></section>` : '';
  const status = collection.status;
  const html = `<article class="knowledge-article">
    <div class="knowledge-eyebrow"><span class="knowledge-status ${escapeAttribute(status)}">${escapeHTML(statusLabel(status))}</span><span>${escapeHTML(readable(page.archetype))}</span></div>
    <div class="knowledge-title-row"><div><h1>${escapeHTML(page.title)}</h1><p>${escapeHTML(page.moduleSlug || 'Repository documentation')}</p></div><button class="knowledge-primary" data-action="refresh" type="button">↻ Refresh wiki</button></div>
    ${diagram ? `<figure class="knowledge-hero-diagram">${diagram}</figure>` : ''}
    <div class="knowledge-markdown">${rendered.html}</div>${related}
  </article>`;
  return { html, sources: mergeSources(rendered.sources, page.diagram?.sources ?? []), outline: rendered.outline, diagrams: rendered.diagrams, artifact: pageArtifact(page, collection.artifact) };
}

export function hydrateMarkdownDiagrams(container: HTMLElement, diagrams: Array<{ id: string; source: string }>): void {
  for (const diagram of diagrams) {
    const target = container.querySelector<HTMLElement>(`[data-knowledge-diagram="${CSS.escape(diagram.id)}"]`);
    if (target) target.innerHTML = safeDiagram(diagram.source, diagram.id);
  }
}

export function renderCodemapWelcome(query: string): string {
  return `<section class="knowledge-codemap-welcome"><div class="knowledge-map-preview"><span>Entry point</span><b>→</b><span>Service</span><b>→</b><span>Storage</span></div><p class="knowledge-kicker">Code-centric guided tours</p><h1>Map an execution flow</h1><p>Ask how a feature works. CodeAtlas builds the factual subgraph first, then asks the model to narrate only those observed nodes and edges.</p><form data-codemap-form><textarea name="query" rows="3">${escapeHTML(query)}</textarea><div><button class="knowledge-primary" type="submit">Generate Codemap</button></div></form><div class="knowledge-prompts">${['How does startup wire the application?', 'Trace a request from the handler to persistence.', 'Where would a change to validation propagate?'].map((text) => `<button data-codemap-example="${escapeAttribute(text)}" type="button">${escapeHTML(text)}</button>`).join('')}</div></section>`;
}

export function renderCodemap(codemap: Codemap): { html: string; sources: SourceReference[]; outline: OutlineItem[]; artifact?: ArtifactMetadata } {
  const sources: SourceReference[] = [...(codemap.diagram?.sources ?? [])];
  const outline: OutlineItem[] = codemap.flows.map((flow, index) => ({ id: `flow-${index + 1}`, title: flow.title, level: 2 }));
  const flows = codemap.flows.map((flow, flowIndex) => `<section class="knowledge-flow" id="flow-${flowIndex + 1}"><header><span>${String(flowIndex + 1).padStart(2, '0')}</span><div><h2>${escapeHTML(flow.title)}</h2><p>${flow.steps.length} grounded steps</p></div></header><ol>${flow.steps.map((step) => {
    if (step.path) sources.push({ path: step.path, startLine: step.line, endLine: step.endLine, label: sourceLabel(step.path, step.line, step.endLine), snippet: step.snippet });
    return `<li><span class="knowledge-step-label">${escapeHTML(step.label)}</span><div><h3>${escapeHTML(step.text || step.nodeId)}</h3>${step.snippet ? `<pre><code>${escapeHTML(step.snippet)}</code></pre>` : ''}${step.path ? sourceButton(step.path, step.line, step.endLine) : ''}</div></li>`;
  }).join('')}</ol></section>`).join('');
  const diagram = codemap.diagram ? safeDiagram(codemap.diagram.source, 'codemap-main') : '';
  const html = `<article class="knowledge-article knowledge-codemap"><div class="knowledge-eyebrow"><span class="knowledge-status ready">Grounded map</span><span>${codemap.nodes.length} nodes · ${codemap.edges.length} edges</span></div><div class="knowledge-title-row"><div><h1>${escapeHTML(codemap.title)}</h1><p>${escapeHTML(codemap.query)}</p></div><button data-action="new-map" type="button">New map</button></div>${codemap.overview ? `<p class="knowledge-lead">${escapeHTML(codemap.overview)}</p>` : ''}${diagram ? `<figure class="knowledge-hero-diagram">${diagram}</figure>` : ''}<div class="knowledge-flows">${flows || '<p class="knowledge-muted">No flow could be grounded for this query.</p>'}</div></article>`;
  return { html, sources: mergeSources(sources), outline, artifact: codemap.artifact };
}

export function renderSidecar(kind: string, sources: SourceReference[], outline: OutlineItem[], artifact?: ArtifactMetadata): string {
  if (kind === 'outline') return outline.length ? `<nav class="knowledge-outline">${outline.map((item) => `<a href="#${escapeAttribute(item.id)}" style="--level:${item.level}">${escapeHTML(item.title)}</a>`).join('')}</nav>` : '<p class="knowledge-muted">No headings in this page.</p>';
  if (kind === 'artifact') return artifact ? `<dl class="knowledge-artifact"><div><dt>Status</dt><dd>${escapeHTML(statusLabel(artifact.status))}</dd></div><div><dt>Provider</dt><dd>${escapeHTML(artifact.provider || '—')}</dd></div><div><dt>Model</dt><dd>${escapeHTML(artifact.model || '—')}</dd></div><div><dt>Snapshot</dt><dd>${escapeHTML(formatCompactHash(artifact.snapshotId))}</dd></div><div><dt>Context pack</dt><dd>${escapeHTML(formatCompactHash(artifact.contextPackHash))}</dd></div><div><dt>Schema</dt><dd>${escapeHTML(artifact.outputSchema || '—')}</dd></div></dl>` : '<p class="knowledge-muted">Artifact metadata is unavailable.</p>';
  return sources.length ? `<div class="knowledge-sources">${sources.map((source) => `<button ${sourceData(source)} type="button"><span>⌘</span><div><strong>${escapeHTML(source.path.split('/').pop() || source.path)}</strong><small>${escapeHTML(sourceLabel(source.path, source.startLine, source.endLine))}</small></div></button>`).join('')}</div>` : '<p class="knowledge-muted">No source references on this view.</p>';
}

export function loadingView(title: string, message: string): string { return `<div class="knowledge-loading"><span></span><h2>${escapeHTML(title)}</h2><p>${escapeHTML(message)}</p></div>`; }
export function errorView(title: string, message: string): string { return `<div class="knowledge-empty"><b>!</b><h2>${escapeHTML(title)}</h2><p>${escapeHTML(message)}</p></div>`; }

function sourceButton(path: string, startLine: number, endLine: number): string { return `<button class="knowledge-source-chip" ${sourceData({ path, startLine, endLine, label: '' })} type="button">${escapeHTML(sourceLabel(path, startLine, endLine))}</button>`; }
function sourceData(source: SourceReference): string { return `data-knowledge-source="${escapeAttribute(source.path)}" data-knowledge-start="${source.startLine}" data-knowledge-end="${source.endLine}"`; }
function safeDiagram(source: string, id: string): string { try { return renderMermaidSubset(source, id); } catch { return '<p class="knowledge-muted">Diagram could not be rendered safely.</p>'; } }
function mergeSources(...groups: SourceReference[][]): SourceReference[] { const map = new Map<string, SourceReference>(); for (const source of groups.flat()) if (source.path) map.set(`${source.path}:${source.startLine}:${source.endLine}`, source); return [...map.values()]; }
function readable(value: string): string { return value.replaceAll('-', ' ').replace(/\b\w/gu, (letter) => letter.toUpperCase()); }
function pageIcon(archetype: string): string { return archetype.includes('overview') ? '◇' : archetype.includes('testing') ? '✓' : archetype.includes('frontend') ? '▱' : '▤'; }
function pageArtifact(page: WikiPage, fallback?: ArtifactMetadata): ArtifactMetadata | undefined { return fallback ? { ...fallback, provider: page.provider || fallback.provider, contextPackHash: page.contextPackHash || fallback.contextPackHash, outputSchema: page.outputSchemaVersion || fallback.outputSchema } : undefined; }
