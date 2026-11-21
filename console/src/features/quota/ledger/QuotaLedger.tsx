/**
 * Ledger view of the quota page: manual resets per account over one dense row
 * per credential, ordered the way papa-proxy's reset-first routing spends them.
 */

import { useMemo, type ReactNode } from 'react';
import { useTranslation } from 'react-i18next';
import type { TFunction } from 'i18next';
import { IconRefreshCw } from '@/components/ui/icons';
import { useNow } from '@/hooks/useNow';
import type { ResolvedTheme } from '@/types';
import { getTypeLabel } from '@/features/authFiles/constants';
import { buildResetDisplay, resolveResetMs } from '@/utils/quota';
import type { CacheTokens } from '@/services/api/cacheStats';
import { normalizeRecentRequestAuthIndex } from '@/utils/recentRequests';
import type { QuotaFileEntry } from '../logic';
import {
  QUOTA_PROGRESS_HIGH_THRESHOLD,
  QUOTA_PROGRESS_MEDIUM_THRESHOLD,
} from '../components/QuotaMeter';
import { codexPlanLabel } from '../providers/codex/planLabel';
import {
  LEDGER_PROVIDERS,
  buildLedgerRows,
  maskCredentialName,
  type LedgerMeter,
  type LedgerProvider,
  type LedgerRow,
} from './ledgerModel';
import { ManualResets } from './ManualResets';
import { useCacheStats } from './useCacheStats';
import styles from './QuotaLedger.module.scss';

export interface QuotaLedgerProps {
  entries: QuotaFileEntry[];
  quotaFor: (entry: QuotaFileEntry) => unknown;
  resolvedTheme: ResolvedTheme;
  showEmails: boolean;
  canUseActions: boolean;
  resettingName: string | null;
  canReset: (entry: QuotaFileEntry) => boolean;
  onRefresh: (entry: QuotaFileEntry) => void;
  onReset: (entry: QuotaFileEntry) => void;
}

const tone = (remaining: number | null) =>
  remaining === null
    ? styles.toneUnknown
    : remaining >= QUOTA_PROGRESS_HIGH_THRESHOLD
      ? styles.toneHigh
      : remaining >= QUOTA_PROGRESS_MEDIUM_THRESHOLD
        ? styles.toneMedium
        : styles.toneLow;

// 0% is reserved for an exhausted window; a sliver left reads `<1%`.
const percent = (value: number | null) =>
  value === null ? '--' : value > 0 && value < 0.5 ? '<1%' : `${Math.round(value)}%`;

/** `in 1 day · 09/13, 13:00`, relative first like the rest of the ledger. */
function ResetLine({
  atMs,
  label,
  now,
  empty,
}: {
  atMs: number | null;
  label: string;
  now: number;
  empty?: string;
}) {
  const { i18n } = useTranslation();
  const display = buildResetDisplay(label, atMs, now, i18n.resolvedLanguage);
  if (!display) return <span className={styles.resetMuted}>{empty ?? ''}</span>;
  return (
    <span className={styles.reset}>
      {display.relative && <span className={styles.resetRelative}>{display.relative}</span>}
      <span className={styles.resetAbsolute}>{display.absolute}</span>
    </span>
  );
}

function MeterCell({
  meter,
  rowExhausted,
  now,
  t,
}: {
  meter: LedgerMeter;
  rowExhausted: boolean;
  now: number;
  t: TFunction;
}) {
  // On an exhausted account the empty window is the one that matters; the rest recede.
  const state =
    meter.remaining === 0 ? styles.meterExhausted : rowExhausted ? styles.meterMuted : '';
  return (
    <div className={`${styles.meter} ${state}`}>
      <div className={styles.meterHead}>
        <span>{t(meter.labelKey)}</span>
        <strong>{percent(meter.remaining)}</strong>
      </div>
      <div className={styles.bar}>
        <span
          className={`${styles.barFill} ${tone(meter.remaining)}`}
          style={{ width: `${meter.remaining ?? 0}%` }}
        />
      </div>
      <ResetLine
        atMs={meter.resetAtMs}
        label={meter.resetLabel}
        now={now}
        empty={t('ledger.no_reset_pending')}
      />
    </div>
  );
}

function PlanLine({ row, now, t }: { row: LedgerRow; now: number; t: TFunction }) {
  const { i18n } = useTranslation();
  const parts: ReactNode[] = [];
  if (row.entry.type === 'claude' && row.planType) {
    parts.push(<span key="plan">{t(`claude_quota.${row.planType}`)}</span>);
  }
  if (row.entry.type === 'codex') {
    const label = codexPlanLabel(t, row.planType);
    if (label) parts.push(<strong key="plan">{label}</strong>);
    const renewMs = resolveResetMs([row.renewsAt]);
    const renew =
      renewMs === null ? null : buildResetDisplay(null, renewMs, now, i18n.resolvedLanguage);
    if (renew) {
      parts.push(
        <span key="renew">
          {t('ledger.renews', { when: renew.absolute })}
          {renew.relative ? ` · ${renew.relative}` : ''}
        </span>
      );
    }
  }
  if (parts.length === 0) return null;
  return (
    <div className={styles.plan}>
      {parts.map((part, index) => (
        <span key={index} className={styles.planPart}>
          {part}
        </span>
      ))}
    </div>
  );
}

