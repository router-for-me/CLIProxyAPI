import { readFileSync } from 'node:fs';
import { describe, expect, test } from 'bun:test';
import { parse as parseYaml } from 'yaml';
import { CONFIG_FIELD_SEARCH_INDEX } from '../src/features/config/searchIndex';
import { FIELD_VALUE_KEYS } from '../src/features/config/constants';
import { DEFAULT_VISUAL_VALUES, type VisualConfigValues } from '../src/types/visualConfig';
import { readCommercialModeFromYaml } from '../src/features/config/hooks/useConfigDocument';
import { runVisualConfig } from './helpers/visualConfig';

const fixture = readFileSync(new URL('./fixtures/visualConfig-v8.yaml', import.meta.url), 'utf8');

describe('v8 visual config contract', () => {
  test('reads nested values without losing explicit false and zero', () => {
    const config = runVisualConfig(fixture);
    expect(config.visualValues).toMatchObject({
      host: 'localhost',
      port: '8317',
      apiKeysText: 'fixture-client',
      commercialMode: false,
      wsAuth: false,
      requestRetry: '0',
      maxRetryCredentials: '0',
      maxRetryInterval: '0',
      authAutoRefreshWorkers: '0',
      logsMaxTotalSizeMb: '0',
      errorLogsMaxFiles: '0',
      antigravitySignatureCacheEnabled: false,
      quotaAntigravityCredits: false,
      codexHeaderUserAgent: 'fixture-oauth-agent',
      streaming: { keepaliveSeconds: '0', bootstrapRetries: '0', nonstreamKeepaliveInterval: '0' },
    });
    expect(config.visualValues).not.toHaveProperty('quotaSwitchProject');
    expect(config.visualValues).not.toHaveProperty('quotaSwitchPreviewModel');
    expect(parseYaml(fixture)).not.toHaveProperty('quota-exceeded');
    expect(config.visualDirty).toBe(false);
    expect(parseYaml(config.applyVisualChangesToYaml(fixture))).toEqual(parseYaml(fixture));
  });

  test('client key writes and deletion never replace upstream provider groups', () => {
    for (const apiKeysText of ['new-fixture-client', '']) {
      const config = runVisualConfig(fixture, [{ apiKeysText }]);
      const output = parseYaml(config.applyVisualChangesToYaml(fixture));
      expect(output['api-keys']).toEqual(parseYaml(fixture)['api-keys']);
      expect(output.access?.['api-keys']).toEqual(apiKeysText ? [apiKeysText] : undefined);
    }
    expect(runVisualConfig('api-keys:\n  codex: []\n').visualValues.apiKeysText).toBe('');
  });

  test('OAuth edits stay OAuth-only and preserve unmoved routing and plugin fields', () => {
    const config = runVisualConfig(fixture, [
      {
        codexHeaderUserAgent: 'new-oauth-agent',
        antigravitySensitiveWords: [],
        quotaAntigravityCredits: true,
      },
    ]);
    const original = parseYaml(fixture);
    const output = parseYaml(config.applyVisualChangesToYaml(fixture));
    expect(output['api-keys']).toEqual(original['api-keys']);
    expect(output.routing).toEqual(original.routing);
    expect(output.plugins).toEqual(original.plugins);
    expect(output).not.toHaveProperty('quota-exceeded');
    expect(output.oauth.providers.codex['header-defaults']['user-agent']).toBe('new-oauth-agent');
    expect(output.oauth.providers.antigravity['sensitive-words']).toBeUndefined();
    expect(output.oauth.providers.antigravity['antigravity-credits']).toBe(true);
    expect(output['codex-header-defaults']).toBeUndefined();
  });

  test('writes zero and false to v8 paths on the latest document while preserving unknown payload data', () => {
    const baseline = fixture
      .replace('request-retry: 0', 'request-retry: 2')
      .replace('ws-auth: false', 'ws-auth: true');
    const initial = runVisualConfig(baseline);
    const rule = initial.visualValues.payloadDefaultRules[0];
    const config = runVisualConfig(baseline, [
      {
        requestRetry: '0',
        wsAuth: false,
        payloadDefaultRules: [{ ...rule, params: [{ ...rule.params[0], value: '1' }] }],
      },
    ]);
    const latest = baseline.replace('future-rule: keep', 'future-rule: server-update');
    const outputText = config.applyVisualChangesToYaml(latest);
    const output = parseYaml(outputText);
    expect(output.routing.retry['request-retry']).toBe(0);
    expect(output.oauth.providers.aistudio['ws-auth']).toBe(false);
    expect(output.requests.payload['future-section']).toEqual({ enabled: false });
    expect(output.requests.payload.default[0]).toMatchObject({
      'future-rule': 'server-update',
      params: { temperature: 1 },
      models: [{ name: 'fixture-model', 'future-model': 'keep' }],
    });
    expect(outputText).toContain('# v8 visual editor fixture');
    expect(output['future-root']).toEqual({ unknown: 'keep' });
    expect(output.payload).toBeUndefined();
  });

  const nullRouting = 'config-version: 8\nserver: {port: 8317}\nrouting: null\n';
  const nullRoutingCases: [string, Partial<VisualConfigValues>, Record<string, unknown>][] = [
    ['request retry', { requestRetry: '3' }, { retry: { 'request-retry': 3 } }],
    ['retry credentials', { maxRetryCredentials: '4' }, { retry: { 'max-retry-credentials': 4 } }],
    ['retry interval', { maxRetryInterval: '5' }, { retry: { 'max-retry-interval': 5 } }],
    ['force prefix', { forceModelPrefix: true }, { 'force-model-prefix': true }],
    ['cooling', { disableCooling: true }, { cooldown: { 'disable-cooling': true } }],
    ['strategy', { routingStrategy: 'fill-first' }, { strategy: 'fill-first' }],
    ['session affinity', { routingSessionAffinity: true }, { 'session-affinity': true }],
    ['affinity TTL', { routingSessionAffinityTTL: '1h' }, { 'session-affinity-ttl': '1h' }],
  ];

  for (const [name, patch, expectedRouting] of nullRoutingCases) {
    test(`null routing accepts ${name} without losing other edits`, () => {
      const config = runVisualConfig(nullRouting, [{ port: '9000', ...patch }]);
      const output = parseYaml(config.applyVisualChangesToYaml(nullRouting));
      expect(output).toEqual({
        'config-version': 8,
        server: { port: 9000 },
        routing: expectedRouting,
      });
    });
  }

  test('normalizes null routing in the latest document and writes explicit zero and false', () => {
    const baseline = `config-version: 8
server: {port: 8317}
routing:
  force-model-prefix: true
  retry: {request-retry: 3, max-retry-credentials: 4, max-retry-interval: 5}
  cooldown: {disable-cooling: true}
`;
    const config = runVisualConfig(baseline, [
      {
        port: '9000',
        requestRetry: '0',
        maxRetryCredentials: '0',
        maxRetryInterval: '0',
        forceModelPrefix: false,
        disableCooling: false,
      },
    ]);
    const output = parseYaml(config.applyVisualChangesToYaml(nullRouting));
    expect(output.server.port).toBe(9000);
    expect(output.routing).toEqual({
      'force-model-prefix': false,
      retry: { 'request-retry': 0, 'max-retry-credentials': 0, 'max-retry-interval': 0 },
      cooldown: { 'disable-cooling': false },
    });
  });

  test('preserves null routing when routing fields are not edited', () => {
    expect(parseYaml(runVisualConfig(nullRouting).applyVisualChangesToYaml(nullRouting))).toEqual(
      parseYaml(nullRouting)
    );
    const config = runVisualConfig(nullRouting, [{ port: '9000' }]);
    expect(parseYaml(config.applyVisualChangesToYaml(nullRouting))).toEqual({
      'config-version': 8,
      server: { port: 9000 },
      routing: null,
    });
  });

  test('commercial mode warning detection uses server.commercial-mode only', () => {
    expect(readCommercialModeFromYaml('server: {commercial-mode: true}')).toBe(true);
    expect(readCommercialModeFromYaml(fixture)).toBe(false);
    expect(readCommercialModeFromYaml('commercial-mode: true')).toBe(false);
    expect(readCommercialModeFromYaml('server: null')).toBe(false);
    expect(readCommercialModeFromYaml('server: [')).toBe(false);
  });
});

