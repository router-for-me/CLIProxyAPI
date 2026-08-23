import test from 'node:test';
import assert from 'node:assert/strict';
import { providerKeyIsLive } from './modelRouteProvider.js';

test('matches an exact compound upstream key', () => {
  assert.equal(providerKeyIsLive('claude:42', ['claude:42']), true);
});

test('does not treat a bare channel as evidence for a compound row', () => {
  assert.equal(providerKeyIsLive('claude:42', ['claude']), false);
});

test('does not mark a different compound row as live', () => {
  assert.equal(providerKeyIsLive('claude:42', ['claude:99']), false);
});

test('matches provider keys case-insensitively and ignores surrounding whitespace', () => {
  assert.equal(providerKeyIsLive(' CLAUDE:42 ', [' claude:42 ']), true);
});

test('does not treat a colon-containing non-channel key as live by its prefix', () => {
  assert.equal(providerKeyIsLive('openai-compatible-foo:bar', ['openai-compatible-foo']), false);
});

test('matches a colon-containing provider key exactly', () => {
  assert.equal(providerKeyIsLive('openai-compatible-foo:bar', ['openai-compatible-foo:bar']), true);
});

test('does not treat a nonnumeric built-in-looking key as a row key', () => {
  assert.equal(providerKeyIsLive('claude:plugin', ['claude']), false);
});
