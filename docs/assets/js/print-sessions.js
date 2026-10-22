import { replacePrintContents } from './apparel-prints.js';

// One worker keeps at most three print-specific editors and 16 MiB of encoded previews.
// Editors retain real projected pixels, so PNG and PSD downloads always describe the active print.
export class PrintSessions {
  constructor(engine, { asset, document, maxEntries = 3, maxPreviewBytes = 16 * 1024 * 1024 }) {
    this.engine = engine;
    this.asset = asset;
    this.initial = document;
    this.smart = document.smart;
    this.maxEntries = maxEntries;
    this.maxPreviewBytes = maxPreviewBytes;
    this.entries = new Map();
    this.edits = new Map();
    this.version = 0;
    this.current = null;
  }

  async activate(print, policy) {
    const key = JSON.stringify(policy);
    let entry = this.entries.get(print);
    if (entry && entry.policy !== key) {
      await this.remove(print);
      entry = null;
    }
    const previousLocal = print.id === 'local'
      ? [...this.entries.keys()].find((item) => item !== print && item.id === 'local') : null;
    if (!entry && previousLocal) {
      const previous = this.entries.get(previousLocal);
      await this.synchronize(previous);
      // ReplaceContents commits atomically, preserving the previous upload if projection fails.
      await replacePrintContents(this.engine, previous.doc, print, policy);
      entry = { ...previous, policy: key, preview: null };
      this.entries.delete(previousLocal);
    }
    if (!entry) {
      while (this.entries.size >= this.maxEntries) {
        const oldest = [...this.entries.keys()].find((item) => this.entries.get(item) !== this.current);
        if (!oldest) throw new Error('没有足够的印花缓存空间');
        await this.remove(oldest);
      }
      const doc = this.initial || (await this.engine.call('document.open', { args: [{ $asset: this.asset }] })).data;
      this.initial = null;
      doc.smart = this.smart;
      entry = { doc, policy: key, version: -1, preview: null };
      try {
        await this.synchronize(entry);
        await replacePrintContents(this.engine, doc, print, policy);
      } catch (error) {
        await this.engine.call('resource.release', { target: doc.document }).catch(() => {});
        throw error;
      }
    } else {
      await this.synchronize(entry);
    }
    this.entries.delete(print);
    this.entries.set(print, entry);
    this.current = entry;
    return entry.doc;
  }

  async synchronize(entry) {
    if (entry.version === this.version) return;
    for (const [id, edit] of this.edits) {
      for (const [operation, value] of Object.entries(edit)) {
        await this.engine.call(operation, { target: entry.doc.editor, args: [id, value] });
      }
    }
    entry.preview = null;
    entry.version = this.version;
  }

  async change(operation, id, value) {
    await this.engine.call(operation, { target: this.current.doc.editor, args: [id, value] });
    const edit = this.edits.get(id) || {};
    edit[operation] = value;
    this.edits.set(id, edit);
    this.version++;
    this.current.version = this.version;
    for (const entry of this.entries.values()) entry.preview = null;
  }

  async render() {
    const entry = this.current;
    if (entry.preview) return { reply: entry.preview, cached: true };
    const reply = await this.engine.call('editor.Composite', {
      target: entry.doc.editor, args: [{}], imageOutput: 'png',
    });
    try {
      // Only encoded PNGs survive the call; retained Go image handles are released below.
      const preview = { ...reply, data: { ...reply.data,
        results: (reply.data?.results || []).map(({ value }) => ({ value })),
      } };
      const bytes = (reply.binaries || []).reduce((size, binary) => size + binary.data.byteLength, 0);
      if (bytes <= this.maxPreviewBytes) {
        let retained = bytes;
        for (const other of [...this.entries.values()].reverse()) {
          if (other === entry || !other.preview) continue;
          retained += other.preview.binaries.reduce((size, binary) => size + binary.data.byteLength, 0);
          if (retained > this.maxPreviewBytes) other.preview = null;
        }
        entry.preview = preview;
      }
      return { reply: preview, cached: false };
    } finally {
      for (const item of reply.data?.results || []) {
        if (item.handle) await this.engine.call('resource.release', { target: item.handle });
      }
    }
  }

  async remove(print) {
    const entry = this.entries.get(print);
    if (!entry) return;
    await this.engine.call('resource.release', { target: entry.doc.document });
    this.entries.delete(print);
    if (this.current === entry) this.current = null;
  }

  async close() {
    for (const print of [...this.entries.keys()]) await this.remove(print);
    if (this.initial) await this.engine.call('resource.release', { target: this.initial.document });
    this.initial = null;
  }
}
