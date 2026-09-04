import { execFileSync } from 'node:child_process';
import { readFileSync, writeFileSync } from 'node:fs';
import { dirname, resolve } from 'node:path';
import { fileURLToPath } from 'node:url';
import { brotliCompressSync, constants as zlibConstants, gzipSync } from 'node:zlib';
import { defineConfig, type Plugin } from 'vite';

const configDir = dirname(fileURLToPath(import.meta.url));
const editor = 'monaco';
const editorVersion = JSON.parse(
  readFileSync(resolve(configDir, 'node_modules/monaco-editor/package.json'), 'utf8'),
).version as string;
const backendDevURL = process.env.CODEATLAS_BACKEND_URL ?? 'http://127.0.0.1:43127';

export default defineConfig({
  root: configDir,
  publicDir: 'public',
  server: {
    host: '127.0.0.1',
    port: 5173,
    strictPort: true,
    proxy: {
      '/api': {
        target: backendDevURL,
        changeOrigin: false,
      },
    },
  },
  build: {
    outDir: '../backend/internal/webui/dist',
    emptyOutDir: true,
    assetsDir: 'assets',
    sourcemap: false,
    minify: true,
    manifest: '.vite/manifest.json',
    rollupOptions: {
      input: resolve(configDir, 'index.html'),
      output: {
        entryFileNames: 'assets/[name]-[hash].js',
        chunkFileNames: (chunk) => `assets/${embeddedAssetName(chunk.name)}-[hash].js`,
        assetFileNames: 'assets/[name]-[hash][extname]',
      },
    },
  },
  plugins: [monacoStyleNonce(), codeAtlasManifest(), precompressAssets()],
});

function monacoStyleNonce(): Plugin {
  const requiredModules = new Set(['domStylesheets.js', 'contextview.js']);
  const patchedModules = new Set<string>();
  return {
    name: 'codeatlas-monaco-style-nonce',
    enforce: 'pre',
    transform(source, id) {
      const moduleID = (id.split('?', 1)[0] ?? id).replaceAll('\\', '/');
      let needle = '';
      let replacement = '';
      let moduleName = '';
      if (moduleID.endsWith('/monaco-editor/esm/vs/base/browser/domStylesheets.js')) {
        moduleName = 'domStylesheets.js';
        needle = "    style.media = 'screen';";
        replacement = `${needle}\n    style.nonce = globalThis.MonacoEnvironment?.styleNonce || '';`;
      } else if (moduleID.endsWith('/monaco-editor/esm/vs/base/browser/ui/contextview/contextview.js')) {
        moduleName = 'contextview.js';
        needle = "                const style = document.createElement('style');";
        replacement = `${needle}\n                style.nonce = globalThis.MonacoEnvironment?.styleNonce || '';`;
      } else {
        return null;
      }
      if (!source.includes(needle)) this.error(`Monaco ${moduleName} nonce hook changed upstream`);
      patchedModules.add(moduleName);
      return source.replace(needle, replacement);
    },
    buildEnd() {
      const missing = [...requiredModules].filter((moduleName) => !patchedModules.has(moduleName));
      if (missing.length > 0) this.error(`Monaco nonce patch did not reach: ${missing.join(', ')}`);
    },
  };
}

// Go's //go:embed omits files whose base name starts with '_' or '.'. Monaco's
// shared basic-language chunk is named "_.contribution" by default, so normalize
// every emitted chunk to an embed-safe deterministic base name.
function embeddedAssetName(name: string): string {
  const normalized = name.replace(/^[._-]+/, 'chunk-');
  return normalized || 'chunk';
}

function precompressAssets(): Plugin {
  return {
    name: 'codeatlas-precompress-assets',
    enforce: 'post',
    writeBundle(outputOptions, bundle) {
      if (!outputOptions.dir) return;
      for (const item of Object.values(bundle)) {
        if (!/^assets\/.+\.(?:css|js)$/.test(item.fileName)) continue;
        // Read the final on-disk bytes: Vite replaces dynamic-import preload
        // placeholders in a later generateBundle hook, after plugin snapshots.
        const outputPath = resolve(outputOptions.dir, item.fileName);
        const source = readFileSync(outputPath);
        writeFileSync(`${outputPath}.gz`, gzipSync(source, { level: 9 }));
        writeFileSync(`${outputPath}.br`, brotliCompressSync(source, {
          params: { [zlibConstants.BROTLI_PARAM_QUALITY]: 11 },
        }));
      }
    },
  };
}

function codeAtlasManifest(): Plugin {
  return {
    name: 'codeatlas-manifest',
    generateBundle(_, bundle) {
      const entrypoints = new Set<string>();
      const initialStyles = new Set<string>();
      const styles = new Set<string>();
      const workers = new Set<string>();
      const other = new Set<string>();
	  let hasMonacoAdapter = false;
	  const requiredLanguageChunks = new Set(['go', 'javascript', 'typescript', 'swift', 'python', 'rust']);
	  const foundLanguageChunks = new Set<string>();

      for (const item of Object.values(bundle)) {
        if (item.type === 'chunk') {
		  const moduleIDs = Object.keys(item.modules);
		  if (moduleIDs.some((moduleId) => moduleId.endsWith('/src/monaco-editor-adapter.ts'))) hasMonacoAdapter = true;
		  for (const language of requiredLanguageChunks) {
			if (moduleIDs.some((moduleId) => moduleId.includes(`/basic-languages/${language}/${language}.contribution.js`))) {
			  foundLanguageChunks.add(language);
			}
		  }
          if (item.isEntry) {
            entrypoints.add(item.fileName);
            const metadata = (item as typeof item & { viteMetadata?: { importedCss?: Set<string> } }).viteMetadata;
            metadata?.importedCss?.forEach((fileName) => initialStyles.add(fileName));
          }
          else if (/worker/i.test(item.fileName)) workers.add(item.fileName);
          else other.add(item.fileName);
          continue;
        }
        if (item.fileName.endsWith('.css') && initialStyles.has(item.fileName)) styles.add(item.fileName);
        else if (/worker/i.test(item.fileName)) workers.add(item.fileName);
        else other.add(item.fileName);
      }

	  const hasEditorWorker = [...workers].some((fileName) => /\/editor\.worker-[^/]+\.js$/.test(fileName));
	  const hasTypeScriptWorker = [...workers].some((fileName) => /\/ts\.worker-[^/]+\.js$/.test(fileName));
	  const missingLanguages = [...requiredLanguageChunks].filter((language) => !foundLanguageChunks.has(language));
	  if (!hasMonacoAdapter || missingLanguages.length > 0 || !hasEditorWorker || !hasTypeScriptWorker) {
		this.error(`production bundle is missing explicit Monaco assets (adapter=${hasMonacoAdapter}, languages=${missingLanguages.join(',') || 'ok'}, editorWorker=${hasEditorWorker}, typescriptWorker=${hasTypeScriptWorker})`);
      }

      this.emitFile({
        type: 'asset',
        fileName: 'codeatlas-manifest.json',
        source: `${JSON.stringify({
          buildVersion: gitVersion(),
          editor,
          editorVersion,
          assets: {
            entrypoints: [...entrypoints].sort(),
            styles: [...styles].sort(),
            workers: [...workers].sort(),
            other: [...other].sort(),
          },
        }, null, 2)}\n`,
      });
    },
  };
}

function gitVersion(): string {
  try {
    return execFileSync('git', ['rev-parse', '--short=12', 'HEAD'], { cwd: resolve(configDir, '..'), encoding: 'utf8' }).trim();
  } catch {
    return 'unknown';
  }
}
