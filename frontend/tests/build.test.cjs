'use strict';

const test = require('node:test');
const assert = require('node:assert/strict');
const fs = require('node:fs');
const path = require('node:path');
const { brotliDecompressSync, gunzipSync } = require('node:zlib');

const frontendRoot = path.resolve(__dirname, '..');
const repoRoot = path.resolve(frontendRoot, '..');
const distRoot = path.resolve(repoRoot, 'backend/internal/webui/dist');

test('production build emits manifest and all referenced assets', () => {
  const manifest = readJSON(path.join(distRoot, 'codeatlas-manifest.json'));
  const dependency = readJSON(path.join(frontendRoot, 'node_modules', 'monaco-editor', 'package.json'));
  assert.equal(manifest.editor, 'monaco');
  assert.equal(manifest.editorVersion, dependency.version);
  assert.ok(manifest.assets.entrypoints.length > 0, 'entrypoint list should not be empty');
  assert.ok(manifest.assets.styles.length > 0, 'style list should not be empty');
  assert.ok(manifest.assets.workers.length >= 2, 'editor and TypeScript workers must be self-hosted');

  for (const asset of allManifestAssets(manifest)) {
    assertNoUnsafePath(asset);
    assertHashedAsset(asset);
    assert.ok(fs.existsSync(path.join(distRoot, asset)), `missing manifest asset ${asset}`);
  }

  const html = fs.readFileSync(path.join(distRoot, 'index.html'), 'utf8');
  assert.match(
    html,
    /<meta name="codeatlas-style-nonce" content="__CODEATLAS_CSP_NONCE__">/,
    'index must expose the per-response nonce placeholder to the Monaco adapter',
  );
  assert.equal(
    (html.match(/<meta name="codeatlas-settings-token" content="__CODEATLAS_SETTINGS_TOKEN__">/g) || []).length,
    1,
    'index must expose exactly one per-process settings token placeholder',
  );
  for (const asset of htmlAssetReferences(html)) {
    assertNoUnsafePath(asset);
    assert.ok(fs.existsSync(path.join(distRoot, asset)), `missing HTML asset ${asset}`);
  }
});

test('production JavaScript and CSS have valid precompressed variants', () => {
  const manifest = readJSON(path.join(distRoot, 'codeatlas-manifest.json'));
  for (const asset of allManifestAssets(manifest).filter((name) => /\.(?:js|css)$/.test(name))) {
    const original = fs.readFileSync(path.join(distRoot, asset));
    const gzip = fs.readFileSync(path.join(distRoot, `${asset}.gz`));
    const brotli = fs.readFileSync(path.join(distRoot, `${asset}.br`));
    assert.deepEqual(gunzipSync(gzip), original, `${asset}.gz must decode to the original`);
    assert.deepEqual(brotliDecompressSync(brotli), original, `${asset}.br must decode to the original`);
  }
});

test('production build does not emit sourcemaps', () => {
  for (const file of walk(distRoot)) {
    assert.notEqual(path.extname(file), '.map', `${path.relative(distRoot, file)} must not be emitted`);
  }
});

test('production bundle does not contain secrets, private dev URLs, or local absolute paths', () => {
  const forbidden = [
    /sk-[A-Za-z0-9]{20,}/,
    /http:\/\/127\.0\.0\.1:8080/,
    /http:\/\/localhost:8080/,
    /100\.98\.1\.45/,
    new RegExp(escapeRegExp(repoRoot)),
    new RegExp(escapeRegExp(process.env.HOME || '/Users/')),
  ];

  for (const file of walk(distRoot)) {
    if (!/\.(html|js|css|json)$/.test(file)) continue;
    const content = fs.readFileSync(file, 'utf8');
    for (const pattern of forbidden) {
      assert.doesNotMatch(content, pattern, `${path.relative(distRoot, file)} contains ${pattern}`);
    }
  }
});

