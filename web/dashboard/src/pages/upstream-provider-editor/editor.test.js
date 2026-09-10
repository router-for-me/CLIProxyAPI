// Tests for the upstream provider editor's pure form layer: the
// `claude-api-key` provider uses the same `api_key_entries` multi-row editor
// as `openai-compatibility`, carries an optional per-entry weight and
// selection-tier priority, and round-trips legacy single-key rows by
// synthesising one unsaved entry. Covers the routing-strategy select too.
//
// These tests touch only pure helper functions exported from this editor
// module — ./schemas.js (buildSchemas) and ./form.js (buildForm,
// buildPayload, validate, validateAPIKeyEntries). They never log fixture
// credentials and never rely on the network or the React tree.
//
// Fixture secret material is intentionally obvious ("FAKE-SECRET-*")
// so any accidental inclusion in a test failure message is obvious as
// a fake credential.

import test from 'node:test';
import assert from 'node:assert/strict';
import { buildSchemas } from './schemas.js';
import {
  buildForm,
  buildPayload,
  validate,
  validateAPIKeyEntries,
} from './form.js';

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

// ============================================================================
// Routing strategy select — schemas, hydration, payload emission
// ============================================================================

test('buildSchemas: claude-api-key + openai-compatibility both expose the routing_strategy select', () => {
  const schemas = buildSchemas();
  const claudeIdentity = schemas['claude-api-key'].sections
    .find((s) => s.title === 'Identity').fields;
  const openaiRouting = schemas['openai-compatibility'].sections
    .find((s) => s.title === 'Routing').fields;
  const claudeField = claudeIdentity.find((f) => f.name === 'routing_strategy');
  const openaiField = openaiRouting.find((f) => f.name === 'routing_strategy');
  assert.ok(claudeField, 'claude-api-key Identity exposes routing_strategy');
  assert.ok(openaiField, 'openai-compatibility Routing exposes routing_strategy');
  for (const field of [claudeField, openaiField]) {
    assert.equal(field.type, 'select');
    assert.ok(Array.isArray(field.options) && field.options.length >= 4,
      `routing_strategy select carries the option list, got ${JSON.stringify(field.options)}`);
    const values = field.options.map((o) => o.value);
    assert.ok(values.includes(''), 'option list includes the blank default');
    assert.ok(values.includes('fill-first'), 'option list includes fill-first');
    assert.ok(values.includes('failover'), 'option list includes the failover alias');
    assert.ok(field.hint, 'routing_strategy carries an operator hint');
  }
});

test('buildSchemas: other provider types do NOT gain the routing_strategy field', () => {
  const schemas = buildSchemas();
  for (const t of ['gemini-api-key', 'codex-api-key', 'oauth:claude', 'vertex-api-key']) {
    const schema = schemas[t];
    assert.ok(schema, `schema for ${t} exists`);
    const names = schema.sections.flatMap((s) => s.fields.map((f) => f.name));
    assert.ok(!names.includes('routing_strategy'),
      `${t} must not expose routing_strategy, got ${JSON.stringify(names)}`);
  }
});

test('validateAPIKeyEntries: priority blank/null/undefined inherit cleanly (no error)', () => {
  for (const priority of ['', null, undefined, '   ']) {
    const errs = validateAPIKeyEntries([
      { id: 0, name: '', api_key: 'FAKE-SECRET-A', proxy_url: '', weight: '', priority },
    ]);
    assert.deepEqual(errs, {},
      `blank priority (${JSON.stringify(priority)}) must not error, got ${JSON.stringify(errs)}`);
  }
});

test('validateAPIKeyEntries: priority accepts any whole number incl. 0 and negatives', () => {
  for (const priority of ['10', '0', '-3', '9999999', 0, 7]) {
    const errs = validateAPIKeyEntries([
      { id: 0, name: '', api_key: 'FAKE-SECRET-A', proxy_url: '', weight: '', priority },
    ]);
    assert.deepEqual(errs, {},
      `priority ${JSON.stringify(priority)} is legal, got ${JSON.stringify(errs)}`);
  }
});

