/**
 * Ledger view: one section per provider, one compact row per credential with
 * up to three aligned window columns (model-scoped weekly, session, shared
 * weekly) and a text refresh action.
 *
 * Load-on-click is preserved: an idle row offers a load button rather than
 * fetching on its own, like the idle card body.
 */

import { useMemo } from 'react';
import { useTranslation } from 'react-i18next';
import { IconRefreshCw } from '@/components/ui/icons';
import { useNow } from '@/hooks/useNow';
import { getTypeLabel } from '@/features/authFiles/constants';
import { resolveQuotaErrorMessage } from '@/utils/quota';
import { getQuotaCacheKey, getQuotaDisplayName } from '@/utils/quota/identity';
import { QUOTA_ADAPTERS, type QuotaCardState } from '../providers';
import type { QuotaProviderType } from '../providers/types';
import { isQuotaRefreshDisabled, type QuotaFileEntry } from '../logic';
import { getQuotaPlanLabel } from '../planLabel';
import { pickLedgerColumns, toQuotaWindows, type LedgerWindow } from '../quotaWindows';
import { formatLedgerReset, formatPercent, formatWindowLabel, meterLevel } from './ledgerFormat';
import styles from './QuotaLedger.module.scss';

const LEDGER_COLUMNS = 3;

const LEVEL_CLASS = {
  high: styles.barHigh,
  medium: styles.barMedium,
  low: styles.barLow,
  unknown: '',
} as const;

export type QuotaLedgerProps = {
  entries: QuotaFileEntry[];
  quotaFor: (entry: QuotaFileEntry) => QuotaCardState | undefined;
  /** Per provider, the model window the summary headlines; rows align their model column to it. */
  headlineKeyFor: (provider: QuotaProviderType) => string | null;
  canUseActions: boolean;
  resettingQuotaName: string | null;
  onRefresh: (entry: QuotaFileEntry) => void;
};

export function QuotaLedger(props: QuotaLedgerProps) {
  const { entries } = props;
  const { t } = useTranslation();

  // Entries arrive provider-grouped in the default order; keep first-seen order
  // so the soonest-recovery sort still groups instead of interleaving.
  const sections = useMemo(() => {
    const byProvider = new Map<QuotaProviderType, QuotaFileEntry[]>();
    entries.forEach((entry) => {
      const list = byProvider.get(entry.type);
      if (list) list.push(entry);
      else byProvider.set(entry.type, [entry]);
    });
    return [...byProvider.entries()];
  }, [entries]);

  return (
    <div className={styles.ledger}>
      {sections.map(([provider, sectionEntries]) => (
        <section key={provider} className={styles.section}>
          <h2 className={styles.sectionTitle}>
            {getTypeLabel(t, provider)}
            <span className={styles.sectionCount}>{sectionEntries.length}</span>
          </h2>
          <div className={styles.rows}>
            {sectionEntries.map((entry) => (
              <LedgerRow
                key={`${entry.type}:${getQuotaCacheKey(entry.file)}`}
                entry={entry}
                {...props}
              />
            ))}
          </div>
        </section>
      ))}
    </div>
  );
}

function LedgerRow({
  entry,
  quotaFor,
  headlineKeyFor,
  canUseActions,
  resettingQuotaName,
  onRefresh,
}: QuotaLedgerProps & { entry: QuotaFileEntry }) {
  const { t } = useTranslation();
  const quota = quotaFor(entry);
  const adapter = QUOTA_ADAPTERS[entry.type];
  const status = quota?.status ?? 'idle';
  const loading = status === 'loading';
  const name = getQuotaDisplayName(entry.file);
  const plan = getQuotaPlanLabel(entry.type, quota, t);
  const canRefresh = canUseActions && !entry.file.disabled;
  const resetting = resettingQuotaName === getQuotaCacheKey(entry.file);
  const columns = useMemo(
    () => pickLedgerColumns(toQuotaWindows(entry.type, quota), headlineKeyFor(entry.type)),
    [entry.type, quota, headlineKeyFor]
  );

  let body;
  if (status === 'idle') {
    body = (
      <button
        type="button"
        className={styles.load}
        onClick={() => onRefresh(entry)}
        disabled={!canRefresh}
      >
        <IconRefreshCw size={13} aria-hidden="true" />
        {t(`${adapter.i18nPrefix}.idle`)}
      </button>
    );
  } else if (loading) {
    body = (
      <div className={styles.message} aria-busy="true">
        {t(`${adapter.i18nPrefix}.loading`)}
      </div>
    );
  } else if (status === 'error') {
    const message = resolveQuotaErrorMessage(
      t,
      quota?.errorStatus,
      quota?.error || t('common.unknown_error')
    );
    body = (
      <div className={`${styles.message} ${styles.messageError}`} role="alert">
        {t(`${adapter.i18nPrefix}.load_failed`, { message })}
      </div>
    );
  } else if (columns.length === 0) {
    body = <div className={styles.message}>{t('quota_management.ledger_no_windows')}</div>;
  } else {
    body = (
      <>
        {columns.map((window) => (
          <LedgerCell key={window.id} window={window} />
        ))}
        {Array.from({ length: LEDGER_COLUMNS - columns.length }, (_, index) => (
          <span key={`pad-${index}`} aria-hidden="true" />
        ))}
      </>
    );
  }

  return (
    <div className={styles.row}>
      <div className={styles.identity}>
        <span className={styles.fileName} title={entry.file.name}>
          {name}
        </span>
        {plan && <span className={styles.plan}>{plan}</span>}
      </div>
      <div className={styles.cells}>{body}</div>
      <div className={styles.actions}>
        {status !== 'idle' && (
          <button
            type="button"
            className={styles.refresh}
            onClick={() => onRefresh(entry)}
            disabled={isQuotaRefreshDisabled(canRefresh, loading, resetting)}
            title={t('auth_files.quota_refresh_hint')}
          >
            <IconRefreshCw size={13} className={loading ? styles.spinning : undefined} />
            {t('auth_files.quota_refresh_single')}
          </button>
        )}
      </div>
    </div>
  );
}

function LedgerCell({ window }: { window: LedgerWindow }) {
  const { t, i18n } = useTranslation();
  const now = useNow();
  const reset = formatLedgerReset(window.resetAtMs, window.resetLabel, now, i18n.resolvedLanguage);
  return (
    <div className={styles.cell}>
      <div className={styles.cellHead}>
        <span className={styles.cellLabel}>{formatWindowLabel(t, window)}</span>
        <span className={styles.cellPercent}>{formatPercent(window.remaining)}</span>
      </div>
      <div className={styles.bar}>
        <span
          className={`${styles.barFill} ${LEVEL_CLASS[meterLevel(window.remaining)]}`}
          style={{ width: `${window.remaining ?? 0}%` }}
        />
      </div>
      <div className={styles.cellReset}>
        {reset ? (
          <>
            {reset.relative && <span className={styles.resetRelative}>{reset.relative}</span>}
            <span>{reset.absolute}</span>
          </>
        ) : (
          <span>{t('quota_management.no_reset_pending')}</span>
        )}
      </div>
    </div>
  );
}
