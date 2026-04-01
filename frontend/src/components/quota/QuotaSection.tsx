/**
 * Generic quota section component.
 */

import { useCallback, useEffect, useMemo, useRef, useState } from 'react';
import { useTranslation } from 'react-i18next';
import { Card } from '@/components/ui/Card';
import { Button } from '@/components/ui/Button';
import { EmptyState } from '@/components/ui/EmptyState';
import { triggerHeaderRefresh } from '@/hooks/useHeaderRefresh';
import { useNotificationStore, useQuotaStore, useThemeStore } from '@/stores';
import type { AuthFileItem, ResolvedTheme } from '@/types';
import { getStatusFromError } from '@/utils/quota';
import { QuotaCard } from './QuotaCard';
import type { QuotaStatusState } from './QuotaCard';
import { useQuotaLoader } from './useQuotaLoader';
import type { QuotaConfig } from './quotaConfigs';
import { useGridColumns } from './useGridColumns';
import { IconRefreshCw } from '@/components/ui/icons';
import styles from '@/pages/QuotaPage.module.scss';

type QuotaUpdater<T> = T | ((prev: T) => T);

type QuotaSetter<T> = (updater: QuotaUpdater<T>) => void;

type ViewMode = 'paged' | 'all' | 'list';

const MAX_ITEMS_PER_PAGE = 25;
const MAX_SHOW_ALL_THRESHOLD = 30;

interface QuotaPaginationState<T> {
  pageSize: number;
  totalPages: number;
  currentPage: number;
  pageItems: T[];
  setPageSize: (size: number) => void;
  goToPrev: () => void;
  goToNext: () => void;
  loading: boolean;
  loadingScope: 'page' | 'all' | null;
  setLoading: (loading: boolean, scope?: 'page' | 'all' | null) => void;
}

const useQuotaPagination = <T,>(items: T[], defaultPageSize = 6): QuotaPaginationState<T> => {
  const [page, setPage] = useState(1);
  const [pageSize, setPageSizeState] = useState(defaultPageSize);
  const [loading, setLoadingState] = useState(false);
  const [loadingScope, setLoadingScope] = useState<'page' | 'all' | null>(null);

  const totalPages = useMemo(
    () => Math.max(1, Math.ceil(items.length / pageSize)),
    [items.length, pageSize]
  );

  const currentPage = useMemo(() => Math.min(page, totalPages), [page, totalPages]);

  const pageItems = useMemo(() => {
    const start = (currentPage - 1) * pageSize;
    return items.slice(start, start + pageSize);
  }, [items, currentPage, pageSize]);

  const setPageSize = useCallback((size: number) => {
    setPageSizeState(size);
    setPage(1);
  }, []);

  const goToPrev = useCallback(() => {
    setPage((prev) => Math.max(1, prev - 1));
  }, []);

  const goToNext = useCallback(() => {
    setPage((prev) => Math.min(totalPages, prev + 1));
  }, [totalPages]);

  const setLoading = useCallback((isLoading: boolean, scope?: 'page' | 'all' | null) => {
    setLoadingState(isLoading);
    setLoadingScope(isLoading ? (scope ?? null) : null);
  }, []);

  return {
    pageSize,
    totalPages,
    currentPage,
    pageItems,
    setPageSize,
    goToPrev,
    goToNext,
    loading,
    loadingScope,
    setLoading
  };
};

interface QuotaSectionProps<TState extends QuotaStatusState, TData> {
  config: QuotaConfig<TState, TData>;
  files: AuthFileItem[];
  loading: boolean;
  disabled: boolean;
}

