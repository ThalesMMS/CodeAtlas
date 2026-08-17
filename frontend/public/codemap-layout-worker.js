'use strict';

let activeRequestId = '';

self.onmessage = (event) => {
  const message = event.data || {};
  if (message.type === 'cancel') {
    if (!message.requestId || message.requestId === activeRequestId) activeRequestId = '';
    return;
  }
  if (message.type !== 'layout') return;

  const request = message.request || {};
  activeRequestId = String(request.requestId || '');
  const started = Date.now();
  try {
    const result = layout(request, started);
    if (activeRequestId === result.requestId) self.postMessage({ type: 'layout-result', result });
  } catch (error) {
    self.postMessage({
      type: 'layout-error',
      requestId: String(request.requestId || ''),
      inputHash: String(request.inputHash || ''),
      error: error && error.message ? error.message : 'layout failed',
    });
  }
};

function layout(request, started) {
  validateRequest(request);
  const options = {
    columnSpacing: positiveNumber(request.options.columnSpacing, 290),
    rowSpacing: positiveNumber(request.options.rowSpacing, 118),
    nodeWidth: positiveNumber(request.options.nodeWidth, 230),
    nodeHeight: positiveNumber(request.options.nodeHeight, 78),
  };
  const warnings = [];
  const nodesById = new Map(request.nodes.map((node) => [node.id, node]));
  const groupOrder = buildGroupOrder(request);
  const columns = groupOrder.map((group) => ({
    group,
    nodes: request.nodes
      .filter((node) => (node.groupId || 'Other') === group.id)
      .sort(compareLayoutNodes),
  })).filter((column) => column.nodes.length > 0);
  if (!columns.length) {
    columns.push({ group: { id: 'Other', order: 0 }, nodes: [...request.nodes].sort(compareLayoutNodes) });
  }

  const positioned = [];
  const positions = new Map();
  let maxRows = 1;
  columns.forEach((column, columnIndex) => {
    maxRows = Math.max(maxRows, column.nodes.length);
    const x = 32 + columnIndex * options.columnSpacing;
    column.nodes.forEach((node, rowIndex) => {
      if (Date.now() - started > 4900) warnings.push('layout timeout guard reached');
      const y = 52 + rowIndex * options.rowSpacing;
      const positionedNode = {
        id: node.id,
        groupId: node.groupId || 'Other',
        x,
        y,
        width: options.nodeWidth,
        height: options.nodeHeight,
        external: Boolean(node.external),
      };
      positioned.push(positionedNode);
      positions.set(node.id, positionedNode);
    });
  });

  const edgeRoutes = request.edges.map((edge, index) => {
    const source = positions.get(edge.source);
    const target = positions.get(edge.target);
    if (!source || !target || !nodesById.has(edge.source) || !nodesById.has(edge.target)) return null;
    const backEdge = target.x <= source.x;
    if (backEdge) warnings.push(`cycle/back-edge ${edge.id}`);
    const parallel = parallelInfo(request.edges, edge, index);
    const offset = (parallel.index - (parallel.count - 1) / 2) * 10;
    const x1 = source.x + source.width;
    const y1 = source.y + source.height / 2 + offset;
    const x2 = target.x;
    const y2 = target.y + target.height / 2 + offset;
    const bend = Math.max(35, Math.abs(x2 - x1) * 0.45);
    const direction = x2 >= x1 ? 1 : -1;
    return {
      id: edge.id,
      source: edge.source,
      target: edge.target,
      type: edge.type,
      x1,
      y1,
      c1x: x1 + bend * direction,
      c1y: y1,
      c2x: x2 - bend * direction,
      c2y: y2,
      x2,
      y2,
      backEdge,
      parallelIndex: parallel.index,
      parallelCount: parallel.count,
    };
  }).filter(Boolean);

  return {
    requestId: String(request.requestId),
    inputHash: String(request.inputHash),
    nodes: positioned,
    edgeRoutes,
    bounds: {
      x: 0,
      y: 0,
      width: Math.max(680, 80 + columns.length * options.columnSpacing),
      height: Math.max(420, 100 + maxRows * options.rowSpacing),
    },
    warnings: unique(warnings),
  };
}

function validateRequest(request) {
  if (!request || typeof request !== 'object') throw new Error('invalid layout request');
  if (!request.requestId || !request.inputHash) throw new Error('missing request identity');
  if (!Array.isArray(request.nodes) || !Array.isArray(request.edges) || !Array.isArray(request.groups)) {
    throw new Error('layout arrays are required');
  }
  if (request.nodes.length > 500 || request.edges.length > 1000) throw new Error('layout payload exceeds worker limits');
  const nodeIds = new Set();
  request.nodes.forEach((node, index) => {
    if (!node || typeof node !== 'object' || typeof node.id !== 'string' || !node.id) throw new Error(`invalid node ${index}`);
    if (nodeIds.has(node.id)) throw new Error(`duplicate node ${node.id}`);
    nodeIds.add(node.id);
  });
  request.edges.forEach((edge, index) => {
    if (!edge || typeof edge !== 'object' || typeof edge.id !== 'string' || !edge.id) throw new Error(`invalid edge ${index}`);
    if (!nodeIds.has(edge.source) || !nodeIds.has(edge.target)) throw new Error(`edge ${edge.id} references missing node`);
  });
}

function buildGroupOrder(request) {
  const groups = request.groups.map((group, index) => ({
    id: String(group.id || `group:${index}`),
    order: Number(group.order) || index,
  }));
  const known = new Set(groups.map((group) => group.id));
  request.nodes.forEach((node) => {
    const groupId = node.groupId || 'Other';
    if (!known.has(groupId)) {
      groups.push({ id: groupId, order: groups.length });
      known.add(groupId);
    }
  });
  return groups.sort((a, b) => {
    if (a.id === 'External') return 1;
    if (b.id === 'External') return -1;
    if (a.order !== b.order) return a.order - b.order;
    return a.id.localeCompare(b.id);
  });
}

function compareLayoutNodes(a, b) {
  if (a.traceRank !== b.traceRank) return a.traceRank - b.traceRank;
  if (b.relevance !== a.relevance) return b.relevance - a.relevance;
  return a.id.localeCompare(b.id);
}

function parallelInfo(edges, edge, index) {
  const matching = edges.filter((item) => item.source === edge.source && item.target === edge.target);
  return {
    index: matching.findIndex((item) => item.id === edge.id || edges.indexOf(item) === index),
    count: matching.length,
  };
}

function positiveNumber(value, fallback) {
  const number = Number(value);
  return Number.isFinite(number) && number > 0 ? number : fallback;
}

function unique(items) {
  return Array.from(new Set(items)).slice(0, 20);
}
