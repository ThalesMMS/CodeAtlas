import fs from 'node:fs/promises';
import path from 'node:path';

export async function writeRunReport({ root, name, scenarios = [], budgets = [], environment = {} }) {
  const reportsDir = path.join(root, 'e2e/reports');
  await fs.mkdir(reportsDir, { recursive: true });
  const payload = {
    reportVersion: 'p10-v1',
    name,
    generatedAt: new Date().toISOString(),
    environment,
    scenarios,
    budgets,
  };
  const jsonPath = path.join(reportsDir, `${name}.json`);
  const markdownPath = path.join(reportsDir, `${name}.md`);
  await fs.writeFile(jsonPath, `${JSON.stringify(payload, null, 2)}\n`);
  await fs.writeFile(markdownPath, renderMarkdown(payload));
  return { jsonPath, markdownPath, payload };
}

function renderMarkdown(payload) {
  const lines = [
    `# ${payload.name} E2E report`,
    '',
    `- reportVersion: ${payload.reportVersion}`,
    `- generatedAt: ${payload.generatedAt}`,
    '',
    '## Scenarios',
    '',
    '| scenario | status | p50/p95/timing |',
    '|---|---|---:|',
  ];
  for (const scenario of payload.scenarios) {
    lines.push(`| ${scenario.id} | ${scenario.status} | ${formatTimings(scenario.timings)} |`);
  }
  if (payload.scenarios.length === 0) {
    lines.push('| none | skipped |  |');
  }
  lines.push('', '## Budgets', '', '| budget | status | details |', '|---|---|---|');
  for (const budget of payload.budgets) {
    lines.push(`| ${budget.measurements?.budgetVersion ?? 'unknown'} | ${budget.ok ? 'passed' : 'failed'} | ${budget.failures?.join('; ') ?? ''} |`);
  }
  if (payload.budgets.length === 0) {
    lines.push('| none | skipped |  |');
  }
  lines.push('', '## Environment', '');
  for (const [key, value] of Object.entries(payload.environment ?? {})) {
    lines.push(`- ${key}: ${value}`);
  }
  lines.push('');
  return `${lines.join('\n')}\n`;
}

function formatTimings(timings = {}) {
  const entries = Object.entries(timings);
  if (entries.length === 0) {
    return '';
  }
  return entries.map(([key, value]) => `${key}=${value}`).join(', ');
}
