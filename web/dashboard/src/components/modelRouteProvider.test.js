import test from 'node:test';
import assert from 'node:assert/strict';
import {
  providerKeyIsLive,
  generateEntryIdentity,
  expandProviderToChoices,
  expandAllProvidersToChoices,
  mergeLiveWithChoices,
} from './modelRouteProvider.js';

// --- providerKeyIsLive ----------------------------------------------------

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

test('lights up an OpenAI provider-level route when an entry key is reported', () => {
  assert.equal(providerKeyIsLive('openai-compatible-foo', ['openai-compatible-foo:bar']), true);
});

test('lights up a named OpenAI entry route when its exact key is reported', () => {
  assert.equal(providerKeyIsLive('openai-compatible-foo:bar', ['openai-compatible-foo:bar']), true);
});

test('does NOT light up an unrelated OpenAI entry when the provider key is reported', () => {
  assert.equal(providerKeyIsLive('openai-compatible-foo:bar', ['openai-compatible-foo']), false);
});

test('does NOT light up an entry when a sibling entry key is reported', () => {
  assert.equal(providerKeyIsLive('openai-compatible-foo:bar', ['openai-compatible-foo:other']), false);
});

test('does not light up an OpenAI provider-level route for a different provider entry', () => {
  assert.equal(providerKeyIsLive('openai-compatible-foo', ['openai-compatible-baz:bar']), false);
});

// --- Task 5: Claude compound live-key behaviour ---------------------------
// Compound provider keys like `claude:<rowID>` and `claude:<rowID>:key-<id>`
// must be matched exactly. A bare `claude` (legacy/OAuth) live key is NOT
// evidence that every compound Claude row is serving the model. The
// bare-prefix broadening stays scoped to OpenAI Compatibility.

test('providerKeyIsLive: exact compound provider key is live', () => {
  assert.equal(providerKeyIsLive('claude:42', ['claude:42']), true);
});

test('providerKeyIsLive: exact compound entry key is live', () => {
  assert.equal(providerKeyIsLive('claude:42:key-7', ['claude:42:key-7']), true);
});

test('providerKeyIsLive: bare unrelated claude does NOT light up claude:<row> provider', () => {
  assert.equal(providerKeyIsLive('claude:42', ['claude']), false);
});

test('providerKeyIsLive: bare unrelated claude does NOT light up claude:<row>:key-<id> entry', () => {
  assert.equal(providerKeyIsLive('claude:42:key-7', ['claude']), false);
});

test('providerKeyIsLive: different compound claude key does NOT light up another row', () => {
  assert.equal(providerKeyIsLive('claude:42', ['claude:99']), false);
});

test('providerKeyIsLive: compound entry key does NOT light up its parent provider route', () => {
  // Bare-prefix broadening stays scoped to OpenAI; Claude compound provider
  // routes require an exact compound hit at the provider level too.
  assert.equal(providerKeyIsLive('claude:42', ['claude:42:key-7']), false);
});

// --- generateEntryIdentity -----------------------------------------------

test('generateEntryIdentity prefers the stable id over the mutable name', () => {
  // Renaming a row would otherwise invalidate any per-model route pin, so the
  // identity MUST be derived from the persisted row id whenever one exists.
  assert.equal(generateEntryIdentity({ name: 'Team-A', id: 17 }), 'key-17');
});

test('generateEntryIdentity falls back to the normalised name only when no id is set', () => {
  assert.equal(generateEntryIdentity({ name: 'Team-A' }), 'team-a');
});

test('generateEntryIdentity treats reserved key-<digits> names as blank', () => {
  // The backend rejects "key-7" as a name, so the dashboard falls back to the
  // id-derived route instead of mirroring a value the operator cannot save.
  assert.equal(generateEntryIdentity({ name: 'key-7', id: 7 }), 'key-7');
  assert.equal(generateEntryIdentity({ name: 'Key-99', id: 7 }), 'key-7');
});

test('generateEntryIdentity returns empty when both name and id are missing', () => {
  assert.equal(generateEntryIdentity({}), '');
  assert.equal(generateEntryIdentity(null), '');
});

// --- expandProviderToChoices ---------------------------------------------

test('expandProviderToChoices skips disabled rows', () => {
  assert.deepEqual(
    expandProviderToChoices({ provider_key: 'openai-compatible-foo', disabled: true }),
    [],
  );
});

