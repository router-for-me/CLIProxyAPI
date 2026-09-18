import { test } from 'node:test';
import assert from 'node:assert/strict';
import { statusFromHealth, cooldownReason, getHealthSummary, partitionByHealth } from './health.js';

const row = (overrides = {}) => ({ id: 1, provider_type: 'openai-compatibility', disabled: false, ...overrides });

test('statusFromHealth: live evidence → live', () => {
  assert.equal(statusFromHealth(row(), { is_live: true }), 'live');
});

test('statusFromHealth: missing live evidence → stale', () => {
  assert.equal(statusFromHealth(row(), {}), 'stale');
});

test('statusFromHealth: breaker_open overrides live', () => {
  assert.equal(statusFromHealth(row(), { is_live: true, breaker_open: true }), 'breaker_open');
});

test('statusFromHealth: cooldown_until overrides live', () => {
  assert.equal(statusFromHealth(row(), { is_live: true, cooldown_until: '2099-01-01T00:00:00Z' }), 'cooldown');
});

test('statusFromHealth: disabled beats live', () => {
  assert.equal(statusFromHealth(row({ disabled: true }), { 1: { is_live: true } }), 'disabled');
});

test('statusFromHealth: undefined liveEntry → stale', () => {
  assert.equal(statusFromHealth(row()), 'stale');
});

test('cooldownReason: past date → null', () => {
  assert.equal(cooldownReason('2020-01-01T00:00:00Z'), null);
});

test('cooldownReason: future date → minutes', () => {
  const future = new Date(Date.now() + 5 * 60 * 1000).toISOString();
  const r = cooldownReason(future);
  assert.match(r, /^retrying in \d+m$/);
});

test('cooldownReason: null → null', () => {
  assert.equal(cooldownReason(null), null);
});

test('getHealthSummary: empty providers', () => {
  const s = getHealthSummary([], {});
  assert.equal(s.total, 0);
  assert.equal(s.live, 0);
});

test('getHealthSummary: mixed states', () => {
  const providers = [
    row({ id: 1 }),
    row({ id: 2, disabled: true }),
    row({ id: 3 }),
    row({ id: 4 }),
  ];
  const liveStatus = {
    1: { is_live: true },
    3: { breaker_open: true },
    4: { cooldown_until: '2099-01-01T00:00:00Z' },
  };
  const s = getHealthSummary(providers, liveStatus);
  assert.equal(s.total, 4);
  assert.equal(s.live, 1);
  assert.equal(s.disabled, 1);
  assert.equal(s.breaker_open, 1);
  assert.equal(s.cooldown, 1);
  assert.equal(s.stale, 0);
});

test('partitionByHealth: returns four buckets with correct membership', () => {
  const providers = [
    row({ id: 1 }),
    row({ id: 2, disabled: true }),
    row({ id: 3 }),
  ];
  const liveStatus = {
    1: { is_live: true },
    3: { breaker_open: true },
  };
  const p = partitionByHealth(providers, liveStatus);
  assert.equal(p.live.length, 1);
  assert.equal(p.breaker.length, 1);
  assert.equal(p.staleOrDisabled.length, 1);
  assert.equal(p.cooldown.length, 0);
});