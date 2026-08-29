import test from 'node:test';
import assert from 'node:assert/strict';
import { filterLiveModels, modelHealthIsLive, unwrapList } from './modelPickerFilters.js';

test('modelHealthIsLive returns true for status healthy', () => {
  assert.equal(modelHealthIsLive({ status: 'healthy', consecutive_failures: 0 }), true);
});

test('modelHealthIsLive returns false for status down or consecutive_failures >= 3', () => {
  assert.equal(modelHealthIsLive({ status: 'healthy', consecutive_failures: 5 }), false);
  assert.equal(modelHealthIsLive({ status: 'down', consecutive_failures: 0 }), false);
});

test('filterLiveModels keeps only models with at least one healthy auth', () => {
  const upstreams = [
    { id: 'u1', model: 'gpt-4o', provider_type: 'openai' },
    { id: 'u2', model: 'gpt-4o', provider_type: 'openai' },
    { id: 'u3', model: 'claude-3-5-sonnet', provider_type: 'claude' },
  ];
  const health = [
    { model: 'gpt-4o', status: 'healthy', consecutive_failures: 0 },
    { model: 'gpt-4o', status: 'down', consecutive_failures: 4 },
    { model: 'claude-3-5-sonnet', status: 'down', consecutive_failures: 9 },
  ];
  const live = filterLiveModels(upstreams, health);
  // gpt-4o has at least one healthy auth; claude-3-5-sonnet has none.
  assert.deepEqual(live.map((u) => u.model).sort(), ['gpt-4o', 'gpt-4o']);
});

test('filterLiveModels tolerates non-array inputs without throwing', () => {
  // Regression: a non-iterable healthRows used to crash the whole page
  // with "(s || []) is not iterable" on first render. The function must
  // return an empty list rather than throw.
  assert.deepEqual(filterLiveModels(null, null), []);
  assert.deepEqual(filterLiveModels([], {}), []);
  assert.deepEqual(filterLiveModels('oops', { settings: {} }), []);
  assert.deepEqual(filterLiveModels([{ model: 'm' }], { error: { message: 'boom' } }), []);
});

// unwrapList coverage for the response shapes actually returned by the
// /v0/management endpoints the playground calls.

test('unwrapList passes through bare arrays', () => {
  assert.deepEqual(unwrapList([1, 2, 3]), [1, 2, 3]);
});

test('unwrapList extracts {models: [...]} (models-catalog)', () => {
  const data = { models: [{ id: 1 }], page: 1, total: 1 };
  assert.deepEqual(unwrapList(data), [{ id: 1 }]);
});

test('unwrapList extracts {providers: [...]} (upstream-providers)', () => {
  const data = { providers: [{ id: 'p1' }] };
  assert.deepEqual(unwrapList(data), [{ id: 'p1' }]);
});

test('unwrapList extracts {snapshots: [...]} (model-health)', () => {
  const data = { snapshots: [{ model: 'm1', status: 'healthy' }], settings: {} };
  assert.deepEqual(unwrapList(data), [{ model: 'm1', status: 'healthy' }]);
});

test('unwrapList extracts {items: [...]} (generic legacy shape)', () => {
  const data = { items: [{ id: 'x' }] };
  assert.deepEqual(unwrapList(data), [{ id: 'x' }]);
});

test('unwrapList returns [] for unknown object shapes (no crash)', () => {
  assert.deepEqual(unwrapList({ error: { message: 'boom' } }), []);
  assert.deepEqual(unwrapList({ settings: { enabled: true } }), []);
});

test('unwrapList returns [] for null/undefined/primitives', () => {
  assert.deepEqual(unwrapList(null), []);
  assert.deepEqual(unwrapList(undefined), []);
  assert.deepEqual(unwrapList(42), []);
  assert.deepEqual(unwrapList('foo'), []);
  assert.deepEqual(unwrapList(true), []);
});

test('unwrapList prefers the first known key when several are present', () => {
  // models-catalog uses {models, ...}, but if a future response contains
  // both, we pick models (the canonical list) over items (the fallback).
  const data = { models: [{ id: 1 }], items: [{ id: 2 }] };
  assert.deepEqual(unwrapList(data), [{ id: 1 }]);
});

// The component-level smoke test for the unified ModelPicker lives in
// the sibling ModelPicker.component.test.jsx — JSX needs the .jsx
// extension for the node ESM loader to transform it.
