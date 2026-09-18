import { test } from 'node:test';
import assert from 'node:assert/strict';

// We can unit-test the pure helpers without React (the JSX uses them).
import { statusFromRow } from './StatusDot.jsx';

test('statusFromRow: breaker_open wins over cooldown and live', () => {
  assert.equal(
    statusFromRow({ isLive: true, cooldownUntil: '2026-09-18T00:00:00Z', breakerOpen: true }),
    'breaker_open',
  );
});

test('statusFromRow: cooldown wins over live', () => {
  assert.equal(
    statusFromRow({ isLive: true, cooldownUntil: '2026-09-18T00:00:00Z', breakerOpen: false }),
    'cooldown',
  );
});

test('statusFromRow: live when no cooldown / no breaker', () => {
  assert.equal(statusFromRow({ isLive: true }), 'live');
});

test('statusFromRow: stale when not live and no cooldown', () => {
  assert.equal(statusFromRow({ isLive: false }), 'stale');
});

test('statusFromRow: stale when not live but cooldownUntil is empty string', () => {
  assert.equal(statusFromRow({ isLive: false, cooldownUntil: '' }), 'stale');
});

test('statusFromRow: stale when not live but cooldownUntil is null', () => {
  assert.equal(statusFromRow({ isLive: false, cooldownUntil: null }), 'stale');
});

test('statusFromRow: defaults when row is empty', () => {
  assert.equal(statusFromRow({}), 'stale');
});