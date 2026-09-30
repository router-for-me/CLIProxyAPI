import test from 'node:test';
import assert from 'node:assert/strict';
import { selectProviderCooldown, cooldownProviderSet } from './useCooldownWatch.js';

const records = [
  { provider: 'gemini', model: '', status: 'cooling', next_retry_after: '2030-01-01T00:00:00Z' },
  { provider: 'openai', model: 'gpt-4o', status: 'cooling', next_retry_after: '2030-02-01T00:00:00Z' },
];

test('returns null when no records are provided', () => {
  assert.equal(selectProviderCooldown(null, { provider: 'gemini' }), null);
  assert.equal(selectProviderCooldown(undefined, { provider: 'gemini' }), null);
  assert.equal(selectProviderCooldown([], { provider: 'gemini' }), null);
});

test('returns null when the provider is not cooling', () => {
  assert.equal(selectProviderCooldown(records, { provider: 'claude' }), null);
});

test('matches an auth-level record for any model on that provider', () => {
  const entry = selectProviderCooldown(records, { provider: 'gemini', model: 'gemini-2.0-flash' });
  assert.ok(entry);
  assert.equal(entry.provider, 'gemini');
  assert.equal(entry.cooldownUntil, '2030-01-01T00:00:00Z');
});

test('model-level records only match the selected model', () => {
  assert.equal(selectProviderCooldown(records, { provider: 'openai', model: 'gpt-4o-mini' }), null);
  const entry = selectProviderCooldown(records, { provider: 'openai', model: 'gpt-4o' });
  assert.ok(entry);
  assert.equal(entry.model, 'gpt-4o');
});

test('provider match is case-insensitive', () => {
  const entry = selectProviderCooldown(records, { provider: 'Gemini' });
  assert.ok(entry);
});

test('cooldownProviderSet lowercases the provider keys', () => {
  const set = cooldownProviderSet(records);
  assert.equal(set.has('gemini'), true);
  assert.equal(set.has('openai'), true);
  assert.equal(set.has('claude'), false);
  assert.deepEqual([...cooldownProviderSet(null)], []);
});
