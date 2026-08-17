#!/usr/bin/env node

// Deterministic, protocol-level rust-analyzer replacement used by E2E.
// It exposes only CodeAtlas' read-only semantic surface and rejects any
// initialization that could discover Cargo projects or execute workspace code.
if (process.argv[2] === '--version') {
  process.stdout.write('rust-analyzer 1.88.0-fake\n');
  process.exit(0);
}

const documents = new Map();
let buffer = Buffer.alloc(0);

process.stdin.on('data', (chunk) => {
  buffer = Buffer.concat([buffer, chunk]);
  drain();
});

function drain() {
  while (true) {
    const headerEnd = buffer.indexOf('\r\n\r\n');
    if (headerEnd < 0) return;
    const header = buffer.subarray(0, headerEnd).toString('ascii');
    const match = /content-length:\s*(\d+)/iu.exec(header);
    if (!match) process.exit(2);
    const length = Number(match[1]);
    const bodyStart = headerEnd + 4;
    if (buffer.length < bodyStart + length) return;
    const body = buffer.subarray(bodyStart, bodyStart + length).toString('utf8');
    buffer = buffer.subarray(bodyStart + length);
    handle(JSON.parse(body));
  }
}

function handle(message) {
  const { id, method, params = {} } = message;
  if (method === 'initialize') {
    if (!safeInitialization(params.initializationOptions)) {
      respondError(id, -32602, 'unsafe rust-analyzer initialization');
      return;
    }
    respond(id, {
      capabilities: {
        positionEncoding: 'utf-16',
        textDocumentSync: 1,
        hoverProvider: true,
        definitionProvider: true,
        referencesProvider: true,
        implementationProvider: true,
        callHierarchyProvider: true,
        semanticTokensProvider: {
          legend: {
            tokenTypes: ['class', 'variable', 'function', 'method', 'property', 'parameter'],
            tokenModifiers: ['declaration', 'definition', 'readonly', 'static'],
          },
          full: true,
        },
      },
      serverInfo: { name: 'codeatlas-rust-analyzer', version: '1.88.0-fake' },
    });
    return;
  }
  if (method === 'shutdown') {
    respond(id, null);
    return;
  }
  if (method === 'textDocument/hover') {
    respond(id, { contents: { kind: 'plaintext', value: 'fn save(&mut self, order: Order)' } });
    return;
  }
  if (method === 'textDocument/definition') {
    const document = documents.get(params.textDocument?.uri);
    respond(id, document ? [{ uri: params.textDocument.uri, range: wordRange(document.text, params.position) }] : []);
    return;
  }
  if (method === 'textDocument/references' || method === 'textDocument/implementation') {
    respond(id, []);
    return;
  }
  if (method === 'textDocument/prepareCallHierarchy') {
    respond(id, []);
    return;
  }
  if (method === 'textDocument/semanticTokens/full') {
    const document = documents.get(params.textDocument?.uri);
    respond(id, { resultId: document ? `v${document.version}` : 'missing', data: document ? semanticData(document.text) : [] });
    return;
  }
  if (method === 'textDocument/didOpen') {
    const item = params.textDocument;
    documents.set(item.uri, { text: item.text, version: item.version });
    publishDiagnostics(item.uri);
    return;
  }
  if (method === 'textDocument/didChange') {
    const item = params.textDocument;
    documents.set(item.uri, { text: params.contentChanges?.[0]?.text ?? '', version: item.version });
    publishDiagnostics(item.uri);
    return;
  }
  if (method === 'textDocument/didClose') {
    documents.delete(params.textDocument?.uri);
    return;
  }
  if (id != null) respond(id, null);
}

function semanticData(text) {
  const tokens = [];
  const patterns = [
    { regex: /\b(?:struct|enum|trait)\s+([A-Za-z_][\w]*)/gu, group: 1, type: 0, modifiers: 2 },
    { regex: /\b(?:const|static|let)\s+(?:mut\s+)?([A-Za-z_][\w]*)/gu, group: 1, type: 1, modifiers: 2 },
    { regex: /\b(?:async\s+)?fn\s+([A-Za-z_][\w]*)/gu, group: 1, type: 3, modifiers: 2 },
  ];
  for (const pattern of patterns) {
    for (const match of text.matchAll(pattern.regex)) {
      const value = match[pattern.group];
      const offset = match.index + match[0].lastIndexOf(value);
      const position = offsetToPosition(text, offset);
      tokens.push({ ...position, length: utf16Length(value), type: pattern.type, modifiers: pattern.modifiers });
    }
  }
  tokens.sort((left, right) => left.line - right.line || left.character - right.character || left.type - right.type);
  const data = [];
  let priorLine = 0;
  let priorCharacter = 0;
  for (const token of tokens) {
    const deltaLine = token.line - priorLine;
    const deltaCharacter = deltaLine === 0 ? token.character - priorCharacter : token.character;
    data.push(deltaLine, deltaCharacter, token.length, token.type, token.modifiers);
    priorLine = token.line;
    priorCharacter = token.character;
  }
  return data;
}

function wordRange(text, position = { line: 0, character: 0 }) {
  const lines = text.split(/\r?\n/u);
  const line = lines[position.line] || '';
  let start = Math.min(position.character, utf16Length(line));
  let end = start;
  while (start > 0 && /[\w]/u.test(line[start - 1])) start -= 1;
  while (end < line.length && /[\w]/u.test(line[end])) end += 1;
  return { start: { line: position.line, character: start }, end: { line: position.line, character: end } };
}

function offsetToPosition(text, offset) {
  const prefix = text.slice(0, offset);
  const lines = prefix.split('\n');
  return { line: lines.length - 1, character: utf16Length(lines.at(-1).replace(/\r$/u, '')) };
}

function utf16Length(value) {
  return [...value].reduce((total, character) => total + (character.codePointAt(0) > 0xffff ? 2 : 1), 0);
}

function publishDiagnostics(uri) {
  const document = documents.get(uri);
  if (!document) return;
  const diagnostics = document.text.includes('RUST_ERROR') ? [{
    range: { start: { line: 0, character: 0 }, end: { line: 0, character: 6 } },
    severity: 1,
    code: 'FAKE_RUST_ERROR',
    source: 'rust-analyzer',
    message: 'Synthetic Rust diagnostic',
  }] : [];
  notify('textDocument/publishDiagnostics', { uri, version: document.version, diagnostics });
}

function safeInitialization(options = {}) {
  const cargo = options.cargo || {};
  const buildScripts = cargo.buildScripts || {};
  const procMacro = options.procMacro || {};
  return Array.isArray(options.linkedProjects)
    && options.linkedProjects.length === 0
    && options.checkOnSave === false
    && cargo.autoreload === false
    && cargo.noDeps === true
    && cargo.sysroot === null
    && buildScripts.enable === false
    && procMacro.enable === false;
}

function respond(id, result) {
  send({ jsonrpc: '2.0', id, result });
}

function respondError(id, code, message) {
  send({ jsonrpc: '2.0', id, error: { code, message } });
}

function notify(method, params) {
  send({ jsonrpc: '2.0', method, params });
}

function send(payload) {
  const body = Buffer.from(JSON.stringify(payload), 'utf8');
  process.stdout.write(`Content-Length: ${body.length}\r\n\r\n`);
  process.stdout.write(body);
}
