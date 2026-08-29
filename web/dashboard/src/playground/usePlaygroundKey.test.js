import test from 'node:test';
import assert from 'node:assert/strict';
import { resolvePlaygroundKey, PLAYGROUND_KEY_NAME } from './usePlaygroundKey.js';

// resolvePlaygroundKey is the pure half of usePlaygroundKey: given a
// stored localStorage value and dependency fakes, it either returns the
// cached key or bootstraps one idempotently.
//
// We test it directly so we don't need a React renderer.

test('returns the cached key without fetching when present', async () => {
  const cached = JSON.stringify({ id: 'k1', secret: 's3cr3t' });
  let listCalls = 0;
  let createCalls = 0;
  const key = await resolvePlaygroundKey({
    cached,
    listKeys: async () => { listCalls++; return []; },
    createKey: async () => { createCalls++; throw new Error('should not be called'); },
    findOrCreateUser: async () => { throw new Error('should not be called'); },
  });
  assert.equal(key, 's3cr3t');
  assert.equal(listCalls, 0);
  assert.equal(createCalls, 0);
});

test('bootstraps a new key when nothing is cached', async () => {
  let listCalls = 0;
  const key = await resolvePlaygroundKey({
    cached: null,
    listKeys: async () => { listCalls++; return []; },
    createKey: async (name, userId) => {
      assert.equal(name, PLAYGROUND_KEY_NAME);
      assert.ok(userId, 'userId must be passed');
      return { id: 'k2', secret: 'fresh-key' };
    },
    findOrCreateUser: async () => 'user-1',
  });
  assert.equal(key, 'fresh-key');
  assert.equal(listCalls, 1);
});

test('falls back to list when create returns 409 (already exists)', async () => {
  const key = await resolvePlaygroundKey({
    cached: null,
    listKeys: async () => [{ id: 'k3', name: PLAYGROUND_KEY_NAME, secret: 'existing-key' }],
    createKey: async () => {
      const e = new Error('Conflict');
      e.status = 409;
      throw e;
    },
    findOrCreateUser: async () => 'user-1',
  });
  assert.equal(key, 'existing-key');
});

test('reuses an existing key found by list without creating', async () => {
  let createCalls = 0;
  const key = await resolvePlaygroundKey({
    cached: null,
    listKeys: async () => [{ id: 'k4', name: PLAYGROUND_KEY_NAME, secret: 'preexisting' }],
    createKey: async () => { createCalls++; throw new Error('should not be called'); },
    findOrCreateUser: async () => 'user-1',
  });
  assert.equal(key, 'preexisting');
  assert.equal(createCalls, 0);
});
