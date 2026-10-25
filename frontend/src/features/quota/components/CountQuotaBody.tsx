import { useMemo } from 'react';
import { useTranslation } from 'react-i18next';
import type { CountQuotaRow } from '@/types';
import { buildResetDisplay, formatKimiResetHint } from '@/utils/quota';
import { useNow } from '@/hooks/useNow';
import { QuotaMeter } from './QuotaMeter';
import { QuotaResetLabel } from './QuotaResetLabel';
import { collectQuotaRowInstants, pickUrgentRowId } from '../resetSchedule';
import type { QuotaClassMap } from '../types';

export function CountQuotaBody({
  rows,
  provider,
  classes,
}: {
  rows: CountQuotaRow[];
  provider: 'kimi' | 'kiro';
  classes: QuotaClassMap;
}) {
  const { t, i18n } = useTranslation();
  const now = useNow();
  const soonestRowId = useMemo(
    () => pickUrgentRowId(collectQuotaRowInstants(provider, { status: 'success', rows }), now),
    [rows, provider, now]
  );
  if (rows.length === 0) {
    return <div className={classes.quotaMessage}>{t(`${provider}_quota.empty_data`)}</div>;
  }
  return (
    <>
      {rows.map((row, index) => {
        const remaining =
          row.limit > 0
            ? Math.max(0, Math.min(100, Math.round(((row.limit - row.used) / row.limit) * 100)))
            : row.used > 0
              ? 0
              : null;
        const label = row.labelKey ? t(row.labelKey, row.labelParams ?? {}) : (row.label ?? '');
        const resetDisplay = buildResetDisplay(
          row.resetAtMs == null ? formatKimiResetHint(t, row.resetHint) : null,
          row.resetAtMs,
          now,
          i18n.resolvedLanguage
        );
        const soon = row.id === soonestRowId;
        return (
          <div
            key={row.id}
            className={classes.quotaRow}
            title={soon ? t('quota_management.soonest_row_hint') : undefined}
          >
            <div className={classes.quotaRowHeader}>
              <span className={classes.quotaModel}>{label}</span>
              <div className={classes.quotaMeta}>
                <span className={classes.quotaPercent}>
                  {remaining === null ? '--' : `${remaining}%`}
                </span>
                {resetDisplay && (
                  <QuotaResetLabel display={resetDisplay} classes={classes} soon={soon} />
                )}
              </div>
            </div>
            <div
              role={remaining === null ? undefined : 'meter'}
              aria-label={label}
              aria-valuemin={remaining === null ? undefined : 0}
              aria-valuemax={remaining === null ? undefined : 100}
              aria-valuenow={remaining ?? undefined}
            >
              <QuotaMeter percent={remaining} classes={classes} index={index} />
            </div>
          </div>
        );
      })}
    </>
  );
}
