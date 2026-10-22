import test from 'node:test';
import assert from 'node:assert/strict';
import { createHash } from 'node:crypto';
import { gzipSync } from 'node:zlib';
import { loadGarmentSample } from './samples.js';

test('apparel sample preserves the Pages subpath and rejects corrupt inputs before import', async () => {
  const oldFetch = globalThis.fetch;
  const files = new Map(['muban.psd', 'mountain.png', 'botanical.png'].map((name) => [name, new TextEncoder().encode(name)]));
  files.set('mountain.psd.gz', gzipSync('mountain.psd'));
  const descriptor = (file) => ({ file, name: file, sha256: createHash('sha256').update(files.get(file)).digest('hex') });
  const manifest = { version: 1, document: descriptor('muban.psd'), prints: [descriptor('mountain.png'), descriptor('botanical.png')] };
  manifest.prints[0].contents = { ...descriptor('mountain.psd.gz'), compression: 'gzip' };
  const requested = [];
  globalThis.fetch = async (url) => {
    requested.push(String(url));
    const name = new URL(url).pathname.split('/').pop();
    return name === 'manifest.json' ? new Response(JSON.stringify(manifest)) : new Response(files.get(name));
  };
  let imported = 0;
  const engine = { async call(operation, request) {
    if (operation === 'asset.put') {
      if (request.kind === 'contents') assert.equal(new TextDecoder().decode(request.bytes), 'mountain.psd');
      return { data: { asset: `a${++imported}` } };
    }
    if (operation === 'document.open') return { data: { document: 'd1' } };
    throw new Error(operation);
  } };
  try {
    const sample = await loadGarmentSample(engine, 'https://example.test/psdkit/demo/samples/');
    assert.equal(sample.asset, 'a1');
    assert.deepEqual(sample.prints.map((item) => item.asset), ['a2', 'a3']);
    assert.equal(sample.prints[0].contentsAsset, 'a4');
    assert.equal(sample.prints[1].contentsAsset, undefined);
    assert.ok(requested.every((url) => url.startsWith('https://example.test/psdkit/demo/samples/')));
    manifest.prints[0].sha256 = 'wrong';
    await assert.rejects(loadGarmentSample(engine, 'https://example.test/psdkit/demo/samples/'), /校验失败/);
    assert.equal(imported, 4);
  } finally { globalThis.fetch = oldFetch; }
});
