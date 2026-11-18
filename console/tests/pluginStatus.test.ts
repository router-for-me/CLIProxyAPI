import { describe, expect, test } from 'bun:test';
import { readFileSync } from 'node:fs';
import {
  getPluginStatus,
  PLUGIN_STATUS_LABEL_KEY,
  PLUGIN_STATUS_TONE,
  type PluginRowStatus,
} from '../src/features/plugins/pluginStatus';

const entry = (enabled: boolean, registered: boolean, effectiveEnabled: boolean) => ({
  enabled,
  registered,
  effectiveEnabled,
});

describe('plugin row status', () => {
  test('an effective plugin is active', () => {
    expect(getPluginStatus(entry(true, true, true), true)).toBe('active');
  });

  test('effective_enabled wins over every other flag', () => {
    // The backend owns the effective formula; the UI never second-guesses it.
    expect(getPluginStatus(entry(false, false, true), false)).toBe('active');
  });

  test('a plugin switched off is disabled, whatever the global switch says', () => {
    expect(getPluginStatus(entry(false, true, false), true)).toBe('disabled');
    expect(getPluginStatus(entry(false, false, false), true)).toBe('disabled');
    expect(getPluginStatus(entry(false, true, false), false)).toBe('disabled');
  });

  test('an enabled plugin under a disabled global switch is paused', () => {
    expect(getPluginStatus(entry(true, false, false), false)).toBe('paused');
    expect(getPluginStatus(entry(true, true, false), false)).toBe('paused');
  });

  test('an enabled plugin missing from the runtime is not registered', () => {
    expect(getPluginStatus(entry(true, false, false), true)).toBe('notRegistered');
  });

  test('an enabled, registered plugin that is still not effective is inactive', () => {
    expect(getPluginStatus(entry(true, true, false), true)).toBe('inactive');
  });

  test('every status has a tone and a translated label in all locales', () => {
    const statuses: PluginRowStatus[] = [
      'active',
      'disabled',
      'paused',
      'notRegistered',
      'inactive',
      'applying',
      'removing',
    ];
    const locales = ['en', 'zh-CN', 'zh-TW', 'ru', 'vi', 'ko', 'tr'];

    for (const locale of locales) {
      const messages = JSON.parse(
        readFileSync(new URL(`../src/i18n/locales/${locale}.json`, import.meta.url), 'utf8')
      ) as { plugin_management: Record<string, unknown> };
      for (const status of statuses) {
        expect(PLUGIN_STATUS_TONE[status]).toBeDefined();
        const [, key] = PLUGIN_STATUS_LABEL_KEY[status].split('.');
        const label = messages.plugin_management[key];
        expect(typeof label === 'string' && label.trim().length > 0).toBe(true);
      }
    }
  });
});
