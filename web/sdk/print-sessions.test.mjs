import assert from 'node:assert/strict';
import test from 'node:test';

import { PrintSessions } from '../../docs/assets/js/print-sessions.js';

function workbench() {
  let sequence = 0;
  const assets = new Map([['template', 'template'], ['mountain', 'mountain'], ['botanical', 'botanical']]);
  const documents = new Map(), editors = new Map(), resources = new Set(), calls = [];
  function open() {
    const doc = { document: 'document' + ++sequence, editor: 'editor' + sequence, smart: 15 };
    const state = { doc, print: null, visible: true, opacity: 255 };
    documents.set(doc.document, state); editors.set(doc.editor, state);
    return doc;
  }
  const initial = open();
  const engine = { async call(operation, request) {
    calls.push([operation, request]);
    if (operation === 'document.open') return { data: open() };
    if (operation === 'asset.put') {
      const asset = 'asset' + ++sequence;
      assets.set(asset, new TextDecoder().decode(request.bytes));
      return { data: { asset } };
    }
    if (operation === 'asset.remove') { assert.ok(assets.delete(request.target)); return {}; }
    if (operation === 'raster.buildPSD') {
      const handle = 'resource' + ++sequence; resources.add(handle);
      return { data: { results: [{ handle }] }, binaries: [{ data: new TextEncoder().encode(assets.get(request.args[0].$asset)) }] };
    }
    if (operation === 'editor.ReplaceContentsWithOptions') {
      const print = assets.get(request.args[1].$asset);
      if (print === 'broken') throw new Error('replacement failed');
      editors.get(request.target).print = print;
      return {};
    }
    if (operation === 'resource.release') {
      const state = documents.get(request.target);
      if (state) { documents.delete(request.target); editors.delete(state.doc.editor); }
      else assert.ok(resources.delete(request.target), 'double release');
      return {};
    }
    if (operation === 'editor.SetVisible' || operation === 'editor.SetOpacity') {
      editors.get(request.target)[operation.endsWith('Visible') ? 'visible' : 'opacity'] = request.args[1];
      return {};
    }
    if (operation === 'editor.Composite') {
      assert.equal(request.imageOutput, 'png');
      const handle = 'resource' + ++sequence; resources.add(handle);
      const state = editors.get(request.target);
      const text = `${state.print}:${state.visible}:${state.opacity}`;
      return { data: { results: [{ handle, value: { image: { binary: 'png' }, text } }] },
        binaries: [{ id: 'png', mime: 'image/png', data: new TextEncoder().encode(text) }] };
    }
    if (operation === 'editor.WritePSD') return structuredClone(editors.get(request.target));
    throw new Error('unexpected operation: ' + operation);
  } };
  const sessions = new PrintSessions(engine, { asset: 'template', document: initial });
  const count = (operation) => calls.filter(([item]) => item === operation).length;
  return { engine, sessions, assets, documents, resources, count };
}

const mountain = { id: 'mountain', asset: 'mountain', name: '山野日落' };
const botanical = { id: 'botanical', asset: 'botanical', name: '向阳花园' };
const policy = { ApproximateCustomWarp: true, AssumeUntaggedSRGB: true };
const text = ({ reply }) => reply.data.results[0].value.text;

test('cached print restores the real editor and skips build, replacement and composition', async () => {
  const state = workbench();
  for (const print of [mountain, botanical]) {
    await state.sessions.activate(print, policy);
    assert.equal((await state.sessions.render()).cached, false);
  }
  const doc = await state.sessions.activate(mountain, policy);
  const cached = await state.sessions.render();
  assert.equal(cached.cached, true);
  assert.equal(text(cached), 'mountain:true:255');
  assert.equal(cached.reply.data.results[0].handle, undefined);
  const exported = await state.engine.call('editor.WritePSD', { target: doc.editor });
  assert.equal(exported.print, 'mountain');
  for (const operation of ['raster.buildPSD', 'editor.ReplaceContentsWithOptions', 'editor.Composite']) {
    assert.equal(state.count(operation), 2);
  }
  assert.equal(state.resources.size, 0);
  await state.sessions.close();
  assert.equal(state.documents.size, 0);
  assert.equal(state.assets.size, 3);
});

test('layer changes invalidate previews and synchronize inactive editors without repeating projection', async () => {
  const state = workbench();
  for (const print of [mountain, botanical]) {
    await state.sessions.activate(print, policy);
    await state.sessions.render();
  }
  await state.sessions.change('editor.SetVisible', 15, false);
  await state.sessions.change('editor.SetOpacity', 15, 128);
  assert.equal(text(await state.sessions.render()), 'botanical:false:128');
  await state.sessions.activate(mountain, policy);
  const rendered = await state.sessions.render();
  assert.equal(rendered.cached, false);
  assert.equal(text(rendered), 'mountain:false:128');
  assert.equal(state.count('editor.ReplaceContentsWithOptions'), 2);
  assert.equal(state.count('editor.Composite'), 4);
  await state.sessions.close();
});

test('uploads, entry eviction, preview budget and close release retained resources', async () => {
  const state = workbench();
  for (const print of [mountain, botanical]) {
    await state.sessions.activate(print, policy);
    await state.sessions.render();
  }
  state.sessions.maxPreviewBytes = 25;
  for (let index = 0; index < 5; index++) {
    const print = { id: 'local', name: 'local.png', blob: new Blob(['local' + index]) };
    await state.sessions.activate(print, policy);
    await state.sessions.render();
    assert.ok(state.sessions.entries.size <= 3);
    assert.ok(state.sessions.entries.has(mountain));
    assert.ok(state.sessions.entries.has(botanical));
    assert.equal(state.documents.size, state.sessions.entries.size);
    const bytes = [...state.sessions.entries.values()].reduce((size, entry) =>
      size + (entry.preview?.binaries.reduce((sum, binary) => sum + binary.data.byteLength, 0) || 0), 0);
    assert.ok(bytes <= 25);
    assert.equal(state.resources.size, 0);
    assert.equal(state.assets.size, 3);
  }
  await state.sessions.close();
  assert.equal(state.documents.size, 0);
});

test('replacement failure closes the new editor and leaves the previous active document intact', async () => {
  const state = workbench();
  const doc = await state.sessions.activate(mountain, policy);
  await state.sessions.render();
  await assert.rejects(state.sessions.activate({ id: 'local', blob: new Blob(['broken']) }, policy), /replacement failed/);
  assert.equal(state.sessions.current.doc, doc);
  assert.equal((await state.sessions.render()).cached, true);
  assert.equal(state.documents.size, 1);
  assert.equal(state.resources.size, 0);
  assert.equal(state.assets.size, 3);
  await state.sessions.activate(mountain, { ...policy, AssumeUntaggedSRGB: false });
  assert.equal(state.count('editor.ReplaceContentsWithOptions'), 3);
  await state.sessions.close();
});

test('a failed new upload preserves the previous local editor and its valid preview', async () => {
  const state = workbench();
  const local = { id: 'local', blob: new Blob(['local']) };
  const doc = await state.sessions.activate(local, policy);
  await state.sessions.render();
  await assert.rejects(state.sessions.activate({ id: 'local', blob: new Blob(['broken']) }, policy), /replacement failed/);
  assert.equal(state.sessions.current.doc, doc);
  assert.ok(state.sessions.entries.has(local));
  assert.equal(text(await state.sessions.render()), 'local:true:255');
  assert.equal(state.documents.size, 1);
  await state.sessions.close();
});
