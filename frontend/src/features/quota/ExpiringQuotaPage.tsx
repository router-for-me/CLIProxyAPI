/**
 * Expiring quota: credentials whose weekly/monthly subscription quota resets
 * soonest, rendered as the shared AuthFileCard so the quota section, health
 * and identity stay identical to the channel credentials tab.
 *
 * Unlike the quota page — click-to-load per card there — this page batch-loads
 * on open because the list itself does not exist without quota data. In
 * account mode quota queries go through the hub's credentialApiCall, which only
 * reaches channel-mounted credentials; unmounted ones never enter the list.
 * Clicking a card navigates to that channel's credentials tab.
 */

import { useCallback, useEffect, useMemo, useRef, useState, type MouseEvent } from 'react';
import { Link, useNavigate } from 'react-router-dom';
import { useTranslation } from 'react-i18next';
import { authFilesApi } from '@/services/api';
import { Button } from '@/components/ui/Button';
import { EmptyState } from '@/components/ui/EmptyState';
import { Skeleton } from '@/components/ui/Skeleton';
import { IconChevronRight, IconRefreshCw, IconTimer } from '@/components/ui/icons';
import { useHeaderRefresh } from '@/hooks/useHeaderRefresh';
import { useNow } from '@/hooks/useNow';
import { useAuthStore, useQuotaStore, useThemeStore } from '@/stores';
import type { AuthFileItem, ResolvedTheme } from '@/types';
import { formatInstantShort, formatRelativeInstant } from '@/utils/quota';
import { getQuotaCacheKey } from '@/utils/quota/identity';
import { isRuntimeOnlyAuthFile } from '@/features/authFiles/constants';
import { AuthFileCard } from '@/features/authFiles/components/AuthFileCard';
import type { AuthFileStatusBarData } from '@/features/authFiles/hooks/useAuthFilesStatusBarCache';
import { PageHeader } from '@/features/sharing/shared';
import { CARD_ENTRANCE_BUDGET_MS } from './constants';
import { classifyQuotaFiles, type QuotaFileEntry } from './logic';
import { buildExpiringEntries, expiryUrgency, type ExpiringQuotaEntry } from './expiringQuota';
import { useQuotaBatchLoader } from './hooks/useQuotaBatchLoader';
import type { QuotaProviderType } from './providers/types';
import type { QuotaCardState } from './providers';
import styles from './ExpiringQuotaPage.module.scss';

const SKELETON_CARD_COUNT = 6;
const EMPTY_STATUS_BAR_CACHE = new Map<string, AuthFileStatusBarData>();

/* The shared card carries interactive controls (quota reset, status toggles);
   a click landing on one must not double as navigation, so the wrapper checks
   the event target before jumping to the channel's credentials tab. */
const INTERACTIVE_SELECTOR =
  'a, button, input, select, textarea, [role="button"], [role="link"], [role="switch"], [role="tab"]';

const channelTarget = (file: AuthFileItem): { id: string; name: string } | null => {
  const rawId = file['channel_id'] ?? file.channelId;
  const id = typeof rawId === 'string' ? rawId.trim() : '';
  if (!id) return null;
  const rawName = file['channel'];
  const name = typeof rawName === 'string' && rawName.trim() ? rawName.trim() : id;
  return { id, name };
};

const entryKey = (entry: QuotaFileEntry): string => `${entry.type}:${getQuotaCacheKey(entry.file)}`;