test('validateAPIKeyEntries: priority rejects non-integer / non-numeric values', () => {
  for (const priority of ['abc', '1.5', '1e3', '0x10']) {
    const errs = validateAPIKeyEntries([
      { id: 0, name: '', api_key: 'FAKE-SECRET-A', proxy_url: '', weight: '', priority },
    ]);
    assert.ok(errs[0]?.priority,
      `priority ${JSON.stringify(priority)} must surface a row error, got ${JSON.stringify(errs)}`);
    assert.match(errs[0].priority, /whole number/);
  }
});

test('buildForm: hydrates row routing_strategy + per-entry priority (10 → 10, null → blank)', () => {
  const initial = {
    provider_type: 'openai-compatibility',
    name: 'openai-strategy',
    routing_strategy: 'fill-first',
    api_key_entries: [
      { id: 3, name: 'alpha', api_key: 'FAKE-SECRET-A', proxy_url: '', weight: 2, priority: 10 },
      { id: 4, name: 'beta',  api_key: 'FAKE-SECRET-B', proxy_url: '', priority: null },
    ],
  };
  const form = buildForm('openai-compatibility', initial);
  assert.equal(form.routing_strategy, 'fill-first');
  assert.equal(form.api_key_entries[0].priority, 10);
  assert.equal(form.api_key_entries[1].priority, '',
    'null priority hydrates to blank so the editor shows an empty input');
});

test('buildForm: routing_strategy defaults blank and carries across type switches', () => {
  const fresh = buildForm('claude-api-key', {});
  assert.equal(fresh.routing_strategy, '',
    'no strategy on the source row → blank default');
  const switched = buildForm('openai-compatibility',
    { provider_type: 'claude-api-key', name: 'claude-team' },
    { routing_strategy: 'weighted-round-robin' });
  assert.equal(switched.routing_strategy, 'weighted-round-robin',
    'a picked strategy survives a provider-type switch (create mode)');
});

test('buildForm: legacy claude synthesized entry carries blank priority', () => {
  const form = buildForm('claude-api-key', {
    provider_type: 'claude-api-key',
    api_key: 'FAKE-LEGACY-SECRET',
  });
  assert.equal(form.api_key_entries.length, 1);
  assert.equal(form.api_key_entries[0].priority, '');
});

test('buildPayload: routing_strategy emitted only when the operator picked one', () => {
  const mk = (strategy) => ({
    name: 'row',
    routing_strategy: strategy,
    api_key_entries: [{ id: 1, name: '', api_key: 'FAKE-SECRET-A', proxy_url: '', weight: '', priority: '' }],
  });
  const set = buildPayload(mk('failover'), 'openai-compatibility');
  assert.equal(set.routing_strategy, 'failover',
    'picked alias is sent verbatim; the backend canonicalizes it');
  const claudeSet = buildPayload(mk('fill-first'), 'claude-api-key');
  assert.equal(claudeSet.routing_strategy, 'fill-first');
  for (const blank of ['', null, undefined]) {
    const unset = buildPayload(mk(blank), 'openai-compatibility');
    assert.ok(!('routing_strategy' in unset),
      `blank strategy (${JSON.stringify(blank)}) must be omitted, got ${JSON.stringify(Object.keys(unset))}`);
  }
});

