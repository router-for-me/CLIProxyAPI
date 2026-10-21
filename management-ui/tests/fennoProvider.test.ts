import { describe, expect, test } from 'bun:test';
import {
  buildFennoAIRaw,
  FENNO_AI_CODEX_BASE_URL,
  FENNO_AI_PROVIDER_NAME,
} from '../src/features/providers/fennoAI';
import { getSponsorProviderDefinition } from '../src/features/providers/sponsorDefinitions';

describe('FennoAI provider aggregation', () => {
  test('does not claim OpenAI configs that its form cannot display', () => {
    const raw = buildFennoAIRaw({
      openaiCompatibility: [
        {
          name: FENNO_AI_PROVIDER_NAME,
          baseUrl: FENNO_AI_CODEX_BASE_URL,
          apiKeyEntries: [{ apiKey: 'openai-key' }],
        },
      ],
      codexApiKeys: [{ apiKey: 'codex-key', baseUrl: FENNO_AI_CODEX_BASE_URL }],
    });

    expect(getSponsorProviderDefinition('fennoAI').protocols).toEqual(['codex', 'claude']);
    expect(raw.openai).toEqual([]);
    expect(raw.codex.map((item) => item.index)).toEqual([0]);
  });
});