function ExpiringCard({
  item,
  resolvedTheme,
  now,
  locale,
  disableControls,
  entranceDelayMs,
}: {
  item: ExpiringQuotaEntry;
  resolvedTheme: ResolvedTheme;
  now: number;
  locale?: string;
  disableControls: boolean;
  entranceDelayMs: number | null;
}) {
  const { t } = useTranslation();
  const navigate = useNavigate();
  const { entry, soonestAtMs } = item;
  const file = entry.file;
  const urgency = expiryUrgency(soonestAtMs, now);
  const channel = channelTarget(file);
  const channelUrl = channel ? `/channels/${channel.id}?tab=resources` : null;
  const channelName = channel?.name ?? file.name;

  const openChannel =
    channelUrl === null
      ? undefined
      : (event: MouseEvent<HTMLDivElement>) => {
          if ((event.target as HTMLElement).closest(INTERACTIVE_SELECTOR)) return;
          navigate(channelUrl);
        };

  return (
    <div
      className={`${styles.item} ${styles[urgency]} ${channelUrl ? styles.itemLinked : ''}`}
      onClick={openChannel}
    >
      <div className={styles.meta}>
        {channelUrl ? (
          <Link to={channelUrl} className={styles.channelLink} title={t('sharing.tab_resources')}>
            <span className={styles.channelName}>{channelName}</span>
            <IconChevronRight size={12} aria-hidden="true" />
          </Link>
        ) : (
          <span className={styles.channelLink}>{channelName}</span>
        )}
        <span className={styles.badge} title={formatInstantShort(soonestAtMs)}>
          <IconTimer size={12} aria-hidden="true" />
          {formatRelativeInstant(soonestAtMs, now, locale)}
        </span>
      </div>
      <AuthFileCard
        file={file}
        compact={false}
        selected={false}
        resolvedTheme={resolvedTheme}
        disableControls={disableControls}
        deleting={null}
        statusUpdating={{}}
        manualRefreshing={{}}
        cooldownResetting={{}}
        quotaFilterType="all"
        statusBarCache={EMPTY_STATUS_BAR_CACHE}
        entranceDelayMs={entranceDelayMs}
      />
    </div>
  );
}

