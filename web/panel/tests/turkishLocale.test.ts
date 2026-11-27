import { describe, expect, test } from 'bun:test';
import { createInstance } from 'i18next';
import en from '@/i18n/locales/en.json';
import tr from '@/i18n/locales/tr.json';
import { LANGUAGE_LABEL_KEYS, LANGUAGE_ORDER } from '@/utils/constants';
import { isSupportedLanguage } from '@/utils/language';

// Key and interpolation parity is covered for every locale by localeParity.test.ts.
const ratios = [
  ['excluded_models.footer_count', 'excluded'],
  ['providersPage.header.activeResources', 'active'],
  ['providersPage.categories.activeCount', 'active'],
  ['providersPage.discovery.selectedCount', 'selected'],
  ['providersPage.toolbar.filter.selectedModels', 'selected'],
  ['claude_reset.count', 'left'],
] as const;

const translator = createInstance();
await translator.init({ lng: 'tr', fallbackLng: false, resources: { tr: { translation: tr } } });

describe('Turkish locale', () => {
  test.each(ratios)('%s shows the part before the total, including zero remaining', (key, part) => {
    expect(translator.t(key, { [part]: 2, total: 10 })).toMatch(/^2\s*\/\s*10(?:\s|$)/);
    expect(translator.t(key, { [part]: 0, total: 3 })).toMatch(/^0\s*\/\s*3(?:\s|$)/);
  });

  test('preserves a valid object in the raw JSON configuration example', () => {
    const hint = tr.config_management.visual.payload_rules.value_raw_json;
    const example = hint.match(/\{"type":"object"\}/)?.[0];
    expect(example).toBeDefined();
    expect(JSON.parse(example!)).toEqual({ type: 'object' });
  });

  test('uses the current global default for Vertex import', () => {
    expect(tr.vertex_import.location_placeholder).toBe(en.vertex_import.location_placeholder);
    expect(tr.vertex_import.location_hint).toContain('global');
    expect(tr.vertex_import.location_hint).not.toContain('us-central1');
  });

  test('registers Turkish as a selectable language with a translated menu label', () => {
    expect(isSupportedLanguage('tr')).toBe(true);
    expect(LANGUAGE_ORDER).toContain('tr');
    expect(translator.t(LANGUAGE_LABEL_KEYS.tr)).toBe('Türkçe');
  });
});
