// Tests for the opencode-go editor layer: the schema exposes the
// wire-format models editor + quota_url, form hydration/serialization
// round-trips per-model wire formats and the quota_url override, and the
// OpenCodeGoPanel pure helpers render windows / map the 404 fail-open.

import test from 'node:test';
import assert from 'node:assert/strict';
import { buildSchemas, isOpenCodeGo, WIRE_FORMAT_OPTIONS, OPENCODE_GO_BASE_URL } from './schemas.js';
import { buildForm, buildPayload } from './form.js';
import {
  WINDOW_LABELS,
  normalizeWindows,
  quotaErrorText,
  percentText,
  savedEntryOptions,
} from './OpenCodeGoPanel.jsx';

test('buildSchemas: opencode-go exists with entries, wire-format models, and quota_url', () => {
  const schemas = buildSchemas();
  const schema = schemas['opencode-go'];
  assert.ok(schema, 'opencode-go schema missing');
  const behavior = schema.sections.find((s) => s.title === 'Behavior').fields.map((f) => f.name);
  assert.ok(behavior.includes('api_key_entries'), 'behavior keeps api_key_entries');
  assert.ok(behavior.includes('quota_url'), 'behavior keeps quota_url');
  const routing = schema.sections.find((s) => s.title === 'Routing').fields;
  const models = routing.find((f) => f.name === 'models');
  assert.equal(models.type, 'opencode_models', 'models editor uses the wire-format variant');
  assert.equal(typeof isOpenCodeGo('opencode-go'), 'boolean');
  assert.ok(isOpenCodeGo('opencode-go'));
  assert.ok(!isOpenCodeGo('openai-compatibility'));
});

test('opencode-go form round-trips wire_format + quota_url', () => {
  const row = {
    id: 7,
    provider_type: 'opencode-go',
    name: 'ocgo',
    base_url: OPENCODE_GO_BASE_URL,
    models: [
      { name: 'glm-5.2', alias: 'glm-5.2' },
      { name: 'minimax-m3', alias: 'minimax-m3', wire_format: 'anthropic' },
    ],
    extra_config: { quota_url: 'https://quota.example/x' },
    api_key_entries: [{ id: 3, name: 'main', api_key: 'FAKE-SECRET-1' }],
  };
  const form = buildForm('opencode-go', row);
  assert.equal(form.models[1]['wire-format'], 'anthropic', 'anthropic wire format hydrates');
  assert.equal(form.models[0]['wire-format'] || '', '', 'default model has no wire format');
  assert.equal(form.quota_url, 'https://quota.example/x');

  const payload = buildPayload(form, 'opencode-go');
  assert.equal(payload.provider_type, 'opencode-go');
  assert.equal(payload.models[1].wire_format, 'anthropic');
  assert.equal(payload.models[0].wire_format, undefined, 'default model omits wire_format');
  assert.equal(payload.extra_config.quota_url, 'https://quota.example/x');

  // Clearing quota_url drops it from extra_config.
  const cleared = { ...form, quota_url: '' };
  const payloadCleared = buildPayload(cleared, 'opencode-go');
  assert.equal(payloadCleared.extra_config.quota_url, undefined, 'cleared quota_url removed');
});

test('WIRE_FORMAT_OPTIONS covers openai default + anthropic', () => {
  assert.ok(WIRE_FORMAT_OPTIONS.some((o) => o.value === '' && /default/.test(o.label)));
  assert.ok(WIRE_FORMAT_OPTIONS.some((o) => o.value === 'anthropic'));
});

test('normalizeWindows orders 5h → weekly → monthly and coerces fields', () => {
  const wins = normalizeWindows({
    windows: [
      { key: 'monthly', used: 5, limit: 10, percent_used: 50, reset_at: '2026-09-08T00:00:00Z' },
      { key: '5h', used: 1, limit: 2, percent_used: 50 },
      { key: 'weekly', used: 700, limit: 1000, percent_used: 70 },
    ],
  });
  assert.deepEqual(wins.map((w) => w.key), ['5h', 'weekly', 'monthly']);
  assert.equal(wins[0].label, WINDOW_LABELS['5h']);
});

test('quotaErrorText maps the 404 fail-open to the explanatory badge', () => {
  const text = quotaErrorText({ error: 'quota endpoint not available upstream (HTTP 404) — set extra_config.quota_url when OpenCode publishes an official endpoint' });
  assert.match(text, /Quota API belum tersedia di upstream/);
  const other = quotaErrorText({ error: 'quota fetch failed: timeout' });
  assert.equal(other, 'quota fetch failed: timeout');
});

test('percentText renders unknown limits as a dash', () => {
  assert.equal(percentText(-1), '—');
  assert.equal(percentText(40), '40%');
  assert.equal(percentText(78.4), '78%');
});

test('savedEntryOptions lists only persisted entries', () => {
  const opts = savedEntryOptions({
    api_key_entries: [
      { id: 3, name: 'main' },
      { id: 0, name: 'unsaved' },
      { id: 4, name: '', disabled: true },
    ],
  });
  assert.deepEqual(opts.map((o) => o.value), ['3', '4']);
  assert.match(opts[1].label, /disabled/);
});
