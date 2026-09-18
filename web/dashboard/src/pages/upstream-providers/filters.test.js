import { test } from 'node:test';
import assert from 'node:assert/strict';
import { applyFilters, applySort } from './filters.js';

const row = (overrides = {}) => ({
  id: 1,
  provider_type: 'openai-compatibility',
  name: 'gpt-4o',
  base_url: 'https://api.example.com',
  priority: 0,
  disabled: false,
  ...overrides,
});

test('applyFilters: empty filters returns all', () => {
  const rows = [row({ id: 1 }), row({ id: 2 })];
  const out = applyFilters(rows, { search: '', typeFilter: '', healthFilters: new Set(), liveStatus: {} });
  assert.equal(out.length, 2);
});

test('applyFilters: search across provider_type', () => {
  const rows = [row({ id: 1, provider_type: 'claude-api-key' }), row({ id: 2, provider_type: 'openai-compatibility' })];
  const out = applyFilters(rows, { search: 'claude', typeFilter: '', healthFilters: new Set(), liveStatus: {} });
  assert.equal(out.length, 1);
  assert.equal(out[0].id, 1);
});

test('applyFilters: search across name field', () => {
  const out = applyFilters([row({ id: 1, name: 'foo' }), row({ id: 2, name: 'bar' })], {
    search: 'foo', typeFilter: '', healthFilters: new Set(), liveStatus: {},
  });
  assert.equal(out.length, 1);
});

test('applyFilters: type filter', () => {
  const out = applyFilters([row({ id: 1, provider_type: 'a' }), row({ id: 2, provider_type: 'b' })], {
    search: '', typeFilter: 'a', healthFilters: new Set(), liveStatus: {},
  });
  assert.equal(out.length, 1);
});

test('applyFilters: type filter "api" matches non-OAuth rows', () => {
  const rows = [
    row({ id: 1, provider_type: 'openai-compatibility' }),
    row({ id: 2, provider_type: 'claude-api-key' }),
    row({ id: 3, provider_type: 'oauth:claude' }),
  ];
  const out = applyFilters(rows, {
    search: '', typeFilter: 'api', healthFilters: new Set(), liveStatus: {},
  });
  assert.equal(out.length, 2);
  assert.deepEqual(out.map((r) => r.id).sort(), [1, 2]);
});

test('applyFilters: type filter "oauth" matches OAuth-prefixed rows', () => {
  const rows = [
    row({ id: 1, provider_type: 'openai-compatibility' }),
    row({ id: 2, provider_type: 'oauth:claude' }),
    row({ id: 3, provider_type: 'oauth:codex' }),
  ];
  const out = applyFilters(rows, {
    search: '', typeFilter: 'oauth', healthFilters: new Set(), liveStatus: {},
  });
  assert.equal(out.length, 2);
  assert.deepEqual(out.map((r) => r.id).sort(), [2, 3]);
});

test('applyFilters: health filter (multi-select)', () => {
  const rows = [
    row({ id: 1 }),
    row({ id: 2, disabled: true }),
    row({ id: 3 }),
  ];
  const liveStatus = {
    1: { is_live: true },
    3: { breaker_open: true },
  };
  const out = applyFilters(rows, {
    search: '', typeFilter: '', healthFilters: new Set(['breaker_open']), liveStatus,
  });
  assert.equal(out.length, 1);
  assert.equal(out[0].id, 3);
});

test('applyFilters: empty health filter set passes all', () => {
  const out = applyFilters([row({ id: 1 }), row({ id: 2 })], {
    search: '', typeFilter: '', healthFilters: new Set(), liveStatus: {},
  });
  assert.equal(out.length, 2);
});

test('applySort: by priority descending', () => {
  const rows = [row({ id: 1, priority: 5 }), row({ id: 2, priority: 10 })];
  const out = applySort(rows, 'priority', 'desc', {});
  assert.equal(out[0].id, 2);
});

test('applySort: by health (worst-first ascending)', () => {
  const rows = [row({ id: 1 }), row({ id: 2 })];
  const liveStatus = {
    1: { is_live: true },
    2: { breaker_open: true },
  };
  const out = applySort(rows, 'health', 'asc', liveStatus);
  assert.equal(out[0].id, 2); // breaker first
});

test('applySort: off returns unchanged', () => {
  const rows = [row({ id: 2 }), row({ id: 1 })];
  const out = applySort(rows, 'off', 'asc', {});
  assert.deepEqual(out.map((r) => r.id), [2, 1]);
});
