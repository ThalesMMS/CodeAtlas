import { test } from 'node:test';
import assert from 'node:assert/strict';
import fs from 'node:fs/promises';
import path from 'node:path';
import { fileURLToPath } from 'node:url';

import { loadJSON } from '../harness/budgets.mjs';

const root = path.resolve(path.dirname(fileURLToPath(import.meta.url)), '../..');

test('P10 scenario matrix covers every required E2E group with automated commands', async () => {
  const matrix = await loadJSON(path.join(root, 'e2e/scenarios/p10-scenarios.json'));
  assert.equal(matrix.scenarioMatrixVersion, 'p10-v1');
  assert.equal(matrix.policy.network, 'loopback-only');

  assert.deepEqual(matrix.behavioralRegressions.map((regression) => regression.id), [
    'provider-schema-compatibility',
    'index-maintenance-contract',
  ]);
  for (const regression of matrix.behavioralRegressions) {
    assert.ok(regression.coverage.length > 0, `${regression.id} must describe behavioral coverage`);
    await fs.access(path.join(root, regression.testFile));
    assert.match(regression.testFile, /^e2e\/tests\/.*\.test\.mjs$/u);
  }

  const requiredIds = [
    'readiness',
    'document-basic',
    'out-of-order',
    'dirty-buffer',
    'external-change',
    'navigation',
    'semantic-visuals',
    'hover-see-more',
    'deepwiki',
    'codemap',
    'sse-gaps',
    'shutdown',
  ];
  assert.deepEqual(matrix.groups.map((group) => group.id), requiredIds);

  for (const group of matrix.groups) {
    assert.equal(group.automated, true, `${group.id} must be automated`);
    assert.ok(Number.isInteger(group.number), `${group.id} must have stable issue group number`);
    assert.ok(group.commands.length > 0, `${group.id} must name executable commands`);
    assert.ok(group.coverage.length > 0, `${group.id} must describe covered behaviors`);
    assert.equal(group.commands.some((command) => /manual|skip|todo/i.test(command)), false, `${group.id} must not rely on manual-only commands`);
  }

  for (const gate of ['security-csp-no-network', 'accessibility', 'performance-budget']) {
    assert.equal(matrix.crossCuttingGates[gate].automated, true, `${gate} must be automated`);
    assert.ok(matrix.crossCuttingGates[gate].commands.length > 0);
  }
});
