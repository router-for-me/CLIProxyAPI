// Tests for the routed upstream provider editor page's pure helpers.
// The page component itself needs a router + toast provider + network, so
// only the mode-resolution contract is covered here (node:test has no DOM
// in this repo; the component suites in src/playground render via
// react-dom/server, which this page's network effects preclude).

import test from 'node:test';
import assert from 'node:assert/strict';
import { resolveEditorMode } from './index.jsx';

test('resolveEditorMode: "new" classifies as create with no provider id', () => {
  assert.deepEqual(resolveEditorMode('new'), { isCreate: true, providerId: null });
});

test('resolveEditorMode: a numeric id classifies as edit and keeps the id', () => {
  assert.deepEqual(resolveEditorMode('42'), { isCreate: false, providerId: '42' });
  assert.deepEqual(resolveEditorMode('7'), { isCreate: false, providerId: '7' });
});

test('resolveEditorMode: "new" never collides with a numeric id ("new1" is edit)', () => {
  // Guard for the route contract: only the exact segment "new" means create.
  // Anything else (the router guarantees non-empty) is treated as a row id.
  const m = resolveEditorMode('new1');
  assert.equal(m.isCreate, false);
  assert.equal(m.providerId, 'new1');
});

test('resolveEditorMode: id 0 is treated as a row id, not create', () => {
  // PG ids are positive integers, but a malformed /upstream-providers/0 URL
  // must not silently flip into create mode (it surfaces the fetch error).
  assert.deepEqual(resolveEditorMode('0'), { isCreate: false, providerId: '0' });
});
