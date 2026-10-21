import { describe, expect, test } from 'bun:test';
import { readFileSync } from 'node:fs';
import { providersApi } from '../src/services/api/providers';

describe('provider workbench editing surface', () => {
  test('keeps normal credential editing without a raw group JSON editor', () => {
    const source = readFileSync(
      new URL('../src/features/providers/ProvidersWorkbenchPage.tsx', import.meta.url),
      'utf8'
    );
    expect(source).not.toContain('ProviderGroupsEditor');
    expect(source).toContain('<ProviderSheet');
    expect(providersApi).not.toHaveProperty('replaceGroups');
    expect(providersApi.updateCodexConfig).toBeFunction();
  });

  test('omits the group explanation from provider forms and locales', () => {
    const source = readFileSync(
      new URL('../src/features/providers/sheets/ProviderSheet.tsx', import.meta.url),
      'utf8'
    );
    expect(source).not.toContain('providersPage.groups.rowHint');
    for (const locale of ['en', 'zh-CN', 'zh-TW', 'ru']) {
      const messages = JSON.parse(
        readFileSync(new URL(`../src/i18n/locales/${locale}.json`, import.meta.url), 'utf8')
      );
      expect(messages.providersPage.groups).toBeUndefined();
    }
  });
});
