// Read-only Monaco viewer for the Codemap presentation page. It renders one
// file at a time with step glyphs in the gutter (the "1a" chips), a purple
// selected-line decoration, and an inline annotation zone under the selected
// step line — mirroring the reference Codemap reading experience.

import * as monaco from 'monaco-editor/esm/vs/editor/editor.api';
import 'monaco-editor/esm/vs/editor/editor.all.js';
import EditorWorker from 'monaco-editor/esm/vs/editor/editor.worker?worker';
import TypeScriptWorker from 'monaco-editor/esm/vs/language/typescript/ts.worker?worker';

import { languageForPath } from './monaco-editor-contract';

export type CodemapStepMarker = {
  id: string;
  line: number;
  text: string;
};

type ViewerTheme = 'dark' | 'light';

const languageLoads = new Map<string, Promise<void>>();

if (!globalThis.MonacoEnvironment) {
  globalThis.MonacoEnvironment = {
    getWorker(_moduleId: string, label: string): Worker {
      if (label === 'typescript' || label === 'javascript') {
        return new TypeScriptWorker({ name: 'codeatlas-typescript-worker' });
      }
      return new EditorWorker({ name: 'codeatlas-editor-worker' });
    },
  };
}

async function ensureLanguage(language: string): Promise<void> {
  if (language !== 'go' && language !== 'javascript' && language !== 'typescript' && language !== 'swift' && language !== 'python' && language !== 'rust') return;
  let load = languageLoads.get(language);
  if (!load) {
    if (language === 'go') load = import('monaco-editor/esm/vs/basic-languages/go/go.contribution.js').then(() => undefined);
    else if (language === 'swift') load = import('monaco-editor/esm/vs/basic-languages/swift/swift.contribution.js').then(() => undefined);
    else if (language === 'python') load = import('monaco-editor/esm/vs/basic-languages/python/python.contribution.js').then(() => undefined);
    else if (language === 'rust') load = import('monaco-editor/esm/vs/basic-languages/rust/rust.contribution.js').then(() => undefined);
    else if (language === 'javascript') load = import('monaco-editor/esm/vs/basic-languages/javascript/javascript.contribution.js').then(() => undefined);
    else load = import('monaco-editor/esm/vs/basic-languages/typescript/typescript.contribution.js').then(() => undefined);
    languageLoads.set(language, load);
  }
  await load;
}

export function glyphClassName(stepID: string): string {
  return `codemap-presentation-glyph-${String(stepID).toLowerCase().replace(/[^a-z0-9-]/g, '-')}`;
}

export function glyphSvgDataURI(label: string, color: string): string {
  const safeLabel = String(label).slice(0, 4).replace(/[^0-9a-z]/gi, '');
  const svg = `<svg xmlns='http://www.w3.org/2000/svg' width='16' height='16' viewBox='0 0 16 16'><text x='8' y='12' text-anchor='middle' font-family='Arial, sans-serif' font-size='9' font-weight='bold' fill='${color}'>${safeLabel}</text></svg>`;
  return `data:image/svg+xml,${encodeURIComponent(svg)}`;
}

