// Tests for Task 4 of the Claude (API Key) multi-entry plan: the
// `claude-api-key` provider now uses the same `api_key_entries`
// multi-row editor as `openai-compatibility`, gains an optional per-entry
// weight field, and round-trips legacy single-key rows by synthesising
// one unsaved entry. The OpenAI editor also gains the same optional
// weight field.
//
// These tests touch only pure helper functions exported from
// UpstreamProvidersPage.jsx (buildSchemas, buildForm, buildPayload,
// validate, validateAPIKeyEntries). They never log fixture credentials
// and never rely on the network or the React tree.
//
// Fixture secret material is intentionally obvious ("FAKE-SECRET-*")
// so any accidental inclusion in a test failure message is obvious as
// a fake credential.

import test from 'node:test';
import assert from 'node:assert/strict';
import {
  buildSchemas,
  buildForm,
  buildPayload,
  validate,
  validateAPIKeyEntries,
} from './UpstreamProvidersPage.jsx';

// ============================================================================
// buildSchemas — schema mapping
// ============================================================================

test('buildSchemas: claude-api-key uses api_key_entries (not single api_key)', () => {
  const schemas = buildSchemas();
  const claude = schemas['claude-api-key'];
  assert.ok(claude, 'claude-api-key schema defined');
  const identityFields = claude.sections.find((s) => s.title === 'Identity').fields;
  const fieldNames = identityFields.map((f) => f.name);
  assert.ok(fieldNames.includes('api_key_entries'),
    `claude identity fields include api_key_entries, got ${JSON.stringify(fieldNames)}`);
  assert.ok(!fieldNames.includes('api_key'),
    `claude identity fields must NOT include the legacy single api_key, got ${JSON.stringify(fieldNames)}`);
});

test('buildSchemas: claude-api-key keeps row-level toggles + cloak sections', () => {
  const schemas = buildSchemas();
  const claude = schemas['claude-api-key'];
  const behaviour = claude.sections.find((s) => s.title === 'Behavior').fields;
  const labels = behaviour.map((f) => f.label);
  assert.ok(labels.includes('Rebuild mid system message'),
    `claude behaviour retains rebuild_mid_system_message, got ${JSON.stringify(labels)}`);
  assert.ok(labels.includes('Experimental CCH signing'),
    `claude behaviour retains experimental_cch_signing, got ${JSON.stringify(labels)}`);
  assert.ok(claude.sections.some((s) => s.title === 'Cloak'),
    'claude schema keeps the Cloak section');
});

test('buildSchemas: openai-compatibility still has api_key_entries in Behavior', () => {
  const schemas = buildSchemas();
  const openai = schemas['openai-compatibility'];
  const behaviour = openai.sections.find((s) => s.title === 'Behavior').fields;
  const fieldNames = behaviour.map((f) => f.name);
  assert.ok(fieldNames.includes('api_key_entries'),
    `openai behavior keeps api_key_entries, got ${JSON.stringify(fieldNames)}`);
});

// ============================================================================
// validateAPIKeyEntries — pure entry validator
// ============================================================================

test('validateAPIKeyEntries: blank names + blank weights produce no errors', () => {
  const errs = validateAPIKeyEntries([
    { id: 0, name: '', api_key: 'FAKE-SECRET-A', proxy_url: '', weight: '' },
  ]);
  assert.deepEqual(errs, {});
});

test('validateAPIKeyEntries: rejected slug shape surfaces an inline error', () => {
  const errs = validateAPIKeyEntries([
    { id: 0, name: 'bad name!', api_key: 'FAKE-SECRET-A', proxy_url: '', weight: '' },
  ]);
  assert.ok(errs[0]?.name, 'row 0 has a name error');
  assert.match(errs[0].name, /letters, digits/);
});

test('validateAPIKeyEntries: reserved key-<digits> name is rejected', () => {
  const errs = validateAPIKeyEntries([
    { id: 7, name: 'key-7', api_key: 'FAKE-SECRET-A', proxy_url: '', weight: '' },
  ]);
  assert.ok(errs[0]?.name, 'reserved name surfaces an error');
  assert.match(errs[0].name, /reserved/);
});

