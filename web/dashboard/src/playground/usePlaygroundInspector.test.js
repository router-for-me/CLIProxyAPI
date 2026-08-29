import test from 'node:test';
import assert from 'node:assert/strict';
import { createInspectorStore } from './usePlaygroundInspector.js';

test('recordOutgoing then recordIncoming populates state keyed by reqId', () => {
  const store = createInspectorStore();
  store.recordOutgoing('r-1', { model: 'm1' });
  store.recordIncoming('r-1', { id: 'cmpl-1' }, { 'content-type': 'application/json' });
  const all = store.snapshot();
  assert.deepEqual(all.outgoing['r-1'], { model: 'm1' });
  assert.deepEqual(all.incoming['r-1'], { id: 'cmpl-1' });
  assert.deepEqual(all.headers['r-1'], { 'content-type': 'application/json' });
});

test('a second recordOutgoing on the same reqId overwrites (retry flow)', () => {
  const store = createInspectorStore();
  store.recordOutgoing('r-1', { model: 'm1', messages: [] });
  store.recordOutgoing('r-1', { model: 'm1', messages: [{ role: 'user', content: 'retry' }] });
  assert.deepEqual(store.snapshot().outgoing['r-1'], { model: 'm1', messages: [{ role: 'user', content: 'retry' }] });
});

test('select(messageId) finds the message in messages[] and returns its reqId-keyed payload', () => {
  const store = createInspectorStore();
  store.recordOutgoing('r-1', { model: 'm1' });
  store.recordIncoming('r-1', { id: 'cmpl-1' }, {});
  const reqId = store.findReqIdForMessage('msg-2', [
    { id: 'msg-1', role: 'user' },
    { id: 'msg-2', role: 'assistant' },
  ]);
  assert.equal(reqId, 'r-1');
  const view = store.viewFor('r-1');
  assert.deepEqual(view.outgoing, { model: 'm1' });
  assert.deepEqual(view.incoming, { id: 'cmpl-1' });
});

test('viewFor returns nulls for unknown reqId (defensive)', () => {
  const store = createInspectorStore();
  const view = store.viewFor('nope');
  assert.equal(view.outgoing, null);
  assert.equal(view.incoming, null);
});

test('clear() wipes all stored payloads', () => {
  const store = createInspectorStore();
  store.recordOutgoing('r-1', { x: 1 });
  store.clear();
  assert.deepEqual(store.snapshot().outgoing, {});
});
