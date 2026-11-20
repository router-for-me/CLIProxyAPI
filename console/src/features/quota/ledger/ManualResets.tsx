/**
 * Manual resets: how many quota resets each account has banked, when they
 * expire, and the button that spends one. Codex counts come from the quota
 * read; Claude counts come from its reset grants, read per account.
 */

import { useCallback, useEffect, useState } from 'react';
import { useTranslation } from 'react-i18next';
import type { TFunction } from 'i18next';
import { getAuthFileIcon } from '@/features/authFiles/constants';
import type { ResolvedTheme } from '@/types';
import { parseIsoToMs } from '@/utils/quota';
import type { QuotaFileEntry } from '../logic';
import { codexPlanLabel } from '../providers/codex/planLabel';
import { useClaudeResetGrants } from '../providers/claude/ClaudeResetGrants';
import { ClaudeResetGrantDialog } from '../providers/claude/ClaudeResetGrantDialog';
import { maskCredentialName, type LedgerRow } from './ledgerModel';
import styles from './ManualResets.module.scss';

export interface ManualResetsProps {
  rows: LedgerRow[];
  quotaFor: (entry: QuotaFileEntry) => unknown;
  resolvedTheme: ResolvedTheme;
  showEmails: boolean;
  canUseActions: boolean;
  resettingName: string | null;
  canReset: (entry: QuotaFileEntry) => boolean;
  onReset: (entry: QuotaFileEntry) => void;
  onRefresh: (entry: QuotaFileEntry) => void;
}

interface ResetInfo {
  /** Resets left; null until the account has been read. */
  count: number | null;
  /** Expiry of each banked reset, soonest first. */
  expires: number[];
  /** Why the count could not be read, shown in place of the expiry. */
  note?: string;
}

interface Action {
  label: string;
  disabled: boolean;
  onClick: () => void;
}

const UNREAD: ResetInfo = { count: null, expires: [] };

function codexInfo(row: LedgerRow): ResetInfo {
  if (!row.resetCredits) return UNREAD;
  const expires = row.resetCredits.expiresAt
    .map(parseIsoToMs)
    .filter((ms): ms is number => ms !== null);
  return { count: row.resetCredits.available, expires };
}

function planOf(row: LedgerRow, t: TFunction): string | null {
  if (row.entry.type === 'codex') return codexPlanLabel(t, row.planType);
  return row.planType ? t(`claude_quota.${row.planType}`) : null;
}

function ResetRowView({
  row,
  info,
  action,
  props,
}: {
  row: LedgerRow;
  info: ResetInfo;
  action: Action | null;
  props: ManualResetsProps;
}) {
  const { t, i18n } = useTranslation();
  const { file } = row.entry;
  const name = props.showEmails ? file.name : maskCredentialName(file.name);
  const plan = planOf(row, t);
  const day = new Intl.DateTimeFormat(i18n.resolvedLanguage, { month: 'short', day: 'numeric' });
  const dates = [...new Set(info.expires.map((ms) => day.format(ms)))];
  const icon = getAuthFileIcon(row.entry.type, props.resolvedTheme);

  return (
    <li className={`${styles.row} ${file.disabled ? styles.rowDisabled : ''}`}>
      <span className={styles.account}>
        {icon ? <img src={icon} alt="" className={styles.icon} /> : null}
        <span className={styles.identity}>
          <span className={styles.name} title={props.showEmails ? file.name : undefined}>
            {name}
          </span>
          {plan ? <span className={styles.plan}>{plan}</span> : null}
        </span>
      </span>
      <span className={styles.detail}>
        <span className={styles.count}>
          <strong className={info.count ? undefined : styles.countNone}>{info.count ?? '–'}</strong>
          <span>{t('ledger.available')}</span>
        </span>
        <span className={info.note ? styles.note : styles.expires}>
          {info.note ??
            (info.count && dates.length > 0
              ? t('ledger.resets_expires', { dates: dates.join(', ') })
              : '')}
        </span>
        <span className={styles.actionSlot}>
          {action ? (
            <button
              type="button"
              className={styles.action}
              disabled={action.disabled}
              onClick={action.onClick}
            >
              {action.label}
            </button>
          ) : null}
        </span>
      </span>
    </li>
  );
}

