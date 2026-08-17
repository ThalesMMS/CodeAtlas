import http from 'node:http';

const defaultVector = Object.freeze([0.11, 0.23, 0.37, 0.41, 0.53, 0.67, 0.79, 0.83]);

export async function startFakeProvider({ scenario = 'happy-path', apiPrefix = '', authLabels = {} } = {}) {
  let activeScenario = scenario;
  const normalizedPrefix = apiPrefix === '' ? '' : `/${String(apiPrefix).replace(/^\/+|\/+$/gu, '')}`;
  const requests = [];
  const server = http.createServer(async (request, response) => {
    const startedAt = Date.now();
    const rawBody = await readRequestBody(request);
    let body = {};
    try {
      body = rawBody === '' ? {} : JSON.parse(rawBody);
    } catch {
      body = { malformed: rawBody };
    }
    requests.push({
      method: request.method,
      path: request.url,
      scenario: activeScenario,
      headers: sanitizeHeaders(request.headers),
      authorizationLabel: authLabels[request.headers.authorization] ?? (request.headers.authorization ? 'unrecognized' : 'missing'),
      body,
      startedAt,
    });

    if (request.method !== 'POST') {
      return writeJSON(response, 405, { error: { message: 'method not allowed' } });
    }
    if (request.url === `${normalizedPrefix}/chat/completions`) {
      return handleChat(response, body, activeScenario);
    }
    if (request.url === `${normalizedPrefix}/embeddings`) {
      return handleEmbeddings(response, body, activeScenario);
    }
    return writeJSON(response, 404, { error: { message: 'unknown fake provider path' } });
  });

  await listen(server);
  const address = server.address();
  const origin = `http://127.0.0.1:${address.port}`;
  const baseURL = `${origin}${normalizedPrefix}`;

  return {
    baseURL,
    requests,
    close: () => closeServer(server),
    async withScenario(nextScenario, fn) {
      const previous = activeScenario;
      activeScenario = nextScenario;
      try {
        return await fn();
      } finally {
        activeScenario = previous;
      }
    },
    async probeChat() {
      const response = await fetch(`${baseURL}/chat/completions`, {
        method: 'POST',
        headers: { 'content-type': 'application/json' },
        body: JSON.stringify({ model: 'fake-codeatlas', messages: [{ role: 'user', content: 'ping' }], max_tokens: 1 }),
      });
      return { status: response.status, body: await response.text() };
    },
  };
}

function handleChat(response, body, scenario) {
  if (scenario === 'schema-keyword-compatibility' && containsJSONKeyword(body.response_format, 'uniqueItems')) {
    return writeJSON(response, 400, {
      error: {
        message: 'Grammar error: Unimplemented keys: ["uniqueItems"]',
        type: 'BadRequestError',
        code: 400,
      },
    });
  }
  switch (scenario) {
    case 'chat-401':
      return writeJSON(response, 401, { error: { message: 'unauthorized' } });
    case 'chat-429':
      return writeJSON(response, 429, { error: { message: 'rate limited' } });
    case 'chat-500':
      return writeJSON(response, 500, { error: { message: 'server error' } });
    case 'chat-invalid-json':
      response.writeHead(200, { 'content-type': 'application/json' });
      return response.end('{not json');
    case 'chat-timeout':
      return setTimeout(() => writeJSON(response, 200, chatResponse('late')), 10_000);
    case 'chat-slow':
      return setTimeout(() => writeJSON(response, 200, chatResponse(chatContentFor(body))), 800);
    case 'chat-truncated':
      return writeJSON(response, 200, chatResponse(JSON.stringify(validExplanation()), { finishReason: 'length' }));
    case 'chat-large':
      return writeJSON(response, 200, chatResponse(JSON.stringify(validExplanation('x'.repeat(32_768)))));
    case 'evidence-invalid':
      return writeJSON(response, 200, chatResponse(JSON.stringify(validExplanation('Invalid evidence response', ['E9999']))));
    default:
      return writeJSON(response, 200, chatResponse(chatContentFor(body)));
  }
}