test('production entrypoint and tests target one application implementation', () => {
  const main = fs.readFileSync(path.join(frontendRoot, 'src', 'main.ts'), 'utf8');
  assert.match(main, /import ['"]\.\.\/app\.js['"]/);
  assert.ok(fs.existsSync(path.join(frontendRoot, 'app.test.cjs')), 'production app.js tests must exist');
  const runtimeModules = walk(path.join(frontendRoot, 'src'))
    .filter((file) => file.endsWith('.ts') && !file.endsWith('.d.ts'))
    .map((file) => path.relative(path.join(frontendRoot, 'src'), file))
    .sort();
  assert.deepEqual(runtimeModules, [
    'codemap-presentation-editor.ts',
    'main.ts',
    'mermaid-lite.ts',
    'monaco-editor-adapter.ts',
    'monaco-editor-contract.ts',
  ]);
});

test('Monaco and language contributions remain lazy production chunks', () => {
  const viteManifest = readJSON(path.join(distRoot, '.vite', 'manifest.json'));
  const entry = viteManifest['index.html'];
  const adapter = viteManifest['src/monaco-editor-adapter.ts'];
  assert.ok(entry?.isEntry, 'index.html must be the production entry');
  assert.ok(entry.dynamicImports?.includes('src/monaco-editor-adapter.ts'), 'Monaco adapter must load after readiness');
  assert.ok(adapter?.isDynamicEntry, 'Monaco adapter must be a dynamic entry');
  assert.ok(
    adapter.dynamicImports?.includes('node_modules/monaco-editor/esm/vs/basic-languages/go/go.contribution.js'),
    'Go language contribution must load on demand',
  );
  assert.ok(
    adapter.dynamicImports?.includes('node_modules/monaco-editor/esm/vs/basic-languages/javascript/javascript.contribution.js'),
    'JavaScript tokenizer must load on demand',
  );
  assert.ok(
    adapter.dynamicImports?.includes('node_modules/monaco-editor/esm/vs/basic-languages/typescript/typescript.contribution.js'),
    'TypeScript tokenizer must load on demand',
  );
  assert.ok(
    adapter.dynamicImports?.includes('node_modules/monaco-editor/esm/vs/basic-languages/swift/swift.contribution.js'),
    'Swift tokenizer must load on demand',
  );
  assert.ok(
    adapter.dynamicImports?.includes('node_modules/monaco-editor/esm/vs/basic-languages/python/python.contribution.js'),
    'Python tokenizer must load on demand',
  );
  assert.ok(
    adapter.dynamicImports?.includes('node_modules/monaco-editor/esm/vs/basic-languages/rust/rust.contribution.js'),
    'Rust tokenizer must load on demand',
  );
});

test('production no longer ships the textarea editor implementation', () => {
  const application = fs.readFileSync(path.join(frontendRoot, 'app.js'), 'utf8');
  const styles = fs.readFileSync(path.join(frontendRoot, 'styles.css'), 'utf8');
  assert.doesNotMatch(application, /createTextareaEditorAdapter/);
  assert.doesNotMatch(styles, /adapter-textarea/);
});

function readJSON(file) {
  return JSON.parse(fs.readFileSync(file, 'utf8'));
}

function allManifestAssets(manifest) {
  return [
    ...manifest.assets.entrypoints,
    ...manifest.assets.styles,
    ...manifest.assets.workers,
    ...manifest.assets.other,
  ];
}

function htmlAssetReferences(html) {
  return Array.from(html.matchAll(/\b(?:src|href)="([^"]+)"/g), (match) => match[1].replace(/^\//, ''))
    .filter((asset) => asset && !asset.startsWith('#') && !asset.startsWith('http:') && !asset.startsWith('https:'));
}

function assertNoUnsafePath(asset) {
  assert.equal(path.isAbsolute(asset), false, `${asset} must be relative`);
  assert.equal(asset.includes('..'), false, `${asset} must not escape dist`);
}

function assertHashedAsset(asset) {
  assert.match(asset, /^assets\/.+-[A-Za-z0-9_-]{8,}\.[A-Za-z0-9]+$/, `${asset} must be hashed`);
}

function walk(root) {
  const files = [];
  for (const entry of fs.readdirSync(root, { withFileTypes: true })) {
    const full = path.join(root, entry.name);
    if (entry.isDirectory()) files.push(...walk(full));
    else files.push(full);
  }
  return files;
}

function escapeRegExp(value) {
  return value.replace(/[.*+?^${}()|[\]\\]/g, '\\$&');
}