function CodexResetRow({ row, props }: { row: LedgerRow; props: ManualResetsProps }) {
  const { t } = useTranslation();
  const { entry } = row;
  const info = codexInfo(row);
  const canAct =
    props.canUseActions &&
    !entry.file.disabled &&
    row.status !== 'loading' &&
    props.canReset(entry);
  const action = info.count
    ? {
        label: t('codex_quota.reset_button'),
        disabled: !canAct || props.resettingName === entry.file.name,
        onClick: () => props.onReset(entry),
      }
    : null;
  return <ResetRowView row={row} info={info} action={action} props={props} />;
}

function ClaudeResetRow({
  row,
  props,
  onRead,
}: {
  row: LedgerRow;
  props: ManualResetsProps;
  onRead: (key: string, count: number | null) => void;
}) {
  const { t } = useTranslation();
  const { entry } = row;
  const reset = useClaudeResetGrants(
    entry.file,
    row.status !== 'idle',
    !props.canUseActions || entry.file.disabled || row.status === 'loading',
    props.quotaFor(entry),
    () => props.onRefresh(entry)
  );
  const expires = reset.grants
    .filter((grant) => grant.resetsLeft > 0)
    .map((grant) => parseIsoToMs(grant.endsAt))
    .filter((ms): ms is number => ms !== null)
    .sort((a, b) => a - b);
  const info: ResetInfo = {
    count: reset.count,
    expires,
    note: reset.message ? t(`claude_reset.${reset.message}`) : undefined,
  };
  const count = reset.count;

  // The panel sorts and totals every account, so it keeps each row's count.
  useEffect(() => {
    onRead(row.key, count);
  }, [onRead, row.key, count]);

  const retry = reset.buttonLabel === 'retry';
  const action =
    info.count || retry
      ? {
          label: retry ? t('claude_reset.retry') : t('codex_quota.reset_button'),
          disabled: reset.blocked,
          onClick: reset.openDialog,
        }
      : null;
  return (
    <>
      <ResetRowView row={row} info={info} action={action} props={props} />
      <ClaudeResetGrantDialog
        open={reset.open}
        grants={reset.grants}
        selected={reset.selected}
        canRedeemGrant={reset.canRedeemGrant}
        onSelect={reset.selectGrant}
        busy={reset.busy}
        loading={reset.loading}
        pendingGrantId={reset.pendingGrantId}
        message={reset.message}
        name={entry.file.name}
        onConfirm={() => void reset.confirm()}
        onClose={reset.closeDialog}
      />
    </>
  );
}

export function ManualResets(props: ManualResetsProps) {
  const { t } = useTranslation();
  const [claudeCounts, setClaudeCounts] = useState<Record<string, number | null>>({});
  const onRead = useCallback((key: string, count: number | null) => {
    setClaudeCounts((prev) => (prev[key] === count ? prev : { ...prev, [key]: count }));
  }, []);

  const countOf = (row: LedgerRow) =>
    row.entry.type === 'codex' ? codexInfo(row).count : (claudeCounts[row.key] ?? null);
  // Most resets first, so the accounts worth spending from lead.
  const rows = [...props.rows].sort(
    (a, b) =>
      (countOf(b) ?? -1) - (countOf(a) ?? -1) || a.entry.file.name.localeCompare(b.entry.file.name)
  );
  // The total covers only accounts whose resets could be read.
  const read = rows.filter((row) => countOf(row) !== null);
  const available = read.reduce((sum, row) => sum + (countOf(row) ?? 0), 0);

  return (
    <section className={styles.panel} aria-labelledby="manual-resets-title">
      <header className={styles.head}>
        <h2 id="manual-resets-title" className={styles.title}>
          {t('ledger.resets_title')}
        </h2>
        {read.length > 0 ? (
          <span className={styles.summary}>
            {t('ledger.resets_summary', { available, count: read.length })}
          </span>
        ) : null}
      </header>
      <ul className={styles.rows}>
        {rows.map((row) =>
          row.entry.type === 'codex' ? (
            <CodexResetRow key={row.key} row={row} props={props} />
          ) : (
            <ClaudeResetRow key={row.key} row={row} props={props} onRead={onRead} />
          )
        )}
      </ul>
    </section>
  );
}