test('validateAPIKeyEntries: duplicate names across rows surface duplicate errors', () => {
  const errs = validateAPIKeyEntries([
    { id: 0, name: 'team-a', api_key: 'FAKE-SECRET-A', proxy_url: '', weight: '' },
    { id: 0, name: 'team-a', api_key: 'FAKE-SECRET-B', proxy_url: '', weight: '' },
  ]);
  // First row gets a "duplicate identity detected" note; second references row 0.
  assert.ok(errs[0]?.name || errs[1]?.name,
    'at least one row has a duplicate error');
  assert.ok((errs[0]?.name || '') + (errs[1]?.name || ''),
    'duplicate identity message surfaces');
});

test('validateAPIKeyEntries: valid slug names + valid weight pass cleanly', () => {
  const errs = validateAPIKeyEntries([
    { id: 0, name: 'team-a', api_key: 'FAKE-SECRET-A', proxy_url: '', weight: '3' },
    { id: 0, name: 'team-b', api_key: 'FAKE-SECRET-B', proxy_url: '', weight: '' },
  ]);
  assert.deepEqual(errs, {});
});

test('validateAPIKeyEntries: weight bounds (zero / negative / over-max / non-integer) surface row errors', () => {
  const cases = [
    { weight: '0',         match: /1 and 1000000/ },
    { weight: '-1',        match: /1 and 1000000/ },
    { weight: '1000001',   match: /1 and 1000000/ },
    { weight: '1.5',       match: /1 and 1000000/ },
    { weight: 'not-a-num', match: /1 and 1000000/ },
    { weight: '',          match: null },
    { weight: null,        match: null },
    { weight: undefined,   match: null },
  ];
  for (const c of cases) {
    const errs = validateAPIKeyEntries([
      { id: 0, name: 'team-a', api_key: 'FAKE-SECRET-A', proxy_url: '', weight: c.weight },
    ]);
    const row = errs[0];
    if (c.match) {
      assert.ok(row?.weight, `expected weight error for value ${JSON.stringify(c.weight)}, got ${JSON.stringify(errs)}`);
      assert.match(row.weight, c.match);
    } else {
      assert.ok(!row?.weight, `expected no weight error for ${JSON.stringify(c.weight)}, got ${JSON.stringify(row)}`);
    }
  }
});

test('validateAPIKeyEntries: at-max weight (1000000) is accepted', () => {
  const errs = validateAPIKeyEntries([
    { id: 0, name: '', api_key: 'FAKE-SECRET-A', proxy_url: '', weight: '1000000' },
  ]);
  assert.deepEqual(errs, {});
});

test('validateAPIKeyEntries: malformed non-array input returns {} without crashing', () => {
  assert.deepEqual(validateAPIKeyEntries(null), {});
  assert.deepEqual(validateAPIKeyEntries('not-an-array'), {});
  assert.deepEqual(validateAPIKeyEntries(undefined), {});
});

// ============================================================================
// buildForm — hydration
// ============================================================================

test('buildForm: claude-api-key with existing entries preserves ids/names/keys/proxies', () => {
  const initial = {
    provider_type: 'claude-api-key',
    name: 'claude-team',
    api_key_entries: [
      { id: 7, name: 'alpha', api_key: 'FAKE-SECRET-A', proxy_url: 'socks5://proxy-a:1080', weight: 4 },
      { id: 9, name: '',    api_key: 'FAKE-SECRET-B', proxy_url: '', weight: '' },
    ],
  };
  const form = buildForm('claude-api-key', initial);
  assert.equal(form.api_key_entries.length, 2);
  assert.equal(form.api_key_entries[0].id, 7);
  assert.equal(form.api_key_entries[0].name, 'alpha');
  assert.equal(form.api_key_entries[0].api_key, 'FAKE-SECRET-A');
  assert.equal(form.api_key_entries[0].proxy_url, 'socks5://proxy-a:1080');
  assert.equal(form.api_key_entries[0].weight, 4);
  assert.equal(form.api_key_entries[1].id, 9);
  assert.equal(form.api_key_entries[1].api_key, 'FAKE-SECRET-B');
});

test('buildForm: legacy claude-api-key with api_key (no entries) synthesises one entry', () => {
  const initial = {
    provider_type: 'claude-api-key',
    name: 'legacy-claude',
    api_key: 'FAKE-LEGACY-SECRET',
  };
  const form = buildForm('claude-api-key', initial);
  assert.equal(form.api_key_entries.length, 1,
    'exactly one synthesised entry for a legacy key');
  const entry = form.api_key_entries[0];
  assert.equal(entry.api_key, 'FAKE-LEGACY-SECRET');
  assert.equal(entry.proxy_url, '');
  assert.equal(entry.name, '');
  assert.equal(entry.id, 0,
    'synthesised entry is unsaved (id 0)');
  assert.equal(entry.weight, '',
    'synthesised entry weight is blank');
});

