import { describe, expect, test } from 'bun:test';
import { readFileSync } from 'node:fs';
import {
  AI_CODE_MIRROR_BASE_URL_OPTIONS,
  buildAiCodeMirrorRaw,
  getAiCodeMirrorProtocolUrls,
  resolveAiCodeMirrorBaseUrl,
} from '../src/features/providers/aiCodeMirror';
import {
  getSponsorProviderDefinition,
  isMultiProtocolSponsorBrand,
} from '../src/features/providers/sponsorDefinitions';

const roots = ['https://api.aicodemirror.ai', 'https://api.claudecode.net.cn'];

describe('AIcodemirror sponsor', () => {
  test('registers four protocols and the documented referral link', () => {
    expect(isMultiProtocolSponsorBrand('aiCodeMirror')).toBe(true);
    const definition = getSponsorProviderDefinition('aiCodeMirror');
    expect(definition.displayName).toBe('AIcodemirror');
    expect([...definition.protocols].sort()).toEqual(['claude', 'codex', 'gemini', 'openai']);
    expect(definition.affiliateUrl).toBe('https://www.aicodemirror.ai/register?invitecode=YNE3NQ');
    expect(definition.supportsUsageCheck).toBe(false);
    expect(definition.baseUrlOptions.map((option) => option.baseUrl)).toEqual(roots);
    expect(resolveAiCodeMirrorBaseUrl(undefined)).toBe(roots[0]);
  });

  for (const root of roots) {
    test(`maps and normalizes all protocol URLs for ${root}`, () => {
      expect(getAiCodeMirrorProtocolUrls(`${root}/v1/`)).toEqual({
        openai: `${root}/v1`,
        codex: `${root}/v1`,
        anthropic: root,
        gemini: root,
      });
      expect(resolveAiCodeMirrorBaseUrl(` ${root.toUpperCase()}/ `)).toBe(root);
    });

    test(`aggregates all protocols without claiming other endpoints for ${root}`, () => {
      const unrelated = { apiKey: 'test-unrelated', baseUrl: 'https://example.com' };
      const raw = buildAiCodeMirrorRaw({
        openaiCompatibility: [
          { name: 'aiCodeMirror', baseUrl: 'https://example.com/v1', apiKeyEntries: [] },
          { name: 'custom-name', baseUrl: `${root}/v1`, sourceIndex: 7, apiKeyEntries: [] },
          { name: 'aiCodeMirror', baseUrl: `${root}.example.com/v1`, apiKeyEntries: [] },
        ],
        claudeApiKeys: [unrelated, { apiKey: 'test-claude', baseUrl: root }],
        codexApiKeys: [unrelated, { apiKey: 'test-codex', baseUrl: `${root}/v1` }],
        geminiApiKeys: [unrelated, { apiKey: 'test-gemini', baseUrl: root }],
      });
      expect(raw.openai.map((item) => item.index)).toEqual([7]);
      expect(raw.claude.map((item) => item.index)).toEqual([1]);
      expect(raw.codex.map((item) => item.index)).toEqual([1]);
      expect(raw.gemini.map((item) => item.index)).toEqual([1]);
    });
  }

  test('handles missing configuration', () => {
    expect(buildAiCodeMirrorRaw(null)).toEqual({ openai: [], claude: [], codex: [], gemini: [] });
  });

  test('provides name and route translations in every locale', () => {
    for (const locale of ['en', 'zh-CN', 'zh-TW', 'ru', 'vi', 'ko', 'tr']) {
      const messages = JSON.parse(readFileSync(`src/i18n/locales/${locale}.json`, 'utf8'));
      expect(messages.providersPage.providerNames.aiCodeMirror).toBe('AIcodemirror');
      for (const option of AI_CODE_MIRROR_BASE_URL_OPTIONS) {
        expect(messages.providersPage.sponsor.urlOptions[option.id]).toBeTruthy();
        expect(
          messages.providersPage.sponsor.urlOptionDescriptions[option.descriptionKey]
        ).toBeTruthy();
      }
    }
  });
});
