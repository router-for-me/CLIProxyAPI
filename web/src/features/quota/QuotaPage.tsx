/**
 * Quota page: provider tabs, the pooled Claude/Codex cards and the per-credential card grid.
 *
 * Behaviour contracts kept through the redesign:
 * - on entry the quota of every credential on the current page is queried once (once per
 *   entry); nothing polls, and the quota history is fetched only on entry and on refresh;
 * - session isolation through cacheGeneration plus request-id dedupe (see useQuotaBatchLoader
 *   and usePoolHistory);
 * - when the file list changes, the quota cache is pruned per provider (deleted files leave
 *   nothing behind);
 * - useHeaderRefresh has a single slot: this page is its only registrant, and the global
 *   refresh re-reads the file list and the quota history.
 */

import { useCallback, useEffect, useMemo, useRef, useState } from 'react';
import { useTranslation } from 'react-i18next';
import { authFilesApi } from '@/services/api';
import { Button } from '@/components/ui/Button';
import { EmptyState } from '@/components/ui/EmptyState';
import { IconSearch, IconX } from '@/components/ui/icons';
import { Select } from '@/components/ui/Select';
import { Skeleton } from '@/components/ui/Skeleton';
import { useHeaderRefresh } from '@/hooks/useHeaderRefresh';
import { useNow } from '@/hooks/useNow';
import { useRevealGroup } from '@/hooks/motion';
import { useAuthStore, useQuotaStore, useThemeStore } from '@/stores';
import type { AuthFileItem, ResolvedTheme } from '@/types';
import { getQuotaCacheKey } from '@/utils/quota/identity';
import { ProviderTabs } from '@/features/authFiles/components/ProviderTabs';
import { PoolCard } from '@/features/quota/components/PoolCard';
import { PoolControls } from '@/features/quota/components/PoolControls';
import { PoolHistoryNotice } from '@/features/quota/components/PoolHistoryNotice';
import { usePoolHistory } from '@/features/quota/hooks/usePoolHistory';
import { historySinceMs, poolRange } from '@/features/quota/pool/calendar';
import {
  POOL_PROVIDERS,
  type BurnWindowHours,
  type FutureUsage,
  type PoolMetric,
  type PoolProvider,
} from '@/features/quota/pool/constants';
import { buildPoolCardModel, worstHistoryStatus } from '@/features/quota/pool/model';
import type { PoolCardModel } from '@/features/quota/pool/types';
import { QuotaHeader } from './components/QuotaHeader';
import { QuotaCard } from './components/QuotaCard';
import {
  CARD_ENTRANCE_BUDGET_MS,
  QUOTA_PAGE_SIZE,
  QUOTA_SORT_MODES,
  QUOTA_TAB_ORDER,
  type QuotaSortMode,
  type QuotaTabId,
} from './constants';
import {
  buildTabCounts,
  canRefreshQuotaAfterList,
  classifyQuotaFiles,
  filterEntriesByTab,
  filterEntriesBySearch,
  paginate,
  sortQuotaEntries,
  type QuotaFileEntry,
} from './logic';
import { nextRecoveryMs } from './resetSchedule';
import { QUOTA_ADAPTERS, getQuotaSetter, type QuotaCardState } from './providers';
import type { QuotaProviderType } from './providers/types';
import { useQuotaAutoLoad } from './hooks/useQuotaAutoLoad';
import { useQuotaActions } from './hooks/useQuotaActions';
import { useQuotaBatchLoader } from './hooks/useQuotaBatchLoader';
import { readQuotaUiState, writeQuotaUiState } from './uiState';
import styles from './QuotaPage.module.scss';

const TAB_IDS: string[] = ['all', ...QUOTA_TAB_ORDER];
const SKELETON_CARD_COUNT = 6;
const POOL_SKELETON_HEIGHT = 432;
const TIME_ZONE = Intl.DateTimeFormat().resolvedOptions().timeZone;

interface PoolPreferences {
  accounts: boolean;
  burnWindow: BurnWindowHours;
  futureUsage: FutureUsage;
  metric: PoolMetric;
}

