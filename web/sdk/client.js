/** A queued, transferable browser bridge. No file is uploaded to a remote server. */
export class PsdkitClient {
  constructor({ workerURL = './wasm/worker.js', timeoutMS = 120000 } = {}) {
    this.workerURL = workerURL;
    this.timeoutMS = timeoutMS;
    this.pending = new Map();
    this.sequence = 0;
    this.start();
  }
  start() {
    this.worker = new Worker(this.workerURL, { name: 'psdkit' });
    this.worker.onmessage = ({ data }) => {
      const pending = this.pending.get(data.id);
      if (!pending) return;
      clearTimeout(pending.timer);
      this.pending.delete(data.id);
      if (data.ready) { pending.resolve(data); return; }
      if (data.reply.ok) pending.resolve(data.reply);
      else {
        const error = new Error(data.reply.error.message);
        Object.assign(error, data.reply.error);
        pending.reject(error);
      }
    };
    this.worker.onerror = (event) => this.terminate(new Error(event.message || 'Worker stopped'));
  }
  call(operation, { target, args = [], links, name, kind, alias, timeoutMS = 60000, imageOutput, bytes } = {}) {
    if (!this.worker) return Promise.reject(new Error('Worker is closed'));
    const id = ++this.sequence;
    const request = { operation, target, args, links, name, kind, alias, timeoutMS, imageOutput };
    return new Promise((resolve, reject) => {
      const timer = setTimeout(() => {
        const error = new Error('Worker timed out; workspace was released');
        error.code = 'worker_timeout';
        this.terminate(error);
      }, this.timeoutMS);
      this.pending.set(id, { resolve, reject, timer });
      const buffer = bytes instanceof ArrayBuffer ? bytes : ArrayBuffer.isView(bytes)
        ? bytes.byteOffset === 0 && bytes.byteLength === bytes.buffer.byteLength
          ? bytes.buffer : bytes.buffer.slice(bytes.byteOffset, bytes.byteOffset + bytes.byteLength) : undefined;
      try { this.worker.postMessage({ id, request, bytes: buffer }, buffer ? [buffer] : []); }
      catch (error) { clearTimeout(timer); this.pending.delete(id); reject(error); }
    });
  }
  ready() { return this.call('__ready'); }
  async importFile(file, kind) {
    return this.call('asset.put', { bytes: await file.arrayBuffer(), name: file.name, kind });
  }
  failAll(error) {
    for (const pending of this.pending.values()) { clearTimeout(pending.timer); pending.reject(error); }
    this.pending.clear();
  }
  terminate(error = new Error('Worker terminated; all sessions were released')) {
    this.worker?.terminate();
    this.worker = null;
    this.failAll(error);
  }
  async reset() { this.terminate(); this.start(); return this.ready(); }
  close() { this.terminate(); }
}

export function binary(reply, id) { return reply.binaries?.find((item) => item.id === id); }

export function download(item, name = item.name) {
  const url = URL.createObjectURL(new Blob([item.data], { type: item.mime }));
  const link = Object.assign(document.createElement('a'), { href: url, download: name });
  link.click();
  setTimeout(() => URL.revokeObjectURL(url), 1000);
}