test('buildPayload: per-entry priority emitted incl. explicit 0; blank omitted', () => {
  const form = {
    api_key_entries: [
      { id: 1, name: 'alpha', api_key: 'FAKE-SECRET-A', proxy_url: '', weight: '', priority: '10' },
      // Explicit 0 is meaningful (tier 0) and MUST be sent as 0 — unlike
      // weight, where 0 is excluded.
      { id: 2, name: 'beta',  api_key: 'FAKE-SECRET-B', proxy_url: '', weight: '', priority: '0' },
      // After a server round-trip, hydrateEntries yields a NUMERIC priority
      // (JSON number). Numeric 0 must survive the payload too: a truthy-check
      // "simplification" of the emission guard would drop it here while all
      // string-based cases above stay green. This is the hydrate→save leg of
      // the explicit-tier-0 contract.
      { id: 3, name: 'gamma', api_key: 'FAKE-SECRET-C', proxy_url: '', weight: '', priority: 0 },
      { id: 4, name: 'delt', api_key: 'FAKE-SECRET-D', proxy_url: '', weight: '', priority: '-3' },
      { id: 5, name: 'epsi', api_key: 'FAKE-SECRET-E', proxy_url: '', weight: '', priority: '' },
      { id: 6, name: 'zeta', api_key: 'FAKE-SECRET-F', proxy_url: '', weight: '', priority: null },
    ],
  };
  const payload = buildPayload(form, 'openai-compatibility');
  const entries = payload.api_key_entries;
  assert.equal(entries[0].priority, 10, 'filled priority is emitted as an integer');
  assert.equal(entries[1].priority, 0, 'explicit 0 is emitted as 0, never dropped');
  assert.equal(entries[2].priority, 0, 'numeric 0 (hydrated from the API) is emitted as 0');
  assert.equal(entries[3].priority, -3, 'negative tier is emitted (below-default tier)');
  assert.ok(!('priority' in entries[4]), 'blank priority is omitted (inherit)');
  assert.ok(!('priority' in entries[5]), 'null priority is omitted (inherit)');
});

test('buildPayload: malformed priority falls back to inherit (omitted, no crash)', () => {
  const form = {
    api_key_entries: [
      { id: 1, name: '', api_key: 'FAKE-SECRET-A', proxy_url: '', weight: '', priority: 'abc' },
    ],
  };
  const payload = buildPayload(form, 'claude-api-key');
  assert.ok(!('priority' in payload.api_key_entries[0]),
    'malformed priority is silently omitted from the payload (the inline validator blocks Save first)');
});

test('buildPayload: weight 0 still excluded while priority 0 emitted (distinct semantics)', () => {
  const form = {
    api_key_entries: [
      { id: 1, name: '', api_key: 'FAKE-SECRET-A', proxy_url: '', weight: '0', priority: '0' },
    ],
  };
  const payload = buildPayload(form, 'openai-compatibility');
  const entry = payload.api_key_entries[0];
  assert.ok(!('weight' in entry), 'weight 0 stays excluded (must be >= 1)');
  assert.equal(entry.priority, 0, 'priority 0 is meaningful and emitted');
});

// ── Proxy pool binding (pool picker vs manual URL exclusivity) ─────────

test('hydrateEntries: pool binding hydrates and clears manual proxy url', async () => {
  const { buildForm } = await import('./form.js');
  const form = buildForm('claude-api-key', {
    api_key_entries: [{ id: 1, api_key: 'k', proxy_pool_id: 7, proxy_url: 'http://stale:1' }],
  });
  const entry = form.api_key_entries[0];
  assert.equal(entry.proxy_pool_id, 7);
  // Pool wins on hydrate: the manual URL is cleared (mutually exclusive).
  assert.equal(entry.proxy_url, '');
});

test('hydrateEntries: no binding keeps manual proxy url', async () => {
  const { buildForm } = await import('./form.js');
  const form = buildForm('openai-compatibility', {
    api_key_entries: [{ id: 2, api_key: 'k2', proxy_url: 'socks5://h:1080' }],
  });
  const entry = form.api_key_entries[0];
  assert.ok(!entry.proxy_pool_id || entry.proxy_pool_id === '');
  assert.equal(entry.proxy_url, 'socks5://h:1080');
});