test('expandProviderToChoices skips rows without a provider_key', () => {
  assert.deepEqual(expandProviderToChoices({ provider_type: 'openai-compatibility' }), []);
});

test('expandProviderToChoices returns only a provider-level choice for non-OpenAI rows', () => {
  const out = expandProviderToChoices({
    provider_type: 'claude-api-key',
    provider_key: 'claude:42',
    name: 'anthropic',
  });
  assert.deepEqual(out, [{
    key: 'claude:42',
    label: 'anthropic',
    providerType: 'claude-api-key',
    level: 'provider',
    identity: '',
    count: 1,
  }]);
});

// --- Task 5: Claude (API Key) entry choices ------------------------------
// Task 5 extends `expandProviderToChoices` so a `claude-api-key` row with
// `api_key_entries` shares the provider/entry construction used by OpenAI
// Compatibility. The provider-level choice carries the entry count and
// each entry with a derivable stable identity becomes its own route.

test('expandProviderToChoices: claude-api-key with entries yields provider + per-entry choices', () => {
  const out = expandProviderToChoices({
    provider_type: 'claude-api-key',
    provider_key: 'claude:42',
    name: 'anthropic',
    api_key_entries: [
      { id: 7, name: 'alpha', api_key: 'FAKE-SECRET-alpha' },
      { id: 9, name: 'beta', api_key: 'FAKE-SECRET-beta' },
    ],
  });
  const keys = out.map((c) => c.key);
  // Provider-level choice mirrors the OpenAI branch; count reflects entries.
  const provider = out.find((c) => c.level === 'provider');
  assert.ok(provider, 'provider-level choice present');
  assert.equal(provider.key, 'claude:42');
  assert.equal(provider.count, 2, 'provider count equals entry count when entries exist');
  // Per-entry choices key off the stable persisted id, never the name.
  assert.ok(keys.includes('claude:42:key-7'), 'id-keyed entry route present');
  assert.ok(keys.includes('claude:42:key-9'), 'id-keyed entry route present');
  // API-key secrets never leak into labels or keys.
  for (const c of out) {
    assert.ok(!c.label.includes('FAKE-SECRET'), 'label never carries secret');
    assert.ok(!c.key.includes('FAKE-SECRET'), 'key never carries secret');
  }
});

test('expandProviderToChoices: claude-api-key omits entries with no derivable identity', () => {
  const out = expandProviderToChoices({
    provider_type: 'claude-api-key',
    provider_key: 'claude:42',
    name: 'anthropic',
    api_key_entries: [
      { id: 7, name: 'alpha', api_key: 'FAKE-SECRET-alpha' },
      // No id and no name → no identity, no entry choice.
      { id: 0, name: '', api_key: 'FAKE-SECRET-ghost' },
      // Whitespace-only name and no id → no identity, no entry choice.
      { id: 0, name: '   ', api_key: 'FAKE-SECRET-blank' },
    ],
  });
  const keys = out.map((c) => c.key);
  assert.ok(keys.includes('claude:42:key-7'), 'persisted id entry present');
  assert.ok(
    !keys.some((k) => k.startsWith('claude:42:') && k !== 'claude:42:key-7'),
    'no other entry-level keys emitted for invalid entries',
  );
  const provider = out.find((c) => c.level === 'provider');
  assert.ok(provider, 'provider-level choice present');
  assert.equal(provider.count, 3, 'provider count reflects all attempted entries');
});

test('expandProviderToChoices: claude-api-key without entries keeps provider-level only', () => {
  const out = expandProviderToChoices({
    provider_type: 'claude-api-key',
    provider_key: 'claude:42',
    name: 'anthropic',
    api_key_entries: [],
  });
  assert.deepEqual(out, [{
    key: 'claude:42',
    label: 'anthropic',
    providerType: 'claude-api-key',
    level: 'provider',
    identity: '',
    count: 1,
  }]);
});

test('expandProviderToChoices: claude-api-key honors reserved key-<digits> name fallback', () => {
  const out = expandProviderToChoices({
    provider_type: 'claude-api-key',
    provider_key: 'claude:42',
    name: 'anthropic',
    api_key_entries: [{ id: 7, name: 'key-7', api_key: 'FAKE-SECRET-reserved' }],
  });
  const keys = out.map((c) => c.key);
  // The reserved name collapses to the implicit key-7 identity derived from id.
  assert.ok(keys.includes('claude:42:key-7'),
    'reserved name still produces the id-keyed entry route');
});

