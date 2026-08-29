import test from 'node:test';
import assert from 'node:assert/strict';
import { listProtocols, getProtocol } from './protocols.js';

test('listProtocols returns the four supported protocols in stable order', () => {
  const ids = listProtocols().map((p) => p.id);
  assert.deepEqual(ids, ['openai-compat', 'gemini', 'claude', 'codex']);
});

test('getProtocol returns an adapter for each known id', () => {
  for (const id of ['openai-compat', 'gemini', 'claude', 'codex']) {
    const a = getProtocol(id);
    assert.equal(a.id, id);
    assert.equal(typeof a.endpoint, 'function');
    assert.equal(typeof a.buildRequest, 'function');
    assert.equal(typeof a.parseStreamChunk, 'function');
    assert.equal(typeof a.buildErrorPayload, 'function');
    assert.equal(typeof a.supports, 'object');
  }
});

test('getProtocol throws on unknown id', () => {
  assert.throws(() => getProtocol('not-a-protocol'), /Unknown protocol/);
});
