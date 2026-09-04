import * as monaco from 'monaco-editor/esm/vs/editor/editor.api';
import 'monaco-editor/esm/vs/editor/editor.all.js';
import EditorWorker from 'monaco-editor/esm/vs/editor/editor.worker?worker';
import TypeScriptWorker from 'monaco-editor/esm/vs/language/typescript/ts.worker?worker';

import { languageForPath, workspaceModelURI } from './monaco-editor-contract';

export type EditorPosition = { line: number; column: number; encoding?: 'utf-16' };
export type EditorRange = { start: EditorPosition; end: EditorPosition };

type OpenModel = {
  documentId: string;
  path: string;
  language?: string;
  version?: number;
  content: string;
  readOnly?: boolean;
};

type Decoration = {
  id?: string;
  range: EditorRange;
  className?: string;
  title?: string;
};

type AdapterCommand = {
  id: string;
  key?: string;
  macKey?: string;
  run(documentId: string): void;
};

type ViewState = {
  cursor: EditorPosition;
  selections: EditorRange[];
  scrollTop: number;
  scrollLeft: number;
  foldedRanges: EditorRange[];
  monaco?: monaco.editor.ICodeEditorViewState | null;
};

type ModelRecord = {
  documentId: string;
  path: string;
  version: number;
  readOnly: boolean;
  readOnlyReason: string;
  model: monaco.editor.ITextModel;
  contentDisposable: monaco.IDisposable;
  decorations: Map<string, string[]>;
  viewState: ViewState;
};

type PointerEvent = {
  clientX: number;
  clientY: number;
  // Explain is an explicit gesture, so the host needs the Shift state that was
  // live when the pointer moved, not whenever the throttled handler runs.
  shiftKey?: boolean;
  editorPosition?: EditorPosition | null;
};

const languageLoads = new Map<string, Promise<void>>();

declare global {
  // Monaco reads this global when it creates editor and TypeScript workers.
  // eslint-disable-next-line no-var
  var MonacoEnvironment: monaco.Environment | undefined;
}

const codeAtlasMonacoEnvironment: monaco.Environment & { styleNonce: string } = {
  styleNonce: document.querySelector<HTMLMetaElement>('meta[name="codeatlas-style-nonce"]')?.content || '',
  getWorker(_moduleId: string, label: string): Worker {
    if (label === 'typescript' || label === 'javascript') {
      return new TypeScriptWorker({ name: 'codeatlas-typescript-worker' });
    }
    return new EditorWorker({ name: 'codeatlas-editor-worker' });
  },
};
globalThis.MonacoEnvironment = codeAtlasMonacoEnvironment;

async function ensureLanguage(language: string): Promise<void> {
  if (language !== 'go' && language !== 'javascript' && language !== 'typescript' && language !== 'swift' && language !== 'python' && language !== 'rust') return;
  let load = languageLoads.get(language);
  if (!load) {
    if (language === 'go') {
      load = import('monaco-editor/esm/vs/basic-languages/go/go.contribution.js').then(() => undefined);
    } else if (language === 'swift') {
      load = import('monaco-editor/esm/vs/basic-languages/swift/swift.contribution.js').then(() => undefined);
    } else if (language === 'python') {
      load = import('monaco-editor/esm/vs/basic-languages/python/python.contribution.js').then(() => undefined);
    } else if (language === 'rust') {
      load = import('monaco-editor/esm/vs/basic-languages/rust/rust.contribution.js').then(() => undefined);
    } else {
      load = (language === 'javascript'
        ? import('monaco-editor/esm/vs/basic-languages/javascript/javascript.contribution.js')
        : import('monaco-editor/esm/vs/basic-languages/typescript/typescript.contribution.js'))
        .then(() => undefined);
    }
    languageLoads.set(language, load);
  }
  await load;
}

function editorPosition(line: number, column: number): EditorPosition {
  return { line, column, encoding: 'utf-16' };
}

function fromMonacoPosition(position: monaco.IPosition): EditorPosition {
  return editorPosition(position.lineNumber, position.column);
}

