// Unit tests for the /dashboard/quota panel.
//
// The dashboard has no React-component testing harness today (only pure
// function tests run under node:test, see LogsPage.test.js +
// ProxyPoolsPage.test.js). We follow that convention by exporting the
// pure helpers from Quota.jsx and asserting their behavior here; the
// component render path is exercised manually + by `npm run build`.

import test from 'node:test';
import assert from 'node:assert/strict';
import {
  formatHeadroomPct,
  pickOverLimit,
  extractPoolKeys,
  severityForWindow,
  poolKeyForProvider,
  mergePoolQuotas,
} from './Quota.jsx';

// --- formatHeadroomPct -----------------------------------------------------

test('formatHeadroomPct: keeps one decimal as-is when finite', () => {
  assert.equal(formatHeadroomPct(99.0), '99.0%');
  assert.equal(formatHeadroomPct(95.45), '95.5%');
  assert.equal(formatHeadroomPct(0), '0.0%');
});

test('formatHeadroomPct: handles null/undefined/NaN as em-dash', () => {
  assert.equal(formatHeadroomPct(null), '—');
  assert.equal(formatHeadroomPct(undefined), '—');
  assert.equal(formatHeadroomPct(Number.NaN), '—');
});

// --- pickOverLimit ---------------------------------------------------------

test('pickOverLimit: returns true when any window is over_limit', () => {
  const windows = [
    { size: '1m', over_limit: false },
    { size: '1h', over_limit: true },
  ];
  assert.equal(pickOverLimit(windows), true);
});

test('pickOverLimit: returns false when no window is over_limit', () => {
  const windows = [
    { size: '1m', over_limit: false },
    { size: '1h', over_limit: false },
  ];
  assert.equal(pickOverLimit(windows), false);
});

test('pickOverLimit: empty array returns false', () => {
  assert.equal(pickOverLimit([]), false);
  assert.equal(pickOverLimit(null), false);
  assert.equal(pickOverLimit(undefined), false);
});

// --- extractPoolKeys -------------------------------------------------------

test('extractPoolKeys: returns one pool key per upstream provider', () => {
  const providers = [
    { id: 1, provider_type: 'oauth:openai' },
    { id: 7, provider_type: 'oauth:openai' },
    { id: 3, provider_type: 'oauth:claude' },
  ];
  const keys = extractPoolKeys(providers);
  assert.deepEqual(keys, [
    'oauth:openai:1',
    'oauth:openai:7',
    'oauth:claude:3',
  ]);
});

test('extractPoolKeys: filters out disabled rows', () => {
  const providers = [
    { id: 1, provider_type: 'oauth:openai', disabled: false },
    { id: 2, provider_type: 'oauth:openai', disabled: true },
  ];
  assert.deepEqual(extractPoolKeys(providers), ['oauth:openai:1']);
});

test('extractPoolKeys: returns [] for empty / missing input', () => {
  assert.deepEqual(extractPoolKeys([]), []);
  assert.deepEqual(extractPoolKeys(null), []);
  assert.deepEqual(extractPoolKeys(undefined), []);
});

test('extractPoolKeys: skips rows missing id or provider_type', () => {
  const providers = [
    { id: 0, provider_type: 'oauth:openai' },
    { id: 5, provider_type: '' },
    { id: null, provider_type: 'oauth:openai' },
  ];
  assert.deepEqual(extractPoolKeys(providers), []);
});

// --- poolKeyForProvider ----------------------------------------------------

test('poolKeyForProvider: builds compound key from provider_type + id', () => {
  assert.equal(
    poolKeyForProvider({ id: 42, provider_type: 'oauth:openai' }),
    'oauth:openai:42',
  );
});

test('poolKeyForProvider: returns null when fields missing', () => {
  assert.equal(poolKeyForProvider(null), null);
  assert.equal(poolKeyForProvider({ id: 1 }), null);
  assert.equal(poolKeyForProvider({ provider_type: 'oauth:openai' }), null);
});

// --- severityForWindow -----------------------------------------------------

test('severityForWindow: maps headroom_pct + over_limit to a tier', () => {
  assert.equal(severityForWindow({ headroom_pct: 90, over_limit: false }), 'ok');
  assert.equal(severityForWindow({ headroom_pct: 15, over_limit: false }), 'warn');
  assert.equal(severityForWindow({ headroom_pct: 3, over_limit: false }), 'over');
  assert.equal(severityForWindow({ headroom_pct: 0, over_limit: true }), 'over');
  assert.equal(severityForWindow({ headroom_pct: -1, over_limit: true }), 'over');
});

// --- mergePoolQuotas -------------------------------------------------------

test('mergePoolQuotas: merges quota responses with their pool keys', () => {
  const keys = ['oauth:openai:1', 'oauth:claude:3'];
  const responses = new Map([
    ['oauth:openai:1', {
      pool_key: 'oauth:openai:1',
      windows: [{ size: '1m', used: 100, limit: 1000, headroom_pct: 90.0, over_limit: false }],
      models: [],
      partial: false,
    }],
    ['oauth:claude:3', {
      pool_key: 'oauth:claude:3',
      windows: [{ size: '1m', used: 0, limit: 0, headroom_pct: 100.0, over_limit: false }],
      models: [],
      partial: false,
    }],
  ]);
  const merged = mergePoolQuotas(keys, responses);
  assert.equal(merged.length, 2);
  // Stable order matching the input keys array.
  assert.equal(merged[0].poolKey, 'oauth:openai:1');
  assert.equal(merged[1].poolKey, 'oauth:claude:3');
  assert.equal(merged[0].windows[0].headroom_pct, 90.0);
  assert.equal(merged[1].windows[0].headroom_pct, 100.0);
});

test('mergePoolQuotas: fills in a stub for a missing pool (fetch failed)', () => {
  const keys = ['oauth:openai:1', 'oauth:claude:3'];
  const responses = new Map([
    ['oauth:openai:1', {
      pool_key: 'oauth:openai:1',
      windows: [],
      models: [],
      partial: false,
    }],
  ]);
  const merged = mergePoolQuotas(keys, responses);
  assert.equal(merged.length, 2);
  assert.equal(merged[1].poolKey, 'oauth:claude:3');
  assert.equal(merged[1].error, true);
  assert.deepEqual(merged[1].windows, []);
});
