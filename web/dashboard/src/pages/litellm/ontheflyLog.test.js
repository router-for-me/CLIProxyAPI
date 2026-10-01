import test from 'node:test';
import assert from 'node:assert/strict';

import {
  formatOnTheFlyOutcome,
  onTheFlyOutcomeBadge,
  summarizeOnTheFlyOutcomes,
} from './ontheflyLog.js';

test('formatOnTheFlyOutcome maps known outcomes', () => {
  assert.equal(formatOnTheFlyOutcome('synced'), 'Synced');
  assert.equal(formatOnTheFlyOutcome('unmatched'), 'Unmatched');
  assert.equal(formatOnTheFlyOutcome('invalid'), 'Invalid');
  assert.equal(formatOnTheFlyOutcome('error'), 'Error');
});

test('formatOnTheFlyOutcome falls back to the raw value then a dash', () => {
  assert.equal(formatOnTheFlyOutcome('custom'), 'custom');
  assert.equal(formatOnTheFlyOutcome(''), '—');
  assert.equal(formatOnTheFlyOutcome(undefined), '—');
});

test('onTheFlyOutcomeBadge maps known outcomes and defaults to muted', () => {
  assert.equal(onTheFlyOutcomeBadge('synced'), 'badge--active');
  assert.equal(onTheFlyOutcomeBadge('unmatched'), 'badge--disabled');
  assert.equal(onTheFlyOutcomeBadge('invalid'), 'badge--warn');
  assert.equal(onTheFlyOutcomeBadge('error'), 'badge--revoked');
  assert.equal(onTheFlyOutcomeBadge('other'), 'badge--muted');
});

test('summarizeOnTheFlyOutcomes counts by outcome and ignores unknown', () => {
  const counts = summarizeOnTheFlyOutcomes([
    { outcome: 'synced' },
    { outcome: 'synced' },
    { outcome: 'unmatched' },
    { outcome: 'error' },
    { outcome: 'mystery' },
    {},
  ]);
  assert.deepEqual(counts, { synced: 2, unmatched: 1, invalid: 0, error: 1 });
});

test('summarizeOnTheFlyOutcomes tolerates null/undefined', () => {
  assert.deepEqual(summarizeOnTheFlyOutcomes(null), { synced: 0, unmatched: 0, invalid: 0, error: 0 });
  assert.deepEqual(summarizeOnTheFlyOutcomes(undefined), { synced: 0, unmatched: 0, invalid: 0, error: 0 });
});