test('expandProviderToChoices yields provider-level + per-entry choices keyed off the stable id', () => {
  const out = expandProviderToChoices({
    provider_type: 'openai-compatibility',
    provider_key: 'openai-compatible-foo',
    name: 'foo',
    api_key_entries: [
      { id: 1, name: 'Team-A', api_key: 'EXAMPLE_KEY_DO_NOT_USE' },
      { id: 2, name: '', api_key: 'EXAMPLE_KEY_DO_NOT_USE' },
    ],
  });
  const keys = out.map((c) => c.key);
  assert.ok(keys.includes('openai-compatible-foo'), 'provider route present');
  // Both entry choices key off the persisted row id; the mutable name must
  // not influence the routing identity.
  assert.ok(keys.includes('openai-compatible-foo:key-1'), 'id-keyed entry route present');
  assert.ok(keys.includes('openai-compatible-foo:key-2'), 'persisted unnamed entry route present');
  // Never expose api_key material anywhere in labels or keys.
  for (const c of out) {
    assert.ok(!c.label.includes('EXAMPLE_KEY_DO_NOT_USE'));
    assert.ok(!c.key.includes('EXAMPLE_KEY_DO_NOT_USE'));
  }
});

test('expandProviderToChoices yields no exact entry choice for an entry without name or id', () => {
  const out = expandProviderToChoices({
    provider_type: 'openai-compatibility',
    provider_key: 'openai-compatible-foo',
    name: 'foo',
    api_key_entries: [{ id: 0, name: '', api_key: 'EXAMPLE_KEY_DO_NOT_USE' }],
  });
  assert.equal(out.length, 1);
  assert.equal(out[0].level, 'provider');
});

test('expandProviderToChoices does not duplicate a reserved key-<id> name as an entry choice', () => {
  const out = expandProviderToChoices({
    provider_type: 'openai-compatibility',
    provider_key: 'openai-compatible-foo',
    api_key_entries: [{ id: 7, name: 'key-7', api_key: 'EXAMPLE_KEY_DO_NOT_USE' }],
  });
  // The reserved name collapses to the implicit key-7 identity, so the
  // generated fallback is the only stable route we can publish.
  const keys = out.map((c) => c.key);
  assert.ok(keys.includes('openai-compatible-foo:key-7'));
});

// --- expandAllProvidersToChoices -----------------------------------------

test('expandAllProvidersToChoices dedupes colliding keys without collapsing distinct identities', () => {
  const choices = expandAllProvidersToChoices([
    {
      provider_type: 'openai-compatibility',
      provider_key: 'openai-compatible-foo',
      name: 'foo',
      api_key_entries: [
        { id: 1, name: 'a' },
        { id: 2, name: 'b' },
      ],
    },
    {
      provider_type: 'openai-compatibility',
      provider_key: 'openai-compatible-foo',
      name: 'foo',
      api_key_entries: [{ id: 3, name: 'a' }],
    },
  ]);
  const providerRows = choices.filter((c) => c.level === 'provider');
  const entryRows = choices.filter((c) => c.level === 'entry');
  assert.equal(providerRows.length, 1, 'provider-level choice is deduped');
  assert.equal(providerRows[0].count, 3, 'count sums entries across rows sharing a key');
  // Three unique id-keyed identities are published once each; renaming an
  // entry must not collapse them because the id is the stable source.
  assert.equal(entryRows.length, 3, 'distinct id-keyed identities are kept');
  assert.deepEqual(
    entryRows.map((c) => c.identity).sort(),
    ['key-1', 'key-2', 'key-3'],
  );
});

// --- mergeLiveWithChoices ------------------------------------------------

test('mergeLiveWithChoices puts configured choices first and appends live-only choices', () => {
  const merged = mergeLiveWithChoices(
    ['live-only:1'],
    [{ key: 'cfg:1', label: 'cfg', providerType: '', level: 'provider', identity: '', count: 1 }],
    new Map(),
  );
  assert.deepEqual(
    merged.map((c) => c.key),
    ['cfg:1', 'live-only:1'],
  );
});

test('mergeLiveWithChoices drops duplicate keys between live and configured', () => {
  const merged = mergeLiveWithChoices(
    ['cfg:1'],
    [{ key: 'cfg:1', label: 'cfg', providerType: '', level: 'provider', identity: '', count: 1 }],
    new Map(),
  );
  assert.equal(merged.length, 1);
});
