import test from 'node:test';
import assert from 'node:assert/strict';
import {
  sendIdFor,
  buildProviderGroups,
  buildCatalogGroups,
  collectSendIds,
  filterGroups,
} from './providerModels.js';

const providers = [
  {
    id: 1,
    name: 'My OpenAI',
    provider_type: 'openai-compatibility',
    provider_key: 'openai-compatible-my-openai',
    models: [
      { name: 'gpt-4o', alias: 'gpt-4o-fast', display_name: 'GPT-4o Fast' },
      { name: 'gpt-4o-mini', alias: '' },
      { name: 'gpt-4o-mini', alias: '' }, // duplicate within provider
    ],
  },
  {
    id: 2,
    name: 'Gemini',
    provider_type: 'gemini',
    provider_key: 'gemini',
    models: [{ name: 'gemini-2.0-flash', alias: 'gem-flash' }],
  },
  {
    id: 3,
    name: 'Empty',
    provider_type: 'claude',
    models: [],
  },
];

test('sendIdFor prefers the alias, falling back to the name', () => {
  assert.equal(sendIdFor({ name: 'n', alias: 'a' }), 'a');
  assert.equal(sendIdFor({ name: 'n', alias: '' }), 'n');
  assert.equal(sendIdFor({ name: '', alias: 'a' }), 'a');
  assert.equal(sendIdFor({}), '');
});

test('buildProviderGroups groups models per provider and uses the alias as sendId', () => {
  const groups = buildProviderGroups(providers);
  // The empty provider is skipped.
  assert.deepEqual(groups.map((g) => g.title), ['Gemini', 'My OpenAI']);
  const openai = groups.find((g) => g.title === 'My OpenAI');
  assert.equal(openai.type, 'openai-compatibility');
  assert.equal(openai.models.length, 2); // duplicate name deduped
  assert.deepEqual(openai.models.map((m) => m.sendId), ['gpt-4o-fast', 'gpt-4o-mini']);
  assert.equal(openai.models[0].isAlias, true);
  assert.equal(openai.models[0].name, 'gpt-4o');
  assert.equal(openai.models[1].isAlias, false);
});

test('buildProviderGroups badges live + cooldown from the live sources', () => {
  const groups = buildProviderGroups(providers, {
    cooldownProviders: new Set(['gemini']),
    liveStatus: { 1: { is_live: true }, 2: { is_live: true } },
  });
  const openai = groups.find((g) => g.title === 'My OpenAI');
  const gemini = groups.find((g) => g.title === 'Gemini');
  assert.equal(openai.live, true);
  assert.equal(openai.cooldown, false);
  assert.equal(gemini.cooldown, true);
});

test('buildProviderGroups tolerates malformed input', () => {
  assert.deepEqual(buildProviderGroups(null), []);
  assert.deepEqual(buildProviderGroups([], {}), []);
  assert.deepEqual(buildProviderGroups([{ models: null }, null]), []);
});

test('buildCatalogGroups skips ids already covered and groups the rest', () => {
  const upstream = buildProviderGroups(providers);
  const seen = collectSendIds(upstream);
  const catalog = [
    { model: 'gpt-4o-fast', provider: 'openai' }, // already covered by alias
    { model: 'legacy-model', provider: 'Other' },
    { model: 'legacy-2', provider: 'Other' },
  ];
  const groups = buildCatalogGroups(catalog, seen);
  assert.equal(groups.length, 1);
  assert.equal(groups[0].title, 'Other');
  assert.deepEqual(groups[0].models.map((m) => m.sendId), ['legacy-model', 'legacy-2']);
});

test('filterGroups matches provider name/type and model name/alias', () => {
  const groups = buildProviderGroups(providers);
  assert.equal(filterGroups(groups, 'gemini').length, 1);
  assert.equal(filterGroups(groups, 'gpt-4o')[0].title, 'My OpenAI');
  assert.equal(filterGroups(groups, 'gpt-4o')[0].models.length, 2);
  // Alias-only match keeps the group but narrows to the matching row.
  const aliasHit = filterGroups(groups, 'gem-flash');
  assert.equal(aliasHit.length, 1);
  assert.equal(aliasHit[0].models.length, 1);
  assert.equal(filterGroups(groups, 'zzz').length, 0);
});