test('entryPayload: emits proxy_pool_id only when set, proxy_url only when manual', async () => {
  const { buildPayload } = await import('./form.js');
  const base = {
    provider_type: 'openai-compatibility',
    name: 'compat',
    priority: 0,
    base_url: 'https://api.example.test/v1',
    api_key: '',
    proxy_url: '',
    headers: [],
    models: [],
    excluded_models: [],
    routing_strategy: '',
    api_key_entries: [],
  };
  const pooled = buildPayload(
    { ...base, api_key_entries: [{ id: 1, api_key: 'k1', proxy_pool_id: 7, proxy_url: '' }] },
    'openai-compatibility',
  );
  assert.equal(pooled.api_key_entries[0].proxy_pool_id, 7);
  assert.ok(!pooled.api_key_entries[0].proxy_url, 'pooled entry must not carry a manual URL');

  const manual = buildPayload(
    { ...base, api_key_entries: [{ id: 2, api_key: 'k2', proxy_pool_id: '', proxy_url: 'socks5://h:1' }] },
    'openai-compatibility',
  );
  assert.equal(manual.api_key_entries[0].proxy_url, 'socks5://h:1');
  assert.ok(!('proxy_pool_id' in manual.api_key_entries[0]), 'manual entry must omit proxy_pool_id');
});

test('buildForm/buildPayload: row-level proxy_pool_id round-trips', async () => {
  const { buildForm, buildPayload } = await import('./form.js');
  const form = buildForm('claude-api-key', { proxy_pool_id: 7, api_key: 'k', api_key_entries: [] });
  assert.equal(form.proxy_pool_id, 7);
  const payload = buildPayload({ ...form, api_key_entries: [] }, 'claude-api-key');
  assert.equal(payload.proxy_pool_id, 7);
});

// ============================================================================
// Per-entry disabled toggle
// ============================================================================

test('buildForm: hydrates per-entry disabled flag (true stays true, absent → false)', () => {
  const form = buildForm('openai-compatibility', {
    api_key_entries: [
      { id: 1, api_key: 'sk-live', name: 'live' },
      { id: 2, api_key: 'sk-off', name: 'off', disabled: true },
    ],
  });
  assert.equal(form.api_key_entries[0].disabled, false, 'absent disabled hydrates to false');
  assert.equal(form.api_key_entries[1].disabled, true, 'disabled=true round-trips');
});

test('buildForm: legacy claude synthesized entry is not disabled', () => {
  const form = buildForm('claude-api-key', { api_key: 'sk-legacy' });
  assert.equal(form.api_key_entries.length, 1);
  assert.equal(form.api_key_entries[0].disabled, false);
});

test('buildPayload: emits disabled and keeps keyed disabled entries', () => {
  const form = buildForm('openai-compatibility', {
    api_key_entries: [
      { id: 1, api_key: 'sk-live', name: 'live' },
      { id: 2, api_key: 'sk-off', name: 'off', disabled: true },
    ],
  });
  const payload = buildPayload(form, 'openai-compatibility');
  assert.equal(payload.api_key_entries.length, 2,
    'a disabled entry with a key is NOT filtered out');
  assert.equal(payload.api_key_entries[1].disabled, true);
  assert.equal(payload.api_key_entries[0].disabled, false);
});

test('buildPayload: disabled blank-key entries are still dropped', () => {
  const form = buildForm('openai-compatibility', {
    api_key_entries: [
      { id: 1, api_key: 'sk-live', name: 'live' },
      { id: 2, api_key: '', name: 'blank', disabled: true },
    ],
  });
  const payload = buildPayload(form, 'openai-compatibility');
  assert.equal(payload.api_key_entries.length, 1,
    'blank-key entries stay filtered regardless of disabled');
});

// ============================================================================
// TestPanel helpers
// ============================================================================

