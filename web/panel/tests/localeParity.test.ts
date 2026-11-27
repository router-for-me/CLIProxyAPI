import { describe, expect, test } from 'bun:test';
import { LANGUAGE_ORDER } from '@/utils/constants';

type Leaves = Record<string, string>;

const PLURAL_SUFFIX = /_(zero|one|two|few|many|other)$/;

const flattenLeaves = (value: unknown, prefix = ''): Leaves => {
  if (!value || typeof value !== 'object' || Array.isArray(value)) {
    return prefix ? { [prefix]: String(value ?? '') } : {};
  }
  return Object.entries(value).reduce<Leaves>((leaves, [key, child]) => {
    const path = prefix ? `${prefix}.${key}` : key;
    return { ...leaves, ...flattenLeaves(child, path) };
  }, {});
};

const loadLeaves = async (locale: string) =>
  flattenLeaves(await Bun.file(`src/i18n/locales/${locale}.json`).json());

/** Groups plural variants (`key_one`, `key_few`, …) with their bare key, as i18next resolves them. */
const groupByBaseKey = (leaves: Leaves) => {
  const groups = new Map<string, Map<string, string>>();
  for (const [key, value] of Object.entries(leaves)) {
    const suffix = key.match(PLURAL_SUFFIX)?.[1] ?? '';
    const base = suffix ? key.slice(0, -suffix.length - 1) : key;
    const forms = groups.get(base) ?? new Map<string, string>();
    forms.set(suffix, value);
    groups.set(base, forms);
  }
  return groups;
};

const interpolationTokens = (values: Iterable<string>) =>
  [...new Set([...values].flatMap((value) => value.match(/\{\{[^}]+\}\}/g) ?? []))]
    .sort()
    .join('|');

const english = groupByBaseKey(await loadLeaves('en'));
const locales = LANGUAGE_ORDER.filter((locale) => locale !== 'en');

describe('locale parity with English', () => {
  test('every registered language has a locale file', async () => {
    expect(LANGUAGE_ORDER).toContain('en');
    for (const locale of LANGUAGE_ORDER) {
      expect(await Bun.file(`src/i18n/locales/${locale}.json`).exists()).toBe(true);
    }
  });

  test('English has no blank messages', () => {
    const blank = [...english].filter(([, forms]) => [...forms.values()].some((v) => !v.trim()));
    expect(blank.map(([key]) => key)).toEqual([]);
  });

  for (const locale of locales) {
    test(`${locale}: same keys, nonempty text, matching interpolation and plural forms`, async () => {
      const translated = groupByBaseKey(await loadLeaves(locale));
      const categories = new Intl.PluralRules(locale).resolvedOptions().pluralCategories;

      expect([...translated.keys()].filter((key) => !english.has(key))).toEqual([]);
      expect([...english.keys()].filter((key) => !translated.has(key))).toEqual([]);

      const blank: string[] = [];
      const tokenMismatches: string[] = [];
      const incompletePlurals: string[] = [];
      for (const [key, enForms] of english) {
        const forms = translated.get(key);
        if (!forms) continue;
        if ([...forms.values()].some((value) => !value.trim())) blank.push(key);
        if (interpolationTokens(forms.values()) !== interpolationTokens(enForms.values())) {
          tokenMismatches.push(key);
        }
        // i18next falls back to the bare key, so plural messages need either it or every
        // category the language's plural rules can select.
        const isPlural = [...enForms.keys()].some(Boolean);
        if (isPlural && !forms.has('') && categories.some((c) => !forms.has(c))) {
          incompletePlurals.push(key);
        }
      }
      expect(blank).toEqual([]);
      expect(tokenMismatches).toEqual([]);
      expect(incompletePlurals).toEqual([]);
    });
  }
});
