import test from 'node:test';
import assert from 'node:assert/strict';
import { prettyJson, rawJson } from './jsonFormat.js';

test('prettyJson renders 2-space indented JSON', () => {
  const out = prettyJson({ a: 1, b: [1, 2] });
  assert.equal(out, '{\n  "a": 1,\n  "b": [\n    1,\n    2\n  ]\n}');
});

test('prettyJson falls back gracefully on circular references', () => {
  const obj = { a: 1 };
  obj.self = obj;
  const out = prettyJson(obj);
  assert.match(out, /\[Circular\]/);
});

test('prettyJson returns the em-dash sentinel for null/undefined', () => {
  assert.equal(prettyJson(null), '—');
  assert.equal(prettyJson(undefined), '—');
});

test('rawJson is a single-line stringification', () => {
  assert.equal(rawJson({ a: 1, b: 2 }), '{"a":1,"b":2}');
});