describe('v8 scalar read/write path parity', () => {
  const numericFields = new Set([
    'port',
    'errorLogsMaxFiles',
    'logsMaxTotalSizeMb',
    'redisUsageQueueRetentionSeconds',
    'requestRetry',
    'maxRetryCredentials',
    'maxRetryInterval',
    'authAutoRefreshWorkers',
    'transientErrorCooldownSeconds',
    'antigravityConnectionPoolMaxIdleConnsPerHost',
    'codexLiveMediaRelayMaxSessions',
    'codexLiveMediaRelayUDPPortMin',
    'codexLiveMediaRelayUDPPortMax',
  ]);
  for (const entry of CONFIG_FIELD_SEARCH_INDEX) {
    const field = FIELD_VALUE_KEYS[entry.fieldId][0];
    if (field.includes('.') || !entry.yamlKeys) continue;
    const key = field as keyof VisualConfigValues;
    const initial = DEFAULT_VISUAL_VALUES[key];
    if (typeof initial !== 'string' && typeof initial !== 'boolean') continue;
    test(`${field} reads and writes ${entry.yamlKeys.join('.')}`, () => {
      const baseline = runVisualConfig(fixture).visualValues[key];
      const value =
        typeof baseline === 'boolean'
          ? !baseline
          : numericFields.has(field)
            ? '1'
            : field === 'routingStrategy'
              ? 'fill-first'
              : field === 'disableImageGeneration'
                ? 'chat'
                : 'fixture-updated';
      const config = runVisualConfig(fixture, [{ [field]: value }]);
      const output = config.applyVisualChangesToYaml(fixture);
      const path = [...entry.yamlKeys!];
      if (field === 'tlsEnable') path.push('enable');
      if (field === 'pluginsEnabled') path.push('enabled');
      const expected =
        field === 'apiKeysText' ? [value] : numericFields.has(field) ? Number(value) : value;
      expect(
        path.reduce<unknown>(
          (node, key) => (node as Record<string, unknown>)[key],
          parseYaml(output)
        )
      ).toEqual(expected);
      expect(runVisualConfig(output).visualValues[key]).toEqual(value);
    });
  }
});
