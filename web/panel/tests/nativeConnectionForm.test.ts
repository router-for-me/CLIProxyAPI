import { expect, test } from 'bun:test';
import { createElement } from 'react';
import { renderToStaticMarkup } from 'react-dom/server';
import { createInstance } from 'i18next';
import { I18nextProvider } from 'react-i18next';
import en from '@/i18n/locales/en.json';
import { BaseProviderForm } from '@/features/providers/sheets/forms/BaseProviderForm';
import {
  buildSharedSettings,
  memberProbeSettings,
  readSharedForm,
  readMemberForm,
  setMemberMode,
} from '@/features/providers/nativeConnectionForm';
import { buildProviderGroups } from '@/features/providers/useProviderWorkbench';
import { normalizeConfigResponse } from '@/services/api/transformers';

const i18n = createInstance();
await i18n.init({ lng: 'en', resources: { en: { translation: en } } });
const group = {
  name: 'Fixture connection',
  'base-url': 'https://example.invalid',
  headers: { 'X-Group': 'fixture' },
  models: [{ name: 'upstream', alias: 'model', future: true }],
  'connection-pool': { enabled: false, future: true },
  keys: [
    { 'api-key': 'fixture-one', priority: 9, models: [{ name: 'first-key-model' }] },
    { 'api-key': 'fixture-two', 'proxy-url': 'direct' },
  ],
};

test('all native credentials are blank password inputs within the same shared settings form', () => {
  const nativeSection = buildProviderGroups(
    normalizeConfigResponse({ 'api-keys': { claude: [group] } })
  ).find((g) => g.id === 'claude')!.nativeSections![0];
  const html = renderToStaticMarkup(
    createElement(
      I18nextProvider,
      { i18n },
      createElement(BaseProviderForm, {
        brand: 'claude',
        mode: 'edit',
        nativeSection,
        resource: null,
        formId: 'fixture',
        mutating: false,
        onNativeSubmit: async () => {},
      })
    )
  );
  expect(html.match(/<form\b/g)).toHaveLength(1);
  expect(html.match(/type="password"/g)).toHaveLength(2);
  expect(html).toContain('Auto · first-key-model');
  expect(html).not.toContain('Auto · upstream');
  expect(html).toContain('value="Fixture connection"');
  expect(html).toContain('value="https://example.invalid"');
  expect(html).toContain(en.providersPage.form.addApiKeyEntry);
  for (const [index, key] of group.keys.entries()) {
    expect(html).toContain(`aria-label="Delete Key #${index + 1}"`);
    expect(html).not.toContain(key['api-key']);
  }
  expect(html).not.toContain('type="submit"'); // One Save, owned by the sheet footer.
});

test('changing shared controls preserves unknown fields, sparse pool properties and key overrides', () => {
  const shared = readSharedForm(group, {
    apiKey: '',
    name: '',
    baseUrl: '',
    prefix: '',
    proxyUrl: '',
    disabled: false,
    excludedModelsText: '',
    models: [],
    headers: [],
  });
  expect(buildSharedSettings('claude', group, shared, shared)).toEqual(group);
  const updated = buildSharedSettings('claude', group, shared, {
    ...shared,
    baseUrl: 'https://new.example.invalid',
    prefix: 'team',
    models: [{ ...shared.models[0], alias: 'renamed' }],
    connectionPool: { ...shared.connectionPool, maxIdleConnsPerHost: 0 },
  });
  expect(updated).toEqual({
    ...group,
    'base-url': 'https://new.example.invalid',
    prefix: 'team',
    models: [{ ...group.models[0], alias: 'renamed' }],
    'connection-pool': { ...group['connection-pool'], 'max-idle-conns-per-host': 0 },
  });
  expect(updated.keys).toBe(group.keys);
});

test('probing a key applies its direct proxy and explicit headers instead of shared defaults', () => {
  const shared = readSharedForm(group, {
    apiKey: '',
    name: '',
    baseUrl: '',
    prefix: '',
    proxyUrl: '',
    disabled: false,
    excludedModelsText: '',
    models: [],
    headers: [],
  });
  const initial = readMemberForm(shared, null);
  const custom = setMemberMode(
    setMemberMode(initial, 'proxyUrl', 'override'),
    'headers',
    'override'
  );
  const input = {
    ...custom,
    apiKey: 'fixture-new',
    proxyUrl: 'direct',
    headers: [{ key: 'X-Key', value: 'fixture' }],
  };
  const connection = { ...shared, proxyUrl: 'http://proxy.invalid:8080' };
  expect(memberProbeSettings(connection, { resource: null, initial, input })).toEqual({
    probeApiKey: 'fixture-new',
    probeProxyUrl: 'direct',
    probeHeaders: input.headers,
    probeModels: shared.models,
  });
  expect(memberProbeSettings(connection, { resource: null, initial, input: initial })).toEqual({
    probeApiKey: '',
    probeProxyUrl: connection.proxyUrl,
    probeHeaders: connection.headers,
    probeModels: shared.models,
  });
});

test('only missing/null unchanged member fields follow the edited group', () => {
  const fixture = {
    ...group,
    'proxy-url': 'http://original.invalid',
    keys: [
      { 'api-key': 'fixture-clear', 'proxy-url': '', headers: {} },
      { 'api-key': 'fixture-inherit', 'proxy-url': null, headers: null },
    ],
  };
  const members = buildProviderGroups(
    normalizeConfigResponse({ 'api-keys': { claude: [fixture] } })
  ).find((g) => g.id === 'claude')!.nativeSections![0].members;
  const shared = readSharedForm(fixture, {
    apiKey: '',
    name: '',
    baseUrl: '',
    prefix: '',
    proxyUrl: '',
    disabled: false,
    excludedModelsText: '',
    models: [],
    headers: [],
  });
  const edited = { ...shared, proxyUrl: 'direct', headers: [{ key: 'X-New', value: 'new' }] };
  for (const [index, resource] of members.entries()) {
    const initial = readMemberForm(shared, resource);
    const probe = (input: typeof shared) =>
      memberProbeSettings(edited, { resource, initial, input });
    const probeApiKey = fixture.keys[index]['api-key'];
    const empty = { probeApiKey, probeProxyUrl: '', probeHeaders: [], probeModels: shared.models };
    const inherited = {
      probeApiKey,
      probeProxyUrl: 'direct',
      probeHeaders: edited.headers,
      probeModels: shared.models,
    };
    expect(probe(initial)).toEqual(index === 0 ? empty : inherited);
    for (const [mode, expected] of [
      ['empty', empty],
      ['inherit', inherited],
    ] as const)
      expect(
        probe(setMemberMode(setMemberMode(initial, 'proxyUrl', mode), 'headers', mode))
      ).toEqual(expected);
  }
});
