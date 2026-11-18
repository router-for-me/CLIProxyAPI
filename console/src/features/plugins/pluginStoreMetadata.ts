import type { PluginStoreEntry, PluginStoreLocale } from '@/types';

// Bare `zh` in registry data means Simplified in practice and must never supply
// Traditional; each language only walks same-script registry locales.
const STORE_LOCALE_CHAINS: Record<string, readonly string[]> = {
  'zh-CN': ['zh-CN', 'zh-Hans', 'zh'],
  'zh-TW': ['zh-TW', 'zh-HK', 'zh-Hant'],
  en: ['en'],
  ru: ['ru'],
  vi: ['vi'],
  ko: ['ko'],
  tr: ['tr'],
};

export type PluginStoreLocalizedFields = Pick<
  PluginStoreEntry,
  'name' | 'description' | 'author' | 'homepage' | 'license' | 'tags'
>;

const firstLocale = (entry: PluginStoreEntry, chain: readonly string[]) => {
  const locales = entry.locales;
  if (!locales) return undefined;
  // The backend canonicalizes keys, but tolerate `zh_cn`/`zh-cn` from other
  // registries or older hosts instead of silently falling back to English.
  const byKey = new Map(
    Object.entries(locales).map(
      ([key, locale]) => [key.trim().toLowerCase().replace(/_/g, '-'), locale] as const
    )
  );
  return chain.map((key) => byKey.get(key.toLowerCase())).find((locale) => locale !== undefined);
};

// One matched locale serves the whole entry; fields it omits fall back to the
// root values instead of borrowing a field from another locale. Unknown UI
// languages (or entries without locales) keep the root values unchanged.
export const getStoreEntryDisplay = (
  entry: PluginStoreEntry,
  language: string
): PluginStoreLocalizedFields => {
  const locale: PluginStoreLocale | undefined = firstLocale(
    entry,
    STORE_LOCALE_CHAINS[language] ?? []
  );
  const tags = locale?.tags?.map((tag) => tag.trim()).filter(Boolean);
  return {
    name: locale?.name?.trim() || entry.name,
    description: locale?.description?.trim() || entry.description,
    author: locale?.author?.trim() || entry.author,
    homepage: locale?.homepage?.trim() || entry.homepage,
    license: locale?.license?.trim() || entry.license,
    tags: tags?.length ? tags : entry.tags,
  };
};