function containsJSONKeyword(value, keyword) {
  if (Array.isArray(value)) return value.some((child) => containsJSONKeyword(child, keyword));
  if (!value || typeof value !== 'object') return false;
  return Object.entries(value).some(([key, child]) => key === keyword || containsJSONKeyword(child, keyword));
}

function handleEmbeddings(response, body, scenario) {
  if (scenario === 'embeddings-invalid') {
    return writeJSON(response, 200, { data: [{ index: 0, embedding: [] }] });
  }
  const input = Array.isArray(body.input) ? body.input : [body.input ?? ''];
  const data = input.map((value, index) => ({
    index,
    embedding: vectorFor(String(value)),
  }));
  if (scenario === 'embeddings-shuffled') {
    data.reverse();
  }
  return writeJSON(response, 200, { data, model: body.model ?? 'fake-embedding' });
}

function chatContentFor(body) {
  if (Number(body.max_tokens) <= 1) {
    return 'ok';
  }
  const systemPrompt = Array.isArray(body.messages)
    ? body.messages.find((message) => message?.role === 'system')?.content ?? ''
    : '';
  if (String(systemPrompt).includes('codemap-narrative/v2')) {
    const structure = codemapStructureFrom(body);
    const nodes = new Map((Array.isArray(structure?.nodes) ? structure.nodes : []).map((node) => [node.id, node]));
    const suggested = Array.isArray(structure?.suggestedFlows) ? structure.suggestedFlows.slice(0, 4) : [];
    const flows = suggested.map((flow, flowIndex) => ({
      title: flow.title,
      entryNodeId: flow.entryNodeId,
      steps: (Array.isArray(flow.nodeIds) ? flow.nodeIds : []).slice(0, 8).map((nodeId, stepIndex) => ({
        label: `${flowIndex + 1}${String.fromCharCode(97 + stepIndex)}`,
        nodeId,
        text: codemapStepText(nodes.get(nodeId), flow.title, stepIndex),
      })),
    })).filter((flow) => flow.entryNodeId && flow.steps.length > 0);
    return JSON.stringify({
      schemaVersion: 'codemap-narrative/v2',
      title: 'Fake deterministic Codemap',
      overview: 'The factual graph is organized into entrypoint flows by the backend.',
      motivation: 'Separate dependency wiring from request processing.',
      details: 'Each validated step is anchored to backend-owned source bytes.',
      trace: flows.flatMap((flow) => flow.steps.map((step) => step.nodeId)).slice(0, 16),
      flows,
      claims: [],
      inferences: [],
      uncertainties: [],
    });
  }
  if (String(systemPrompt).includes('wiki-plan/v1')) {
    return JSON.stringify(wikiPlanFromInventory(wikiInventoryFrom(body)));
  }
  if (String(systemPrompt).includes('wiki-page/v4')) {
    const pack = contextPackFrom(body);
    const plan = wikiPagePlanFrom(body);
    const evidence = Array.isArray(pack?.evidence) ? pack.evidence.filter((item) => typeof item?.id === 'string') : [];
    const primary = evidence[0];
    const code = evidence.find((item) => item?.kind === 'ast' && String(item?.content ?? '').trim() !== '');
    const claims = primary ? [{ text: `${plan?.title ?? 'This page'} is grounded in indexed ${primary.kind ?? 'repository'} evidence.`, evidenceIds: [primary.id] }] : [];
    const tables = primary ? [{ kind: 'table', columns: ['Element', 'Role'], rows: [[String(primary.title ?? 'Evidence'), String(plan?.archetype ?? 'page')]], evidenceIds: [primary.id] }] : [];
    return JSON.stringify({
      schemaVersion: 'wiki-page/v4',
      title: plan?.title ?? 'Fake DeepWiki page',
      sections: [{ heading: wikiHeadingFor(plan?.archetype), claims, codeEvidenceIds: code ? [code.id] : [], tables }],
      relatedPages: Array.isArray(plan?.allowedRelatedPages) ? plan.allowedRelatedPages.slice(0, 2) : [],
      inferences: [],
      limitations: [],
    });
  }
  if (String(systemPrompt).includes('explanation/v2')) {
    return JSON.stringify(explanationFromPack(contextPackFrom(body), {
      seeMore: String(systemPrompt).includes('See Also'),
    }));
  }
  return JSON.stringify(validExplanation(undefined, []));
}