/** A pool view preference that survives reloads within the browser session. */
const usePoolPreference = <Key extends keyof PoolPreferences>(
  key: Key,
  fallback: PoolPreferences[Key]
) => {
  const [value, setValue] = useState<PoolPreferences[Key]>(() => {
    const stored: Partial<PoolPreferences> | null = readQuotaUiState();

    return stored?.[key] ?? fallback;
  });
  const update = useCallback(
    (next: PoolPreferences[Key]) => {
      setValue(next);
      writeQuotaUiState({ [key]: next });
    },
    [key]
  );

  return [value, update] as const;
};

const getPoolTabs = (tab: QuotaTabId): PoolProvider[] =>
  POOL_PROVIDERS.filter((provider) => tab === 'all' || provider === tab);

const isPoolModel = (model: PoolCardModel | null): model is PoolCardModel => model !== null;

export function QuotaPage() {
  const { t, i18n } = useTranslation();
  const connectionStatus = useAuthStore((state) => state.connectionStatus);
  const resolvedTheme: ResolvedTheme = useThemeStore((state) => state.resolvedTheme);
  const locale = i18n.resolvedLanguage ?? 'en';

  const [files, setFiles] = useState<AuthFileItem[]>([]);
  const [filesLoadedAt, setFilesLoadedAt] = useState<number | null>(null);
  const [loading, setLoading] = useState(true);
  const [error, setError] = useState('');
  const [tab, setTab] = useState<QuotaTabId>(() => readQuotaUiState()?.tab ?? 'all');
  const [sortMode, setSortMode] = useState<QuotaSortMode>(
    () => readQuotaUiState()?.sortMode ?? 'default'
  );
  const [metric, setMetric] = usePoolPreference('metric', 'remaining');
  const [burnWindow, setBurnWindow] = usePoolPreference('burnWindow', 3);
  const [futureUsage, setFutureUsage] = usePoolPreference('futureUsage', 'same');
  const [accounts, setAccounts] = usePoolPreference('accounts', false);
  const [rangeOffset, setRangeOffset] = useState(0);
  const [historyToken, setHistoryToken] = useState(0);
  const [page, setPage] = useState(1);
  const [search, setSearch] = useState('');
  const searchInputRef = useRef<HTMLInputElement>(null);
  // Entrance cascade for the header and tabs (title → meta → actions → tabs, 70ms apart)
  const revealRef = useRevealGroup<HTMLDivElement>();

  const disableControls = connectionStatus !== 'connected';

  /* ---------- File list ---------- */

  const sessionGeneration = useQuotaStore((state) => state.cacheGeneration);
  const [filesGeneration, setFilesGeneration] = useState<number | null>(null);
  const listRequestRef = useRef(0);
  const loadFiles = useCallback(async () => {
    const requestId = ++listRequestRef.current;
    if (connectionStatus !== 'connected') {
      setFiles([]);
      setFilesGeneration(null);
      setLoading(false);
      return;
    }
    const isCurrent = () =>
      requestId === listRequestRef.current &&
      sessionGeneration === useQuotaStore.getState().cacheGeneration;
    setLoading(true);
    setError('');
    try {
      const data = await authFilesApi.list();
      if (!isCurrent()) return;
      setFiles(data?.files || []);
      setFilesGeneration(sessionGeneration);
      setFilesLoadedAt(Date.now());
    } catch (err: unknown) {
      if (!isCurrent()) return;
      const message = err instanceof Error ? err.message : t('notification.refresh_failed');
      setError(message);
    } finally {
      if (isCurrent()) setLoading(false);
    }
  }, [connectionStatus, sessionGeneration, t]);

  const refreshPage = useCallback(() => {
    setHistoryToken((token) => token + 1);

    return loadFiles();
  }, [loadFiles]);

  useHeaderRefresh(refreshPage);

  useEffect(() => {
    void loadFiles();

    return () => {
      listRequestRef.current += 1;
    };
  }, [loadFiles]);

  /* ---------- Quota cache ----------
   * Read before classifying and sorting: "soonest recovery first" needs it for the sort key. */

  const antigravityQuota = useQuotaStore((state) => state.antigravityQuota);
  const claudeQuota = useQuotaStore((state) => state.claudeQuota);
  const codexQuota = useQuotaStore((state) => state.codexQuota);
  const devinQuota = useQuotaStore((state) => state.devinQuota);
  const kimiQuota = useQuotaStore((state) => state.kimiQuota);
  const metaQuota = useQuotaStore((state) => state.metaQuota);
  const xaiQuota = useQuotaStore((state) => state.xaiQuota);

  const quotaByType = useMemo<Record<QuotaProviderType, Record<string, QuotaCardState>>>(
    () =>
      ({
        antigravity: antigravityQuota,
        claude: claudeQuota,
        codex: codexQuota,
        devin: devinQuota,
        kimi: kimiQuota,
        meta: metaQuota,
        xai: xaiQuota,
      }) as unknown as Record<QuotaProviderType, Record<string, QuotaCardState>>,
    [antigravityQuota, claudeQuota, codexQuota, devinQuota, kimiQuota, metaQuota, xaiQuota]
  );

  const getQuota = useCallback(
    (entry: QuotaFileEntry): QuotaCardState | undefined =>
      quotaByType[entry.type][getQuotaCacheKey(entry.file)],
    [quotaByType]
  );

  /* ---------- Classify / filter / sort / paginate ---------- */

  // The pools tick with the minute clock, but the sort reads it only for "soonest recovery
  // first". Otherwise pageItems would change identity every minute and keep re-running the
  // refresh-all falling-edge effect below.
  const now = useNow();
  const sortNow = sortMode === 'default' ? 0 : now;

  const entries = useMemo(() => classifyQuotaFiles(files), [files]);
  const tabCounts = useMemo(() => buildTabCounts(entries), [entries]);
  const filteredEntries = useMemo(
    () => filterEntriesBySearch(filterEntriesByTab(entries, tab), search),
    [entries, tab, search]
  );
  const handleSearchChange = useCallback((value: string) => {
    setSearch(value);
    setPage(1);
  }, []);

  const resolveNextRecovery = useCallback(
    (entry: QuotaFileEntry) => nextRecoveryMs(entry.type, getQuota(entry), sortNow),
    [getQuota, sortNow]
  );
  // Sort before paginating, or "soonest recovery" would only hold within the current page.
  const sortedEntries = useMemo(
    () => sortQuotaEntries(filteredEntries, sortMode, resolveNextRecovery),
    [filteredEntries, sortMode, resolveNextRecovery]
  );

  const { pageItems, currentPage, totalPages } = useMemo(
    () => paginate(sortedEntries, page, QUOTA_PAGE_SIZE),
    [sortedEntries, page]
  );

  const handleTabChange = useCallback((next: string) => {
    setTab(next as QuotaTabId);
    setPage(1);
    writeQuotaUiState({ tab: next as QuotaTabId });
  }, []);

  const handleSortModeChange = useCallback((next: string) => {
    setSortMode(next as QuotaSortMode);
    setPage(1);
    writeQuotaUiState({ sortMode: next as QuotaSortMode });
  }, []);

  const sortOptions = useMemo(
    () =>
      QUOTA_SORT_MODES.map((mode) => ({ value: mode, label: t(`quota_management.sort_${mode}`) })),
    [t]
  );

  const { loadedCount, attentionCount } = useMemo(() => {
    let loaded = 0;
    let attention = 0;
    entries.forEach((entry) => {
      const status = quotaByType[entry.type][getQuotaCacheKey(entry.file)]?.status;
      if (status === 'success') loaded += 1;
      else if (status === 'error') attention += 1;
    });
    return { loadedCount: loaded, attentionCount: attention };
  }, [entries, quotaByType]);

  // Pruning: once the file list settles, each provider cache keeps only credentials that still exist
  useEffect(() => {
    if (loading || error || filesGeneration !== sessionGeneration) return;
    const survivorsByType = new Map<QuotaProviderType, Set<string>>(
      QUOTA_TAB_ORDER.map((type) => [type, new Set<string>()])
    );
    entries.forEach((entry) => survivorsByType.get(entry.type)?.add(getQuotaCacheKey(entry.file)));

    QUOTA_TAB_ORDER.forEach((type) => {
      const survivors = survivorsByType.get(type) ?? new Set<string>();
      const setQuota = getQuotaSetter(QUOTA_ADAPTERS[type]);
      setQuota((prev) => {
        const staleKeys = Object.keys(prev).filter((name) => !survivors.has(name));
        if (staleKeys.length === 0) return prev;
        const next = { ...prev };
        staleKeys.forEach((name) => delete next[name]);
        return next;
      });
    });
  }, [entries, error, filesGeneration, loading, sessionGeneration]);

  /* ---------- Loading and actions ---------- */

  const { batchLoading, loadQuota } = useQuotaBatchLoader();
  const { resettingQuotaName, refreshQuota, resetQuota } = useQuotaActions(disableControls);

  const pendingRefreshRef = useRef<number | null>(null);
  const prevLoadingRef = useRef(loading);

  // Refresh all: re-read the file list and the history, and once the list settles (the falling
  // edge of loading) batch-load the current page's quota
  const handleRefreshAll = useCallback(() => {
    if (disableControls) return;
    pendingRefreshRef.current = sessionGeneration;
    setHistoryToken((token) => token + 1);
    void loadFiles();
  }, [disableControls, loadFiles, sessionGeneration]);

  useEffect(() => {
    const wasLoading = prevLoadingRef.current;
    prevLoadingRef.current = loading;

    const requestedSession = pendingRefreshRef.current;
    if (requestedSession === null) return;
    if (requestedSession !== sessionGeneration) {
      pendingRefreshRef.current = null;
      return;
    }
    if (loading || !wasLoading) return;

    pendingRefreshRef.current = null;
    if (
      canRefreshQuotaAfterList(
        requestedSession,
        sessionGeneration,
        filesGeneration,
        Boolean(error),
        disableControls
      )
    ) {
      void loadQuota(pageItems);
    }
  }, [disableControls, error, filesGeneration, loading, loadQuota, pageItems, sessionGeneration]);

  // Claude and Codex quota comes from the server's own tracking, so refreshing
  // one row means re-reading the auth file list, not calling the provider.
  const refreshEntry = useCallback(
    (entry: QuotaFileEntry) => {
      if (entry.type === 'claude' || entry.type === 'codex') {
        handleRefreshAll();
        return;
      }
      void refreshQuota(entry.file, QUOTA_ADAPTERS[entry.type]);
    },
    [handleRefreshAll, refreshQuota]
  );

  useQuotaAutoLoad(
    pageItems,
    disableControls ||
      loading ||
      batchLoading ||
      Boolean(error) ||
      filesGeneration !== sessionGeneration,
    loadQuota
  );

  const canUseActions = !disableControls && !loading && filesGeneration === sessionGeneration;

  /* ---------- One-time entrance cascade for the first cards ----------
   * cardsAnimated flips right after the first data render. Mounted cards captured their own
   * delay at mount (useState initialiser in QuotaCard); cards mounted later by a tab switch,
   * page change or refresh get null, so the cascade never replays. */

  const [cardsAnimated, setCardsAnimated] = useState(false);
  const enableCardEntrance = !cardsAnimated && !loading && pageItems.length > 0;
  useEffect(() => {
    if (enableCardEntrance) {
      setCardsAnimated(true);
    }
  }, [enableCardEntrance]);
  const cardEntranceDelay = (index: number): number | null => {
    if (!enableCardEntrance) return null;
    if (pageItems.length <= 1) return 0;
    return Math.round((index / (pageItems.length - 1)) * CARD_ENTRANCE_BUDGET_MS);
  };

  /* ---------- Pools ----------
   * The pools cover every credential of the provider, regardless of search and paging. Burn,
   * the pool value and the projection are evaluated at dataAt (when the list was loaded), so
   * the numbers stay put between refreshes while the Now marker and countdowns keep ticking. */

  const poolTabs = getPoolTabs(tab);
  const poolProviders = useMemo(
    () => getPoolTabs(tab).filter((provider) => tabCounts[provider] > 0),
    [tab, tabCounts]
  );
  const dataAt = Math.min(now, filesLoadedAt ?? now);
  const range = poolRange(now, rangeOffset, TIME_ZONE);

  const history = usePoolHistory({
    enabled:
      connectionStatus === 'connected' &&
      !loading &&
      filesGeneration === sessionGeneration &&
      poolProviders.length > 0,
    sinceMs: historySinceMs(range, now),
    refreshToken: historyToken,
    sessionGeneration,
  });

  const models = useMemo(
    () =>
      poolProviders
        .map((provider) =>
          buildPoolCardModel({
            burnWindow,
            dataAt,
            files: entries.filter((entry) => entry.type === provider).map((entry) => entry.file),
            futureUsage,
            history: history.result,
            metric,
            now,
            provider,
            rangeOffset,
            timeZone: TIME_ZONE,
          })
        )
        .filter(isPoolModel),
    [
      burnWindow,
      dataAt,
      entries,
      futureUsage,
      history.result,
      metric,
      now,
      poolProviders,
      rangeOffset,
    ]
  );

  // Before the first list arrives there are no models yet; reserve the tab's pool slots so the
  // skeletons stand where the cards will appear.
  const poolSlots = loading && filesLoadedAt === null ? poolTabs.length : models.length;
  const historyNoticeStatus = worstHistoryStatus(models);
  const historyErrorMessage =
    history.result?.status === 'error' ? history.result.message : undefined;
  const showAccounts = accounts || poolSlots === 0;

  const handleRetryHistory = useCallback(() => setHistoryToken((token) => token + 1), []);
  const handlePrevRange = () => setRangeOffset(range.offset - 1);
  const handleNextRange = () => setRangeOffset(range.offset + 1);
  const handleTodayRange = () => setRangeOffset(0);
  /* ---------- Render ---------- */

  const isEmpty = !loading && filteredEntries.length === 0;

  return (
    <div className={styles.page} ref={revealRef}>
      <QuotaHeader
        totalCount={entries.length}
        loadedCount={loadedCount}
        attentionCount={attentionCount}
        refreshing={loading || batchLoading}
        disableControls={disableControls}
        onRefreshAll={handleRefreshAll}
      />

      <section className={styles.workbench}>
        <div className={styles.tabsRow} data-reveal>
          <ProviderTabs
            types={TAB_IDS}
            counts={tabCounts}
            active={tab}
            resolvedTheme={resolvedTheme}
            onChange={handleTabChange}
          />
        </div>

        {poolSlots > 0 && (
          <PoolControls
            accounts={accounts}
            burnWindow={burnWindow}
            futureUsage={futureUsage}
            locale={locale}
            metric={metric}
            onAccountsChange={setAccounts}
            onBurnWindowChange={setBurnWindow}
            onFutureUsageChange={setFutureUsage}
            onMetricChange={setMetric}
            onNext={handleNextRange}
            onPrev={handlePrevRange}
            onToday={handleTodayRange}
            range={range}
            timeZone={TIME_ZONE}
          />
        )}

        {historyNoticeStatus !== null && (
          <PoolHistoryNotice
            message={historyErrorMessage}
            onRetry={handleRetryHistory}
            status={historyNoticeStatus}
          />
        )}

        {error && (
          <div className={styles.errorBanner} role="alert">
            {error}
          </div>
        )}

        {poolSlots > 0 && (
          <div className={styles.pools}>
            {loading
              ? poolTabs
                  .slice(0, poolSlots)
                  .map((provider) => (
                    <Skeleton
                      key={provider}
                      height={POOL_SKELETON_HEIGHT}
                      rounded={10}
                      aria-hidden="true"
                    />
                  ))
              : models.map((model) => (
                  <PoolCard
                    key={model.provider}
                    locale={locale}
                    metric={metric}
                    model={model}
                    resolvedTheme={resolvedTheme}
                    timeZone={TIME_ZONE}
                  />
                ))}
          </div>
        )}

        {showAccounts && (
          <section className={styles.accounts}>
            <div className={styles.toolbar}>
              <div className={styles.search}>
                <IconSearch size={16} className={styles.searchIcon} aria-hidden="true" />
                <input
                  ref={searchInputRef}
                  className={styles.searchInput}
                  type="search"
                  value={search}
                  onChange={(event) => handleSearchChange(event.target.value)}
                  placeholder={t('quota_management.search_placeholder')}
                  aria-label={t('quota_management.search_label')}
                />
                {search && (
                  <button
                    type="button"
                    className={styles.clearSearch}
                    aria-label={t('quota_management.search_clear')}
                    title={t('quota_management.search_clear')}
                    onClick={() => {
                      handleSearchChange('');
                      searchInputRef.current?.focus();
                    }}
                  >
                    <IconX size={14} aria-hidden="true" />
                  </button>
                )}
              </div>
              <div className={styles.sort}>
                <Select
                  value={sortMode}
                  options={sortOptions}
                  onChange={handleSortModeChange}
                  ariaLabel={t('quota_management.sort_label')}
                  size="sm"
                />
              </div>
            </div>

            {loading ? (
              <div className={styles.grid} aria-hidden="true">
                {Array.from({ length: SKELETON_CARD_COUNT }, (_, index) => (
                  <Skeleton key={index} height={168} rounded={14} />
                ))}
              </div>
            ) : isEmpty ? (
              <EmptyState
                title={
                  search.trim()
                    ? t('quota_management.search_empty_title')
                    : tab === 'all'
                      ? t('quota_management.empty_title')
                      : t(`${QUOTA_ADAPTERS[tab].i18nPrefix}.empty_title`)
                }
                description={
                  search.trim()
                    ? t('quota_management.search_empty_desc')
                    : tab === 'all'
                      ? t('quota_management.empty_desc')
                      : t(`${QUOTA_ADAPTERS[tab].i18nPrefix}.empty_desc`)
                }
                action={
                  search.trim() ? (
                    <Button variant="secondary" size="sm" onClick={() => handleSearchChange('')}>
                      {t('quota_management.search_clear')}
                    </Button>
                  ) : tab === 'all' ? undefined : (
                    <Button variant="secondary" size="sm" onClick={() => handleTabChange('all')}>
                      {t('auth_files.filter_all')}
                    </Button>
                  )
                }
              />
            ) : (
              <div className={styles.grid}>
                {pageItems.map((entry, index) => (
                  <QuotaCard
                    key={`${entry.type}:${getQuotaCacheKey(entry.file)}`}
                    entry={entry}
                    quota={getQuota(entry)}
                    resolvedTheme={resolvedTheme}
                    canRefresh={canUseActions && !entry.file.disabled}
                    resetting={resettingQuotaName === getQuotaCacheKey(entry.file)}
                    entranceDelayMs={cardEntranceDelay(index)}
                    onRefresh={() => refreshEntry(entry)}
                    onReset={() => resetQuota(entry.file, QUOTA_ADAPTERS[entry.type])}
                  />
                ))}
              </div>
            )}

            {!loading && filteredEntries.length > QUOTA_PAGE_SIZE && (
              <div className={styles.pagination}>
                <Button
                  variant="secondary"
                  size="sm"
                  onClick={() => setPage(Math.max(1, currentPage - 1))}
                  disabled={currentPage <= 1}
                >
                  {t('auth_files.pagination_prev')}
                </Button>
                <div className={styles.pageInfo}>
                  {t('auth_files.pagination_info', {
                    current: currentPage,
                    total: totalPages,
                    count: filteredEntries.length,
                  })}
                </div>
                <Button
                  variant="secondary"
                  size="sm"
                  onClick={() => setPage(Math.min(totalPages, currentPage + 1))}
                  disabled={currentPage >= totalPages}
                >
                  {t('auth_files.pagination_next')}
                </Button>
              </div>
            )}
          </section>
        )}
      </section>
    </div>
  );
}
