import { sha256 } from './hash';
import type { DocumentSyncController, SpikeDocument, SyncEvent } from './types';

type Listener = (event: SyncEvent) => void;

export class FakeDocumentSyncController implements DocumentSyncController {
  private document: SpikeDocument | null = null;
  private contentHash = '';
  private dirty = false;
  private conflict = false;
  private pendingContent: string | null = null;
  private pendingVersion = 0;
  private listeners = new Set<Listener>();

  async open(input: SpikeDocument): Promise<SyncEvent> {
    this.document = { ...input };
    this.contentHash = await sha256(input.content);
    this.dirty = false;
    this.conflict = false;
    this.pendingContent = null;
    this.pendingVersion = input.version;
    return this.emit('opened', input.version, this.contentHash);
  }

  queueLocalChange(content: string): number {
    this.mustDocument();
    this.pendingVersion = this.document!.version + 1;
    this.pendingContent = content;
    this.dirty = true;
    return this.pendingVersion;
  }

  async flush(): Promise<SyncEvent> {
    const document = this.mustDocument();
    if (this.pendingContent == null) {
      return this.emit('acked', document.version, this.contentHash);
    }
    const nextContent = this.pendingContent;
    const nextVersion = this.pendingVersion;
    const nextHash = await sha256(nextContent);
    this.document = { ...document, content: nextContent, version: nextVersion };
    this.contentHash = nextHash;
    this.pendingContent = null;
    return this.emit('acked', nextVersion, nextHash);
  }

  applyAck(version: number): SyncEvent {
    const document = this.mustDocument();
    if (version < document.version) {
      return this.emit('acked', document.version, this.contentHash, 'stale ack ignored');
    }
    this.document = { ...document, version };
    return this.emit('acked', version, this.contentHash);
  }

  simulateOutOfOrderAck(version: number): SyncEvent {
    const document = this.mustDocument();
    const accepted = version >= document.version;
    if (accepted) this.document = { ...document, version };
    return this.emit('acked', this.document!.version, this.contentHash, accepted ? undefined : 'out-of-order ack ignored');
  }

  simulateExternalConflict(contentHash: string): SyncEvent {
    const document = this.mustDocument();
    this.conflict = true;
    return this.emit('conflict', document.version, contentHash, 'external change preserved local buffer');
  }

  async save(): Promise<SyncEvent> {
    const document = this.mustDocument();
    if (this.conflict) {
      return this.emit('conflict', document.version, this.contentHash, 'save blocked by external conflict');
    }
    this.dirty = false;
    return this.emit('saved', document.version, this.contentHash);
  }

  async close(discard = false): Promise<SyncEvent> {
    const document = this.mustDocument();
    if (this.dirty && !discard) {
      return this.emit('conflict', document.version, this.contentHash, 'close dirty blocked');
    }
    return this.emit('closed', document.version, this.contentHash);
  }

  snapshot(): SpikeDocument & { contentHash: string; dirty: boolean; conflict: boolean } {
    const document = this.mustDocument();
    return { ...document, contentHash: this.contentHash, dirty: this.dirty, conflict: this.conflict };
  }

  subscribe(listener: Listener): () => void {
    this.listeners.add(listener);
    return () => this.listeners.delete(listener);
  }

  private mustDocument(): SpikeDocument {
    if (!this.document) throw new Error('document not opened');
    return this.document;
  }

  private emit(type: SyncEvent['type'], version: number, contentHash: string, message?: string): SyncEvent {
    const event: SyncEvent = { type, path: this.mustDocument().path, version, contentHash, message };
    for (const listener of this.listeners) listener(event);
    return event;
  }
}
