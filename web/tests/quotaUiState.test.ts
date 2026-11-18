/**
 * Session-scoped quota page preferences.
 *
 * The merge-on-write case is the one that matters: every control writes a single
 * field and knows nothing about the others, so a whole-object write would make
 * changing a tab silently reset the sort or the pool view.
 */

import { afterAll, beforeEach, describe, expect, test } from 'bun:test';
import { readQuotaUiState, writeQuotaUiState } from '@/features/quota/uiState';

const KEY = 'quotaPage.uiState';

const EMPTY_STATE = {
  accounts: undefined,
  burnWindow: undefined,
  futureUsage: undefined,
  metric: undefined,
  sortMode: undefined,
  tab: undefined,
};

/** Test files share one process, so a fake `window` left behind would leak. */
const originalWindow = (globalThis as { window?: unknown }).window;

/** bun's global has no sessionStorage; a Map-backed stub is enough here. */
const installSessionStorage = () => {
  const store = new Map<string, string>();
  const storage = {
    clear: () => store.clear(),
    getItem: (key: string) => store.get(key) ?? null,
    key: (index: number) => [...store.keys()][index] ?? null,
    get length() {
      return store.size;
    },
    removeItem: (key: string) => void store.delete(key),
    setItem: (key: string, value: string) => void store.set(key, value),
  };

  (globalThis as unknown as { window: unknown }).window = { sessionStorage: storage };

  return storage;
};

let storage: ReturnType<typeof installSessionStorage>;

beforeEach(() => {
  storage = installSessionStorage();
});

afterAll(() => {
  if (originalWindow === undefined) {
    delete (globalThis as { window?: unknown }).window;
  } else {
    (globalThis as { window?: unknown }).window = originalWindow;
  }
});

describe('quota ui state', () => {
  test('round-trips every preference', () => {
    const state = {
      accounts: true,
      burnWindow: 6,
      futureUsage: 'half',
      metric: 'drain',
      sortMode: 'soonest',
      tab: 'codex',
    } as const;

    writeQuotaUiState(state);

    expect(readQuotaUiState()).toEqual(state);
  });

  test('round-trips false booleans', () => {
    writeQuotaUiState({ accounts: false });

    expect(readQuotaUiState()).toEqual({ ...EMPTY_STATE, accounts: false });
  });

  test('retains the Devin tab across visits', () => {
    writeQuotaUiState({ sortMode: 'soonest', tab: 'devin' });

    expect(readQuotaUiState()).toMatchObject({ sortMode: 'soonest', tab: 'devin' });
  });

  test('writing one preference preserves the others', () => {
    writeQuotaUiState({ sortMode: 'soonest' });
    writeQuotaUiState({ burnWindow: 1 });
    writeQuotaUiState({ tab: 'kimi' });

    expect(readQuotaUiState()).toEqual({
      ...EMPTY_STATE,
      burnWindow: 1,
      sortMode: 'soonest',
      tab: 'kimi',
    });
  });

  test('rejects values that are not part of the current contract', () => {
    storage.setItem(
      KEY,
      JSON.stringify({
        accounts: 'yes',
        futureUsage: 'triple',
        metric: 'grid',
        sortMode: 'by-vibes',
        tab: 'not-a-tab',
      })
    );

    expect(readQuotaUiState()).toEqual(EMPTY_STATE);
  });

  test('accepts only the numeric burn windows 1, 3 and 6', () => {
    for (const burnWindow of [1, 3, 6]) {
      storage.setItem(KEY, JSON.stringify({ burnWindow }));

      expect(readQuotaUiState()?.burnWindow).toBe(burnWindow);
    }

    for (const burnWindow of [2, '3', null, 0]) {
      storage.setItem(KEY, JSON.stringify({ burnWindow }));

      expect(readQuotaUiState()?.burnWindow).toBeUndefined();
    }
  });

  test('ignores a stored view mode from the removed ledger view', () => {
    storage.setItem(KEY, JSON.stringify({ tab: 'claude', viewMode: 'cards' }));

    expect(readQuotaUiState()).toEqual({ ...EMPTY_STATE, tab: 'claude' });

    writeQuotaUiState({ metric: 'remaining' });

    expect(storage.getItem(KEY)).not.toContain('viewMode');
  });

  test('survives absent, malformed, and non-object payloads', () => {
    expect(readQuotaUiState()).toBeNull();

    storage.setItem(KEY, '{not json');

    expect(readQuotaUiState()).toBeNull();

    storage.setItem(KEY, '"a string"');

    expect(readQuotaUiState()).toBeNull();

    storage.setItem(KEY, 'null');

    expect(readQuotaUiState()).toBeNull();
  });
});
