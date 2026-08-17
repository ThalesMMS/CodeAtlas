// A deliberately small, local renderer for the exact Mermaid subset emitted by
// backend/internal/diagram. It is lazy-loaded, accepts no directives/styles/
// links, and emits CSP-safe SVG without inline CSS or event handlers.

type FlowNode = { id: string; label: string; group: number };
type FlowGroup = { id: number; label: string; nodes: FlowNode[] };
type FlowEdge = { source: string; target: string; label: string; dashed: boolean };
type Point = { x: number; y: number; width: number; height: number };

const FLOW_GROUP_WIDTH = 250;
const FLOW_NODE_WIDTH = 210;
const FLOW_NODE_HEIGHT = 48;
const FLOW_GROUP_GAP = 24;
const FLOW_NODE_GAP = 22;

export function renderMermaidSubset(source: string, renderId = 'codeatlas-mermaid'): string {
  const lines = source.replace(/\r\n/g, '\n').trim().split('\n');
  if (lines[0] === 'graph TD') return renderFlowchart(lines.slice(1), renderId);
  if (lines[0] === 'sequenceDiagram') return renderSequence(lines.slice(1), renderId);
  throw new Error('unsupported Mermaid subset');
}

function renderFlowchart(lines: string[], renderId: string): string {
  const groups: FlowGroup[] = [];
  const edges: FlowEdge[] = [];
  let current: FlowGroup | null = null;
  for (const line of lines) {
    let match = line.match(/^  subgraph g(\d+)\["([^"<>]{1,200})"\]$/u);
    if (match) {
      current = { id: Number(match[1]!), label: decodeEntities(match[2]!), nodes: [] };
      groups.push(current);
      continue;
    }
    match = line.match(/^    n(\d+)\["([^"<>]{1,240})"\]$/u);
    if (match && current) {
      current.nodes.push({ id: `n${match[1]!}`, label: decodeEntities(match[2]!), group: current.id });
      continue;
    }
    if (line === '    direction TB') continue;
    if (line === '  end') {
      current = null;
      continue;
    }
    match = line.match(/^  (n\d+) -->\|([\p{L}\p{N}\s._/()#-]{1,80})\| (n\d+)$/u);
    if (match) {
      edges.push({ source: match[1]!, target: match[3]!, label: match[2]!.trim(), dashed: false });
      continue;
    }
    match = line.match(/^  (n\d+) -\. ([\p{L}\p{N}\s._/()#-]{1,80}) \.-> (n\d+)$/u);
    if (match) {
      edges.push({ source: match[1]!, target: match[3]!, label: match[2]!.trim(), dashed: true });
      continue;
    }
    throw new Error('invalid flowchart subset line');
  }
  if (!groups.length || groups.some((group) => !group.nodes.length)) throw new Error('invalid flowchart subset');

  const maxRows = Math.max(...groups.map((group) => group.nodes.length));
  const width = 32 + groups.length * FLOW_GROUP_WIDTH + (groups.length - 1) * FLOW_GROUP_GAP;
  const height = 72 + maxRows * (FLOW_NODE_HEIGHT + FLOW_NODE_GAP) + 30;
  const positions = new Map<string, Point>();
  const groupSVG: string[] = [];
  groups.forEach((group, groupIndex) => {
    const x = 16 + groupIndex * (FLOW_GROUP_WIDTH + FLOW_GROUP_GAP);
    const groupHeight = 46 + group.nodes.length * (FLOW_NODE_HEIGHT + FLOW_NODE_GAP);
    groupSVG.push(`<g class="cluster"><rect x="${x}" y="12" width="${FLOW_GROUP_WIDTH}" height="${groupHeight}" rx="8"/><text x="${x + 12}" y="34">${escapeXML(group.label)}</text></g>`);
    group.nodes.forEach((node, nodeIndex) => {
      const point = {
        x: x + (FLOW_GROUP_WIDTH - FLOW_NODE_WIDTH) / 2,
        y: 48 + nodeIndex * (FLOW_NODE_HEIGHT + FLOW_NODE_GAP),
        width: FLOW_NODE_WIDTH,
        height: FLOW_NODE_HEIGHT,
      };
      positions.set(node.id, point);
      groupSVG.push(`<g class="node" data-node-id="${escapeXML(node.id)}"><rect x="${point.x}" y="${point.y}" width="${point.width}" height="${point.height}" rx="7"/><text x="${point.x + point.width / 2}" y="${point.y + 29}" text-anchor="middle">${escapeXML(node.label)}</text></g>`);
    });
  });

  const marker = safeIdentifier(renderId) + '-arrow';
  const edgeSVG: string[] = [];
  for (const edge of edges) {
    const sourcePoint = positions.get(edge.source);
    const targetPoint = positions.get(edge.target);
    if (!sourcePoint || !targetPoint) throw new Error('flowchart edge references a missing node');
    const startX = sourcePoint.x + sourcePoint.width / 2;
    const startY = sourcePoint.y + sourcePoint.height;
    const endX = targetPoint.x + targetPoint.width / 2;
    const endY = targetPoint.y;
    const midY = startY + (endY - startY) / 2;
    const path = `M ${startX} ${startY} C ${startX} ${midY}, ${endX} ${midY}, ${endX} ${endY}`;
    edgeSVG.push(`<g class="edgePath${edge.dashed ? ' dashed' : ''}"><path d="${path}" marker-end="url(#${marker})"${edge.dashed ? ' stroke-dasharray="6 4"' : ''}/><text x="${(startX + endX) / 2}" y="${midY - 5}" text-anchor="middle">${escapeXML(edge.label)}</text></g>`);
  }

  return `<svg xmlns="http://www.w3.org/2000/svg" viewBox="0 0 ${width} ${height}" aria-hidden="true"><defs><marker id="${marker}" viewBox="0 0 10 10" refX="9" refY="5" markerWidth="7" markerHeight="7" orient="auto-start-reverse"><path d="M 0 0 L 10 5 L 0 10 z"/></marker></defs>${groupSVG.join('')}${edgeSVG.join('')}</svg>`;
}

function renderSequence(lines: string[], renderId: string): string {
  const participants: Array<{ id: string; label: string }> = [];
  const messages: Array<{ source: string; target: string; label: string }> = [];
  for (const line of lines) {
    let match = line.match(/^  participant (p\d+) as ([\p{L}\p{N}\s._/()#-]{1,240})$/u);
    if (match) {
      participants.push({ id: match[1]!, label: match[2]!.trim() });
      continue;
    }
    match = line.match(/^  (p\d+)->>(p\d+): calls ([\p{L}\p{N}\s._/()#-]{1,160})$/u);
    if (match) {
      messages.push({ source: match[1]!, target: match[2]!, label: `calls ${match[3]!.trim()}` });
      continue;
    }
    throw new Error('invalid sequence subset line');
  }
  if (participants.length < 2 || !messages.length) throw new Error('invalid sequence subset');
  const indexByID = new Map(participants.map((participant, index) => [participant.id, index]));
  const columnWidth = 190;
  const width = Math.max(520, 40 + participants.length * columnWidth);
  const height = 104 + messages.length * 58 + 36;
  const marker = safeIdentifier(renderId) + '-arrow';
  const parts: string[] = [`<defs><marker id="${marker}" viewBox="0 0 10 10" refX="9" refY="5" markerWidth="7" markerHeight="7" orient="auto-start-reverse"><path d="M 0 0 L 10 5 L 0 10 z"/></marker></defs>`];
  participants.forEach((participant, index) => {
    const center = 40 + index * columnWidth + columnWidth / 2;
    parts.push(`<g class="actor" data-participant-id="${escapeXML(participant.id)}"><rect x="${center - 78}" y="16" width="156" height="42" rx="7"/><text x="${center}" y="42" text-anchor="middle">${escapeXML(participant.label)}</text><line class="actor-line" x1="${center}" y1="58" x2="${center}" y2="${height - 20}" stroke-dasharray="5 5"/></g>`);
  });
  messages.forEach((message, index) => {
    const sourceIndex = indexByID.get(message.source);
    const targetIndex = indexByID.get(message.target);
    if (sourceIndex == null || targetIndex == null) throw new Error('sequence edge references a missing participant');
    const sourceX = 40 + sourceIndex * columnWidth + columnWidth / 2;
    const targetX = 40 + targetIndex * columnWidth + columnWidth / 2;
    const y = 92 + index * 58;
    parts.push(`<g class="message"><line class="messageLine0" x1="${sourceX}" y1="${y}" x2="${targetX}" y2="${y}" marker-end="url(#${marker})"/><text x="${(sourceX + targetX) / 2}" y="${y - 8}" text-anchor="middle">${escapeXML(message.label)}</text></g>`);
  });
  return `<svg xmlns="http://www.w3.org/2000/svg" viewBox="0 0 ${width} ${height}" aria-hidden="true">${parts.join('')}</svg>`;
}

function decodeEntities(value: string): string {
  return value
    .replaceAll('&quot;', '"')
    .replaceAll('&gt;', '>')
    .replaceAll('&lt;', '<')
    .replaceAll('&amp;', '&');
}

function escapeXML(value: string): string {
  return value
    .replaceAll('&', '&amp;')
    .replaceAll('<', '&lt;')
    .replaceAll('>', '&gt;')
    .replaceAll('"', '&quot;')
    .replaceAll("'", '&apos;');
}

function safeIdentifier(value: string): string {
  const normalized = value.replace(/[^a-z0-9_-]/gi, '-').slice(0, 120);
  return normalized || 'codeatlas-mermaid';
}
