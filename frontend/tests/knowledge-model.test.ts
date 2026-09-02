import test from 'node:test';
import assert from 'node:assert/strict';
import { normalizeJobSnapshot } from '../src/knowledge-api';
import {
  buildWikiTree,
  filterWikiTree,
  flattenWikiTree,
  normalizeCodemap,
  normalizeDeepWikiCollection,
  parseKnowledgeLink,
  renderKnowledgeMarkdown,
  statusLabel,
} from '../src/knowledge-model';

test('normalizes DeepWiki pages and builds a stable overview-first hierarchy', () => {
  const collection = normalizeDeepWikiCollection({
    status: 'ready',
    snapshotId: 'snapshot-1',
    artifact: {
      artifactId: 'artifact-7',
      artifactRevision: 4,
      inputSnapshotId: 'sha256:abc',
      contextPackHash: 'pack-123',
      provider: 'openai-compatible',
      model: 'qwen',
      createdAt: '2026-09-02T12:00:00Z',
      status: 'current',
    },
    pages: [
      { slug: '2-service', title: 'Service', parentSlug: 'overview', markdown: 'Service' },
      { slug: 'overview', title: 'Overview', markdown: 'Overview' },
      { slug: '2.1-validation', title: 'Validation', parentSlug: '2-service', markdown: 'Validation' },
    ],
  });
  const tree = buildWikiTree(collection.pages);
  assert.equal(tree[0]?.page.slug, 'overview');
  assert.deepEqual(flattenWikiTree(tree).map((node) => [node.page.slug, node.depth]), [
    ['overview', 0],
    ['2-service', 1],
    ['2.1-validation', 2],
  ]);
  assert.deepEqual(flattenWikiTree(filterWikiTree(tree, 'validation')).map((node) => node.page.slug), [
    'overview', '2-service', '2.1-validation',
  ]);
  assert.equal(collection.artifact?.id, 'artifact-7');
  assert.equal(collection.artifact?.revision, 4);
  assert.equal(collection.artifact?.snapshotId, 'sha256:abc');
  assert.equal(collection.artifact?.contextPackHash, 'pack-123');
  assert.equal(collection.artifact?.model, 'qwen');
});

test('parses only safe wiki, source, and https links', () => {
  assert.deepEqual(parseKnowledgeLink('wiki:architecture-overview'), { wikiSlug: 'architecture-overview' });
  assert.deepEqual(parseKnowledgeLink('internal/order/service.go#L18-L23'), {
    source: {
      path: 'internal/order/service.go',
      startLine: 18,
      endLine: 23,
      label: 'internal/order/service.go:18-23',
    },
  });
  assert.equal(parseKnowledgeLink('../secret.txt#L1'), null);
  assert.equal(parseKnowledgeLink('javascript:alert(1)'), null);
  assert.equal(parseKnowledgeLink('/etc/passwd'), null);
  assert.equal(parseKnowledgeLink('internal/%ZZ.go'), null);
  assert.equal(parseKnowledgeLink('http://example.com'), null);
  assert.equal(parseKnowledgeLink('https://github.com/openai/openai')?.external, 'https://github.com/openai/openai');
});

test('renders grounded markdown without admitting arbitrary HTML or unsafe URLs', () => {
  const markdown = [
    '# Architecture',
    '',
    '<script>alert(1)</script>',
    '',
    'Backend-safe text: &lt;generic&gt; &amp; value.',
    '',
    '- Calls [Submit](internal/order/service.go#L18-L23).',
    '- Continue at [Service](wiki:2-service).',
    '',
    '| Layer | Responsibility |',
    '| --- | --- |',
    '| HTTP | Decode |',
    '',
    '```mermaid',
    'graph TD',
    '  subgraph g0["service"]',
    '    direction TB',
    '    n0["Submit · service.go"]',
    '  end',
    '```',
  ].join('\n');
  const rendered = renderKnowledgeMarkdown(markdown);
  assert.match(rendered.html, /<h1 id="architecture">Architecture<\/h1>/);
  assert.match(rendered.html, /&lt;script&gt;alert\(1\)&lt;\/script&gt;/);
  assert.doesNotMatch(rendered.html, /<script>/);
  assert.match(rendered.html, /Backend-safe text: &lt;generic&gt; &amp; value\./);
  assert.doesNotMatch(rendered.html, /&amp;lt;generic/);
  assert.match(rendered.html, /data-knowledge-source="internal\/order\/service.go"/);
  assert.match(rendered.html, /data-knowledge-wiki="2-service"/);
  assert.match(rendered.html, /<table>/);
  assert.equal(rendered.diagrams.length, 1);
  assert.equal(rendered.sources[0]?.startLine, 18);
  assert.deepEqual(rendered.outline, [{ id: 'architecture', title: 'Architecture', level: 1 }]);
});

test('normalizes Codemap job result envelopes and status labels', () => {
  const codemap = normalizeCodemap({ result: {
    query: 'request flow',
    title: 'Request flow',
    artifact: {
      artifactId: 'codemap-1',
      artifactRevision: 2,
      inputSnapshotId: 'sha256:def',
      createdAt: '2026-09-02T12:00:00Z',
    },
    diagram: {
      source: 'graph TD',
      sources: [{
        nodeId: 'main',
        label: 'main',
        path: 'main.go',
        range: { start: { line: 10 }, end: { line: 14 } },
      }],
    },
    flows: [{
      title: 'HTTP',
      entryNodeId: 'main',
      steps: [{ label: '1a', nodeId: 'main', text: 'Starts', path: 'main.go', line: 10, snippet: 'func main()' }],
    }],
  } });
  assert.equal(codemap.title, 'Request flow');
  assert.equal(codemap.flows[0]?.steps[0]?.endLine, 10);
  assert.equal(codemap.artifact?.id, 'codemap-1');
  assert.equal(codemap.diagram?.sources?.[0]?.startLine, 10);
  assert.equal(codemap.diagram?.sources?.[0]?.endLine, 14);
  assert.equal(statusLabel('ready'), 'Current');
  assert.equal(statusLabel('succeeded'), 'Completed');
});

test('normalizes job envelopes and structured progress', () => {
  const job = normalizeJobSnapshot({
    job: {
      id: 'job-1',
      state: 'running',
      stage: 'deepwiki.generate',
      message: 'Generating pages',
      progress: { completed: 2, total: 4, percent: 50 },
    },
  });
  assert.equal(job.id, 'job-1');
  assert.equal(job.state, 'running');
  assert.equal(job.progress, 50);

  const derived = normalizeJobSnapshot({ id: 'job-2', state: 'running', progress: { completed: 3, total: 4 } });
  assert.equal(derived.progress, 75);
});
