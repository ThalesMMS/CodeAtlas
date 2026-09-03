'use strict';

const test = require('node:test');
const assert = require('node:assert/strict');
const {
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
} = require('../codemap-report.js');

test('isCodemapRoute accepts only a single artifact segment', () => {
  assert.equal(isCodemapRoute('/codemaps/cm-123'), true);
  assert.equal(isCodemapRoute('/codemaps/cm-123/'), true);
  assert.equal(isCodemapRoute('/codemaps/cm-123/extra'), false);
  assert.equal(isCodemapRoute('/workspace'), false);
});

test('alphabeticSuffix remains readable beyond z', () => {
  assert.equal(alphabeticSuffix(0), 'a');
  assert.equal(alphabeticSuffix(25), 'z');
  assert.equal(alphabeticSuffix(26), 'aa');
  assert.equal(alphabeticSuffix(27), 'ab');
});

test('parseCanonicalHash accepts canonical hashes and rejects malformed fragments', () => {
  assert.equal(parseCanonicalHash('#2B'), '2b');
  assert.equal(parseCanonicalHash('#%32%62'), '2b');
  assert.equal(parseCanonicalHash('#section-2'), '');
  assert.equal(parseCanonicalHash('#%E0%A4%A'), '');
});

test('parseStepRefs splits prose into text and individual step references', () => {
  assert.deepEqual(parseStepRefs('starts at [1a], crosses [1d]/[2a].'), [
    { type: 'text', text: 'starts at ' },
    { type: 'ref', id: '1a' },
    { type: 'text', text: ', crosses ' },
    { type: 'ref', id: '1d' },
    { type: 'text', text: '/' },
    { type: 'ref', id: '2a' },
    { type: 'text', text: '.' },
  ]);
});

test('parseStepRefs expands comma-grouped references', () => {
  assert.deepEqual(parseStepRefs('validated at [1b, 1c]'), [
    { type: 'text', text: 'validated at ' },
    { type: 'ref', id: '1b' },
    { type: 'text', text: ', ' },
    { type: 'ref', id: '1c' },
  ]);
});

test('parseStepRefs leaves non-reference brackets untouched', () => {
  assert.deepEqual(parseStepRefs('array[0] and [note]'), [
    { type: 'text', text: 'array[0] and [note]' },
  ]);
});

test('sectionNumberForFlow numbers sections by position', () => {
  assert.equal(sectionNumberForFlow({ steps: [{ label: '3a' }] }, 0), 1);
  assert.equal(sectionNumberForFlow({ steps: [] }, 4), 5);
});

test('ensureStepLabels assigns canonical per-section labels and keeps the source label', () => {
  const flows = ensureStepLabels([
    { steps: [{ label: '1a' }, { label: '2a' }, { label: '' }] },
    { steps: [{ label: '1b' }] },
  ]);
  const labels = flows.flatMap((flow) => flow.steps.map((step) => step.label));
  assert.deepEqual(labels, ['1a', '1b', '1c', '2a']);
  assert.equal(flows[0].steps[1].sourceLabel, '2a');
  assert.equal(flows[1].steps[0].sourceLabel, '1b');
});

test('fileTabsForFlows lists unique step files in first-use order', () => {
  const files = fileTabsForFlows([
    { steps: [{ path: 'web/src/app.ts' }, { path: 'web/src/checkout.ts' }] },
    { steps: [{ path: 'cmd/api/main.go' }, { path: 'web/src/app.ts' }, { path: '' }] },
  ]);
  assert.deepEqual(files, ['web/src/app.ts', 'web/src/checkout.ts', 'cmd/api/main.go']);
});

test('breadcrumbSegments and fileName normalize separators', () => {
  assert.deepEqual(breadcrumbSegments('web\\src\\app.ts'), ['web', 'src', 'app.ts']);
  assert.equal(fileName('internal/order/http.go'), 'http.go');
  assert.equal(fileName(''), '');
});

