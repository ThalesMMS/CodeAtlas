import type { SourceReference } from './knowledge-types';

const SOURCE_FRAGMENT = /^#L(\d+)(?:-L(\d+))?$/u;
const SAFE_SEGMENT = /^[^\u0000-\u001f<>:"|?*]+$/u;

export function sourceLabel(path: string, startLine = 0, endLine = startLine): string {
  if (startLine <= 0) return path;
  return `${path}:${startLine}${endLine > startLine ? `-${endLine}` : ''}`;
}

export function parseKnowledgeLink(href: string): {
  wikiSlug?: string;
  source?: SourceReference;
  external?: string;
} | null {
  const value = href.trim();
  if (value.startsWith('wiki:')) {
    const slug = value.slice(5);
    return /^[A-Za-z0-9._-]+$/u.test(slug) ? { wikiSlug: slug } : null;
  }
  if (/^https:\/\//iu.test(value)) {
    try {
      const url = new URL(value);
      return url.protocol === 'https:' ? { external: url.toString() } : null;
    } catch {
      return null;
    }
  }
  if (/^[A-Za-z][A-Za-z0-9+.-]*:/u.test(value) || value.startsWith('//')) return null;

  const hash = value.indexOf('#');
  const rawPath = hash >= 0 ? value.slice(0, hash) : value;
  const fragment = hash >= 0 ? value.slice(hash) : '';
  const path = safePath(rawPath);
  if (!path) return null;

  let startLine = 0;
  let endLine = 0;
  if (fragment) {
    const match = fragment.match(SOURCE_FRAGMENT);
    if (!match) return null;
    startLine = Number(match[1]);
    endLine = Number(match[2] ?? match[1]);
    if (!validLine(startLine) || !validLine(endLine) || endLine < startLine) return null;
  }
  return { source: { path, startLine, endLine, label: sourceLabel(path, startLine, endLine) } };
}

function validLine(line: number): boolean {
  return Number.isSafeInteger(line) && line > 0;
}

function safePath(raw: string): string | null {
  let decoded: string;
  try {
    decoded = decodeURIComponent(raw).replaceAll('\\', '/').replace(/^\.\//u, '');
  } catch {
    return null;
  }
  if (!decoded || decoded.startsWith('/') || /^[A-Za-z]:\//u.test(decoded)) return null;
  const segments = decoded.split('/');
  if (segments.some((part) => !part || part === '.' || part === '..' || !SAFE_SEGMENT.test(part))) return null;
  return segments.join('/');
}

export function escapeHTML(value: string): string {
  const decoded = value.replace(/&(amp|lt|gt|quot|#39|#34);/gu, (_all, name: string) => {
    if (name === 'amp') return '&';
    if (name === 'lt') return '<';
    if (name === 'gt') return '>';
    if (name === 'quot' || name === '#34') return '"';
    return "'";
  });
  return decoded
    .replaceAll('&', '&amp;')
    .replaceAll('<', '&lt;')
    .replaceAll('>', '&gt;')
    .replaceAll('"', '&quot;')
    .replaceAll("'", '&#39;');
}

export function escapeAttribute(value: string): string {
  return escapeHTML(value).replaceAll('`', '&#96;');
}