function toMonacoPosition(position: EditorPosition): monaco.IPosition {
  return { lineNumber: position.line, column: position.column };
}

function fromMonacoRange(range: monaco.IRange): EditorRange {
  return {
    start: editorPosition(range.startLineNumber, range.startColumn),
    end: editorPosition(range.endLineNumber, range.endColumn),
  };
}

function toMonacoRange(range: EditorRange): monaco.Range {
  return new monaco.Range(range.start.line, range.start.column, range.end.line, range.end.column);
}

function toMonacoSelection(range: EditorRange): monaco.Selection {
  return new monaco.Selection(range.start.line, range.start.column, range.end.line, range.end.column);
}

function defaultViewState(): ViewState {
  const cursor = editorPosition(1, 1);
  return {
    cursor,
    selections: [{ start: cursor, end: cursor }],
    scrollTop: 0,
    scrollLeft: 0,
    foldedRanges: [],
    monaco: null,
  };
}

function commandKeybinding(command: AdapterCommand): number | null {
  const binding = (isMacPlatform() ? command.macKey : command.key) || command.key || '';
  const parts = binding.toLowerCase().split('+').filter(Boolean);
  if (!parts.length) return null;
  const keyName = parts.at(-1) ?? '';
  const keyCode = keyCodeFor(keyName);
  if (keyCode == null) return null;
  let result = keyCode;
  if (parts.includes('mod') || parts.includes('cmd') || parts.includes('ctrl')) result |= monaco.KeyMod.CtrlCmd;
  if (parts.includes('alt') || parts.includes('option')) result |= monaco.KeyMod.Alt;
  if (parts.includes('shift')) result |= monaco.KeyMod.Shift;
  return result;
}

function keyCodeFor(name: string): monaco.KeyCode | null {
  if (/^[a-z]$/.test(name)) return monaco.KeyCode.KeyA + (name.charCodeAt(0) - 97);
  if (/^f(?:[1-9]|1[0-2])$/.test(name)) return monaco.KeyCode.F1 + (Number(name.slice(1)) - 1);
  if (name === 'arrowleft') return monaco.KeyCode.LeftArrow;
  if (name === 'arrowright') return monaco.KeyCode.RightArrow;
  if (name === 'arrowup') return monaco.KeyCode.UpArrow;
  if (name === 'arrowdown') return monaco.KeyCode.DownArrow;
  return null;
}

function isMacPlatform(): boolean {
  return typeof navigator !== 'undefined' && /Mac|iPhone|iPad/.test(navigator.platform);
}