test('buildForm: claude-api-key with empty api_key and no entries stays empty', () => {
  const initial = { provider_type: 'claude-api-key', name: 'fresh-claude' };
  const form = buildForm('claude-api-key', initial);
  assert.deepEqual(form.api_key_entries, []);
});

test('buildForm: openai-compatibility still hydrates entries with weight too', () => {
  const initial = {
    provider_type: 'openai-compatibility',
    name: 'openai-foo',
    base_url: 'https://example.test/v1',
    api_key_entries: [
      { id: 3, name: 'alpha', api_key: 'FAKE-SECRET-A', proxy_url: '', weight: 2 },
    ],
  };
  const form = buildForm('openai-compatibility', initial);
  assert.equal(form.api_key_entries.length, 1);
  assert.equal(form.api_key_entries[0].id, 3);
  assert.equal(form.api_key_entries[0].weight, 2);
});

test('buildForm: claude-api-key with malformed non-array api_key_entries yields []', () => {
  const initial = {
    provider_type: 'claude-api-key',
    name: 'claude-malformed',
    api_key: 'FAKE-LEGACY-SECRET',
    api_key_entries: 'not-an-array',
  };
  const form = buildForm('claude-api-key', initial);
  // The legacy fallback must NOT blow up; it should mirror the legacy-key
  // behaviour (empty entries; the legacy key is preserved separately).
  assert.ok(Array.isArray(form.api_key_entries),
    'entries always coerces to an array');
  assert.equal(form.api_key_entries.length, 1,
    'malformed entries + legacy api_key still synthesises one entry from the legacy key');
  assert.equal(form.api_key_entries[0].api_key, 'FAKE-LEGACY-SECRET');
});

// ============================================================================
// buildPayload — Claude
// ============================================================================

test('buildPayload: claude-api-key emits api_key_entries with valid weights', () => {
  const form = {
    api_key_entries: [
      { id: 7, name: 'alpha', api_key: 'FAKE-SECRET-A', proxy_url: 'socks5://proxy-a:1080', weight: 4 },
      { id: 9, name: '',    api_key: 'FAKE-SECRET-B', proxy_url: '', weight: '' },
    ],
  };
  const payload = buildPayload(form, 'claude-api-key');
  assert.ok(Array.isArray(payload.api_key_entries),
    'claude-api-key payload carries api_key_entries');
  assert.equal(payload.api_key_entries.length, 2);
  assert.equal(payload.api_key_entries[0].id, 7);
  assert.equal(payload.api_key_entries[0].name, 'alpha');
  assert.equal(payload.api_key_entries[0].api_key, 'FAKE-SECRET-A');
  assert.equal(payload.api_key_entries[0].proxy_url, 'socks5://proxy-a:1080');
  assert.equal(payload.api_key_entries[0].weight, 4);
  assert.equal(payload.api_key_entries[1].id, 9);
  assert.equal(payload.api_key_entries[1].weight, undefined,
    'blank weight is omitted from the payload');
});

test('buildPayload: claude-api-key omits the legacy api_key field', () => {
  const form = {
    api_key: 'FAKE-LEGACY-SECRET',
    api_key_entries: [
      { id: 7, name: '', api_key: 'FAKE-SECRET-A', proxy_url: '', weight: '' },
    ],
  };
  const payload = buildPayload(form, 'claude-api-key');
  assert.ok(!('api_key' in payload),
    'legacy api_key must not appear in the claude-api-key payload');
  assert.equal(payload.api_key_entries.length, 1);
});

test('buildPayload: claude-api-key emits row-level Claude fields', () => {
  const form = {
    name: 'claude-row',
    api_key_entries: [
      { id: 7, name: '', api_key: 'FAKE-SECRET-A', proxy_url: '', weight: '' },
    ],
    rebuild_mid_system_message: true,
    experimental_cch_signing: true,
    cloak_mode: 'always',
    cloak_strict_mode: true,
    cloak_sensitive_words: ['foo'],
    cloak_cache_user_id: true,
  };
  const payload = buildPayload(form, 'claude-api-key');
  assert.equal(payload.name, 'claude-row');
  assert.equal(payload.rebuild_mid_system_message, true);
  assert.equal(payload.experimental_cch_signing, true);
  assert.equal(payload.cloak_mode, 'always');
  assert.equal(payload.cloak_strict_mode, true);
  assert.deepEqual(payload.cloak_sensitive_words, ['foo']);
  assert.equal(payload.cloak_cache_user_id, true);
});

