import { afterEach, describe, expect, test } from 'bun:test';
import {
  buildCodexResponsesEndpoint,
  buildOpenAIChatCompletionsEndpoint,
} from '../src/components/providers/utils';
import {
  KIMI_ANTHROPIC_BASE_URL,
  KIMI_CHINESE_AFFILIATE_URL,
  KIMI_DOMESTIC_ANTHROPIC_BASE_URL,
  KIMI_DOMESTIC_BASE_URL,
  KIMI_DOMESTIC_OPENAI_BASE_URL,
  KIMI_INTERNATIONAL_AFFILIATE_URL,
  KIMI_LEGACY_OPENAI_BASE_URL,
  KIMI_OPENAI_BASE_URL,
  buildKimiRaw,
  getKimiAffiliateUrl,
  getKimiProtocolUrls,
  isKimiClaudeProvider,
  isKimiCodexProvider,
  isKimiOpenAIProvider,
  resolveKimiBaseUrl,
} from '../src/features/providers/kimi';
import { PROVIDER_LOGOS } from '../src/features/providers/brandLogos';
import { PROVIDER_BRAND_ORDER } from '../src/features/providers/descriptors';
import { getSponsorProviderDefinition } from '../src/features/providers/sponsorDefinitions';
import { apiCallApi } from '../src/services/api/apiCall';
import { modelsApi } from '../src/services/api/models';

const originalApiCallRequest = apiCallApi.request;

afterEach(() => {
  apiCallApi.request = originalApiCallRequest;
});