function wikiPlanFromInventory(inventory) {
  const knownPaths = Array.isArray(inventory?.knownPaths) ? inventory.knownPaths : [];
  const modules = Array.isArray(inventory?.modules) ? inventory.modules : [];
  const all = knownPaths.length > 0 ? knownPaths : ['README.md'];
  const rootConfigs = knownPaths.filter((value) => !value.includes('/') && /^(go\.mod|package\.json|README|Makefile)/u.test(value));
  const entryPaths = modules.filter((module) => module?.entrypoint).flatMap((module) => module.paths ?? []);
  const pages = [
    { slug: 'overview', title: 'Overview', parentSlug: '', scopePaths: all, archetype: 'overview' },
    { slug: '1-getting-started', title: 'Getting started', parentSlug: 'overview', scopePaths: [...new Set([...rootConfigs, ...entryPaths])].slice(0, 30).length ? [...new Set([...rootConfigs, ...entryPaths])].slice(0, 30) : all, archetype: 'getting-started' },
    { slug: '2-architecture-overview', title: 'Architecture overview', parentSlug: 'overview', scopePaths: all, archetype: 'architecture-overview' },
  ];
  let section = 3;
  for (const module of modules) {
    if (pages.length >= 21) break;
    const paths = Array.isArray(module?.paths) ? module.paths : [];
    if (paths.length === 0) continue;
    const frontend = String(module.language ?? '').includes('script') || String(module.name ?? '').startsWith('web');
    const slug = `${section}-${frontend ? 'frontend' : 'backend'}-${slugPart(module.name)}`;
    pages.push({ slug, title: `${frontend ? 'Frontend' : 'Backend'}: ${module.name}`, parentSlug: 'overview', scopePaths: paths, archetype: frontend ? 'frontend' : 'module' });
    if (!frontend && String(module.name ?? '').startsWith('internal/')) {
      const layers = [
        ['domain-model', 'Domain model', /model|domain|entity|types/u],
        ['service-layer', 'Service layer', /service|usecase|application/u],
        ['repository-layer', 'Repository layer', /repository|store|persistence/u],
        ['http-handler', 'HTTP handler', /http|handler|controller|route/u],
      ];
      layers.forEach(([suffix, title, matcher], index) => {
        const scoped = paths.filter((value) => matcher.test(value.split('/').at(-1) ?? '') && !value.includes('_test.'));
        if (scoped.length > 0 && pages.length < 23) pages.push({ slug: `${section}.${index + 1}-${suffix}`, title, parentSlug: slug, scopePaths: scoped, archetype: 'layer' });
      });
    }
    section += 1;
  }
  const tests = knownPaths.filter((value) => value.includes('_test.') || value.includes('.test.'));
  if (tests.length > 0 && pages.length < 24) pages.push({ slug: `${section}-testing`, title: 'Testing', parentSlug: 'overview', scopePaths: tests, archetype: 'testing' });
  if (pages.length < 25) pages.push({ slug: `${section + 1}-glossary`, title: 'Glossary', parentSlug: 'overview', scopePaths: all, archetype: 'glossary' });
  return { schemaVersion: 'wiki-plan/v1', pages };
}

