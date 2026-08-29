import test from 'node:test';
import assert from 'node:assert/strict';
import { selectProviderCooldown } from './useCooldownWatch.js';

const snapshot = {
  providers: {
    'prov-a': { state: 'live', cooldownUntil: null },
    'prov-b': { state: 'cooldown', cooldownUntil: '2030-01-01T00:00:00Z' },
  },
};

test('returns null when no snapshot is provided', () => {
  assert.equal(selectProviderCooldown(null, 'prov-a'), null);
  assert.equal(selectProviderCooldown(undefined, 'prov-a'), null);
});

test('returns null when the provider key is unknown', () => {
  assert.equal(selectProviderCooldown(snapshot, 'prov-c'), null);
});

test('returns null when the provider is live', () => {
  assert.equal(selectProviderCooldown(snapshot, 'prov-a'), null);
});

test('returns the cooldown entry when the provider is in cooldown', () => {
  const entry = selectProviderCooldown(snapshot, 'prov-b');
  assert.ok(entry);
  assert.equal(entry.state, 'cooldown');
  assert.equal(entry.cooldownUntil, '2030-01-01T00:00:00Z');
});

test('tolerates snapshot with missing providers map', () => {
  assert.equal(selectProviderCooldown({}, 'prov-a'), null);
  assert.equal(selectProviderCooldown({ providers: null }, 'prov-a'), null);
});
