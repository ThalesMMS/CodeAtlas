export type KnowledgeMode = 'wiki' | 'codemap';

export interface ArtifactMetadata {
  id: string;
  status: string;
  provider: string;
  model: string;
  promptVersion: string;
  outputSchema: string;
  snapshotId: string;
  contextPackHash: string;
  revision?: number;
  generatedAt: string;
  staleReasons: string[];
}

export interface SourceReference {
  path: string;
  startLine: number;
  endLine: number;
  label: string;
  snippet?: string;
}

export interface MermaidDiagram {
  source: string;
  title?: string;
  kind?: string;
  version?: string;
  sources: SourceReference[];
}

export interface WikiLink {
  slug: string;
  title: string;
  relation?: string;
}

export interface WikiPage {
  slug: string;
  title: string;
  kind: string;
  archetype: string;
  parentSlug: string;
  moduleSlug: string;
  scopePaths: string[];
  relatedPages: WikiLink[];
  markdown: string;
  provider: string;
  updatedAt: string;
  sourceHash: string;
  contextPackHash: string;
  policyVersion: string;
  outputSchemaVersion: string;
  diagram?: MermaidDiagram;
}

export interface DeepWikiCollection {
  status: string;
  snapshotId: string;
  pages: WikiPage[];
  lastError: string;
  artifact?: ArtifactMetadata;
  generation?: {
    id: string;
    currentPage: string;
    completedPages: number;
    totalPages: number;
  };
}

export interface WikiTreeNode {
  page: WikiPage;
  depth: number;
  children: WikiTreeNode[];
}

export interface CodemapFlowStep {
  label: string;
  nodeId: string;
  text: string;
  path: string;
  line: number;
  endLine: number;
  snippet: string;
}

export interface CodemapFlow {
  title: string;
  entryNodeId: string;
  steps: CodemapFlowStep[];
}

export interface Codemap {
  query: string;
  title: string;
  overview: string;
  flows: CodemapFlow[];
  trace: string[];
  nodes: Array<{ id: string; label: string; kind: string; group: string; path: string }>;
  edges: Array<{ id: string; source: string; target: string; type: string; path: string; line: number }>;
  provider: string;
  generatedAt: string;
  snapshotId: string;
  contextPackHash: string;
  policyVersion: string;
  outputSchemaVersion: string;
  diagram?: MermaidDiagram;
  artifact?: ArtifactMetadata;
}

export interface OutlineItem {
  id: string;
  title: string;
  level: number;
}

export interface RenderedMarkdown {
  html: string;
  diagrams: Array<{ id: string; source: string }>;
  sources: SourceReference[];
  outline: OutlineItem[];
}
