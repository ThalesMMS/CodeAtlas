import assert from 'node:assert/strict';
import test from 'node:test';

import { languageForPath, workspaceModelURI } from '../src/monaco-editor-contract';

test('Monaco language selection covers all production language families', () => {
  const cases = new Map([
    ['cmd/api/main.go', 'go'],
    ['web/app.js', 'javascript'],
    ['web/app.jsx', 'javascript'],
    ['web/app.mjs', 'javascript'],
    ['web/app.cjs', 'javascript'],
    ['web/app.ts', 'typescript'],
    ['web/app.tsx', 'typescript'],
    ['web/app.mts', 'typescript'],
    ['web/app.cts', 'typescript'],
    ['Sources/Commerce/Order.swift', 'swift'],
    ['commerce/service.py', 'python'],
    ['src/service.rs', 'rust'],
  ]);
  for (const [path, language] of cases) assert.equal(languageForPath(path), language, path);
});

test('Monaco normalizes declared Python language aliases', () => {
  assert.equal(languageForPath('script', 'python'), 'python');
  assert.equal(languageForPath('script', 'py'), 'python');
});

test('Monaco normalizes declared Rust language aliases', () => {
  assert.equal(languageForPath('script', 'rust'), 'rust');
  assert.equal(languageForPath('script', 'rs'), 'rust');
});

test('Monaco model URI is workspace scoped, relative and preserves the extension', () => {
  assert.equal(workspaceModelURI('web/src/order service.tsx'), 'codeatlas://workspace/web/src/order%20service.tsx');
  assert.throws(() => workspaceModelURI('../cmd/api/main.go'), /workspace-relative/);
  assert.throws(() => workspaceModelURI('/Users/example/secret.go'), /workspace-relative/);
});
