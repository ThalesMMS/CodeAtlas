import { parseKnowledgeLink, sourceLabel } from './knowledge-links';
import { renderKnowledgeMarkdown } from './knowledge-markdown';
import type {
  ArtifactMetadata, Codemap, CodemapFlow, CodemapFlowStep, DeepWikiCollection,
  MermaidDiagram, WikiPage, WikiTreeNode,
} from './knowledge-types';

export * from './knowledge-types';
export { parseKnowledgeLink, sourceLabel, renderKnowledgeMarkdown };

export function normalizeDeepWikiCollection(value: unknown): DeepWikiCollection {
  const record = asRecord(value);
  const pages = array(record.pages).map(normalizeWikiPage).filter((page) => page.slug);
  const generation = asRecord(record.generation);
  return {
    status: string(record.status) || (pages.length ? 'ready' : 'not_generated'),
    snapshotId: string(record.snapshotId),
    pages,
    lastError: string(record.lastError),
    artifact: normalizeArtifact(record.artifact),
    generation: Object.keys(generation).length ? {
      id: string(generation.id),
      currentPage: string(generation.currentPage),
      completedPages: number(generation.completedPages),
      totalPages: number(generation.totalPages),
    } : undefined,
  };
}

export function normalizeCodemap(value: unknown): Codemap {
  const envelope = asRecord(value);
  const record = Object.keys(asRecord(envelope.result)).length ? asRecord(envelope.result) : envelope;
  return {
    query: string(record.query),
    title: string(record.title) || 'Codemap',
    overview: string(record.overview),
    trace: strings(record.trace),
    flows: array(record.flows).map((value): CodemapFlow => {
      const flow = asRecord(value);
      return {
        title: string(flow.title) || 'Execution flow',
        entryNodeId: string(flow.entryNodeId),
        steps: array(flow.steps).map((value): CodemapFlowStep => {
          const step = asRecord(value);
          const line = number(step.line);
          return {
            label: string(step.label), nodeId: string(step.nodeId), text: string(step.text),
            path: string(step.path), line, endLine: number(step.endLine) || line,
            snippet: string(step.snippet),
          };
        }),
      };
    }),
    nodes: array(record.nodes).map((value) => {
      const node = asRecord(value);
      return { id: string(node.id), label: string(node.label), kind: string(node.kind), group: string(node.group), path: string(node.path) };
    }),
    edges: array(record.edges).map((value) => {
      const edge = asRecord(value);
      return { id: string(edge.id), source: string(edge.source), target: string(edge.target), type: string(edge.type), path: string(edge.path), line: number(edge.line) };
    }),
    provider: string(record.provider),
    generatedAt: string(record.generatedAt),
    snapshotId: string(record.snapshotId),
    contextPackHash: string(record.contextPackHash),
    policyVersion: string(record.policyVersion),
    outputSchemaVersion: string(record.outputSchemaVersion),
    diagram: normalizeDiagram(record.diagram),
    artifact: normalizeArtifact(record.artifact),
  };
}

export function buildWikiTree(pages: WikiPage[]): WikiTreeNode[] {
  const map = new Map<string, WikiTreeNode>();
  for (const page of pages) map.set(page.slug, { page, depth: 0, children: [] });
  const roots: WikiTreeNode[] = [];
  for (const node of map.values()) {
    const parent = node.page.parentSlug ? map.get(node.page.parentSlug) : undefined;
    if (parent && parent !== node) parent.children.push(node);
    else roots.push(node);
  }
  const compare = (a: WikiTreeNode, b: WikiTreeNode): number => {
    if (a.page.slug === 'overview') return -1;
    if (b.page.slug === 'overview') return 1;
    return a.page.slug.localeCompare(b.page.slug, undefined, { numeric: true });
  };
  const visited = new Set<string>();
  const walk = (nodes: WikiTreeNode[], depth: number): WikiTreeNode[] => nodes.sort(compare).flatMap((node) => {
    if (visited.has(node.page.slug)) return [];
    visited.add(node.page.slug);
    node.depth = depth;
    node.children = walk(node.children, depth + 1);
    return [node];
  });
  const tree = walk(roots, 0);
  for (const node of map.values()) if (!visited.has(node.page.slug)) tree.push(...walk([node], 0));
  return tree;
}

