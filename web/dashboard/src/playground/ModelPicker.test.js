import test from 'node:test';
import assert from 'node:assert/strict';
import { unwrapList } from './modelPickerFilters.js';

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