export function QuotaSection<TState extends QuotaStatusState, TData>({
  config,
  files,
  loading,
  disabled
}: QuotaSectionProps<TState, TData>) {
  const { t } = useTranslation();
  const resolvedTheme: ResolvedTheme = useThemeStore((state) => state.resolvedTheme);
  const showNotification = useNotificationStore((state) => state.showNotification);
  const setQuota = useQuotaStore((state) => state[config.storeSetter]) as QuotaSetter<
    Record<string, TState>
  >;

  const listModelFilter = useQuotaStore((state) => state.antigravityListModelFilter);
  const setListModelFilter = useQuotaStore((state) => state.setAntigravityListModelFilter);
  const showAntigravityCredit = useQuotaStore((state) => state.showAntigravityCredit);

  /* Removed useRef */
  const [columns, gridRef] = useGridColumns(380); // Min card width 380px matches SCSS
  const [viewMode, setViewMode] = useState<ViewMode>('paged');
  const [showTooManyWarning, setShowTooManyWarning] = useState(false);
  const hasListMode = Boolean(config.listGroups && config.listGroups.length > 0);

  const filteredFiles = useMemo(() => files.filter((file) => config.filterFn(file)), [
    files,
    config
  ]);
  const showAllAllowed = filteredFiles.length <= MAX_SHOW_ALL_THRESHOLD;
  const effectiveViewMode: ViewMode =
    viewMode === 'list' ? 'list'
      : viewMode === 'all' && !showAllAllowed ? 'paged' : viewMode;

  const {
    pageSize,
    totalPages,
    currentPage,
    pageItems,
    setPageSize,
    goToPrev,
    goToNext,
    loading: sectionLoading,
    setLoading
  } = useQuotaPagination(filteredFiles);

  useEffect(() => {
    if (showAllAllowed) return;
    if (viewMode !== 'all') return;

    let cancelled = false;
    queueMicrotask(() => {
      if (cancelled) return;
      setViewMode('paged');
      setShowTooManyWarning(true);
    });

    return () => {
      cancelled = true;
    };
  }, [showAllAllowed, viewMode]);

  // Update page size based on view mode and columns
  useEffect(() => {
    if (effectiveViewMode === 'all' || effectiveViewMode === 'list') {
      setPageSize(Math.max(1, filteredFiles.length));
    } else {
      // Paged mode: 3 rows * columns, capped to avoid oversized pages.
      setPageSize(Math.min(columns * 3, MAX_ITEMS_PER_PAGE));
    }
  }, [effectiveViewMode, columns, filteredFiles.length, setPageSize]);

  const { quota, loadQuota } = useQuotaLoader(config);

  const pendingQuotaRefreshRef = useRef(false);
  const prevFilesLoadingRef = useRef(loading);

  const handleRefresh = useCallback(() => {
    pendingQuotaRefreshRef.current = true;
    void triggerHeaderRefresh();
  }, []);

  useEffect(() => {
    const wasLoading = prevFilesLoadingRef.current;
    prevFilesLoadingRef.current = loading;

    if (!pendingQuotaRefreshRef.current) return;
    if (loading) return;
    if (!wasLoading) return;

    pendingQuotaRefreshRef.current = false;
    const scope = effectiveViewMode === 'all' || effectiveViewMode === 'list' ? 'all' : 'page';
    const targets = effectiveViewMode === 'all' || effectiveViewMode === 'list' ? filteredFiles : pageItems;
    if (targets.length === 0) return;
    loadQuota(targets, scope, setLoading);
  }, [loading, effectiveViewMode, filteredFiles, pageItems, loadQuota, setLoading]);

  useEffect(() => {
    if (loading) return;
    if (filteredFiles.length === 0) {
      setQuota({});
      return;
    }
    setQuota((prev) => {
      const nextState: Record<string, TState> = {};
      filteredFiles.forEach((file) => {
        const cached = prev[file.name];
        if (cached) {
          nextState[file.name] = cached;
        }
      });
      return nextState;
    });
  }, [filteredFiles, loading, setQuota]);

  const refreshQuotaForFile = useCallback(
    async (file: AuthFileItem) => {
      if (disabled || file.disabled) return;
      if (quota[file.name]?.status === 'loading') return;

      setQuota((prev) => ({
        ...prev,
        [file.name]: config.buildLoadingState()
      }));

      try {
        const data = await config.fetchQuota(file, t);
        setQuota((prev) => ({
          ...prev,
          [file.name]: config.buildSuccessState(data)
        }));
        showNotification(t('auth_files.quota_refresh_success', { name: file.name }), 'success');
      } catch (err: unknown) {
        const message = err instanceof Error ? err.message : t('common.unknown_error');
        const status = getStatusFromError(err);
        setQuota((prev) => ({
          ...prev,
          [file.name]: config.buildErrorState(message, status)
        }));
        showNotification(
          t('auth_files.quota_refresh_failed', { name: file.name, message }),
          'error'
        );
      }
    },
    [config, disabled, quota, setQuota, showNotification, t]
  );

  const titleNode = (
    <div className={styles.titleWrapper}>
      <span>{t(`${config.i18nPrefix}.title`)}</span>
      {filteredFiles.length > 0 && (
        <span className={styles.countBadge}>
          {filteredFiles.length}
        </span>
      )}
    </div>
  );

  const isRefreshing = sectionLoading || loading;

  return (
    <Card
      title={titleNode}
      extra={
        <div className={styles.headerActions}>
          <div className={styles.viewModeToggle}>
            <Button
              variant="secondary"
              size="sm"
              className={`${styles.viewModeButton} ${
                effectiveViewMode === 'paged' ? styles.viewModeButtonActive : ''
              }`}
              onClick={() => setViewMode('paged')}
            >
              {t('auth_files.view_mode_paged')}
            </Button>
            <Button
              variant="secondary"
              size="sm"
              className={`${styles.viewModeButton} ${
                effectiveViewMode === 'all' ? styles.viewModeButtonActive : ''
              }`}
              onClick={() => {
                if (filteredFiles.length > MAX_SHOW_ALL_THRESHOLD) {
                  setShowTooManyWarning(true);
                } else {
                  setViewMode('all');
                }
              }}
            >
              {t('auth_files.view_mode_all')}
            </Button>
            {hasListMode && (
              <Button
                variant="secondary"
                size="sm"
                className={`${styles.viewModeButton} ${
                  effectiveViewMode === 'list' ? styles.viewModeButtonActive : ''
                }`}
                onClick={() => setViewMode('list')}
              >
                {t('auth_files.view_mode_list')}
              </Button>
            )}
          </div>
          <Button
            variant="secondary"
            size="sm"
            className={styles.refreshAllButton}
            onClick={handleRefresh}
            disabled={disabled || isRefreshing}
            loading={isRefreshing}
            title={t('quota_management.refresh_all_credentials')}
            aria-label={t('quota_management.refresh_all_credentials')}
          >
            {!isRefreshing && <IconRefreshCw size={16} />}
            {t('quota_management.refresh_all_credentials')}
          </Button>
        </div>
      }
    >
      {filteredFiles.length === 0 ? (
        <EmptyState
          title={t(`${config.i18nPrefix}.empty_title`)}
          description={t(`${config.i18nPrefix}.empty_desc`)}
        />
      ) : effectiveViewMode === 'list' && config.listGroups ? (
        <ListModeContent
          config={config}
          files={filteredFiles}
          quota={quota}
          listModelFilter={listModelFilter}
          setListModelFilter={setListModelFilter}
          showCredit={showAntigravityCredit}
          disabled={disabled}
          onRefreshFile={refreshQuotaForFile}
        />
      ) : (
        <>
          <div ref={gridRef} className={config.gridClassName}>
            {pageItems.map((item) => (
              <QuotaCard
                key={item.name}
                item={item}
                quota={quota[item.name]}
                resolvedTheme={resolvedTheme}
                i18nPrefix={config.i18nPrefix}
                cardIdleMessageKey={config.cardIdleMessageKey}
                cardClassName={config.cardClassName}
                defaultType={config.type}
                canRefresh={!disabled && !item.disabled}
                onRefresh={() => void refreshQuotaForFile(item)}
                renderQuotaItems={config.renderQuotaItems}
              />
            ))}
          </div>
          {filteredFiles.length > pageSize && effectiveViewMode === 'paged' && (
            <div className={styles.pagination}>
              <Button
                variant="secondary"
                size="sm"
                onClick={goToPrev}
                disabled={currentPage <= 1}
              >
                {t('auth_files.pagination_prev')}
              </Button>
              <div className={styles.pageInfo}>
                {t('auth_files.pagination_info', {
                  current: currentPage,
                  total: totalPages,
                  count: filteredFiles.length
                })}
              </div>
              <Button
                variant="secondary"
                size="sm"
                onClick={goToNext}
                disabled={currentPage >= totalPages}
              >
                {t('auth_files.pagination_next')}
              </Button>
            </div>
          )}
        </>
      )}
      {showTooManyWarning && (
        <div className={styles.warningOverlay} onClick={() => setShowTooManyWarning(false)}>
          <div className={styles.warningModal} onClick={(e) => e.stopPropagation()}>
            <p>{t('auth_files.too_many_files_warning')}</p>
            <Button variant="primary" size="sm" onClick={() => setShowTooManyWarning(false)}>
              {t('common.confirm')}
            </Button>
          </div>
        </div>
      )}
    </Card>
  );
}

