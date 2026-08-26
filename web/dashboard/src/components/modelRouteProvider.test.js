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

// --- generateEntryIdentity -----------------------------------------------

test('generateEntryIdentity returns the normalised name when valid', () => {
  assert.equal(generateEntryIdentity({ name: 'Team-A', id: 17 }), 'team-a');
});

test('generateEntryIdentity falls back to key-<id> when unnamed', () => {
  assert.equal(generateEntryIdentity({ id: 42 }), 'key-42');
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

test('expandProviderToChoices yields provider-level + named-entry + persisted-unnamed-entry choices', () => {
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
  assert.ok(keys.includes('openai-compatible-foo:team-a'), 'named entry route present');
  assert.ok(keys.includes('openai-compatible-foo:key-2'), 'unnamed persisted entry route present');
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
  // Two unique identities (a, b) are published once each; the colliding "a"
  // identity does not produce a second copy because both rows expose the
  // same exact route.
  assert.equal(entryRows.length, 2, 'distinct identities are kept');
  assert.deepEqual(
    entryRows.map((c) => c.identity).sort(),
    ['a', 'b'],
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
