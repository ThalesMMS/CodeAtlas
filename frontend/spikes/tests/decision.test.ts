import test from 'node:test';
import assert from 'node:assert/strict';

import { decide, gateResults } from '../benchmarks/decision';
import type { EditorBenchmarkMetrics } from '../shared/types';

test('gateResults permits editor style relaxations without eliminating candidate', () => {
  const gates = gateResults({
    externalRequests: [],
    axeViolations: 0,
    cspRelaxations: ["style-src 'unsafe-inline'", 'img-src data:'],
    cspViolations: [],
  });

  assert.equal(gates.eliminated, false);
  assert.deepEqual(gates.reasons, []);
});

test('gateResults eliminates unsafe script CSP relaxations', () => {
  const inlineScript = gateResults({
    externalRequests: [],
    axeViolations: 0,
    cspRelaxations: ["script-src 'unsafe-inline'"],
    cspViolations: [],
  });
  const unsafeEval = gateResults({
    externalRequests: [],
    axeViolations: 0,
    cspRelaxations: ['unsafe-eval'],
    cspViolations: [],
  });

  assert.equal(inlineScript.eliminated, true);
  assert.match(inlineScript.reasons.join(' '), /CSP unsafe/);
  assert.equal(unsafeEval.eliminated, true);
  assert.match(unsafeEval.reasons.join(' '), /CSP unsafe/);
});

test('decide is inconclusive when every editor is eliminated', () => {
  const decision = decide([
    result('monaco', true),
    result('codemirror', true),
  ]);

  assert.equal(decision.editor, null);
  assert.equal(decision.inconclusive, true);
  assert.match(decision.reason, /no editor passed gates/);
});

test('decide chooses the only candidate without eliminatory gates', () => {
  const decision = decide([
    result('monaco', false),
    result('codemirror', true),
  ]);

  assert.equal(decision.editor, 'monaco');
  assert.equal(decision.inconclusive, undefined);
});

function result(editor: EditorBenchmarkMetrics['editor'], eliminated: boolean): EditorBenchmarkMetrics {
  return {
    editor,
    build: { gzipBytes: 0, cspRelaxations: [], rawBytes: 0, chunks: 0, workerBytes: 0, workerCount: 0, coldBuildMs: 0, warmBuildMs: 0, dependencyCount: 0, licenses: {} },
    runtime: { typingP95Ms: 0, openLimitMs: 0, firstUsableMs: 0, open100KiBMs: 0, open1MiBMs: 0, tabSwitchP95Ms: 0, typingP50Ms: 0, semanticTokensMs: 0, diagnosticsMs: 0, revealRangeMs: 0, memoryOneModelBytes: null, memoryThirtyModelsBytes: null, memoryAfterDisposeBytes: null, longTasks: 0 },
    accessibility: { axeViolations: 0, keyboard: 'pass', screenReaderNotes: '', highContrast: 'pass', reducedMotion: 'pass', zoom200: 'pass', imeComposition: 'pass' },
    csp: { passed: true, externalRequests: [], requiresUnsafeEval: false, requiresUnsafeInline: false, requiresBlobWorker: false },
    gates: { eliminated, reasons: eliminated ? ['test gate'] : [] },
    version: 'test',
  };
}
