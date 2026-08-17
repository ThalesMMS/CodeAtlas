export type SpikeLanguage = 'go' | 'javascript' | 'typescript' | 'tsx';

export interface SpikeDocument {
  path: string;
  language: SpikeLanguage;
  version: number;
  content: string;
  readonly?: boolean;
}

export interface EditorPosition {
  line: number;
  column: number;
}

export interface EditorRange {
  start: EditorPosition;
  end: EditorPosition;
}

export type SpikeDiagnosticSeverity = 'error' | 'warning' | 'info';

export interface SpikeDiagnostic {
  id: string;
  range: EditorRange;
  severity: SpikeDiagnosticSeverity;
  message: string;
  source: string;
  version: number;
}

export type SpikeSemanticTokenType = 'function' | 'type' | 'variable' | 'keyword';

export interface SpikeSemanticToken {
  id: string;
  range: EditorRange;
  type: SpikeSemanticTokenType;
  modifiers?: string[];
  version: number;
}

export interface SpikeHover {
  range: EditorRange;
  summary: string;
  evidenceIds: string[];
  seeMoreLabel: string;
  pinned?: boolean;
}

export interface EditorSpike {
  openDocument(input: SpikeDocument): Promise<void>;
  replaceContent(content: string, version: number): Promise<void>;
  insertText(text: string, version: number): Promise<void>;
  getContent(): string;
  getPosition(): EditorPosition;
  setPosition(position: EditorPosition): void;
  setDiagnostics(items: SpikeDiagnostic[]): void;
  setSemanticTokens(tokens: SpikeSemanticToken[]): void;
  showHover(input: SpikeHover): Promise<void>;
  revealRange(range: EditorRange): void;
  dispose(): void;
}

export interface SyncEvent {
  type: 'opened' | 'changed' | 'acked' | 'conflict' | 'saved' | 'closed';
  path: string;
  version: number;
  contentHash: string;
  message?: string;
}

export interface DocumentSyncController {
  open(input: SpikeDocument): Promise<SyncEvent>;
  queueLocalChange(content: string): number;
  flush(): Promise<SyncEvent>;
  applyAck(version: number): SyncEvent;
  simulateOutOfOrderAck(version: number): SyncEvent;
  simulateExternalConflict(contentHash: string): SyncEvent;
  save(): Promise<SyncEvent>;
  close(discard?: boolean): Promise<SyncEvent>;
  snapshot(): SpikeDocument & { contentHash: string; dirty: boolean; conflict: boolean };
  subscribe(listener: (event: SyncEvent) => void): () => void;
}

export interface EditorBenchmarkMetrics {
  editor: 'monaco' | 'codemirror';
  version: string;
  build: {
    rawBytes: number;
    gzipBytes: number;
    chunks: number;
    workerBytes: number;
    workerCount: number;
    coldBuildMs: number;
    warmBuildMs: number;
    dependencyCount: number;
    licenses: Record<string, string>;
    cspRelaxations: string[];
  };
  runtime: {
    firstUsableMs: number;
    open100KiBMs: number;
    open1MiBMs: number;
    openLimitMs: number;
    tabSwitchP95Ms: number;
    typingP50Ms: number;
    typingP95Ms: number;
    semanticTokensMs: number;
    diagnosticsMs: number;
    revealRangeMs: number;
    memoryOneModelBytes: number | null;
    memoryThirtyModelsBytes: number | null;
    memoryAfterDisposeBytes: number | null;
    longTasks: number;
  };
  accessibility: {
    axeViolations: number;
    keyboard: 'pass' | 'fail';
    screenReaderNotes: string;
    highContrast: 'pass' | 'needs-work';
    reducedMotion: 'pass' | 'needs-work';
    zoom200: 'pass' | 'needs-work';
    imeComposition: 'pass' | 'needs-work';
  };
  csp: {
    passed: boolean;
    externalRequests: string[];
    requiresUnsafeEval: boolean;
    requiresUnsafeInline: boolean;
    requiresBlobWorker: boolean;
  };
  gates: {
    eliminated: boolean;
    reasons: string[];
  };
}

export interface EditorSpikeAuditSummary {
  vulnerabilityCounts: Record<string, number>;
  advisories: Array<{
    name: string;
    severity: string;
    via: string[];
    fixAvailable: string | boolean;
  }>;
}

export interface EditorSpikeResultsPayload {
  generatedAt: string;
  environment: {
    node: string;
    platform: string;
    arch: string;
    browser: string;
  };
  results: EditorBenchmarkMetrics[];
  audit: EditorSpikeAuditSummary;
  decision: {
    editor: 'monaco' | 'codemirror' | null;
    reason: string;
    inconclusive?: true;
  };
}