/**
 * Prompt-cache traffic for the last hour. A thread that keeps its account reads
 * its cache back; one that hops accounts keeps writing it again.
 */
function CacheLine({ tokens, t }: { tokens: CacheTokens | undefined; t: TFunction }) {
  const { i18n } = useTranslation();
  if (!tokens || tokens.requests === 0) return null;
  const input = tokens.uncachedInputTokens + tokens.cacheReadTokens + tokens.cacheWriteTokens;
  const compact = new Intl.NumberFormat(i18n.resolvedLanguage, {
    notation: 'compact',
    maximumFractionDigits: 1,
  });
  const hit = input > 0 ? Math.round((tokens.cacheReadTokens / input) * 100) : null;
  return (
    <div className={styles.cache} title={t('ledger.cache_hint')}>
      <span>{t('ledger.cache_label')}</span>
      {hit !== null && (
        <span className={hit < 50 ? styles.cacheLow : undefined}>
          {t('ledger.cache_hit', { percent: hit })}
        </span>
      )}
      <span>{t('ledger.cache_read', { tokens: compact.format(tokens.cacheReadTokens) })}</span>
      <span>{t('ledger.cache_write', { tokens: compact.format(tokens.cacheWriteTokens) })}</span>
    </div>
  );
}

function LedgerRowView({
  row,
  props,
  now,
  cache,
  t,
}: {
  row: LedgerRow;
  props: QuotaLedgerProps;
  now: number;
  cache: CacheTokens | undefined;
  t: TFunction;
}) {
  const { entry } = row;
  const name = props.showEmails ? entry.file.name : maskCredentialName(entry.file.name);
  const busy = row.status === 'loading';
  const canAct = props.canUseActions && !entry.file.disabled && !busy;

  let body: ReactNode;
  if (row.status === 'success') {
    body = (
      <>
        {row.meters.length === 0 && <div className={styles.message}>{t('ledger.no_windows')}</div>}
        {row.meters.map((meter) => (
          <MeterCell key={meter.id} meter={meter} rowExhausted={row.exhausted} now={now} t={t} />
        ))}
      </>
    );
  } else if (row.status === 'error') {
    body = <div className={styles.messageError}>{row.error || t('ledger.load_failed')}</div>;
  } else {
    body = (
      <div className={styles.message}>{busy ? t('ledger.loading') : t('ledger.not_loaded')}</div>
    );
  }

  return (
    <div className={`${styles.row} ${entry.file.disabled ? styles.rowDisabled : ''}`}>
      <div className={styles.identity}>
        <div className={styles.nameLine}>
          <span className={styles.name} title={props.showEmails ? entry.file.name : undefined}>
            {name}
          </span>
          {row.exhausted && (
            <span className={styles.exhausted} title={t('ledger.exhausted_hint')}>
              {t('ledger.exhausted')}
            </span>
          )}
          {row.nextUp && (
            <span className={styles.nextUp} title={t('ledger.next_up_hint')}>
              {t('ledger.next_up')}
            </span>
          )}
        </div>
        <PlanLine row={row} now={now} t={t} />
        <CacheLine tokens={cache} t={t} />
      </div>
      <div className={styles.meters}>{body}</div>
      <div className={styles.actions}>
        <button
          type="button"
          className={styles.action}
          disabled={!canAct}
          onClick={() => props.onRefresh(entry)}
        >
          <IconRefreshCw size={13} className={busy ? styles.spinning : undefined} />
          {t('ledger.refresh_quota')}
        </button>
      </div>
    </div>
  );
}

export function QuotaLedger(props: QuotaLedgerProps) {
  const { t } = useTranslation();
  const now = useNow();
  const { entries, quotaFor } = props;
  const cacheStats = useCacheStats(entries.length > 0);
  const cacheFor = (row: LedgerRow) => {
    const authIndex = normalizeRecentRequestAuthIndex(row.entry.file.authIndex);
    return authIndex ? cacheStats?.byAuthIndex[authIndex]?.recent : undefined;
  };

  const groups = useMemo(
    () =>
      LEDGER_PROVIDERS.map((provider: LedgerProvider) => {
        const rows = buildLedgerRows(provider, entries, quotaFor, now);
        return { provider, rows };
      }).filter((group) => group.rows.length > 0),
    [entries, quotaFor, now]
  );

  if (groups.length === 0) return null;

  return (
    <section className={styles.ledger} aria-label={t('ledger.title')}>
      <ManualResets
        rows={groups.flatMap((group) => group.rows)}
        quotaFor={quotaFor}
        resolvedTheme={props.resolvedTheme}
        showEmails={props.showEmails}
        canUseActions={props.canUseActions}
        resettingName={props.resettingName}
        canReset={props.canReset}
        onReset={props.onReset}
        onRefresh={props.onRefresh}
      />
      {groups.map((group) => (
        <div key={group.provider} className={styles.group}>
          <h2 className={styles.groupTitle}>
            {getTypeLabel(t, group.provider)}
            <span className={styles.groupCount}>{group.rows.length}</span>
          </h2>
          {group.rows.map((row) => (
            <LedgerRowView
              key={row.key}
              row={row}
              props={props}
              now={now}
              cache={cacheFor(row)}
              t={t}
            />
          ))}
        </div>
      ))}
    </section>
  );
}
