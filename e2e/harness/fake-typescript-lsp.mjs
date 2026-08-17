#!/usr/bin/env node

// Deterministic, protocol-level TypeScript LSP used by browser E2E. It executes
// no workspace code and implements only the read-only capabilities CodeAtlas
// allowlists.
if (process.argv.includes('--version')) {
  process.stdout.write('5.3.0-fake\n');
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
            tokenTypes: ['class', 'interface', 'type', 'parameter', 'variable', 'property', 'function', 'member'],
            tokenModifiers: ['declaration', 'readonly', 'async', 'local'],
          },
          full: true,
        },
      },
      serverInfo: { name: 'codeatlas-fake-typescript-lsp', version: '5.3.0-fake' },
    });
    return;
  }
  if (method === 'shutdown') {
    respond(id, null);
    return;
  }
  if (method === 'textDocument/hover') {
    respond(id, { contents: { kind: 'plaintext', value: 'const semanticValue: number' } });
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
    { regex: /\bclass\s+([A-Za-z_$][\w$]*)/gu, group: 1, type: 0, modifiers: 1 },
    { regex: /\binterface\s+([A-Za-z_$][\w$]*)/gu, group: 1, type: 1, modifiers: 1 },
    { regex: /\bfunction\s+([A-Za-z_$][\w$]*)/gu, group: 1, type: 6, modifiers: 1 },
    { regex: /\b(?:const|let|var)\s+([A-Za-z_$][\w$]*)/gu, group: 1, type: 4, modifiers: 1 },
  ];
  for (const pattern of patterns) {
    for (const match of text.matchAll(pattern.regex)) {
      const offset = match.index + match[0].lastIndexOf(match[pattern.group]);
      const position = offsetToPosition(text, offset);
      tokens.push({ ...position, length: utf16Length(match[pattern.group]), type: pattern.type, modifiers: pattern.modifiers });
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
  while (start > 0 && /[\w$]/u.test(line[start - 1])) start -= 1;
  while (end < line.length && /[\w$]/u.test(line[end])) end += 1;
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
  const diagnostics = document.text.includes('TYPE_ERROR') ? [{
    range: { start: { line: 0, character: 0 }, end: { line: 0, character: 5 } },
    severity: 1,
    code: 'FAKE_TS_ERROR',
    source: 'fake-ts',
    message: 'Synthetic TypeScript diagnostic',
  }] : [];
  notify('textDocument/publishDiagnostics', { uri, version: document.version, diagnostics });
}

function respond(id, result) {
  send({ jsonrpc: '2.0', id, result });
}

function notify(method, params) {
  send({ jsonrpc: '2.0', method, params });
}

function send(payload) {
  const body = Buffer.from(JSON.stringify(payload), 'utf8');
  process.stdout.write(`Content-Length: ${body.length}\r\n\r\n`);
  process.stdout.write(body);
}
