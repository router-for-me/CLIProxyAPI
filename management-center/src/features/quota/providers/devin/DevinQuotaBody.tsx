import { useTranslation } from 'react-i18next';
import type { DevinQuotaState } from '@/types';
import { useNow } from '@/hooks/useNow';
import { buildResetDisplay } from '@/utils/quota';
import { QuotaMeter } from '../../components/QuotaMeter';
import { QuotaResetLabel } from '../../components/QuotaResetLabel';
import { collectQuotaRowInstants, pickUrgentRowId } from '../../resetSchedule';
import type { QuotaBodyProps } from '../../types';

export function DevinQuotaBody({ quota, classes }: QuotaBodyProps<DevinQuotaState>) {
  const { t, i18n } = useTranslation();
  const now = useNow();
  const locale = i18n.resolvedLanguage;
  const urgentRow = pickUrgentRowId(collectQuotaRowInstants('devin', quota), now);
  const planEnd = buildResetDisplay(null, quota.planEndMs, now, locale);

  return (
    <>
      {(quota.plan || planEnd) && (
        <div className={classes.codexPlan}>
          {quota.plan && (
            <span className={classes.codexPlanItem}>
              <span className={classes.codexPlanLabel}>{t('devin_quota.plan_label')}</span>
              <span className={classes.codexPlanValue}>{quota.plan}</span>
            </span>
          )}
          {planEnd && (
            <span className={classes.codexPlanItem}>
              <span className={classes.codexPlanLabel}>{t('devin_quota.plan_end')}</span>
              <QuotaResetLabel display={planEnd} classes={classes} />
            </span>
          )}
        </div>
      )}
      {quota.windows.map((window, index) => {
        const reset = buildResetDisplay(null, window.resetAtMs, now, locale);
        const soon = window.id === urgentRow;
        const label = t(`devin_quota.${window.id}`);
        return (
          <div key={window.id} className={classes.quotaRow}>
            <div className={classes.quotaRowHeader}>
              <span className={classes.quotaModel}>{label}</span>
              <div className={classes.quotaMeta}>
                <span className={classes.quotaPercent}>
                  {window.remainingPercent === null
                    ? t('devin_quota.unavailable')
                    : `${window.remainingPercent}%`}
                </span>
                {reset ? (
                  <QuotaResetLabel display={reset} classes={classes} soon={soon} />
                ) : (
                  <span className={classes.quotaReset}>{t('devin_quota.reset_unknown')}</span>
                )}
              </div>
            </div>
            <div
              role={window.remainingPercent === null ? undefined : 'meter'}
              aria-label={window.remainingPercent === null ? undefined : label}
              aria-valuemin={window.remainingPercent === null ? undefined : 0}
              aria-valuemax={window.remainingPercent === null ? undefined : 100}
              aria-valuenow={window.remainingPercent ?? undefined}
            >
              <QuotaMeter percent={window.remainingPercent} classes={classes} index={index} />
            </div>
          </div>
        );
      })}
    </>
  );
}
