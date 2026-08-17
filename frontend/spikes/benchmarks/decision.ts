import type { EditorBenchmarkMetrics } from '../shared/types';

export interface GateInput {
  externalRequests: Iterable<string>;
  axeViolations: number;
  cspRelaxations: string[];
  cspViolations: Iterable<string>;
}

export function gateResults(input: GateInput) {
  const reasons: string[] = [];
  if (Array.from(input.externalRequests).length > 0) reasons.push('external network request');
  if (input.axeViolations > 4) reasons.push('accessibility smoke gate exceeded');
  if (Array.from(input.cspViolations).length > 0) reasons.push('CSP violation under relaxed editor policy');
  if (requiresUnsafeScriptCsp(input.cspRelaxations)) reasons.push('CSP unsafe eval/inline script required');
  return { eliminated: reasons.length > 0, reasons };
}

export function decide(results: EditorBenchmarkMetrics[]) {
  const viable = results.filter((result) => !result.gates.eliminated);
  if (viable.length === 0) {
    return { editor: null, reason: 'no editor passed gates', inconclusive: true as const };
  }
  if (viable.length === 1) return { editor: viable[0].editor, reason: 'only editor without eliminatory gate' };
  const sorted = [...viable].sort((a, b) => score(b) - score(a));
  return { editor: sorted[0].editor, reason: 'weighted score: bundle/runtime/accessibility/CSP/maintenance' };
}

export function score(result: EditorBenchmarkMetrics): number {
  return 100
    - result.build.gzipBytes / 20_000
    - result.runtime.typingP95Ms / 10
    - result.runtime.openLimitMs / 100
    - result.accessibility.axeViolations * 5
    - result.build.cspRelaxations.length * 10;
}

function requiresUnsafeScriptCsp(cspRelaxations: string[]): boolean {
  return cspRelaxations.some((relaxation) => {
    const lower = relaxation.toLowerCase();
    return lower.includes('unsafe-eval') || (lower.startsWith('script-src') && lower.includes('unsafe-inline'));
  });
}
