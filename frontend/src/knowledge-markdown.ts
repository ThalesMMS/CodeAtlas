import { escapeAttribute, escapeHTML, parseKnowledgeLink } from './knowledge-links';
import type { OutlineItem, RenderedMarkdown, SourceReference } from './knowledge-types';

const INLINE = /\[([^\]]+)\]\(([^)]+)\)|`([^`]+)`|\*\*([^*]+)\*\*|(?<!\*)\*([^*]+)\*(?!\*)/gu;
const HEADING = /^(#{1,6})\s+(.+)$/u;
const LIST = /^\s*[-*+]\s+(.+)$/u;
const TABLE_RULE = /^\s*\|?(?:\s*:?-{3,}:?\s*\|)+\s*:?-{3,}:?\s*\|?\s*$/u;

export function renderKnowledgeMarkdown(markdown: string): RenderedMarkdown {
  const lines = markdown.replace(/\r\n?/gu, '\n').trim().split('\n');
  const html: string[] = [];
  const diagrams: Array<{ id: string; source: string }> = [];
  const sourceMap = new Map<string, SourceReference>();
  const outline: OutlineItem[] = [];
  let paragraph: string[] = [];
  let items: string[] = [];

  const addSource = (source: SourceReference): void => {
    sourceMap.set(`${source.path}\0${source.startLine}\0${source.endLine}`, source);
  };
  const inline = (text: string): string => renderInline(text, addSource);
  const flushParagraph = (): void => {
    if (paragraph.length) html.push(`<p>${inline(paragraph.join(' '))}</p>`);
    paragraph = [];
  };
  const flushList = (): void => {
    if (items.length) html.push(`<ul>${items.map((item) => `<li>${inline(item)}</li>`).join('')}</ul>`);
    items = [];
  };
  const flush = (): void => { flushParagraph(); flushList(); };

  for (let index = 0; index < lines.length;) {
    const line = lines[index] ?? '';
    if (line.startsWith('```')) {
      flush();
      const language = line.slice(3).trim().toLowerCase();
      const body: string[] = [];
      index += 1;
      while (index < lines.length && !(lines[index] ?? '').startsWith('```')) {
        body.push(lines[index] ?? '');
        index += 1;
      }
      if (index < lines.length) index += 1;
      const code = body.join('\n');
      if (language === 'mermaid') {
        const id = `knowledge-diagram-${diagrams.length + 1}`;
        diagrams.push({ id, source: code });
        html.push(`<figure class="knowledge-diagram"><div id="${id}" data-knowledge-diagram="${id}"></div><details><summary>Diagram source</summary><pre><code>${escapeHTML(code)}</code></pre></details></figure>`);
      } else {
        const attr = language ? ` class="language-${escapeAttribute(language)}"` : '';
        html.push(`<pre class="knowledge-code"><code${attr}>${escapeHTML(code)}</code></pre>`);
      }
      continue;
    }

    const heading = line.match(HEADING);
    if (heading) {
      flush();
      const level = heading[1]?.length ?? 1;
      const title = heading[2]?.trim() ?? '';
      const id = uniqueId(title, outline);
      outline.push({ id, title: stripMarkdown(title), level });
      html.push(`<h${level} id="${escapeAttribute(id)}">${inline(title)}</h${level}>`);
      index += 1;
      continue;
    }

    if (line.trim().startsWith('|') && index + 1 < lines.length && TABLE_RULE.test(lines[index + 1] ?? '')) {
      flush();
      const header = splitRow(line);
      const rows: string[][] = [];
      index += 2;
      while (index < lines.length && (lines[index] ?? '').trim().startsWith('|')) {
        rows.push(splitRow(lines[index] ?? ''));
        index += 1;
      }
      html.push(`<div class="knowledge-table"><table><thead><tr>${header.map((cell) => `<th>${inline(cell)}</th>`).join('')}</tr></thead><tbody>${rows.map((row) => `<tr>${header.map((_cell, i) => `<td>${inline(row[i] ?? '')}</td>`).join('')}</tr>`).join('')}</tbody></table></div>`);
      continue;
    }

    const list = line.match(LIST);
    if (list) {
      flushParagraph();
      items.push(list[1]?.trim() ?? '');
      index += 1;
      continue;
    }

    if (!line.trim()) {
      flush();
      index += 1;
      continue;
    }
    if (/^---+$/u.test(line.trim())) {
      flush();
      html.push('<hr>');
      index += 1;
      continue;
    }
    if (items.length) flushList();
    paragraph.push(line.trim());
    index += 1;
  }

  flush();
  return { html: html.join('\n'), diagrams, sources: [...sourceMap.values()], outline };
}

function renderInline(value: string, addSource: (source: SourceReference) => void): string {
  let html = '';
  let cursor = 0;
  for (const match of value.matchAll(INLINE)) {
    const start = match.index ?? 0;
    html += escapeHTML(value.slice(cursor, start));
    if (match[1] != null && match[2] != null) {
      const label = escapeHTML(match[1]);
      const link = parseKnowledgeLink(match[2]);
      if (link?.wikiSlug) {
        html += `<a href="wiki:${escapeAttribute(link.wikiSlug)}" data-knowledge-wiki="${escapeAttribute(link.wikiSlug)}">${label}</a>`;
      } else if (link?.source) {
        addSource(link.source);
        html += `<a href="${escapeAttribute(match[2])}" data-knowledge-source="${escapeAttribute(link.source.path)}" data-knowledge-start="${link.source.startLine}" data-knowledge-end="${link.source.endLine}">${label}</a>`;
      } else if (link?.external) {
        html += `<a href="${escapeAttribute(link.external)}" target="_blank" rel="noreferrer noopener">${label}</a>`;
      } else html += label;
    } else if (match[3] != null) html += `<code>${escapeHTML(match[3])}</code>`;
    else if (match[4] != null) html += `<strong>${escapeHTML(match[4])}</strong>`;
    else html += `<em>${escapeHTML(match[5] ?? '')}</em>`;
    cursor = start + match[0].length;
  }
  return html + escapeHTML(value.slice(cursor));
}

function splitRow(value: string): string[] {
  return value.trim().replace(/^\|/u, '').replace(/\|$/u, '').split(/(?<!\\)\|/u).map((cell) => cell.trim().replaceAll('\\|', '|'));
}

function uniqueId(title: string, outline: OutlineItem[]): string {
  const base = stripMarkdown(title).normalize('NFKD').replace(/[\u0300-\u036f]/gu, '').toLowerCase().replace(/[^a-z0-9]+/gu, '-').replace(/^-|-$/gu, '') || 'section';
  const used = new Set(outline.map((item) => item.id));
  let id = base;
  for (let suffix = 2; used.has(id); suffix += 1) id = `${base}-${suffix}`;
  return id;
}

function stripMarkdown(value: string): string {
  return value.replace(/\[([^\]]+)\]\([^)]+\)/gu, '$1').replace(/[*_`]/gu, '').trim();
}
