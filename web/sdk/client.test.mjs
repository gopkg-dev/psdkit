import test from 'node:test';
import assert from 'node:assert/strict';
import { PsdkitClient } from './client.js';

class FakeWorker {
  constructor() { this.messages = []; this.stopped = false; }
  postMessage(message, transfers) {
    if (this.rejectPost) throw new Error('post failed');
    this.messages.push({ message: structuredClone(message, { transfer: transfers }), transfers });
  }
  terminate() { this.stopped = true; }
  respond(id, reply) { this.onmessage({ data: { id, reply } }); }
}
globalThis.Worker = FakeWorker;

test('transfers only the requested byte view and routes out-of-order replies', async () => {
  const client = new PsdkitClient();
  const source = new Uint8Array([91, 2, 3, 92]);
  const first = client.call('asset.put', { bytes: source.subarray(1, 3) });
  const second = client.call('system.state');
  const worker = client.worker;
  assert.deepEqual([...new Uint8Array(worker.messages[0].message.bytes)], [2, 3]);
  assert.equal(source.byteLength, 4);
  worker.respond(2, { ok: true, data: 'second' });
  worker.respond(1, { ok: true, data: 'first' });
  assert.equal((await first).data, 'first');
  assert.equal((await second).data, 'second');
  assert.equal(client.pending.size, 0);
  client.close();
});

test('original library fault fields survive the bridge', async () => {
  const client = new PsdkitClient();
  const pending = client.call('render.source');
  client.worker.respond(1, { ok: false, error: { code: 'unsupported', message: 'quilt gate', path: 'layers[0]' } });
  await assert.rejects(pending, { code: 'unsupported', message: 'quilt gate', path: 'layers[0]' });
  client.close();
});

test('timeout terminates the workspace and rejects every pending call', async () => {
  const client = new PsdkitClient({ timeoutMS: 20 });
  const worker = client.worker;
  const calls = [client.call('render.source'), client.call('editor.WritePSD')];
  const results = await Promise.allSettled(calls);
  assert.ok(results.every((item) => item.status === 'rejected' && item.reason.code === 'worker_timeout'));
  assert.equal(worker.stopped, true);
  assert.equal(client.worker, null);
  assert.equal(client.pending.size, 0);
});

test('failed postMessage and fatal Worker errors release pending calls', async () => {
  const client = new PsdkitClient();
  client.worker.rejectPost = true;
  await assert.rejects(client.call('asset.put'), /post failed/);
  assert.equal(client.pending.size, 0);
  client.worker.rejectPost = false;
  const pending = client.call('system.state');
  client.worker.onerror({ message: 'runtime crashed' });
  await assert.rejects(pending, /runtime crashed/);
  assert.equal(client.worker, null);
});

test('reset creates a fresh Worker and waits for readiness', async () => {
  const client = new PsdkitClient();
  const old = client.worker;
  const reset = client.reset();
  assert.equal(old.stopped, true);
  assert.notEqual(client.worker, old);
  client.worker.onmessage({ data: { id: 1, ready: true, version: 'test' } });
  assert.equal((await reset).version, 'test');
  client.close();
});

test('image transfer preference is passed through as a per-call option', async () => {
  const client = new PsdkitClient();
  const pending = client.call('editor.Composite', { target: 'editor', args: [{}], imageOutput: 'png' });
  assert.equal(client.worker.messages[0].message.request.imageOutput, 'png');
  client.worker.respond(1, { ok: true, data: {} });
  await pending;
  client.close();
});
