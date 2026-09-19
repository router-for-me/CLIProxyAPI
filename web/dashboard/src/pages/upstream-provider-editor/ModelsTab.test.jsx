import { test } from 'node:test';
import assert from 'node:assert/strict';
import ModelsTab from './ModelsTab.jsx';

test('ModelsTab: module exports a function component', () => {
  assert.equal(typeof ModelsTab, 'function');
});
