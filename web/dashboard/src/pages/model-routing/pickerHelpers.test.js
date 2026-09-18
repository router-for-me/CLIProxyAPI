import { test } from 'node:test';
import assert from 'node:assert/strict';
import {
  mergePinnedAfterPin,
  clearPending,
  partitionPicker,
} from './pickerHelpers.js';

test('mergePinnedAfterPin: appends new pin', () => {
  const prev = [];
  const result = { provider_key: 'claude:42', priority: 11 };
  const candidate = { name: 'Claude prod', live: true };
  const next = mergePinnedAfterPin(prev, result, candidate);
  assert.equal(next.length, 1);
  assert.equal(next[0].provider_key, 'claude:42');
  assert.equal(next[0].priority, 11);
  assert.equal(next[0].name, 'Claude prod');
  assert.equal(next[0].is_live, true);
});

test('mergePinnedAfterPin: updates existing pin in place', () => {
  const prev = [
    { provider_key: 'claude:42', priority: 5, is_live: false, name: 'old' },
    { provider_key: 'openai:1', priority: 10, is_live: true, name: 'OpenAI' },
  ];
  const result = { provider_key: 'claude:42', priority: 11 };
  const candidate = { name: 'Claude prod', live: true };
  const next = mergePinnedAfterPin(prev, result, candidate);
  assert.equal(next.length, 2);
  assert.equal(next[0].priority, 11);
  assert.equal(next[0].is_live, true);
  assert.equal(next[1].provider_key, 'openai:1');
  assert.equal(next[1].priority, 10);
});

test('mergePinnedAfterPin: handles nil previous pinned', () => {
  const next = mergePinnedAfterPin(null, { provider_key: 'x:1', priority: 10 }, { name: 'x', live: true });
  assert.equal(next.length, 1);
  assert.equal(next[0].provider_key, 'x:1');
});

test('mergePinnedAfterPin: candidate.live=false → is_live=false', () => {
  const next = mergePinnedAfterPin(
    [],
    { provider_key: 'openai:1', priority: 11 },
    { name: 'Stale', live: false },
  );
  assert.equal(next[0].is_live, false);
});

test('clearPending: removes the key', () => {
  const before = { 'claude:42': true, 'openai:1': false };
  const after = clearPending(before, 'claude:42');
  assert.deepEqual(after, { 'openai:1': false });
});

test('clearPending: no-op when key not present', () => {
  const before = { 'claude:42': true };
  const after = clearPending(before, 'openai:1');
  assert.equal(after, before); // same reference returned for immutability
});

test('clearPending: handles null input', () => {
  const after = clearPending(null, 'x:1');
  assert.equal(after, null);
});

test('partitionPicker: handles null', () => {
  assert.deepEqual(partitionPicker(null), { live: [], stale: [], pinned: [] });
});

test('partitionPicker: handles undefined arrays', () => {
  assert.deepEqual(partitionPicker({}), { live: [], stale: [], pinned: [] });
});

test('partitionPicker: passes through populated arrays', () => {
  const picker = {
    live: [{ provider_key: 'a:1' }],
    stale: [{ provider_key: 'b:1' }],
    pinned: [{ provider_key: 'c:1', priority: 10 }],
  };
  assert.deepEqual(partitionPicker(picker), picker);
});