import { normalizeProviderKey } from './constants';

type ModelAliasDraftEntry = {
  id?: string;
  name?: string;
  alias?: string;
  fork?: boolean;
  forceMapping?: boolean;
};

export const getStringSetSignature = (values: Iterable<string>): string =>
  JSON.stringify(
    Array.from(new Set(values), (value) => value.trim())
      .filter(Boolean)
      .sort()
  );

export const getModelAliasDraftSignature = (entries: ModelAliasDraftEntry[]): string =>
  JSON.stringify(
    entries
      .map((entry) => ({
        name: entry.name ?? '',
        alias: entry.alias ?? '',
        fork: entry.fork === true,
        forceMapping: typeof entry.forceMapping === 'boolean' ? entry.forceMapping : undefined,
      }))
      .filter(
        (entry) =>
          entry.name !== '' ||
          entry.alias !== '' ||
          entry.fork !== true ||
          entry.forceMapping !== undefined
      )
  );

export const isOAuthEditorDirty = (
  initialProvider: string,
  currentProvider: string,
  baselineContentSignature: string,
  currentContentSignature: string
): boolean =>
  normalizeProviderKey(initialProvider) !== normalizeProviderKey(currentProvider) ||
  baselineContentSignature !== currentContentSignature;
