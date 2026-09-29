import { describe, expect, test } from 'bun:test';
import { buildProviderGroups } from '../src/features/providers/useProviderWorkbench';
import type { Config } from '../src/types';

const retiredEndpoints = [
  ['code0', 'https://code0.ai'],
  ['lmuAI', 'https://api.lmuai.com'],
  ['infistar', 'https://coneverse.com'],
  ['infistar', 'https://infistar.ai'],
  ['claudeApi', 'https://gw.apito.ai'],
  ['claudeApi', 'https://gw.claudeapi.com'],
] as const;

describe('removed quick-access providers', () => {
  test('removes retired brand groups', () => {
    const ids = buildProviderGroups({}).map((group) => group.id);
    for (const [brand] of retiredEndpoints) {
      expect(ids).not.toContain(brand);
    }
    for (const brand of ['apikeyFun', 'fennoAI', 'qiniuCloud', 'kimi']) {
      expect(ids).toContain(brand);
    }
  });

  for (const [name, baseUrl] of retiredEndpoints) {
    test(`keeps ${baseUrl} configs editable in generic protocol groups`, () => {
      const key = {
        apiKey: 'test-retired-key',
        baseUrl,
        excludedModels: ['*', 'excluded-model'],
        fingerprintProfile: 'claude-code-cli',
      };
      const openai = {
        name,
        baseUrl: `${baseUrl}/v1`,
        apiKeyEntries: [{ apiKey: 'test-retired-key' }],
        sourceIndex: 7,
        disabled: true,
      };
      const config: Config = {
        geminiApiKeys: [key],
        codexApiKeys: [key],
        claudeApiKeys: [key],
        openaiCompatibility: [openai],
      };
      const groups = buildProviderGroups(config);
      for (const brand of ['gemini', 'codex', 'claude', 'openaiCompatibility']) {
        const resources = groups.find((group) => group.id === brand)!.resources;
        expect(resources).toHaveLength(1);
        const resource = resources[0];
        expect(resource.brand).toBe(brand);
        expect(resource.disabled).toBe(true);
        expect(resource.raw).toBe(brand === 'openaiCompatibility' ? openai : key);
        expect(resource.selector).toEqual(
          brand === 'openaiCompatibility'
            ? { brand, name, index: 7 }
            : { brand, apiKey: key.apiKey, baseUrl, index: 0 }
        );
        if (brand === 'claude') expect(resource.flags.claudeCodeCliProfile).toBe(true);
      }
      expect(groups.flatMap((group) => group.resources)).toHaveLength(4);
    });
  }
});
