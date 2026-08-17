import { readFile, writeFile } from 'node:fs/promises';
import { join } from 'node:path';
import { fileURLToPath } from 'node:url';

const root = fileURLToPath(new URL('..', import.meta.url));
const data = JSON.parse(await readFile(join(root, 'results/editor-spike-results.json'), 'utf8'));

const lines = [
  '# Monaco vs CodeMirror 6',
  '',
  `Decision: **${data.decision.editor ?? 'inconclusive'}**`,
  '',
  '| Criterion | Monaco | CodeMirror |',
  '|---|---:|---:|',
];

const byEditor = Object.fromEntries(data.results.map((item: any) => [item.editor, item]));
for (const [label, getter] of [
  ['Gzip bytes', (r: any) => r.build.gzipBytes],
  ['Worker count', (r: any) => r.build.workerCount],
  ['Open 1.5 MiB ms', (r: any) => r.runtime.openLimitMs.toFixed(1)],
  ['Typing p95 ms', (r: any) => r.runtime.typingP95Ms.toFixed(1)],
  ['Axe violations', (r: any) => r.accessibility.axeViolations],
  ['CSP relaxations', (r: any) => r.build.cspRelaxations.join('; ') || 'none'],
  ['Gate result', (r: any) => r.gates.eliminated ? r.gates.reasons.join('; ') : 'pass'],
] as const) {
  lines.push(`| ${label} | ${getter(byEditor.monaco)} | ${getter(byEditor.codemirror)} |`);
}

lines.push('', 'Raw compact JSON: `frontend/spikes/results/editor-spike-results.json`', '');
lines.push(`Audit vulnerabilities: ${JSON.stringify(data.audit.vulnerabilityCounts)}`, '');
if (data.audit.advisories.length > 0) {
  lines.push('| Package | Severity | Via | Fix |', '|---|---|---|---|');
  for (const advisory of data.audit.advisories) {
    lines.push(`| ${advisory.name} | ${advisory.severity} | ${advisory.via.join(', ')} | ${String(advisory.fixAvailable)} |`);
  }
  lines.push('');
}
await writeFile(join(root, 'REPORT.md'), `${lines.join('\n')}\n`);
console.log('wrote frontend/spikes/REPORT.md');