export function createCodemapCodeViewer() {
  let editor: monaco.editor.IStandaloneCodeEditor | null = null;
  let styleElement: HTMLStyleElement | null = null;
  let currentPath = '';
  let markers: CodemapStepMarker[] = [];
  let markerDecorations: string[] = [];
  let selectionDecorations: string[] = [];
  let zoneIDs: string[] = [];
  let glyphColor = '#a855f7';
  let onGlyphClick: (line: number) => void = () => {};
  const models = new Map<string, monaco.editor.ITextModel>();
  let disposed = false;

  const requireEditor = () => {
    if (disposed || !editor) throw new Error('codemap code viewer not mounted');
    return editor;
  };

  const ensureStyles = () => {
    if (styleElement?.isConnected) return styleElement;
    styleElement = document.createElement('style');
    const nonce = document.querySelector<HTMLMetaElement>('meta[name="codeatlas-style-nonce"]')?.content || '';
    if (nonce) styleElement.nonce = nonce;
    document.head.appendChild(styleElement);
    return styleElement;
  };

  const refreshGlyphStyles = () => {
    const sheet = ensureStyles();
    sheet.textContent = markers
      .map((marker) => `.${glyphClassName(marker.id)} { background: url("${glyphSvgDataURI(marker.id, glyphColor)}") center center / 16px 16px no-repeat !important; cursor: pointer; }`)
      .join('\n');
  };

  const clearSelection = () => {
    const instance = requireEditor();
    selectionDecorations = instance.deltaDecorations(selectionDecorations, []);
    if (zoneIDs.length) {
      const stale = zoneIDs;
      zoneIDs = [];
      instance.changeViewZones((accessor) => {
        stale.forEach((id) => accessor.removeZone(id));
      });
    }
  };

  const viewer = {
    mount(host: HTMLElement, options: { theme?: ViewerTheme } = {}) {
      if (disposed) throw new Error('codemap code viewer disposed');
      host.replaceChildren();
      editor = monaco.editor.create(host, {
        ariaLabel: 'Codemap source viewer',
        automaticLayout: true,
        readOnly: true,
        domReadOnly: true,
        theme: options.theme === 'light' ? 'vs' : 'vs-dark',
        fontFamily: 'Consolas, SFMono-Regular, Liberation Mono, Menlo, monospace',
        fontSize: 12,
        lineHeight: 16,
        lineNumbers: 'on',
        glyphMargin: true,
        folding: false,
        minimap: { enabled: false },
        scrollBeyondLastLine: false,
        renderLineHighlight: 'none',
        wordWrap: 'off',
        contextmenu: false,
        links: false,
        hover: { enabled: false },
        occurrencesHighlight: 'off',
        selectionHighlight: false,
        wordBasedSuggestions: 'off',
        quickSuggestions: false,
        codeLens: false,
        inlayHints: { enabled: 'off' },
        fixedOverflowWidgets: true,
        unicodeHighlight: { ambiguousCharacters: false, invisibleCharacters: false },
      });
      editor.onMouseDown((event) => {
        if (event.target.type !== monaco.editor.MouseTargetType.GUTTER_GLYPH_MARGIN) return;
        const line = event.target.position?.lineNumber;
        if (line) onGlyphClick(line);
      });
    },
    async showFile(input: { path: string; content: string }): Promise<void> {
      const instance = requireEditor();
      const path = String(input.path || '');
      let model = models.get(path) ?? null;
      if (!model || model.isDisposed()) {
        const language = languageForPath(path, '');
        await ensureLanguage(language);
        if (disposed || !editor) return;
        model = monaco.editor.createModel(input.content, language);
        models.set(path, model);
      }
      clearSelection();
      markerDecorations = [];
      selectionDecorations = [];
      currentPath = path;
      instance.setModel(model);
    },
    currentFile(): string {
      return currentPath;
    },
    setMarkers(nextMarkers: CodemapStepMarker[]) {
      const instance = requireEditor();
      markers = (nextMarkers || []).filter((marker) => Number.isFinite(marker.line) && marker.line > 0);
      refreshGlyphStyles();
      markerDecorations = instance.deltaDecorations(markerDecorations, markers.map((marker) => ({
        range: new monaco.Range(marker.line, 1, marker.line, 1),
        options: {
          glyphMarginClassName: glyphClassName(marker.id),
          glyphMarginHoverMessage: { value: `Step ${marker.id}`, isTrusted: false },
          stickiness: monaco.editor.TrackedRangeStickiness.NeverGrowsWhenTypingAtEdges,
        },
      })));
    },
    markerAtLine(line: number): CodemapStepMarker | null {
      return markers.find((marker) => marker.line === line) || null;
    },
    selectMarker(marker: CodemapStepMarker | null, options: { reveal?: boolean } = {}) {
      const instance = requireEditor();
      clearSelection();
      if (!marker) return;
      selectionDecorations = instance.deltaDecorations([], [{
        range: new monaco.Range(marker.line, 1, marker.line, 1),
        options: {
          isWholeLine: true,
          className: 'codemap-presentation-selected-line',
          stickiness: monaco.editor.TrackedRangeStickiness.NeverGrowsWhenTypingAtEdges,
        },
      }]);
      if (marker.text) {
        const domNode = document.createElement('div');
        domNode.className = 'codemap-presentation-step-zone';
        const inner = document.createElement('div');
        inner.className = 'codemap-presentation-step-zone-card';
        const text = document.createElement('span');
        text.textContent = marker.text;
        inner.appendChild(text);
        domNode.appendChild(inner);
        instance.changeViewZones((accessor) => {
          zoneIDs.push(accessor.addZone({
            afterLineNumber: Math.max(0, marker.line - 1),
            heightInPx: 30,
            domNode,
          }));
        });
      }
      if (options.reveal !== false) {
        instance.revealLineInCenterIfOutsideViewport(marker.line, monaco.editor.ScrollType.Smooth);
      }
    },
    setTheme(theme: ViewerTheme) {
      glyphColor = theme === 'light' ? '#9333ea' : '#a855f7';
      refreshGlyphStyles();
      monaco.editor.setTheme(theme === 'light' ? 'vs' : 'vs-dark');
    },
    setGlyphClickListener(listener: (line: number) => void) {
      onGlyphClick = typeof listener === 'function' ? listener : () => {};
    },
    layout() {
      editor?.layout();
    },
    dispose() {
      if (disposed) return;
      disposed = true;
      models.forEach((model) => model.dispose());
      models.clear();
      styleElement?.remove();
      styleElement = null;
      editor?.dispose();
      editor = null;
    },
  };
  return viewer;
}