describe('Kimi provider', () => {
  test('defaults to the domestic OpenAI-compatible, Claude, and Responses endpoints', () => {
    expect(getKimiProtocolUrls(undefined)).toEqual({
      openai: 'https://api.moonshot.cn/v1',
      anthropic: 'https://api.moonshot.cn/anthropic',
      codex: 'https://api.moonshot.cn/v1',
      gemini: '',
    });
    expect(getKimiProtocolUrls(KIMI_OPENAI_BASE_URL)).toEqual({
      openai: 'https://api.moonshot.ai/v1',
      anthropic: 'https://api.moonshot.ai/anthropic',
      codex: 'https://api.moonshot.ai/v1',
      gemini: '',
    });
    expect(buildOpenAIChatCompletionsEndpoint(KIMI_OPENAI_BASE_URL)).toBe(
      'https://api.moonshot.ai/v1/chat/completions'
    );
    expect(buildCodexResponsesEndpoint(KIMI_OPENAI_BASE_URL)).toBe(
      'https://api.moonshot.ai/v1/responses'
    );
    expect(getSponsorProviderDefinition('kimi').protocols).toEqual([
      'openai',
      'claude',
      'codex',
    ]);
  });

  test('offers overseas and domestic URLs and maps the domestic protocol endpoints', () => {
    expect(
      getSponsorProviderDefinition('kimi').baseUrlOptions.map(({ id, baseUrl }) => ({
        id,
        baseUrl,
      }))
    ).toEqual([
      { id: 'domestic', baseUrl: KIMI_DOMESTIC_OPENAI_BASE_URL },
      { id: 'overseas', baseUrl: KIMI_OPENAI_BASE_URL },
    ]);
    expect(resolveKimiBaseUrl(undefined)).toBe(KIMI_DOMESTIC_OPENAI_BASE_URL);
    expect(resolveKimiBaseUrl(KIMI_DOMESTIC_BASE_URL)).toBe(KIMI_DOMESTIC_OPENAI_BASE_URL);
    expect(resolveKimiBaseUrl(KIMI_LEGACY_OPENAI_BASE_URL)).toBe(KIMI_OPENAI_BASE_URL);
    expect(resolveKimiBaseUrl(KIMI_DOMESTIC_ANTHROPIC_BASE_URL)).toBe(
      KIMI_DOMESTIC_OPENAI_BASE_URL
    );
    expect(getKimiProtocolUrls(KIMI_DOMESTIC_OPENAI_BASE_URL)).toEqual({
      openai: 'https://api.moonshot.cn/v1',
      anthropic: 'https://api.moonshot.cn/anthropic',
      codex: 'https://api.moonshot.cn/v1',
      gemini: '',
    });
  });

  test('discovers models through the versioned OpenAI endpoint', async () => {
    let requestedUrl = '';
    apiCallApi.request = (async (payload) => {
      requestedUrl = payload.url;
      return { statusCode: 200, header: {}, bodyText: '', body: { data: [] } };
    }) as typeof apiCallApi.request;

    await modelsApi.fetchModelsViaApiCall(KIMI_OPENAI_BASE_URL, 'test-key');

    expect(requestedUrl).toBe('https://api.moonshot.ai/v1/models');
  });

  test('uses the domestic registration link for Chinese and the international link otherwise', () => {
    expect(getKimiAffiliateUrl('zh-CN')).toBe(KIMI_CHINESE_AFFILIATE_URL);
    expect(getKimiAffiliateUrl('zh-TW')).toBe(KIMI_CHINESE_AFFILIATE_URL);
    expect(getKimiAffiliateUrl('en')).toBe(KIMI_INTERNATIONAL_AFFILIATE_URL);
    expect(getKimiAffiliateUrl('ru')).toBe(KIMI_INTERNATIONAL_AFFILIATE_URL);
  });

  test('uses the OAuth-style theme surface for its provider icon', () => {
    expect(PROVIDER_LOGOS.kimi.themeSurface).toBeTrue();
  });

  test('is the first provider in the catalog', () => {
    expect(PROVIDER_BRAND_ORDER[0]).toBe('kimi');
  });

  test('recognizes Kimi configs only by supported protocol endpoint', () => {
    expect(
      isKimiOpenAIProvider({
        name: 'Kimi',
        baseUrl: 'https://custom.example.com',
      })
    ).toBeFalse();
    expect(
      isKimiOpenAIProvider({
        name: 'moonshot',
        baseUrl: `${KIMI_OPENAI_BASE_URL}/`,
      })
    ).toBeTrue();
    expect(
      isKimiOpenAIProvider({
        name: 'legacy-moonshot',
        baseUrl: KIMI_LEGACY_OPENAI_BASE_URL,
      })
    ).toBeTrue();
    expect(
      isKimiOpenAIProvider({
        name: 'domestic-moonshot',
        baseUrl: KIMI_DOMESTIC_OPENAI_BASE_URL,
      })
    ).toBeTrue();
    expect(
      isKimiClaudeProvider({ apiKey: 'sk-test', baseUrl: KIMI_ANTHROPIC_BASE_URL })
    ).toBeTrue();
    expect(
      isKimiClaudeProvider({ apiKey: 'sk-test', baseUrl: KIMI_DOMESTIC_ANTHROPIC_BASE_URL })
    ).toBeTrue();
    expect(isKimiCodexProvider({ apiKey: 'sk-test', baseUrl: KIMI_OPENAI_BASE_URL })).toBeTrue();
    expect(
      isKimiCodexProvider({ apiKey: 'sk-test', baseUrl: KIMI_DOMESTIC_OPENAI_BASE_URL })
    ).toBeTrue();
  });

  test('aggregates only the Kimi OpenAI-compatible, Claude, and Responses configs', () => {
    const raw = buildKimiRaw({
      openaiCompatibility: [
        { name: 'kimi', baseUrl: KIMI_OPENAI_BASE_URL },
        { name: 'other', baseUrl: 'https://example.com' },
      ],
      claudeApiKeys: [
        { apiKey: 'kimi-key', baseUrl: KIMI_ANTHROPIC_BASE_URL },
        { apiKey: 'other-key', baseUrl: 'https://api.anthropic.com' },
      ],
      codexApiKeys: [
        { apiKey: 'kimi-response-key', baseUrl: KIMI_OPENAI_BASE_URL },
        { apiKey: 'other-response-key', baseUrl: 'https://example.com/v1' },
      ],
    });

    expect(raw.openai.map((item) => item.index)).toEqual([0]);
    expect(raw.claude.map((item) => item.index)).toEqual([0]);
    expect(raw.codex.map((item) => item.index)).toEqual([0]);
    expect(raw.gemini).toEqual([]);
  });
});
