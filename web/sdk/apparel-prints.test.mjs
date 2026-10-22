import assert from 'node:assert/strict';
import test from 'node:test';

import { normalizePrint, replacePrintContents } from '../../docs/assets/js/apparel-prints.js';

function workbench({ failure } = {}) {
  const assets = new Set(['builtin']), resources = new Map();
  const calls = [];
  let sequence = 0;
  return {
    assets, resources, calls,
    async call(operation, request) {
      calls.push([operation, request]);
      if (operation === 'asset.put') {
        const asset = 'a' + ++sequence; assets.add(asset);
        return { data: { asset } };
      }
      if (operation === 'raster.buildPSD') {
        const handle = 'r' + ++sequence;
        const input = request.args[0].$asset;
        assert.ok(assets.has(input));
        resources.set(handle, input);
        return { data: { results: [{ handle }] }, binaries: [{ data: new ArrayBuffer(4) }] };
      }
      if (operation === 'editor.ReplaceContentsWithOptions') {
        assert.ok(assets.has(request.args[1].$asset));
        if (failure) throw failure;
        return {};
      }
      if (operation === 'resource.release') {
        assert.ok(resources.delete(request.target));
        return {};
      }
      if (operation === 'asset.remove') {
        assert.ok(![...resources.values()].includes(request.target), 'input removed before dependent handle');
        assert.ok(assets.delete(request.target));
        return {};
      }
      throw new Error('Unexpected operation: ' + operation);
    },
  };
}

test('repeated bundled and local print replacement releases temporary assets and dependent handles', async () => {
  const engine = workbench();
  const policy = { ApproximateCustomWarp: true, AssumeUntaggedSRGB: true };
  const doc = { editor: 'editor', smart: 15 };
  for (const print of [
    { asset: 'builtin', name: 'bundled.png' },
    { blob: new Blob(['local pixels']), name: 'local.png' },
    { asset: 'builtin', name: 'bundled.png' },
  ]) {
    await replacePrintContents(engine, doc, print, policy);
    assert.deepEqual([...engine.assets], ['builtin']);
    assert.equal(engine.resources.size, 0);
  }
  const replacements = engine.calls.filter(([operation]) => operation === 'editor.ReplaceContentsWithOptions');
  assert.equal(replacements.length, 3);
  for (const [, request] of replacements) {
    assert.equal(request.target, 'editor');
    assert.equal(request.args[0], 15);
    assert.deepEqual(request.args[2], policy);
  }
});

test('rejected replacement preserves its error and releases temporary local inputs', async () => {
  const failure = new Error('unsupported edit');
  const engine = workbench({ failure });
  await assert.rejects(replacePrintContents(engine, { editor: 'editor', smart: 15 }, {
    blob: new Blob(['local pixels']), name: 'local.png',
  }, {}), (error) => error === failure);
  assert.deepEqual([...engine.assets], ['builtin']);
  assert.equal(engine.resources.size, 0);
});

test('unsupported formats and oversized files are rejected before browser decoding', async () => {
  await assert.rejects(normalizePrint({ type: 'image/svg+xml', size: 6 }), /PNG、JPEG 或 WebP/);
  await assert.rejects(normalizePrint({ type: 'image/png', size: 21 * 1024 * 1024 }), /20 MB/);
});

test('prebuilt contents skip PNG decoding and PSD encoding without removing the borrowed asset', async () => {
  const engine = workbench();
  engine.assets.add('contents');
  await replacePrintContents(engine, { editor: 'editor', smart: 15 }, { contentsAsset: 'contents' }, {});
  assert.deepEqual(engine.calls.map(([operation]) => operation), ['editor.ReplaceContentsWithOptions']);
  assert.deepEqual([...engine.assets], ['builtin', 'contents']);
});
