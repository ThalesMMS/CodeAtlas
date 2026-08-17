import test from 'node:test';
import assert from 'node:assert/strict';
import { renderMermaidSubset } from '../src/mermaid-lite';

test('mermaid-lite renders deterministic flowchart SVG without inline styles', () => {
  const source = [
    'graph TD',
    '  subgraph g0["cmd"]',
    '    direction TB',
    '    n0["main · main.go"]',
    '  end',
    '  subgraph g1["internal"]',
    '    direction TB',
    '    n1["Save · repository.go"]',
    '  end',
    '  n0 -->|calls| n1',
  ].join('\n');
  const first = renderMermaidSubset(source, 'flow-1');
  const second = renderMermaidSubset(source, 'flow-1');
  assert.equal(first, second);
  assert.match(first, /^<svg[^>]+>/);
  assert.match(first, /class="edgePath"/);
  assert.doesNotMatch(first, /<style| style=| on\w+=/i);
});

test('mermaid-lite renders sequence SVG and rejects dangling participants', () => {
  const source = [
    'sequenceDiagram',
    '  participant p0 as main main.go',
    '  participant p1 as Save repository.go',
    '  p0->>p1: calls Save',
  ].join('\n');
  const svg = renderMermaidSubset(source, 'sequence-1');
  assert.match(svg, /class="messageLine0"/);
  assert.match(svg, /calls Save/);
  assert.throws(() => renderMermaidSubset(source.replace('p1: calls', 'p9: calls'), 'bad'), /missing participant/);
});