function wikiHeadingFor(archetype) {
  if (archetype === 'getting-started') return 'Build and run';
  if (archetype === 'testing') return 'Testing patterns';
  if (archetype === 'architecture-overview') return 'Main architecture flow';
  return 'Responsibilities and flow';
}

function slugPart(value) {
  return String(value ?? 'module').toLowerCase().replace(/[^a-z0-9]+/gu, '-').replace(/^-|-$/gu, '');
}

function wikiInventoryFrom(body) {
  return taggedJSONFrom(body, 'CODEATLAS_WIKI_INVENTORY');
}

function wikiPagePlanFrom(body) {
  return taggedJSONFrom(body, 'CODEATLAS_WIKI_PAGE_PLAN');
}

function taggedJSONFrom(body, tag) {
  const userPrompt = Array.isArray(body.messages)
    ? body.messages.find((message) => message?.role === 'user')?.content ?? ''
    : '';
  const start = `<${tag}>\n`;
  const end = `\n</${tag}>`;
  const value = String(userPrompt);
  const startIndex = value.indexOf(start);
  const endIndex = value.indexOf(end, startIndex + start.length);
  if (startIndex < 0 || endIndex < 0) return null;
  try {
    return JSON.parse(value.slice(startIndex + start.length, endIndex));
  } catch {
    return null;
  }
}

function codemapStepText(node, flowTitle, stepIndex) {
  const label = String(node?.label ?? 'code step');
  if (stepIndex === 0) return `Starts ${flowTitle.toLowerCase()} at ${label}`;
  if (label === 'NewMemoryRepository') return 'Creates the repository';
  if (label === 'NewService') return 'Injects the application service';
  if (label === 'NewHandler') return 'Builds the HTTP handler';
  if (label === 'Submit') return 'Validates and submits the order';
  if (label === 'Save') return 'Persists the order';
  return `Continues through ${label}`;
}

function codemapStructureFrom(body) {
  const userPrompt = Array.isArray(body.messages)
    ? body.messages.find((message) => message?.role === 'user')?.content ?? ''
    : '';
  const match = String(userPrompt).match(/<CODEATLAS_CODEMAP_STRUCTURE>\n([\s\S]*?)\n<\/CODEATLAS_CODEMAP_STRUCTURE>/u);
  if (!match) return null;
  try {
    return JSON.parse(match[1]);
  } catch {
    return null;
  }
}

function explanationFromPack(pack, { seeMore = false } = {}) {
  const evidence = Array.isArray(pack?.evidence) ? pack.evidence : [];
  const target = evidence.find((item) => item?.symbolId === pack?.target?.symbolId) ?? evidence[0];
  const usage = evidence.find((item) => item?.relation === 'usage_site');
  if (seeMore) {
    const definitions = evidence.filter((item) => item?.relation === 'definition');
    const tests = evidence.filter((item) => item?.kind === 'test');
    const named = (name) => definitions.find((item) => {
      const title = String(item?.title ?? '').split(' — ')[0];
      const symbolName = title.split(/::|\./u).pop() ?? '';
      return symbolName === name;
    });
    const order = named('Order');
    const repository = named('Repository');
    const memoryRepository = named('MemoryRepository');
    const selectedCode = [order, repository, usage].filter((item) => typeof item?.id === 'string').map((item) => item.id);
    const observations = [];
    if (order && repository) {
      observations.push({ text: 'Order is the aggregate and Repository is the persistence contract.', evidenceIds: [order.id, repository.id] });
    }
    if (memoryRepository) {
      observations.push({ text: 'MemoryRepository is the in-memory Repository implementation.', evidenceIds: [memoryRepository.id] });
    }
    if (usage) {
      observations.push({ text: 'The entry point wires repository, service, and handler from the package.', evidenceIds: [usage.id] });
    }
    if (tests.length > 0) {
      observations.push({ text: 'The package includes a service test that verifies valid orders are persisted.', evidenceIds: tests.map((item) => item.id).slice(0, 3) });
    }
    const impactEvidence = repository ?? order ?? target;
    return {
      schemaVersion: 'explanation/v2',
      summary: 'order is the Go package containing the domain model, persistence contract, service, and HTTP adapter for order management.',
      codeEvidenceIds: selectedCode.slice(0, 3),
      observations,
      inferences: [],
      uncertainties: [],
      changeImpact: impactEvidence ? [{ text: 'Changing the central contract can affect its implementation and service wiring.', evidenceIds: [impactEvidence.id] }] : [],
    };
  }
  const packageAPI = evidence.filter((item) => item?.relation === 'package_api').slice(0, 5);
  if (target && usage && packageAPI.length > 0) {
    const apiNames = packageAPI.map((item) => String(item.title ?? '').split(' — ')[0]).join(', ');
    return {
      schemaVersion: 'explanation/v2',
      summary: 'order is the workspace package that implements the order workflow.',
      observations: [
        { text: 'It is imported from example.com/tinycommerce/internal/order.', evidenceIds: [target.id] },
        { text: 'At this usage site it creates the repository, service, and HTTP handler.', evidenceIds: [usage.id] },
        { text: `Its key exported API includes ${apiNames}.`, evidenceIds: packageAPI.map((item) => item.id) },
      ],
      inferences: [],
      uncertainties: [],
      changeImpact: [],
    };
  }
  const ids = target?.id ? [target.id] : [];
  return validExplanation('Fake grounded CodeAtlas response for deterministic E2E.', ids);
}

