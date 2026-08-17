import './styles.css';

import { FakeDocumentSyncController } from './document-controller';
import { diagnosticSet, hoverFixture, semanticTokenSet, syntheticDocumentContent } from './scenarios';
import type { EditorRange, EditorSpike, SpikeDocument, SyncEvent } from './types';

export interface SpikePage {
  root: HTMLElement;
  editorHost: HTMLElement;
  hover: HTMLElement;
  problems: HTMLElement;
  status: HTMLElement;
  controller: FakeDocumentSyncController;
  setActiveDocument(document: SpikeDocument): Promise<void>;
  updateProblems(count: number): void;
}

export function mountSpikePage(editorName: string, documents: SpikeDocument[]): SpikePage {
  const root = document.getElementById('app');
  if (!root) throw new Error('#app not found');
  root.innerHTML = `
    <main class="spike-shell">
      <header class="spike-toolbar">
        <div>
          <span class="spike-title">${escapeText(editorName)}</span>
          <span data-testid="dirty-indicator" aria-live="polite"></span>
        </div>
        <div class="spike-controls">
          <select data-testid="document-select" aria-label="Document"></select>
          <button data-action="hover" type="button">Hover</button>
          <button data-action="diagnostics" type="button">Diagnostics</button>
          <button data-action="tokens" type="button">Tokens</button>
          <button data-action="conflict" type="button">Conflict</button>
          <button data-action="save" type="button">Save</button>
        </div>
      </header>
      <nav class="spike-tabs" data-testid="tabs" aria-label="Open documents"></nav>
      <section class="editor-host" data-testid="editor-host"></section>
      <footer class="spike-status">
        <span data-testid="status">Initializing</span>
        <div class="problems" data-testid="problems"></div>
      </footer>
      <aside class="spike-hover hidden" role="dialog" aria-live="polite" data-testid="hover-card"></aside>
    </main>
  `;
  const select = root.querySelector<HTMLSelectElement>('[data-testid="document-select"]')!;
  const tabs = root.querySelector<HTMLElement>('[data-testid="tabs"]')!;
  const status = root.querySelector<HTMLElement>('[data-testid="status"]')!;
  const dirty = root.querySelector<HTMLElement>('[data-testid="dirty-indicator"]')!;
  const problems = root.querySelector<HTMLElement>('[data-testid="problems"]')!;
  const hover = root.querySelector<HTMLElement>('[data-testid="hover-card"]')!;
  const editorHost = root.querySelector<HTMLElement>('[data-testid="editor-host"]')!;
  const controller = new FakeDocumentSyncController();

  for (const doc of documents) {
    const option = document.createElement('option');
    option.value = doc.path;
    option.textContent = doc.path;
    select.appendChild(option);
    const tab = document.createElement('button');
    tab.type = 'button';
    tab.textContent = doc.path;
    tab.dataset.path = doc.path;
    tab.setAttribute('role', 'tab');
    tabs.appendChild(tab);
  }

  controller.subscribe((event: SyncEvent) => {
    status.textContent = `${event.type} v${event.version}${event.message ? `: ${event.message}` : ''}`;
    const snapshot = controller.snapshot();
    dirty.textContent = snapshot.dirty ? 'modified' : '';
  });

  return {
    root,
    editorHost,
    hover,
    problems,
    status,
    controller,
    async setActiveDocument(doc: SpikeDocument) {
      select.value = doc.path;
      for (const button of Array.from(tabs.querySelectorAll('button'))) {
        button.setAttribute('aria-selected', String(button.dataset.path === doc.path));
      }
      await controller.open(doc);
    },
    updateProblems(count: number) {
      problems.replaceChildren();
      for (let i = 0; i < Math.min(count, 12); i += 1) {
        const item = document.createElement('span');
        item.className = i % 5 === 0 ? 'problem-error' : 'problem-warning';
        item.textContent = `D${i + 1}`;
        problems.appendChild(item);
      }
      if (count > 12) {
        const more = document.createElement('span');
        more.textContent = `+${count - 12}`;
        problems.appendChild(more);
      }
    },
  };
}

export function installHarness(editor: EditorSpike, page: SpikePage, documents: SpikeDocument[]) {
  const api = {
    async runBasicScenario() {
      const first = documents[0];
      await page.setActiveDocument(first);
      await editor.openDocument(first);
      await editor.replaceContent(`${first.content}\n// local edit\n`, first.version + 1);
      page.controller.queueLocalChange(editor.getContent());
      await page.controller.flush();
      const expanded = syntheticDocumentContent(620 * 1024);
      await editor.replaceContent(expanded, first.version + 2);
      editor.setDiagnostics(diagnosticSet(100, first.version + 2));
      page.updateProblems(100);
      editor.setSemanticTokens(semanticTokenSet(10_000, first.version + 2));
      await editor.showHover(hoverFixture);
      editor.revealRange(hoverFixture.range);
      editor.setPosition({ line: 3, column: 3 });
      return { contentLength: editor.getContent().length, position: editor.getPosition() };
    },
    async openFixture(document: SpikeDocument) {
      editor.setDiagnostics([]);
      editor.setSemanticTokens([]);
      page.updateProblems(0);
      await page.setActiveDocument(document);
      await editor.openDocument(document);
      return editor.getContent().length;
    },
    async typeBurst(iterations = 40) {
      const start = performance.now();
      const samples: number[] = [];
      for (let i = 0; i < iterations; i += 1) {
        const tick = performance.now();
        await editor.insertText(`\n// burst ${i}`, page.controller.snapshot().version + i + 1);
        samples.push(performance.now() - tick);
      }
      samples.sort((a, b) => a - b);
      return {
        totalMs: performance.now() - start,
        p50: percentile(samples, 0.5),
        p95: percentile(samples, 0.95),
      };
    },
    applyDiagnostics(count: number) {
      const start = performance.now();
      editor.setDiagnostics(diagnosticSet(count, page.controller.snapshot().version));
      page.updateProblems(count);
      return performance.now() - start;
    },
    applySemanticTokens(count: number) {
      const start = performance.now();
      editor.setSemanticTokens(semanticTokenSet(count, page.controller.snapshot().version));
      return performance.now() - start;
    },
    reveal(range: EditorRange) {
      const start = performance.now();
      editor.revealRange(range);
      return performance.now() - start;
    },
    simulateConflict(hash: string) {
      return page.controller.simulateExternalConflict(hash);
    },
    dispose() {
      editor.dispose();
    },
  };
  Object.assign(window, { spike: editor, spikeHarness: api, spikeReady: true });
  return api;
}

function percentile(values: number[], p: number): number {
  if (values.length === 0) return 0;
  const index = Math.min(values.length - 1, Math.max(0, Math.floor(values.length * p)));
  return values[index];
}

function escapeText(value: string): string {
  return value.replace(/[&<>"']/g, (char) => ({ '&': '&amp;', '<': '&lt;', '>': '&gt;', '"': '&quot;', "'": '&#039;' })[char]!);
}
