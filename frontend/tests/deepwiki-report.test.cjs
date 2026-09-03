'use strict';

const test = require('node:test');
const assert = require('node:assert/strict');
const {
  parseWikiRoute,
  wikiRouteFor,
  buildPageTree,
  filterPageTree,
  formatRelativeTime,
  lastUpdatedAt,
  statusLabel,
  preferredTheme,
} = require('../deepwiki-report.js');

test('parseWikiRoute accepts the wiki root and page slugs only', () => {
  assert.deepEqual(parseWikiRoute('/wiki'), { slug: '' });
  assert.deepEqual(parseWikiRoute('/wiki/'), { slug: '' });
  assert.deepEqual(parseWikiRoute('/wiki/4.1-domain-model'), { slug: '4.1-domain-model' });
  assert.deepEqual(parseWikiRoute('/wiki/Overview'), { slug: 'overview' });
  assert.equal(parseWikiRoute('/wiki/a/b'), null);
  assert.equal(parseWikiRoute('/workspace'), null);
  assert.equal(parseWikiRoute('/wikis'), null);
});

test('wikiRouteFor round-trips slugs', () => {
  assert.equal(wikiRouteFor(''), '/wiki');
  assert.equal(wikiRouteFor('4.1-domain-model'), '/wiki/4.1-domain-model');
});

test('buildPageTree nests by parentSlug and keeps order', () => {
  const tree = buildPageTree([
    { slug: 'overview', title: 'Overview' },
    { slug: '1-getting-started', parentSlug: 'overview' },
    { slug: '4-backend', parentSlug: 'overview' },
    { slug: '4.1-domain-model', parentSlug: '4-backend' },
    { slug: 'orphan', parentSlug: 'missing-parent' },
  ]);
  assert.deepEqual(tree.map((node) => node.page.slug), ['overview', 'orphan']);
  assert.deepEqual(tree[0].children.map((node) => node.page.slug), ['1-getting-started', '4-backend']);
  assert.deepEqual(tree[0].children[1].children.map((node) => node.page.slug), ['4.1-domain-model']);
});

test('filterPageTree keeps matches and their ancestors', () => {
  const tree = buildPageTree([
    { slug: 'overview', title: 'Overview' },
    { slug: '4-backend', parentSlug: 'overview', title: 'Backend: Order Service' },
    { slug: '4.1-domain-model', parentSlug: '4-backend', title: 'Domain model' },
    { slug: '5-glossary', parentSlug: 'overview', title: 'Glossary' },
  ]);
  const filtered = filterPageTree(tree, 'domain');
  assert.equal(filtered.length, 1);
  assert.equal(filtered[0].page.slug, 'overview');
  assert.equal(filtered[0].children.length, 1);
  assert.equal(filtered[0].children[0].page.slug, '4-backend');
  assert.equal(filtered[0].children[0].children[0].page.slug, '4.1-domain-model');
  assert.deepEqual(filterPageTree(tree, ''), tree);
});

test('formatRelativeTime reports coarse English ages', () => {
  const now = Date.parse('2026-08-28T12:00:00Z');
  assert.equal(formatRelativeTime('2026-08-28T11:59:40Z', now), 'just now');
  assert.equal(formatRelativeTime('2026-08-28T11:30:00Z', now), '30 minutes ago');
  assert.equal(formatRelativeTime('2026-08-27T12:00:00Z', now), '1 day ago');
  assert.equal(formatRelativeTime('2026-06-20T12:00:00Z', now), '2 months ago');
  assert.equal(formatRelativeTime('not-a-date', now), '');
});

test('lastUpdatedAt picks the newest page or artifact timestamp', () => {
  assert.equal(lastUpdatedAt({
    pages: [{ updatedAt: '2026-08-01T00:00:00Z' }, { updatedAt: '2026-08-20T00:00:00Z' }],
    artifact: { createdAt: '2026-08-10T00:00:00Z' },
  }), '2026-08-20T00:00:00.000Z');
  assert.equal(lastUpdatedAt({ pages: [] }), '');
});

test('statusLabel hides ready and names the other states', () => {
  assert.equal(statusLabel('ready'), '');
  assert.equal(statusLabel('stale'), 'Out of date');
  assert.equal(statusLabel('generating'), 'Generating…');
  assert.equal(statusLabel('failed'), 'Generation failed');
});

test('preferredTheme defaults to dark and honors the stored light preference', () => {
  assert.equal(preferredTheme(null), 'dark');
  assert.equal(preferredTheme({ getItem: () => 'light' }), 'light');
});
