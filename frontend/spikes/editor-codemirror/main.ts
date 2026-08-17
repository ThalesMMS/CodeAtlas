import { defaultKeymap, history, historyKeymap, indentWithTab } from '@codemirror/commands';
import { javascript } from '@codemirror/lang-javascript';
import { go } from '@codemirror/lang-go';
import { bracketMatching, defaultHighlightStyle, syntaxHighlighting } from '@codemirror/language';
import { highlightSelectionMatches, search, searchKeymap } from '@codemirror/search';
import { EditorState, StateEffect, StateField, type Extension } from '@codemirror/state';
import { Decoration, EditorView, ViewPlugin, drawSelection, dropCursor, highlightActiveLine, highlightActiveLineGutter, keymap, lineNumbers, rectangularSelection, type DecorationSet } from '@codemirror/view';

import { baseDocuments } from '../shared/scenarios';
import { installHarness, mountSpikePage } from '../shared/ui';
import type { EditorPosition, EditorRange, EditorSpike, SpikeDiagnostic, SpikeDocument, SpikeHover, SpikeSemanticToken } from '../shared/types';

const setMarks = StateEffect.define<DecorationSet>();
const markField = StateField.define<DecorationSet>({
  create: () => Decoration.none,
  update(value, transaction) {
    let next = value.map(transaction.changes);
    for (const effect of transaction.effects) {
      if (effect.is(setMarks)) next = effect.value;
    }
    return next;
  },
  provide: (field) => EditorView.decorations.from(field),
});

class CodeMirrorSpike implements EditorSpike {
  private view: EditorView;
  private states = new Map<string, EditorState>();
  private currentPath = '';
  private version = 1;
  private diagnostics: SpikeDiagnostic[] = [];
  private tokens: SpikeSemanticToken[] = [];
  private evidenceRange: EditorRange | null = null;

  constructor(private host: HTMLElement, private hoverCard: HTMLElement) {
    this.view = new EditorView({
      state: this.createState(baseDocuments[0]),
      parent: host,
    });
  }

  async openDocument(input: SpikeDocument): Promise<void> {
    if (this.currentPath) this.states.set(this.currentPath, this.view.state);
    this.currentPath = input.path;
    this.version = input.version;
    const state = this.states.get(input.path) ?? this.createState(input);
    this.view.setState(state);
    this.renderMarks();
  }

  async replaceContent(content: string, version: number): Promise<void> {
    this.version = version;
    this.view.dispatch({
      changes: { from: 0, to: this.view.state.doc.length, insert: content },
      selection: { anchor: 0 },
    });
    this.renderMarks();
  }

  async insertText(text: string, version: number): Promise<void> {
    const selection = this.view.state.selection.main;
    this.version = version;
    this.view.dispatch({
      changes: { from: selection.from, to: selection.to, insert: text },
      selection: { anchor: selection.from + text.length },
    });
  }

  getContent(): string {
    return this.view.state.doc.toString();
  }

  getPosition(): EditorPosition {
    return offsetToPosition(this.view.state, this.view.state.selection.main.head);
  }

  setPosition(position: EditorPosition): void {
    const offset = positionToOffset(this.view.state, position);
    this.view.dispatch({ selection: { anchor: offset }, effects: EditorView.scrollIntoView(offset, { y: 'center' }) });
    this.view.focus();
  }

  setDiagnostics(items: SpikeDiagnostic[]): void {
    this.diagnostics = items.filter((item) => item.version === this.version || item.version <= this.version);
    this.renderMarks();
  }

  setSemanticTokens(tokens: SpikeSemanticToken[]): void {
    this.tokens = tokens.filter((token) => token.version === this.version || token.version <= this.version);
    this.renderMarks();
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
    this.evidenceRange = range;
    this.renderMarks();
    this.view.dispatch({ effects: EditorView.scrollIntoView(positionToOffset(this.view.state, range.start), { y: 'center' }) });
  }

  dispose(): void {
    this.view.destroy();
    this.states.clear();
  }

  private createState(document: SpikeDocument): EditorState {
    return EditorState.create({
      doc: document.content,
      extensions: [
        lineNumbers(),
        highlightActiveLineGutter(),
        history(),
        drawSelection(),
        dropCursor(),
        rectangularSelection(),
        highlightActiveLine(),
        bracketMatching(),
        syntaxHighlighting(defaultHighlightStyle, { fallback: true }),
        search(),
        highlightSelectionMatches(),
        languageFor(document),
        markField,
        EditorView.lineWrapping,
        EditorState.tabSize.of(2),
        EditorState.readOnly.of(Boolean(document.readonly)),
        EditorView.editable.of(!document.readonly),
        keymap.of([indentWithTab, ...defaultKeymap, ...historyKeymap, ...searchKeymap]),
        ViewPlugin.define(() => ({})),
      ],
    });
  }

  private renderMarks(): void {
    const marks: { from: number; to: number; decoration: Decoration }[] = [];
    for (const token of this.tokens) {
      addMark(marks, this.view.state, token.range, Decoration.mark({ class: `token-${token.type}` }));
    }
    for (const diagnostic of this.diagnostics) {
      addMark(marks, this.view.state, diagnostic.range, Decoration.mark({
        class: diagnostic.severity === 'error' ? 'diagnostic-error' : 'diagnostic-warning',
        attributes: { title: diagnostic.message },
      }));
    }
    if (this.evidenceRange) {
      addMark(marks, this.view.state, this.evidenceRange, Decoration.mark({ class: 'evidence-decoration' }));
    }
    marks.sort((a, b) => a.from - b.from || a.to - b.to);
    this.view.dispatch({ effects: setMarks.of(Decoration.set(marks.map((mark) => mark.decoration.range(mark.from, mark.to)), true)) });
  }
}

function languageFor(document: SpikeDocument): Extension {
  if (document.language === 'go') return go();
  return javascript({ typescript: document.language !== 'javascript', jsx: document.language === 'tsx' });
}

function positionToOffset(state: EditorState, position: EditorPosition): number {
  const line = state.doc.line(Math.max(1, Math.min(position.line, state.doc.lines)));
  return Math.max(line.from, Math.min(line.to, line.from + position.column - 1));
}

function offsetToPosition(state: EditorState, offset: number): EditorPosition {
  const line = state.doc.lineAt(Math.max(0, Math.min(offset, state.doc.length)));
  return { line: line.number, column: offset - line.from + 1 };
}

function addMark(marks: { from: number; to: number; decoration: Decoration }[], state: EditorState, range: EditorRange, decoration: Decoration) {
  const from = positionToOffset(state, range.start);
  const to = positionToOffset(state, range.end);
  if (to <= from) return;
  marks.push({ from, to, decoration });
}

const page = mountSpikePage('CodeMirror 6 spike', baseDocuments);
const spike = new CodeMirrorSpike(page.editorHost, page.hover);
installHarness(spike, page, baseDocuments);
await page.setActiveDocument(baseDocuments[0]);
await spike.openDocument(baseDocuments[0]);
