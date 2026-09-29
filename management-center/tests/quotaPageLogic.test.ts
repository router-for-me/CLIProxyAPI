import { describe, expect, test } from 'bun:test';
import { QUOTA_PAGE_SIZE } from '@/features/quota/constants';
import {
  buildTabCounts,
  canRefreshQuotaAfterList,
  classifyQuotaFiles,
  filterEntriesByTab,
  filterEntriesBySearch,
  isQuotaRefreshDisabled,
  paginate,
  resolveQuotaProviderType,
  sortQuotaEntries,
  type QuotaFileEntry,
} from '@/features/quota/logic';
import type { AuthFileItem } from '@/types';

const file = (name: string, provider: string, extra: Partial<AuthFileItem> = {}): AuthFileItem =>
  ({ name, provider, ...extra }) as AuthFileItem;

const FILES: AuthFileItem[] = [
  file('codex-a.json', 'codex'),
  file('claude-a.json', 'claude'),
  file('kimi-a.json', 'kimi'),
  file('codex-b.json', 'codex'),
  file('grok-a.json', 'grok'), // 别名归一到 xai
  file('gemini-a.json', 'gemini'), // 不支持额度
  file('claude-off.json', 'claude', { disabled: true }), // 停用
];

describe('refresh-all list handoff', () => {
  test('requires a successful list from the same session as the user action', () => {
    expect(canRefreshQuotaAfterList(1, 1, 1, false, false)).toBe(true);
    expect(canRefreshQuotaAfterList(1, 1, 1, true, false)).toBe(false);
    expect(canRefreshQuotaAfterList(1, 2, 2, false, false)).toBe(false);
    expect(canRefreshQuotaAfterList(2, 2, 1, false, false)).toBe(false);
    expect(canRefreshQuotaAfterList(1, 1, null, false, false)).toBe(false);
    expect(canRefreshQuotaAfterList(1, 1, 1, false, true)).toBe(false);
  });
});

describe('resolveQuotaProviderType', () => {
  test('maps provider aliases and rejects unsupported or disabled files', () => {
    expect(resolveQuotaProviderType(file('a', 'grok'))).toBe('xai');
    expect(resolveQuotaProviderType(file('a', 'antigravity'))).toBe('antigravity');
    expect(resolveQuotaProviderType(file('a', 'gemini'))).toBeNull();
    expect(resolveQuotaProviderType(file('a', 'claude', { disabled: true }))).toBeNull();
  });
});

describe('classifyQuotaFiles', () => {
  test('drops unsupported and disabled files', () => {
    const entries = classifyQuotaFiles(FILES);
    expect(entries.map((entry) => entry.file.name)).not.toContain('gemini-a.json');
    expect(entries.map((entry) => entry.file.name)).not.toContain('claude-off.json');
    expect(entries).toHaveLength(5);
  });

  test('orders entries by provider tab order', () => {
    const entries = classifyQuotaFiles(FILES);
    expect(entries.map((entry) => entry.type)).toEqual(['claude', 'codex', 'codex', 'xai', 'kimi']);
  });
});

describe('buildTabCounts', () => {
  test('counts per provider plus an all total, zero-filling empty tabs', () => {
    expect(buildTabCounts(classifyQuotaFiles(FILES))).toEqual({
      all: 5,
      claude: 1,
      antigravity: 0,
      codex: 2,
      xai: 1,
      kimi: 1,
      devin: 0,
      meta: 0,
    });
  });
});

describe('filterEntriesByTab', () => {
  const entries = classifyQuotaFiles(FILES);

  test("passes everything through on the 'all' tab", () => {
    expect(filterEntriesByTab(entries, 'all')).toHaveLength(5);
  });

  test('filters to a single provider', () => {
    expect(filterEntriesByTab(entries, 'codex').map((entry) => entry.file.name)).toEqual([
      'codex-a.json',
      'codex-b.json',
    ]);
    expect(filterEntriesByTab(entries, 'antigravity')).toEqual([]);
  });
});

describe('filterEntriesBySearch', () => {
  const entries = classifyQuotaFiles([
    ...FILES,
    file('personal.json', 'codex', { email: 'Alice@Example.com' }),
    file('work.json', 'claude', { email: 'Alice@Example.com' }),
    file('private.json', 'codex', { account: 'secret-api-key' }),
  ]);

  test('ignores case and surrounding whitespace when matching filenames or emails', () => {
    expect(filterEntriesBySearch(entries, ' CODEX-A ').map(({ file }) => file.name)).toEqual([
      'codex-a.json',
    ]);
    expect(filterEntriesBySearch(entries, ' ALICE@example ').map(({ file }) => file.name)).toEqual([
      'work.json',
      'personal.json',
    ]);
  });

  test('keeps all entries for empty searches and returns none for missing accounts', () => {
    expect(filterEntriesBySearch(entries, '')).toBe(entries);
    expect(filterEntriesBySearch(entries, '   ')).toBe(entries);
    expect(filterEntriesBySearch(entries, 'missing')).toEqual([]);
    expect(filterEntriesBySearch(entries, 'secret-api-key')).toEqual([]);
  });

  test('combines with provider tabs without changing the original entries', () => {
    const before = [...entries];
    const matches = filterEntriesBySearch(filterEntriesByTab(entries, 'codex'), 'alice');
    expect(matches.map(({ file }) => file.name)).toEqual(['personal.json']);
    expect(entries).toEqual(before);
  });

  test('filters before pagination so refresh targets include matches beyond the first page', () => {
    const all = classifyQuotaFiles(
      Array.from({ length: QUOTA_PAGE_SIZE + 2 }, (_, index) =>
        file(`codex-${index}.json`, 'codex', {
          email: index >= QUOTA_PAGE_SIZE ? 'target@example.com' : 'other@example.com',
        })
      )
    );
    const filtered = filterEntriesBySearch(filterEntriesByTab(all, 'codex'), 'target@');
    const sorted = sortQuotaEntries(filtered, 'default', () => null);
    const { pageItems, totalPages } = paginate(sorted, 1, QUOTA_PAGE_SIZE);
    expect(pageItems.map(({ file }) => file.name)).toEqual(['codex-20.json', 'codex-21.json']);
    expect(totalPages).toBe(1);
    expect(paginate(filterEntriesBySearch(all, 'missing'), 1, QUOTA_PAGE_SIZE).pageItems).toEqual(
      []
    );
  });
});

