import test from 'node:test';
import assert from 'node:assert/strict';

import { FakeDocumentSyncController } from '../shared/document-controller';
import { semanticTokenSet, syntheticDocumentContent } from '../shared/scenarios';

const doc = {
  path: 'sample/main.go',
  language: 'go' as const,
  version: 1,
  content: 'package main\nfunc A() {}\n',
};

test('document controller coalesces changes and advances version on ack', async () => {
  const controller = new FakeDocumentSyncController();
  await controller.open(doc);
  assert.equal(controller.snapshot().dirty, false);

  const nextVersion = controller.queueLocalChange('package main\nfunc B() {}\n');
  assert.equal(nextVersion, 2);
  assert.equal(controller.snapshot().dirty, true);

  const ack = await controller.flush();
  assert.equal(ack.type, 'acked');
  assert.equal(ack.version, 2);
  assert.equal(controller.snapshot().version, 2);
  assert.equal(controller.snapshot().content, 'package main\nfunc B() {}\n');
});

test('document controller ignores out-of-order ack and preserves local version', async () => {
  const controller = new FakeDocumentSyncController();
  await controller.open({ ...doc, version: 4 });
  const event = controller.simulateOutOfOrderAck(2);
  assert.equal(event.version, 4);
  assert.match(event.message ?? '', /out-of-order/);
});

test('document controller blocks save while external conflict is active', async () => {
  const controller = new FakeDocumentSyncController();
  await controller.open(doc);
  controller.queueLocalChange('package main\nfunc Local() {}\n');
  await controller.flush();
  controller.simulateExternalConflict('sha256:external');
  const save = await controller.save();
  assert.equal(save.type, 'conflict');
  assert.equal(controller.snapshot().conflict, true);
});

test('semantic token fixture spreads benchmark tokens across non-empty ranges', () => {
  const tokens = semanticTokenSet(10_000, 3);
  const lines = new Set(tokens.map((token) => token.range.start.line));

  assert.equal(tokens.length, 10_000);
  assert.equal(lines.size, 10_000);
  assert.ok(tokens.every((token) => token.range.end.line === token.range.start.line));
  assert.ok(tokens.every((token) => token.range.end.column > token.range.start.column));
});

test('synthetic large-file fixture keeps source-like line boundaries', () => {
  const content = syntheticDocumentContent(100 * 1024);
  const lines = content.split('\n');

  assert.equal(content.length, 100 * 1024);
  assert.ok(lines.length > 1000);
  assert.ok(lines.every((line) => line.length < 96));
});