export function flattenWikiTree(tree: WikiTreeNode[]): WikiTreeNode[] {
  const output: WikiTreeNode[] = [];
  for (const node of tree) { output.push(node, ...flattenWikiTree(node.children)); }
  return output;
}

export function filterWikiTree(tree: WikiTreeNode[], query: string): WikiTreeNode[] {
  const needle = query.trim().toLowerCase();
  if (!needle) return tree;
  return tree.flatMap((node): WikiTreeNode[] => {
    const children = filterWikiTree(node.children, query);
    const text = `${node.page.title} ${node.page.slug} ${node.page.archetype}`.toLowerCase();
    return text.includes(needle) || children.length ? [{ ...node, children }] : [];
  });
}

export function formatCompactHash(value: string, size = 10): string {
  const clean = value.trim();
  return clean ? clean.slice(0, size) : '—';
}

export function statusLabel(status: string): string {
  const labels: Record<string, string> = {
    ready: 'Current', stale: 'Update available', generating: 'Generating', failed: 'Generation failed',
    not_generated: 'Not generated', queued: 'Queued', running: 'Running', succeeded: 'Completed',
    completed: 'Completed', cancelled: 'Cancelled', canceled: 'Cancelled',
  };
  return labels[status.toLowerCase()] ?? (status || 'Unknown');
}

function normalizeWikiPage(value: unknown): WikiPage {
  const record = asRecord(value);
  return {
    slug: string(record.slug), title: string(record.title) || string(record.slug), kind: string(record.kind),
    archetype: string(record.archetype || record.kind), parentSlug: string(record.parentSlug),
    moduleSlug: string(record.moduleSlug), scopePaths: strings(record.scopePaths),
    relatedPages: array(record.relatedPages).map((value) => {
      if (typeof value === 'string') return { slug: value, title: value };
      const link = asRecord(value);
      return { slug: string(link.slug), title: string(link.title) || string(link.slug), relation: string(link.relation) };
    }).filter((link) => link.slug),
    markdown: string(record.markdown), provider: string(record.provider), updatedAt: string(record.updatedAt),
    sourceHash: string(record.sourceHash), contextPackHash: string(record.contextPackHash),
    policyVersion: string(record.policyVersion), outputSchemaVersion: string(record.outputSchemaVersion),
    diagram: normalizeDiagram(record.diagram),
  };
}

function normalizeDiagram(value: unknown): MermaidDiagram | undefined {
  const record = asRecord(value);
  const source = string(record.source);
  if (!source) return undefined;
  return {
    source, title: string(record.title), kind: string(record.kind), version: string(record.version),
    sources: array(record.sources).map((value) => {
      const item = asRecord(value); const range = asRecord(item.range);
      const start = asRecord(range.start); const end = asRecord(range.end);
      const path = string(item.path); const startLine = number(item.startLine) || number(start.line);
      const endLine = number(item.endLine) || number(end.line) || startLine;
      return { path, startLine, endLine, label: string(item.label) || sourceLabel(path, startLine, endLine), snippet: string(item.snippet) };
    }).filter((item) => item.path),
  };
}

function normalizeArtifact(value: unknown): ArtifactMetadata | undefined {
  const record = asRecord(value);
  if (!Object.keys(record).length) return undefined;
  return {
    id: string(record.artifactId) || string(record.id), status: string(record.status), provider: string(record.provider),
    model: string(record.model), promptVersion: string(record.promptVersion), outputSchema: string(record.outputSchema),
    snapshotId: string(record.inputSnapshotId) || string(record.snapshotId), contextPackHash: string(record.contextPackHash),
    revision: optionalNumber(record.artifactRevision) ?? optionalNumber(record.revision),
    generatedAt: string(record.createdAt) || string(record.generatedAt), staleReasons: strings(record.staleReasons),
  };
}

function asRecord(value: unknown): Record<string, unknown> { return value && typeof value === 'object' && !Array.isArray(value) ? value as Record<string, unknown> : {}; }
function array(value: unknown): unknown[] { return Array.isArray(value) ? value : []; }
function string(value: unknown): string { return typeof value === 'string' ? value : ''; }
function strings(value: unknown): string[] { return array(value).filter((item): item is string => typeof item === 'string'); }
function number(value: unknown): number { return typeof value === 'number' && Number.isFinite(value) ? value : 0; }
function optionalNumber(value: unknown): number | undefined { return typeof value === 'number' && Number.isFinite(value) ? value : undefined; }
