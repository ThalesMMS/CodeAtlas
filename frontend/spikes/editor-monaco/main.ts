import * as monaco from 'monaco-editor';
import editorWorker from 'monaco-editor/editor/editor.worker.js?worker';
import tsWorker from 'monaco-editor/language/typescript/ts.worker.js?worker';

import { baseDocuments } from '../shared/scenarios';
import { lineColumnToOffset, offsetToLineColumn } from '../shared/hash';
import { installHarness, mountSpikePage } from '../shared/ui';
import type { EditorPosition, EditorRange, EditorSpike, SpikeDiagnostic, SpikeDocument, SpikeHover, SpikeSemanticToken } from '../shared/types';

(self as any).MonacoEnvironment = {
  getWorker(_: string, label: string) {
    if (label === 'typescript' || label === 'javascript') return new tsWorker({ name: 'monaco-ts-worker' });
    return new editorWorker({ name: 'monaco-editor-worker' });
  },
};

monaco.languages.register({ id: 'go' });

class MonacoSpike implements EditorSpike {
  private editor: monaco.editor.IStandaloneCodeEditor;
  private models = new Map<string, monaco.editor.ITextModel>();
  private evidenceDecorations: monaco.editor.IEditorDecorationsCollection;
  private tokenDecorations: monaco.editor.IEditorDecorationsCollection;
  private diagnosticDecorations: monaco.editor.IEditorDecorationsCollection;
  private currentPath = '';
  private version = 1;

  constructor(private host: HTMLElement, private hoverCard: HTMLElement) {
    this.editor = monaco.editor.create(host, {
      automaticLayout: true,
      fontSize: 13,
      lineNumbers: 'on',
      minimap: { enabled: false },
      tabSize: 2,
      insertSpaces: true,
      scrollBeyondLastLine: false,
      accessibilitySupport: 'on',
    });
    this.evidenceDecorations = this.editor.createDecorationsCollection();
    this.tokenDecorations = this.editor.createDecorationsCollection();
    this.diagnosticDecorations = this.editor.createDecorationsCollection();
  }

  async openDocument(input: SpikeDocument): Promise<void> {
    this.currentPath = input.path;
    this.version = input.version;
    let model = this.models.get(input.path);
    if (!model) {
      model = monaco.editor.createModel(input.content, monacoLanguage(input.language), monaco.Uri.parse(`file:///${input.path}`));
      this.models.set(input.path, model);
    } else {
      model.setValue(input.content);
    }
    this.editor.setModel(model);
    this.editor.updateOptions({ readOnly: Boolean(input.readonly) });
  }

  async replaceContent(content: string, version: number): Promise<void> {
    const model = this.mustModel();
    this.version = version;
    model.pushEditOperations([], [{ range: model.getFullModelRange(), text: content }], () => null);
  }

  async insertText(text: string, version: number): Promise<void> {
    const position = this.editor.getPosition() ?? new monaco.Position(1, 1);
    this.version = version;
    this.editor.executeEdits('codeatlas-spike', [{
      range: new monaco.Range(position.lineNumber, position.column, position.lineNumber, position.column),
      text,
      forceMoveMarkers: true,
    }]);
  }

  getContent(): string {
    return this.mustModel().getValue();
  }

  getPosition(): EditorPosition {
    const position = this.editor.getPosition() ?? new monaco.Position(1, 1);
    return { line: position.lineNumber, column: position.column };
  }

  setPosition(position: EditorPosition): void {
    this.editor.setPosition({ lineNumber: position.line, column: position.column });
    this.editor.revealPositionInCenter({ lineNumber: position.line, column: position.column });
    this.editor.focus();
  }

  setDiagnostics(items: SpikeDiagnostic[]): void {
    const model = this.mustModel();
    const current = items.filter((item) => item.version === this.version || item.version <= this.version);
    monaco.editor.setModelMarkers(model, 'codeatlas-spike', current.map((item) => ({
      startLineNumber: item.range.start.line,
      startColumn: item.range.start.column,
      endLineNumber: item.range.end.line,
      endColumn: item.range.end.column,
      message: item.message,
      source: item.source,
      severity: item.severity === 'error' ? monaco.MarkerSeverity.Error : item.severity === 'warning' ? monaco.MarkerSeverity.Warning : monaco.MarkerSeverity.Info,
    })));
    this.diagnosticDecorations.set(current.map((item) => ({
      range: toMonacoRange(item.range),
      options: { inlineClassName: item.severity === 'error' ? 'diagnostic-error' : 'diagnostic-warning', hoverMessage: { value: item.message } },
    })));
  }

  setSemanticTokens(tokens: SpikeSemanticToken[]): void {
    const current = tokens.filter((token) => token.version === this.version || token.version <= this.version);
    this.tokenDecorations.set(current.map((token) => ({
      range: toMonacoRange(token.range),
      options: { inlineClassName: `token-${token.type}` },
    })));
  }

  async showHover(input: SpikeHover): Promise<void> {
    const top = Math.min(window.innerHeight - 280, Math.max(48, input.range.start.line * 18));
    this.hoverCard.classList.remove('hidden');
    this.hoverCard.style.left = '24px';
    this.hoverCard.style.top = `${top}px`;
    this.hoverCard.innerHTML = '';
    const title = document.createElement('strong');
    title.textContent = input.summary.slice(0, 240);
    this.hoverCard.appendChild(title);
    const list = document.createElement('ul');
    for (const id of input.evidenceIds.slice(0, 6)) {
      const item = document.createElement('li');
      item.textContent = id;
      list.appendChild(item);
    }
    this.hoverCard.appendChild(list);
    const button = document.createElement('button');
    button.type = 'button';
    button.textContent = input.seeMoreLabel;
    this.hoverCard.appendChild(button);
  }

  revealRange(range: EditorRange): void {
    this.evidenceDecorations.set([{
      range: toMonacoRange(range),
      options: { className: 'evidence-decoration', inlineClassName: 'evidence-decoration' },
    }]);
    this.editor.revealRangeInCenter(toMonacoRange(range));
  }

  dispose(): void {
    this.editor.dispose();
    for (const model of this.models.values()) model.dispose();
    this.models.clear();
  }

  private mustModel(): monaco.editor.ITextModel {
    const model = this.editor.getModel();
    if (!model) throw new Error('Monaco model not opened');
    return model;
  }
}

function monacoLanguage(language: SpikeDocument['language']): string {
  if (language === 'tsx') return 'typescript';
  return language;
}

function toMonacoRange(range: EditorRange): monaco.Range {
  return new monaco.Range(range.start.line, range.start.column, range.end.line, range.end.column);
}

function installMonacoPositionHelpers() {
  Object.assign(window, {
    monacoSpikeOffsetToPosition(content: string, offset: number) {
      return offsetToLineColumn(content, offset);
    },
    monacoSpikePositionToOffset(content: string, position: EditorPosition) {
      return lineColumnToOffset(content, position.line, position.column);
    },
  });
}

const page = mountSpikePage('Monaco Editor spike', baseDocuments);
const spike = new MonacoSpike(page.editorHost, page.hover);
installHarness(spike, page, baseDocuments);
installMonacoPositionHelpers();
await page.setActiveDocument(baseDocuments[0]);
await spike.openDocument(baseDocuments[0]);