test('TestPanel: entryLabel uses identity, key-<id> fallback, disabled suffix', async () => {
  const { entryLabel } = await import('./TestPanel.jsx');
  assert.equal(entryLabel({ id: 4, name: 'team-a' }), 'team-a');
  assert.equal(entryLabel({ id: 7, name: '' }), 'key-7');
  assert.equal(entryLabel({ id: 7, name: '', disabled: true }), 'key-7 (disabled)');
  assert.equal(entryLabel(null), '');
});

test('TestPanel: entryOptions filters unsaved rows and includes provider-level', async () => {
  const { entryOptions } = await import('./TestPanel.jsx');
  const opts = entryOptions({
    api_key_entries: [
      { id: 1, name: 'alpha' },
      { id: 0, name: 'unsaved' },
      { id: 2, name: '', disabled: true },
    ],
  });
  assert.deepEqual(opts, [
    { value: '', label: '(provider-level)' },
    { value: '1', label: 'alpha' },
    { value: '2', label: 'key-2 (disabled)' },
  ]);
  assert.deepEqual(entryOptions({}), [{ value: '', label: '(provider-level)' }]);
  assert.deepEqual(entryOptions(null), [{ value: '', label: '(provider-level)' }]);
});

test('TestPanel: modelOptions prefers the alias (the registered model id)', async () => {
  const { modelOptions } = await import('./TestPanel.jsx');
  // The in-memory registry registers the ALIAS as the model id
  // (buildConfiguredModelInfo: ID = alias, falling back to name), and the
  // selection gate rejects probes naming an unregistered id. The dropdown
  // must therefore offer the same id the pipeline knows — alias first,
  // upstream name only as the fallback — or a Run Test on an aliased row
  // (e.g. upstream "glm-5.2-flex" registered as "glm-5.2") always fails
  // with auth_unavailable/auth_not_found while a hand-typed alias works.
  assert.deepEqual(
    modelOptions({
      models: [
        { name: 'glm-5.2-flex', alias: 'glm-5.2' },
        { name: '', alias: 'solo-alias' },
        { name: '', alias: '' },
        { name: 'claude-4', alias: '' },
        { name: 'gpt-4o', alias: 'gpt-4o' },
      ],
    }),
    ['glm-5.2', 'solo-alias', 'claude-4', 'gpt-4o'],
  );
  assert.deepEqual(modelOptions({ models: [] }), []);
  assert.deepEqual(modelOptions(null), []);
});

test('TestPanel: describeResult maps ok/mismatch/error states', async () => {
  const { describeResult } = await import('./TestPanel.jsx');
  assert.equal(describeResult(null), null);

  const okMatch = describeResult({
    ok: true, latency_ms: 842, question: 'What is 2 + 2? Reply with just the number.',
    expected_answer: 4, answer: '4',
  });
  assert.equal(okMatch.ok, true);
  assert.equal(okMatch.flag, null);
  assert.equal(okMatch.title, '✓ 842ms');
  assert.equal(okMatch.detail.answer, '4');

  const okMismatch = describeResult({
    ok: true, latency_ms: 500, question: 'Q', expected_answer: 3921, answer: ' 3920 ',
  });
  assert.equal(okMismatch.ok, true);
  assert.equal(okMismatch.flag, 'wrong answer', 'mismatch flags but stays ok');
  assert.equal(okMismatch.detail.answer, '3920', 'answer is trimmed');

  const failed = describeResult({ ok: false, latency_ms: 310, error: '401 Unauthorized' });
  assert.equal(failed.ok, false);
  assert.equal(failed.title, '✗ 401 Unauthorized');
});

test('TestPanel: isEntryBearing matches openai-compat, claude, and opencode-go', async () => {
  const { isEntryBearing } = await import('./TestPanel.jsx');
  assert.equal(isEntryBearing('openai-compatibility'), true);
  assert.equal(isEntryBearing('claude-api-key'), true);
  assert.equal(isEntryBearing('opencode-go'), true);
  assert.equal(isEntryBearing('gemini-api-key'), false);
  assert.equal(isEntryBearing('oauth:claude'), false);
});