export function ExpiringQuotaPage() {
  const { t, i18n } = useTranslation();
  const connectionStatus = useAuthStore((state) => state.connectionStatus);
  const account = useAuthStore((state) => state.account);
  const resolvedTheme = useThemeStore((state) => state.resolvedTheme);

  const [files, setFiles] = useState<AuthFileItem[]>([]);
  const [loading, setLoading] = useState(true);
  const [error, setError] = useState('');

  /* ---------- File list ---------- */

  const sessionGeneration = useQuotaStore((state) => state.cacheGeneration);
  const [filesGeneration, setFilesGeneration] = useState<number | null>(null);
  const [listVersion, setListVersion] = useState(0);
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
      setListVersion((version) => version + 1);
    } catch (err: unknown) {
      if (!isCurrent()) return;
      const message = err instanceof Error ? err.message : t('notification.refresh_failed');
      setError(message);
    } finally {
      if (isCurrent()) setLoading(false);
    }
  }, [connectionStatus, sessionGeneration, t]);

  useHeaderRefresh(loadFiles);

  useEffect(() => {
    void loadFiles();
    return () => {
      listRequestRef.current += 1;
    };
  }, [loadFiles]);

  /* ---------- Quota states ---------- */

  const antigravityQuota = useQuotaStore((state) => state.antigravityQuota);
  const claudeQuota = useQuotaStore((state) => state.claudeQuota);
  const codexQuota = useQuotaStore((state) => state.codexQuota);
  const devinQuota = useQuotaStore((state) => state.devinQuota);
  const kiroQuota = useQuotaStore((state) => state.kiroQuota);
  const qoderQuota = useQuotaStore((state) => state.qoderQuota);
  const kimiQuota = useQuotaStore((state) => state.kimiQuota);
  const metaQuota = useQuotaStore((state) => state.metaQuota);
  const xaiQuota = useQuotaStore((state) => state.xaiQuota);
  const zcodeQuota = useQuotaStore((state) => state.zcodeQuota);

  const quotaByType = useMemo<Record<QuotaProviderType, Record<string, QuotaCardState>>>(
    () =>
      ({
        antigravity: antigravityQuota,
        claude: claudeQuota,
        codex: codexQuota,
        devin: devinQuota,
        kiro: kiroQuota,
        qoder: qoderQuota,
        kimi: kimiQuota,
        meta: metaQuota,
        xai: xaiQuota,
        zcode: zcodeQuota,
      }) as unknown as Record<QuotaProviderType, Record<string, QuotaCardState>>,
    [
      antigravityQuota,
      claudeQuota,
      codexQuota,
      devinQuota,
      kiroQuota,
      qoderQuota,
      kimiQuota,
      metaQuota,
      xaiQuota,
      zcodeQuota,
    ]
  );

  const getQuota = useCallback(
    (entry: QuotaFileEntry): QuotaCardState | undefined =>
      quotaByType[entry.type][getQuotaCacheKey(entry.file)],
    [quotaByType]
  );

  /* ---------- Classification ---------- */

  const entries = useMemo(() => {
    return classifyQuotaFiles(files).filter(({ file }) => {
      // AI providers (openai-compatible channel upstreams) carry no subscription quota.
      if (file.kind === 'provider') return false;
      if (isRuntimeOnlyAuthFile(file)) return false;
      // Account mode quota queries only reach channel-mounted credentials.
      if (account && !channelTarget(file)) return false;
      return true;
    });
  }, [files, account]);

  /* ---------- Auto batch load ---------- */

  const { batchLoading, loadQuota } = useQuotaBatchLoader();
  const autoLoadRef = useRef(0);
  /* Provider groups land one by one; revealing each as it arrives makes cards
     pop in individually. settledVersion flips only when the whole batch is
     done, so the skeleton holds until the grid can render in one pass. */
  const [settledVersion, setSettledVersion] = useState(0);
  const batchWatchRef = useRef<{ version: number; active: boolean }>({
    version: 0,
    active: false,
  });
  useEffect(() => {
    if (loading || error || filesGeneration !== sessionGeneration) return;
    if (autoLoadRef.current === listVersion) return;
    autoLoadRef.current = listVersion;
    if (entries.length === 0) {
      setSettledVersion(listVersion);
      return;
    }
    batchWatchRef.current = { version: listVersion, active: true };
    void loadQuota(entries);
  }, [entries, error, filesGeneration, listVersion, loadQuota, loading, sessionGeneration]);

  useEffect(() => {
    const watch = batchWatchRef.current;
    if (!watch.active || batchLoading) return;
    watch.active = false;
    setSettledVersion(watch.version);
  }, [batchLoading]);

  /* ---------- Expiring rows ---------- */

  const now = useNow(true);
  const fresh = useMemo(
    () => buildExpiringEntries(entries, getQuota, now),
    [entries, getQuota, now]
  );

  /* A refresh replaces the credential's quota state with a bare loading one,
     so its expiring rows vanish until the fetch lands — the card would blink
     out and pop back. Remember each card's soonest reset and keep rendering it
     while a refresh is in flight; only fresh success data (or a passed reset)
     may drop a card. The card body reads live store data, so only the sort
     key needs remembering. */
  const [soonestByKey, setSoonestByKey] = useState<ReadonlyMap<string, number>>(() => new Map());
  useEffect(() => {
    setSoonestByKey((prev) => {
      const next = new Map(prev);
      let changed = false;
      fresh.forEach((item) => {
        const key = entryKey(item.entry);
        if (next.get(key) !== item.soonestAtMs) {
          next.set(key, item.soonestAtMs);
          changed = true;
        }
      });
      const listed = new Set(entries.map(entryKey));
      next.forEach((_, key) => {
        if (!listed.has(key)) {
          next.delete(key);
          changed = true;
        }
      });
      return changed ? next : prev;
    });
  }, [fresh, entries]);

  const displayed = useMemo(() => {
    const merged = new Map<string, ExpiringQuotaEntry>();
    fresh.forEach((item) => merged.set(entryKey(item.entry), item));
    entries.forEach((entry) => {
      const key = entryKey(entry);
      if (merged.has(key) || getQuota(entry)?.status === 'success') return;
      const soonestAtMs = soonestByKey.get(key);
      if (soonestAtMs === undefined || soonestAtMs <= now) return;
      merged.set(key, { entry, rows: [], soonestAtMs });
    });
    return Array.from(merged.values()).sort((a, b) => a.soonestAtMs - b.soonestAtMs);
  }, [fresh, entries, getQuota, now, soonestByKey]);

  const { pendingCount, failedCount } = useMemo(() => {
    let pending = 0;
    let failed = 0;
    entries.forEach((entry) => {
      const status = getQuota(entry)?.status;
      if (status === 'error') failed += 1;
      else if (status !== 'success') pending += 1;
    });
    return { pendingCount: pending, failedCount: failed };
  }, [entries, getQuota]);

  const refreshing = loading || batchLoading;
  const disableControls = connectionStatus !== 'connected';
  const batchSettled = settledVersion === listVersion;
  const showSkeleton = loading || (displayed.length === 0 && entries.length > 0 && !batchSettled);
  const showEmpty = !loading && !error && displayed.length === 0 && batchSettled;

  /* First batch of visible cards captures its staggered entrance once; cards
     mounted by late-arriving quota get null — refreshes must not replay it. */
  const [cardsAnimated, setCardsAnimated] = useState(false);
  const enableCardEntrance = !cardsAnimated && displayed.length > 0;
  useEffect(() => {
    if (enableCardEntrance) setCardsAnimated(true);
  }, [enableCardEntrance]);
  const cardEntranceDelay = (index: number): number | null => {
    if (!enableCardEntrance) return null;
    if (displayed.length <= 1) return 0;
    return Math.round((index / (displayed.length - 1)) * CARD_ENTRANCE_BUDGET_MS);
  };

  return (
    <div className={styles.page}>
      <PageHeader
        title={t('sharing.quota_expiry')}
        hint={t('sharing.quota_expiry_hint')}
        actions={
          <Button
            variant="secondary"
            size="sm"
            onClick={() => void loadFiles()}
            disabled={disableControls || refreshing}
          >
            <IconRefreshCw size={14} className={refreshing ? styles.spinning : undefined} />
            {t('quota_management.refresh_all_credentials')}
          </Button>
        }
      />

      {error && (
        <div className={styles.errorBanner} role="alert">
          {error}
        </div>
      )}

      {showSkeleton ? (
        <div className={styles.grid} aria-hidden="true">
          {Array.from({ length: SKELETON_CARD_COUNT }, (_, index) => (
            <Skeleton key={index} height={200} rounded={14} />
          ))}
        </div>
      ) : showEmpty ? (
        <EmptyState
          title={
            entries.length === 0
              ? t('sharing.quota_expiry_empty_title')
              : t('sharing.quota_expiry_none_title')
          }
          description={
            entries.length === 0
              ? t('sharing.quota_expiry_empty_desc')
              : t('sharing.quota_expiry_none_desc')
          }
        />
      ) : (
        <div className={styles.grid}>
          {displayed.map((item, index) => (
            <ExpiringCard
              key={entryKey(item.entry)}
              item={item}
              resolvedTheme={resolvedTheme}
              now={now}
              locale={i18n.resolvedLanguage}
              disableControls={disableControls}
              entranceDelayMs={cardEntranceDelay(index)}
            />
          ))}
        </div>
      )}

      {!loading && !showSkeleton && (pendingCount > 0 || failedCount > 0) && (
        <p className={styles.footnote}>
          {pendingCount > 0 &&
            t('sharing.quota_expiry_querying', {
              done: entries.length - pendingCount,
              total: entries.length,
            })}
          {pendingCount > 0 && failedCount > 0 && ' · '}
          {failedCount > 0 && t('sharing.quota_expiry_failed', { count: failedCount })}
        </p>
      )}
    </div>
  );
}
