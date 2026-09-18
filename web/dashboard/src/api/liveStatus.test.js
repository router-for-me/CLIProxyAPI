import { test } from 'node:test';
import assert from 'node:assert/strict';
import { isProviderRowLive } from './liveStatus.js';

test('isProviderRowLive: empty row', () => {
  assert.equal(isProviderRowLive('', ['claude']), false);
  assert.equal(isProviderRowLive('   ', ['claude']), false);
  assert.equal(isProviderRowLive(null, ['claude']), false);
});

test('isProviderRowLive: compound exact match', () => {
  assert.equal(isProviderRowLive('claude:42:key-7', ['claude:42:key-7']), true);
});

test('isProviderRowLive: compound case-insensitive', () => {
  assert.equal(isProviderRowLive('CLAUDE:42:KEY-7', ['claude:42:key-7']), true);
});

test('isProviderRowLive: claude:42 NOT live when only claude is evidence', () => {
  // Mirrors the Go helper rule — bare legacy OAuth does NOT prove compound is live.
  assert.equal(isProviderRowLive('claude:42', ['claude']), false);
});

test('isProviderRowLive: OpenAI-Compat provider-level entry-live fallback', () => {
  assert.equal(isProviderRowLive('openai-compatible-foo', ['openai-compatible-foo:bar']), true);
});

test('isProviderRowLive: OpenAI-Compat provider-level provider-live fallback', () => {
  assert.equal(isProviderRowLive('openai-compatible-foo', ['openai-compatible-foo']), true);
});

test('isProviderRowLive: OpenAI-Compat different provider not live', () => {
  assert.equal(isProviderRowLive('openai-compatible-foo', ['openai-compatible-baz:bar']), false);
});

test('isProviderRowLive: OpenAI-Compat compound is not a provider-level route', () => {
  assert.equal(isProviderRowLive('openai-compatible-foo:bar', ['openai-compatible-foo']), false);
});

test('isProviderRowLive: cooldown blocks even with live evidence', () => {
  assert.equal(
    isProviderRowLive('claude:42', ['claude:42'], { 'claude:42': true }),
    false,
  );
});

test('isProviderRowLive: cooldown blocks compound', () => {
  assert.equal(
    isProviderRowLive('claude:42:key-7', ['claude:42:key-7'], { 'claude:42:key-7': true }),
    false,
  );
});

test('isProviderRowLive: no evidence', () => {
  assert.equal(isProviderRowLive('claude:42', []), false);
  assert.equal(isProviderRowLive('claude:42', null), false);
  assert.equal(isProviderRowLive('claude:42', undefined), false);
});

test('isProviderRowLive: empty evidence after trim', () => {
  assert.equal(isProviderRowLive('claude:42', ['', '  ']), false);
});

test('isProviderRowLive: bare channel no compound', () => {
  assert.equal(isProviderRowLive('claude', ['claude']), true);
});

test('isProviderRowLive: different channel no match', () => {
  assert.equal(isProviderRowLive('openai:42', ['claude']), false);
});

test('isProviderRowLive: whitespace-only evidence is filtered', () => {
  assert.equal(isProviderRowLive('claude:42', ['   ', 'claude:42  ']), true);
});