function contextPackFrom(body) {
  const userPrompt = Array.isArray(body.messages)
    ? body.messages.find((message) => message?.role === 'user')?.content ?? ''
    : '';
  const match = String(userPrompt).match(/<CODEATLAS_CONTEXT_PACK>\n([\s\S]*?)\n<\/CODEATLAS_CONTEXT_PACK>/u);
  if (!match) return null;
  try {
    return JSON.parse(match[1]);
  } catch {
    return null;
  }
}

function validExplanation(summary = 'Fake grounded CodeAtlas response for deterministic E2E.', evidenceIds = ['E1']) {
  return {
    schemaVersion: 'explanation/v2',
    summary,
    observations: [
      {
        text: 'The fake provider returned a structured response from an isolated local scenario.',
        evidenceIds,
      },
    ],
    inferences: [],
    uncertainties: [],
    changeImpact: [],
  };
}

function chatResponse(content, { finishReason = 'stop' } = {}) {
  return {
    id: 'fake-chatcmpl-codeatlas',
    model: 'fake-codeatlas',
    choices: [
      {
        index: 0,
        message: { role: 'assistant', content },
        finish_reason: finishReason,
      },
    ],
  };
}

function vectorFor(value) {
  let hash = 2166136261;
  for (const char of value) {
    hash ^= char.codePointAt(0);
    hash = Math.imul(hash, 16777619) >>> 0;
  }
  return defaultVector.map((base, index) => {
    const byte = (hash >>> ((index % 4) * 8)) & 0xff;
    return Number((base + byte / 1000).toFixed(6));
  });
}

function writeJSON(response, status, body) {
  response.writeHead(status, { 'content-type': 'application/json' });
  response.end(JSON.stringify(body));
}

async function readRequestBody(request) {
  const chunks = [];
  for await (const chunk of request) {
    chunks.push(chunk);
  }
  return Buffer.concat(chunks).toString('utf8');
}

function sanitizeHeaders(headers) {
  const copy = { ...headers };
  if (copy.authorization) {
    copy.authorization = 'Bearer [redacted]';
  }
  return copy;
}

function listen(server) {
  return new Promise((resolve, reject) => {
    server.once('error', reject);
    server.listen(0, '127.0.0.1', () => {
      server.off('error', reject);
      resolve();
    });
  });
}

function closeServer(server) {
  return new Promise((resolve, reject) => {
    server.close((error) => (error ? reject(error) : resolve()));
  });
}