export function createMonacoEditorAdapter() {
  const records = new Map<string, ModelRecord>();
  const listeners = {
    content: new Set<(event: unknown) => void>(),
    cursor: new Set<(event: unknown) => void>(),
    mousemove: new Set<(event: PointerEvent) => void>(),
    mouseleave: new Set<(event: PointerEvent) => void>(),
  };
  const commandDisposables = new Map<string, monaco.IDisposable>();
  const pendingOpens = new Map<string, Promise<void>>();
  const modelGenerations = new Map<string, number>();
  const editorDisposables: monaco.IDisposable[] = [];
  const suppressedDocuments = new Set<string>();
  let editor: monaco.editor.IStandaloneCodeEditor | null = null;
  let activeDocumentId = '';
  let activationGeneration = 0;
  let disposed = false;

  const assertLive = () => {
    if (disposed) throw new Error('editor adapter disposed');
  };
  const requireEditor = () => {
    assertLive();
    if (!editor) throw new Error('Monaco editor not mounted');
    return editor;
  };
  const requireRecord = (documentId: string) => {
    const record = records.get(documentId);
    if (!record) throw new Error(`unknown editor model ${documentId}`);
    return record;
  };
  const emitCursor = () => {
    if (!activeDocumentId || !editor) return;
    const event = {
      documentId: activeDocumentId,
      position: adapter.getPosition(activeDocumentId),
      selections: adapter.getSelection(activeDocumentId),
    };
    listeners.cursor.forEach((listener) => listener(event));
  };
  const on = <T>(set: Set<(event: T) => void>, listener: (event: T) => void) => {
    set.add(listener);
    return { dispose: () => set.delete(listener) };
  };

  const adapter = {
    mount(target: HTMLElement) {
      assertLive();
      target.replaceChildren();
      target.classList.add('adapter-monaco-host');
      editor = monaco.editor.create(target, {
        ariaLabel: 'Code editor',
        automaticLayout: true,
        accessibilitySupport: 'auto',
        theme: 'vs-dark',
        fontFamily: 'SFMono-Regular, Consolas, Liberation Mono, Menlo, monospace',
        fontSize: 13,
        lineHeight: 21,
        lineNumbers: 'on',
        minimap: { enabled: false },
        scrollBeyondLastLine: false,
        tabSize: 2,
        insertSpaces: true,
        renderWhitespace: 'selection',
        wordWrap: 'off',
        fixedOverflowWidgets: true,
        unicodeHighlight: { ambiguousCharacters: true, invisibleCharacters: true },
        // CodeAtlas owns hover, navigation, diagnostics and completion semantics.
        // Disable Monaco contributors which would duplicate them and otherwise
        // leave cancellable cross-model requests alive during tab switches.
        hover: { enabled: false },
        links: false,
        occurrencesHighlight: 'off',
        selectionHighlight: false,
        wordBasedSuggestions: 'off',
        quickSuggestions: false,
        suggestOnTriggerCharacters: false,
        parameterHints: { enabled: false },
        codeLens: false,
        inlayHints: { enabled: 'off' },
      });
      editorDisposables.push(
        editor.onDidChangeCursorSelection(emitCursor),
        editor.onMouseMove((event) => {
          const browserEvent = event.event.browserEvent;
          const pointer: PointerEvent = {
            clientX: browserEvent.clientX,
            clientY: browserEvent.clientY,
            shiftKey: browserEvent.shiftKey === true,
            editorPosition: event.target.position ? fromMonacoPosition(event.target.position) : null,
          };
          listeners.mousemove.forEach((listener) => listener(pointer));
        }),
        editor.onMouseLeave((event) => {
          const browserEvent = event.event.browserEvent;
          const pointer: PointerEvent = { clientX: browserEvent.clientX, clientY: browserEvent.clientY };
          listeners.mouseleave.forEach((listener) => listener(pointer));
        }),
      );
    },
    async openModel(input: OpenModel) {
      assertLive();
      const instance = requireEditor();
	  const requestedActivation = ++activationGeneration;
      const existing = records.get(input.documentId);
      if (existing) {
        await this.updateModel(input);
		if (!disposed && editor === instance && requestedActivation === activationGeneration && records.has(input.documentId)) {
		  this.activateModel(input.documentId);
		}
        return;
      }
      let pending = pendingOpens.get(input.documentId);
      if (!pending) {
		const modelGeneration = (modelGenerations.get(input.documentId) ?? 0) + 1;
		modelGenerations.set(input.documentId, modelGeneration);
		pending = (async () => {
		  const language = languageForPath(input.path, input.language);
		  await ensureLanguage(language);
		  if (disposed || editor !== instance || modelGenerations.get(input.documentId) !== modelGeneration || records.has(input.documentId)) return;
		  const uri = monaco.Uri.parse(workspaceModelURI(input.path));
		  const collision = monaco.editor.getModel(uri);
		  if (collision) {
			const owned = [...records.values()].some((record) => record.model === collision);
			if (owned) return;
			collision.dispose();
		  }
		  if (disposed || editor !== instance || modelGenerations.get(input.documentId) !== modelGeneration) return;
		  const model = monaco.editor.createModel(input.content, language, uri);
		  const record: ModelRecord = {
			documentId: input.documentId,
			path: input.path,
			version: input.version ?? 0,
			readOnly: Boolean(input.readOnly),
			readOnlyReason: '',
			model,
			contentDisposable: { dispose() {} },
			decorations: new Map(),
			viewState: defaultViewState(),
		  };
		  record.contentDisposable = model.onDidChangeContent(() => {
			if (suppressedDocuments.has(input.documentId)) return;
			const event = {
			  documentId: input.documentId,
			  content: model.getValue(),
			  version: record.version,
			  origin: 'local',
			};
			listeners.content.forEach((listener) => listener(event));
		  });
		  records.set(input.documentId, record);
		})();
		pendingOpens.set(input.documentId, pending);
		void pending.finally(() => {
		  if (pendingOpens.get(input.documentId) === pending) pendingOpens.delete(input.documentId);
		});
	  }
	  await pending;
	  if (disposed || editor !== instance || !records.has(input.documentId)) return;
	  await this.updateModel(input);
	  if (requestedActivation === activationGeneration && records.has(input.documentId)) this.activateModel(input.documentId);
    },
    async updateModel(input: Partial<OpenModel> & { documentId: string }, _origin = 'remote') {
      assertLive();
      const record = requireRecord(input.documentId);
      if (typeof input.version === 'number') record.version = input.version;
      if (typeof input.readOnly === 'boolean') record.readOnly = input.readOnly;
      if (typeof input.content === 'string' && input.content !== record.model.getValue()) {
        suppressedDocuments.add(input.documentId);
        try {
          record.model.setValue(input.content);
        } finally {
          suppressedDocuments.delete(input.documentId);
        }
      }
      if (activeDocumentId === input.documentId) this.setReadOnly(input.documentId, record.readOnly, record.readOnlyReason);
      emitCursor();
    },
    closeModel(documentId: string) {
      assertLive();
	  modelGenerations.set(documentId, (modelGenerations.get(documentId) ?? 0) + 1);
	  pendingOpens.delete(documentId);
      const record = records.get(documentId);
      if (!record) return;
      if (activeDocumentId === documentId) {
        requireEditor().setModel(null);
        activeDocumentId = '';
      }
      record.contentDisposable.dispose();
      record.model.dispose();
      records.delete(documentId);
    },
    activateModel(documentId: string) {
      assertLive();
      const instance = requireEditor();
      if (activeDocumentId === documentId) {
        const current = requireRecord(documentId);
        this.setReadOnly(documentId, current.readOnly, current.readOnlyReason);
        emitCursor();
        return;
      }
      if (activeDocumentId && records.has(activeDocumentId)) this.saveViewState(activeDocumentId);
      const record = requireRecord(documentId);
      activeDocumentId = documentId;
      instance.setModel(record.model);
      this.setReadOnly(documentId, record.readOnly, record.readOnlyReason);
      this.restoreViewState(documentId, record.viewState);
      emitCursor();
    },
    getContent(documentId: string) {
      return requireRecord(documentId).model.getValue();
    },
    getPosition(documentId: string) {
      const record = requireRecord(documentId);
      const position = documentId === activeDocumentId ? requireEditor().getPosition() : null;
      return position ? fromMonacoPosition(position) : record.viewState.cursor;
    },
    getSelection(documentId: string) {
      const record = requireRecord(documentId);
      const selections = documentId === activeDocumentId ? requireEditor().getSelections() : null;
      return selections?.map(fromMonacoRange) ?? record.viewState.selections;
    },
    saveViewState(documentId: string): ViewState {
      const record = requireRecord(documentId);
      if (documentId !== activeDocumentId) return record.viewState;
      const instance = requireEditor();
      record.viewState = {
        cursor: this.getPosition(documentId),
        selections: this.getSelection(documentId),
        scrollTop: instance.getScrollTop(),
        scrollLeft: instance.getScrollLeft(),
        foldedRanges: record.viewState.foldedRanges,
        monaco: instance.saveViewState(),
      };
      return record.viewState;
    },
    restoreViewState(documentId: string, viewState: ViewState) {
      const record = requireRecord(documentId);
      record.viewState = viewState || defaultViewState();
      if (documentId !== activeDocumentId) return;
      const instance = requireEditor();
      if (record.viewState.monaco) {
        instance.restoreViewState(record.viewState.monaco);
      } else {
        const selections = record.viewState.selections?.length
          ? record.viewState.selections.map(toMonacoSelection)
          : [monaco.Selection.fromPositions(toMonacoPosition(record.viewState.cursor))];
        instance.setSelections(selections);
        instance.setScrollTop(record.viewState.scrollTop || 0);
        instance.setScrollLeft(record.viewState.scrollLeft || 0);
      }
    },
    revealRange(documentId: string, range: EditorRange) {
      requireRecord(documentId);
      if (documentId !== activeDocumentId) return;
      const instance = requireEditor();
      const target = toMonacoRange(range);
      instance.setSelection(target);
      instance.revealRangeInCenterIfOutsideViewport(target, monaco.editor.ScrollType.Smooth);
      instance.focus();
      emitCursor();
    },
    wordRangeAtPosition(documentId: string, position: EditorPosition | null) {
      const record = requireRecord(documentId);
      if (!position || position.line < 1 || position.column < 1) return null;
      const word = record.model.getWordAtPosition(toMonacoPosition(position));
      if (!word) return null;
      return {
        start: editorPosition(position.line, word.startColumn),
        end: editorPosition(position.line, word.endColumn),
      };
    },
    setReadOnly(documentId: string, readOnly: boolean, reason = '') {
      const record = requireRecord(documentId);
      record.readOnly = Boolean(readOnly);
      record.readOnlyReason = reason;
      if (documentId === activeDocumentId) {
        requireEditor().updateOptions({
          readOnly: record.readOnly,
          readOnlyMessage: { value: reason || 'File in read-only mode' },
        });
      }
    },
    setDecorations(documentId: string, owner: string, items: Decoration[]) {
      const record = requireRecord(documentId);
      const previous = record.decorations.get(owner) ?? [];
      const next = record.model.deltaDecorations(previous, (items || []).map((item) => ({
        range: toMonacoRange(item.range),
        options: {
          className: item.className || undefined,
          inlineClassName: item.className || undefined,
          hoverMessage: item.title ? { value: item.title, isTrusted: false } : undefined,
        },
      })));
      record.decorations.set(owner, next);
    },
    positionFromMouseEvent(event: PointerEvent) {
      if (event.editorPosition) return event.editorPosition;
      const target = requireEditor().getTargetAtClientPoint(event.clientX, event.clientY);
      return target?.position ? fromMonacoPosition(target.position) : null;
    },
    invalidatePointerMetrics() {
      editor?.layout();
    },
    registerCommand(command: AdapterCommand) {
      assertLive();
      const instance = requireEditor();
      commandDisposables.get(command.id)?.dispose();
      const keybinding = commandKeybinding(command);
      const disposable = keybinding == null
        ? { dispose() {} }
        : instance.addAction({
          id: `codeatlas.${command.id}`,
          label: command.id,
          keybindings: [keybinding],
          run: () => command.run(activeDocumentId),
        });
      commandDisposables.set(command.id, disposable);
	  return { dispose: () => {
		disposable.dispose();
		if (commandDisposables.get(command.id) === disposable) commandDisposables.delete(command.id);
	  } };
    },
    onDidChangeContent(listener: (event: unknown) => void) { return on(listeners.content, listener); },
    onDidChangeCursor(listener: (event: unknown) => void) { return on(listeners.cursor, listener); },
    onMouseMove(listener: (event: PointerEvent) => void) { return on(listeners.mousemove, listener); },
    onMouseLeave(listener: (event: PointerEvent) => void) { return on(listeners.mouseleave, listener); },
    dispose() {
      if (disposed) return;
      disposed = true;
      commandDisposables.forEach((item) => item.dispose());
      commandDisposables.clear();
      editorDisposables.forEach((item) => item.dispose());
      records.forEach((record) => {
        record.contentDisposable.dispose();
        record.model.dispose();
      });
      records.clear();
	  pendingOpens.clear();
	  modelGenerations.clear();
      Object.values(listeners).forEach((set) => set.clear());
      editor?.dispose();
      editor = null;
    },
  };
  return adapter;
}
