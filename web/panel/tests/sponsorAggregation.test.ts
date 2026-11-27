import { describe, expect, test } from 'bun:test';
import { createInstance } from 'i18next';
import en from '@/i18n/locales/en.json';
import { normalizeConfigResponse } from '@/services/api/transformers';
import {
  APIKEY_FUN_CODEX_BASE_URL,
  APIKEY_FUN_ANTHROPIC_BASE_URL,
} from '@/features/providers/sponsor';
import { buildProviderGroups } from '@/features/providers/useProviderWorkbench';
import { prepareSponsorWrite } from '@/features/providers/sponsorBundle';

import { getSponsorAggregationConflict } from '../src/features/providers/sponsorDefinitions';
import type { SponsorProviderRaw } from '../src/features/providers/types';

const emptyRaw = (): SponsorProviderRaw => ({
  openai: [],
  claude: [],
  codex: [],
  gemini: [],
});

describe('sponsor aggregation safety', () => {
  test('detects multiple configs for one protocol', () => {
    const raw = emptyRaw();
    raw.codex = [
      { index: 0, config: { apiKey: 'first' } },
      { index: 1, config: { apiKey: 'second' } },
    ];

    expect(getSponsorAggregationConflict(raw)).toBe('multiple-configs');
  });

  test('detects multiple OpenAI API keys in one config', () => {
    const raw = emptyRaw();
    raw.openai = [
      {
        index: 0,
        config: {
          name: 'Sponsor',
          baseUrl: 'https://example.com/v1',
          apiKeyEntries: [{ apiKey: 'first' }, { apiKey: 'second' }],
        },
      },
    ];

    expect(getSponsorAggregationConflict(raw)).toBe('multiple-openai-keys');
  });

  test('allows the supported one-config-per-protocol shape', () => {
    const raw = emptyRaw();
    raw.codex = [{ index: 0, config: { apiKey: 'codex' } }];
    raw.openai = [
      {
        index: 0,
        config: {
          name: 'Sponsor',
          baseUrl: 'https://example.com/v1',
          apiKeyEntries: [{ apiKey: 'openai' }],
        },
      },
    ];

    expect(getSponsorAggregationConflict(raw)).toBeNull();
  });
});

const sponsorConfig = (codexKeys: string[][]) =>
  normalizeConfigResponse({
    'api-keys': {
      codex: codexKeys.map((keys) => ({
        'base-url': APIKEY_FUN_CODEX_BASE_URL,
        keys: keys.map((key) => ({ 'api-key': key })),
      })),
      claude: [
        { 'base-url': APIKEY_FUN_ANTHROPIC_BASE_URL, keys: [{ 'api-key': 'claude-fixture' }] },
      ],
    },
  });

const i18n = createInstance();
await i18n.init({ lng: 'en', resources: { en: { translation: en } } });

describe('sponsor write preflight', () => {
  test('rejects submissions containing an ambiguous protocol, including mixed submissions', () => {
    const config = sponsorConfig([['first'], ['second']]);
    const groups = buildProviderGroups(config);
    expect(groups.find((group) => group.id === 'apikeyFun')?.ambiguousProtocols).toEqual(['codex']);
    expect(groups.find((group) => group.id === 'codex')?.nativeSections).toHaveLength(2);
    expect(() => prepareSponsorWrite('apikeyFun', config, [{ protocol: 'codex' }], i18n.t)).toThrow(
      'Multiple groups match'
    );
    expect(() =>
      prepareSponsorWrite(
        'apikeyFun',
        config,
        [{ protocol: 'claude' }, { protocol: 'codex' }],
        i18n.t
      )
    ).toThrow(en.providersPage.sponsor.protocols.codexResponses);
    // A different protocol can still be added; ambiguous native groups are not mutation targets.
    const raw = prepareSponsorWrite(
      'apikeyFun',
      config,
      [{ protocol: 'openai' }, { protocol: 'claude' }],
      i18n.t
    );
    expect(raw.codex).toEqual([]);
    expect(raw.claude).toHaveLength(1);
    expect(raw.openai).toEqual([]);
  });

  test('counts only eligible singleton groups, not keys from native connection pools', () => {
    const entries = [{ protocol: 'codex' }] as const;
    const pool = ['pooled-a', 'pooled-b'];
    expect(prepareSponsorWrite('apikeyFun', sponsorConfig([pool]), entries, i18n.t).codex).toEqual(
      []
    );
    const raw = prepareSponsorWrite(
      'apikeyFun',
      sponsorConfig([pool, ['singleton']]),
      entries,
      i18n.t
    );
    expect(raw.codex.map(({ config }) => config.apiKey)).toEqual(['singleton']);
  });
});
