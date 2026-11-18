import { describe, expect, test } from 'bun:test';
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
