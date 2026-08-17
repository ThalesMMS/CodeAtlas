import { test } from 'node:test';
import assert from 'node:assert/strict';
import path from 'node:path';
import { fileURLToPath } from 'node:url';

import { checkBundleBudget, loadJSON } from '../harness/budgets.mjs';
import { writeRunReport } from '../harness/reporter.mjs';

const root = path.resolve(path.dirname(fileURLToPath(import.meta.url)), '../..');

test('p10-monaco-v1 bundle budget is versioned and enforced against production assets', async () => {
  const budgetPath = path.join(root, 'e2e/budgets/bundle-budget.json');
  const budget = await loadJSON(budgetPath);
  const interactionBudget = await loadJSON(path.join(root, 'e2e/budgets/interaction-budget.json'));

  assert.equal(budget.budgetVersion, 'p10-monaco-v1');
  assert.equal(interactionBudget.budgetVersion, 'p10-monaco-v1');
  assert.equal(interactionBudget.mainThread.codemapLayout, 'worker');
  assert.ok(interactionBudget.memory.residualGrowthPercent > 0);
  assert.ok(budget.mainJsGzipBytes > 0, 'main JS budget must be populated');
  assert.ok(budget.totalInitialGzipBytes > 0, 'initial bundle budget must be populated');
  assert.ok(budget.workerGzipBytes > 0, 'worker budget must be populated');
  assert.ok(budget.maxInitialRequests > 0, 'initial request budget must be populated');

  const result = await checkBundleBudget({
    root,
    distDir: path.join(root, 'backend/internal/webui/dist'),
    budgetPath,
  });

  assert.equal(result.ok, true, result.failures.join('\n'));
  assert.equal(result.measurements.budgetVersion, 'p10-monaco-v1');
  assert.ok(result.measurements.mainJsGzipBytes > 0);
  assert.ok(result.measurements.totalInitialGzipBytes >= result.measurements.mainJsGzipBytes);

  const report = await writeRunReport({
    root,
    name: 'budget-red-green',
    scenarios: [{ id: 'bundle-budget', status: 'passed', timings: result.measurements }],
    budgets: [result],
  });
  assert.match(report.jsonPath, /e2e\/reports\/budget-red-green\.json$/);
  assert.match(report.markdownPath, /e2e\/reports\/budget-red-green\.md$/);
});