test('deriveSectionSummary prefers the backend summary and falls back to a factual span', () => {
  assert.equal(deriveSectionSummary({ summary: 'Backend HTTP layer.' }), 'Backend HTTP layer.');
  assert.equal(
    deriveSectionSummary({ steps: [{ path: 'a/http.go' }, { path: 'a/service.go' }] }),
    '2 grounded steps, from http.go to service.go.',
  );
  assert.equal(
    deriveSectionSummary({ steps: [{ path: 'a/http.go' }] }),
    '1 grounded step in http.go.',
  );
});

test('buildStepTree nests steps by backend depth without skipping levels', () => {
  const roots = buildStepTree([
    { label: '1a', depth: 0 },
    { label: '1b', depth: 1 },
    { label: '1c', depth: 1 },
    { label: '1d', depth: 2 },
    { label: '1e', depth: 5 }, // malformed jump clamps to one level deeper
    { label: '1f', depth: 0 },
  ]);
  const shape = (node) => [node.step.label, node.children.map(shape)];
  assert.deepEqual(roots.map(shape), [
    ['1a', [
      ['1b', []],
      ['1c', [
        ['1d', [
          ['1e', []],
        ]],
      ]],
    ]],
    ['1f', []],
  ]);
});

test('groupTreeSiblings groups consecutive siblings by enclosing symbol', () => {
  const nodesById = new Map([
    ['n1', { id: 'n1', label: 'Handler.Create' }],
    ['n2', { id: 'n2', label: 'Service.Submit' }],
  ]);
  const siblings = [
    { step: { label: '1a', nodeId: 'n1' }, children: [] },
    { step: { label: '1b', nodeId: 'n1' }, children: [] },
    { step: { label: '1c', nodeId: 'n2' }, children: [] },
    { step: { label: '1d', nodeId: 'missing' }, children: [] },
  ];
  const runs = groupTreeSiblings(siblings, nodesById);
  assert.deepEqual(runs.map((run) => [run.symbol, run.nodes.map((node) => node.step.label)]), [
    ['Handler.Create', ['1a', '1b']],
    ['Service.Submit', ['1c']],
    ['', ['1d']],
  ]);
});

test('parseMarkdownBlocks separates headings, lists, and paragraphs', () => {
  assert.deepEqual(parseMarkdownBlocks('## Routing\n\nFirst line\nsecond line\n\n- one\n- two'), [
    { type: 'heading', level: 2, text: 'Routing' },
    { type: 'paragraph', text: 'First line second line' },
    { type: 'list', items: ['one', 'two'] },
  ]);
  assert.deepEqual(parseMarkdownBlocks(''), []);
});

test('preferredTheme defaults to dark and honors the stored light preference', () => {
  assert.equal(preferredTheme(null), 'dark');
  assert.equal(preferredTheme({ getItem: () => 'light' }), 'light');
  assert.equal(preferredTheme({ getItem: () => 'unexpected' }), 'dark');
});

test('copyText falls back to manual copy after Clipboard API rejection', async () => {
  const navigatorDescriptor = Object.getOwnPropertyDescriptor(globalThis, 'navigator');
  const originalPrompt = globalThis.prompt;
  let prompted = null;
  try {
    Object.defineProperty(globalThis, 'navigator', {
      configurable: true,
      value: { clipboard: { writeText: async () => { throw new Error('denied'); } } },
    });
    globalThis.prompt = (message, value) => { prompted = [message, value]; return value; };
    assert.equal(await copyText('copy value'), false);
    assert.deepEqual(prompted, ['Copy to clipboard:', 'copy value']);
  } finally {
    if (navigatorDescriptor) Object.defineProperty(globalThis, 'navigator', navigatorDescriptor);
    else delete globalThis.navigator;
    if (originalPrompt === undefined) delete globalThis.prompt;
    else globalThis.prompt = originalPrompt;
  }
});