/* ── List mode sub-component ─────────────────────────────────────── */

interface ListModeContentProps<TState extends QuotaStatusState, TData> {
  config: QuotaConfig<TState, TData>;
  files: AuthFileItem[];
  quota: Record<string, TState>;
  listModelFilter: string[];
  setListModelFilter: (filter: string[]) => void;
  showCredit: boolean;
  disabled: boolean;
  onRefreshFile: (file: AuthFileItem) => void;
}

function ListModeContent<TState extends QuotaStatusState, TData>({
  config,
  files,
  quota,
  listModelFilter,
  setListModelFilter,
  showCredit,
  disabled,
  onRefreshFile,
}: ListModeContentProps<TState, TData>) {
  const { t } = useTranslation();
  const allGroups = config.listGroups ?? [];
  const visibleGroups = listModelFilter.length > 0
    ? allGroups.filter((g) => listModelFilter.includes(g.id))
    : allGroups;

  const toggleGroup = useCallback(
    (groupId: string) => {
      setListModelFilter(
        listModelFilter.includes(groupId)
          ? listModelFilter.filter((id) => id !== groupId)
          : [...listModelFilter, groupId]
      );
    },
    [listModelFilter, setListModelFilter]
  );

  const getPercentClass = (value: number | null): string => {
    if (value === null) return styles.quotaListCellIdle;
    if (value >= 70) return styles.quotaListCellHigh;
    if (value >= 30) return styles.quotaListCellMedium;
    return styles.quotaListCellLow;
  };

  const showCreditColumn = showCredit && Boolean(config.getListCreditBalance);

  return (
    <div className={styles.listModeContainer}>
      <div className={styles.listModelFilter}>
        <span className={styles.listFilterLabel}>{t('auth_files.list_filter_label')}</span>
        {allGroups.map((group) => {
          const active = listModelFilter.length === 0 || listModelFilter.includes(group.id);
          return (
            <button
              key={group.id}
              type="button"
              className={`${styles.listFilterChip} ${active ? styles.listFilterChipActive : ''}`}
              onClick={() => toggleGroup(group.id)}
            >
              {group.label}
            </button>
          );
        })}
        {listModelFilter.length > 0 && (
          <button
            type="button"
            className={styles.listFilterReset}
            onClick={() => setListModelFilter([])}
          >
            {t('auth_files.list_filter_reset')}
          </button>
        )}
      </div>
      <div className={styles.listTableWrapper}>
        <table className={styles.listTable}>
          <thead>
            <tr>
              <th className={styles.listThFile}>{t('auth_files.list_col_file')}</th>
              <th className={styles.listThStatus}>{t('auth_files.list_col_status')}</th>
              {showCreditColumn && (
                <th className={styles.listThCredit}>{t('antigravity_quota.credit_label')}</th>
              )}
              {visibleGroups.map((g) => (
                <th key={g.id} className={styles.listThModel}>{g.label}</th>
              ))}
            </tr>
          </thead>
          <tbody>
            {files.map((file) => {
              const q = quota[file.name];
              const status = q?.status ?? 'idle';
              const isIdle = status === 'idle';
              const isLoading = status === 'loading';
              const isError = status === 'error';

              return (
                <tr key={file.name} className={isError ? styles.listRowError : ''}>
                  <td className={styles.listTdFile}>
                    <span className={styles.listFileName}>{file.name}</span>
                  </td>
                  <td className={styles.listTdStatus}>
                    {isLoading ? (
                      <span className={styles.listStatusLoading}>...</span>
                    ) : isError ? (
                      <span className={styles.listStatusError} title={q?.error}>!</span>
                    ) : isIdle ? (
                      <button
                        type="button"
                        className={styles.listStatusIdle}
                        disabled={disabled || file.disabled}
                        onClick={() => onRefreshFile(file)}
                      >
                        {t('auth_files.list_click_load')}
                      </button>
                    ) : (
                      <span className={styles.listStatusOk}>OK</span>
                    )}
                  </td>
                  {showCreditColumn && (
                    <td className={styles.listTdCredit}>
                      {status === 'success' && q && config.getListCreditBalance
                        ? (() => {
                            const balance = config.getListCreditBalance(q);
                            return balance !== null ? String(balance) : '-';
                          })()
                        : '-'}
                    </td>
                  )}
                  {visibleGroups.map((g) => {
                    const value = status === 'success' && q && config.getListGroupValue
                      ? config.getListGroupValue(q, g.id)
                      : null;
                    return (
                      <td key={g.id} className={`${styles.listTdModel} ${getPercentClass(value)}`}>
                        {status === 'success' ? (value !== null ? `${value}%` : '-') : '-'}
                      </td>
                    );
                  })}
                </tr>
              );
            })}
          </tbody>
        </table>
      </div>
    </div>
  );
}
