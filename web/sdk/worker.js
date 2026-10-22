/* Classic Worker entry. The SDK never loads Go on the main UI thread. */
let runtimePromise;
let queue = Promise.resolve();

function initialize() {
  if (runtimePromise) return runtimePromise;
  runtimePromise = new Promise(async (resolve, reject) => {
    try {
      importScripts(new URL('./wasm_exec.js', self.location.href).href);
      const go = new Go();
      self.__psdkitReady = resolve;
      const response = await fetch(new URL('./psdkit.wasm', self.location.href));
      if (!response.ok) throw new Error(`Wasm HTTP ${response.status}`);
      const bytes = await response.arrayBuffer();
      const { instance } = await WebAssembly.instantiate(bytes, go.importObject);
      go.run(instance).catch(reject);
    } catch (error) { reject(error); }
  });
  return runtimePromise;
}

self.onmessage = (event) => {
  const { id, request, bytes } = event.data;
  queue = queue.then(async () => {
    try {
      const version = await initialize();
      if (request.operation === '__ready') { self.postMessage({ id, ready: true, version }); return; }
      const json = JSON.stringify(request, (_, value) => typeof value === 'bigint' ? { $int64: value.toString() } : value);
      const result = await self.psdkitWasm.call(json, bytes ? new Uint8Array(bytes) : undefined);
      const reply = JSON.parse(result.json);
      const transfers = [];
      for (let index = 0; index < (reply.binaries || []).length; index++) {
        const buffer = result.payloads[index].buffer;
        reply.binaries[index].data = buffer;
        transfers.push(buffer);
      }
      self.postMessage({ id, reply }, transfers);
    } catch (error) {
      self.postMessage({ id, reply: { ok: false, error: { code: 'worker_error', message: String(error) } } });
    }
  });
};
