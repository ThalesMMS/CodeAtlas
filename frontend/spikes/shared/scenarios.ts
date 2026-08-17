import type { SpikeDiagnostic, SpikeDocument, SpikeHover, SpikeSemanticToken } from './types';

export const baseDocuments: SpikeDocument[] = [
  {
    path: 'sample/main.go',
    language: 'go',
    version: 1,
    content: 'package main\n\nfunc Checkout(total int) int {\n\treturn total + 1\n}\n',
  },
  {
    path: 'web/checkout.js',
    language: 'javascript',
    version: 1,
    content: 'export function checkout(total) {\n  return total + 1;\n}\n',
  },
  {
    path: 'web/checkout.ts',
    language: 'typescript',
    version: 1,
    content: 'export function checkout(total: number): number {\n  return total + 1;\n}\n',
  },
  {
    path: 'web/Checkout.tsx',
    language: 'tsx',
    version: 1,
    content: 'export function CheckoutView() {\n  return <button>Checkout</button>;\n}\n',
  },
];

export function diagnosticSet(count: number, version: number): SpikeDiagnostic[] {
  return Array.from({ length: count }, (_, index) => ({
    id: `diag-${index}`,
    range: {
      start: { line: 1 + (index % 20), column: 1 },
      end: { line: 1 + (index % 20), column: 8 },
    },
    severity: index % 5 === 0 ? 'error' : 'warning',
    message: `Synthetic diagnostic ${index + 1}`,
    source: 'spike',
    version,
  }));
}

export function semanticTokenSet(count: number, version: number): SpikeSemanticToken[] {
  const types: SpikeSemanticToken['type'][] = ['function', 'type', 'variable', 'keyword'];
  return Array.from({ length: count }, (_, index) => ({
    id: `token-${index}`,
    range: {
      start: { line: index + 1, column: 1 },
      end: { line: index + 1, column: 6 },
    },
    type: types[index % types.length],
    modifiers: index % 2 === 0 ? ['readonly'] : [],
    version,
  }));
}

export function syntheticDocumentContent(size: number): string {
  const line = 'export const value = 1; // synthetic editor spike fixture\n';
  let content = '';
  while (content.length < size) content += line;
  return content.slice(0, size);
}

export const hoverFixture: SpikeHover = {
  range: { start: { line: 3, column: 6 }, end: { line: 3, column: 14 } },
  summary: 'Checkout calculates the final amount and preserves navigable evidence.',
  evidenceIds: ['ev:file:sample/main.go:3', 'ev:symbol:Checkout'],
  seeMoreLabel: 'See more',
};