describe('isQuotaRefreshDisabled', () => {
  test('blocks a single-card refresh while the same quota is resetting', () => {
    expect(isQuotaRefreshDisabled(true, false, true)).toBe(true);
    expect(isQuotaRefreshDisabled(true, false, false)).toBe(false);
  });
});

describe('paginate', () => {
  const items = Array.from({ length: 45 }, (_, index) => index);

  test('uses the configured 20-item page size', () => {
    expect(QUOTA_PAGE_SIZE).toBe(20);
    expect(paginate(items, 2, QUOTA_PAGE_SIZE)).toEqual({
      pageItems: items.slice(20, 40),
      currentPage: 2,
      totalPages: 3,
    });
  });

  test('clamps an out-of-range page instead of returning an empty slice', () => {
    expect(paginate(items, 9, QUOTA_PAGE_SIZE).currentPage).toBe(3);
    expect(paginate(items, 9, QUOTA_PAGE_SIZE).pageItems).toEqual(items.slice(40));
    expect(paginate(items, 0, QUOTA_PAGE_SIZE).currentPage).toBe(1);
  });

  test('keeps at least one page when the list is empty', () => {
    expect(paginate([], 1, QUOTA_PAGE_SIZE)).toEqual({
      pageItems: [],
      currentPage: 1,
      totalPages: 1,
    });
  });
});

describe('sortQuotaEntries', () => {
  const entries = classifyQuotaFiles(FILES);
  const byName = (list: QuotaFileEntry[]) => list.map((entry) => entry.file.name);

  /** Recovery instants keyed by file name; anything absent resolves to null. */
  const resolver = (instants: Record<string, number>) => (entry: QuotaFileEntry) =>
    instants[entry.file.name] ?? null;

  test('default mode preserves order but returns a new array', () => {
    const sorted = sortQuotaEntries(entries, 'default', () => 1);
    expect(byName(sorted)).toEqual(byName(entries));
    expect(sorted).not.toBe(entries);
  });

  test('orders loaded credentials by how soon they recover, across providers', () => {
    const sorted = sortQuotaEntries(
      entries,
      'soonest',
      resolver({
        'codex-a.json': 300,
        'claude-a.json': 100,
        'kimi-a.json': 200,
        'codex-b.json': 400,
        'grok-a.json': 50,
      })
    );
    expect(byName(sorted)).toEqual([
      'grok-a.json',
      'claude-a.json',
      'kimi-a.json',
      'codex-a.json',
      'codex-b.json',
    ]);
  });

  test('sinks credentials with no instant, keeping their provider-grouped order', () => {
    // Loading is click-to-fetch, so an unloaded tail is the normal case.
    const sorted = sortQuotaEntries(
      entries,
      'soonest',
      resolver({ 'codex-b.json': 200, 'kimi-a.json': 100 })
    );
    expect(byName(sorted)).toEqual([
      'kimi-a.json',
      'codex-b.json',
      // unresolved tail, in the order classifyQuotaFiles produced
      'claude-a.json',
      'codex-a.json',
      'grok-a.json',
    ]);
  });

  test('leaves the order untouched when nothing has loaded', () => {
    expect(byName(sortQuotaEntries(entries, 'soonest', () => null))).toEqual(byName(entries));
  });

  test('breaks ties on the original position, so equal instants stay stable', () => {
    const sorted = sortQuotaEntries(entries, 'soonest', () => 500);
    expect(byName(sorted)).toEqual(byName(entries));
  });

  test('does not mutate the input', () => {
    const input = [...entries];
    sortQuotaEntries(input, 'soonest', resolver({ 'codex-b.json': 1 }));
    expect(input).toEqual(entries);
  });

  test('sorts before paginating, so the globally soonest lands on page one', () => {
    // Last in the default order, first to recover.
    const last = entries[entries.length - 1].file.name;
    const sorted = sortQuotaEntries(entries, 'soonest', resolver({ [last]: 1 }));
    expect(paginate(sorted, 1, 2).pageItems[0].file.name).toBe(last);
  });
});