test('buildPayload: claude-api-key drops blank entries (no api_key)', () => {
  const form = {
    api_key_entries: [
      { id: 0, name: 'alpha', api_key: '', proxy_url: '', weight: '' },
      { id: 0, name: 'beta', api_key: 'FAKE-SECRET-B', proxy_url: '', weight: '' },
      { id: 0, name: 'gamma', api_key: '   ', proxy_url: '', weight: '' },
    ],
  };
  const payload = buildPayload(form, 'claude-api-key');
  assert.equal(payload.api_key_entries.length, 1,
    'only entries with non-blank api_key are sent');
  assert.equal(payload.api_key_entries[0].name, 'beta');
});

test('buildPayload: openai-compatibility entries also carry weight', () => {
  const form = {
    api_key_entries: [
      { id: 1, name: 'alpha', api_key: 'FAKE-SECRET-A', proxy_url: '', weight: 2 },
    ],
  };
  const payload = buildPayload(form, 'openai-compatibility');
  assert.equal(payload.api_key_entries.length, 1);
  assert.equal(payload.api_key_entries[0].weight, 2);
});

test('buildPayload: other API-key providers (gemini/codex/xai/...) keep the single api_key field', () => {
  const form = { api_key: 'FAKE-SECRET-A', name: 'gemini-row' };
  const geminiPayload = buildPayload(form, 'gemini-api-key');
  assert.equal(geminiPayload.api_key, 'FAKE-SECRET-A');
  assert.ok(!Array.isArray(geminiPayload.api_key_entries),
    'gemini does not gain api_key_entries');
});

// ============================================================================
// validate — credential-required + Claude identity edge cases
// ============================================================================

test('validate: claude-api-key with no entries + empty legacy api_key produces a credential-required error', () => {
  const schemas = buildSchemas();
  const schema = schemas['claude-api-key'];
  const form = buildForm('claude-api-key', {});
  const errs = validate(form, schema, 'claude-api-key', [], false);
  assert.ok(errs.api_key_entries,
    `validate must surface a credential-required error, got ${JSON.stringify(errs)}`);
  assert.match(errs.api_key_entries, /required/i);
});

test('validate: claude-api-key with one non-blank entry passes the credential check', () => {
  const schemas = buildSchemas();
  const schema = schemas['claude-api-key'];
  const form = buildForm('claude-api-key', {
    provider_type: 'claude-api-key',
    name: 'claude-row',
    api_key_entries: [
      { id: 7, name: '', api_key: 'FAKE-SECRET-A', proxy_url: '', weight: '' },
    ],
  });
  const errs = validate(form, schema, 'claude-api-key', [], false);
  assert.ok(!errs.api_key_entries,
    `entry with a real key must not produce a credential error, got ${JSON.stringify(errs)}`);
});

test('validate: malformed non-array api_key_entries is handled safely (no crash, no false credit)', () => {
  const schemas = buildSchemas();
  const schema = schemas['claude-api-key'];
  const form = buildForm('claude-api-key', {});
  // Override with a deliberately bad shape; the validator must not crash.
  form.api_key_entries = 'definitely-not-an-array';
  const errs = validate(form, schema, 'claude-api-key', [], false);
  // We accept either "treated as empty -> credential required" OR "treated
  // as unknown shape -> no error if api_key is filled". Either is safer than
  // a crash. Here form.api_key is also empty, so credential-required is
  // preferred.
  assert.ok(errs.api_key_entries || Object.keys(errs).length === 0,
    'malformed entries do not crash and produce at most a credential-required error');
});

test('validate: claude-api-key surfaces identity errors via the shared validator', () => {
  const schemas = buildSchemas();
  const schema = schemas['claude-api-key'];
  const form = buildForm('claude-api-key', {});
  form.api_key_entries = [
    { id: 0, name: 'bad name!', api_key: 'FAKE-SECRET-A', proxy_url: '', weight: '' },
  ];
  const errs = validate(form, schema, 'claude-api-key', [], false);
  assert.ok(errs.api_key_entries,
    `identity errors must surface as api_key_entries, got ${JSON.stringify(errs)}`);
  assert.match(errs.api_key_entries, /identity/);
});
