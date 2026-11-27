import { useTranslation } from 'react-i18next';
import type { CopilotQuotaState } from '@/types';
import { QuotaMeter } from '../../components/QuotaMeter';
import type { QuotaBodyProps } from '../../types';

export function CopilotQuotaBody({ quota, classes }: QuotaBodyProps<CopilotQuotaState>) {
  const { t } = useTranslation();
  const data = quota.data;
  if (!data) return <div className={classes.quotaMessage}>{t('copilot_quota.unavailable')}</div>;

  const label = t('copilot_quota.premium');
  const percent = Number(data.percentRemaining.toFixed(1));
  return (
    <div className={classes.quotaRow}>
      <div className={classes.quotaRowHeader}>
        <span className={classes.quotaModel}>{label}</span>
        <div className={classes.quotaMeta}>
          <span className={classes.quotaPercent}>{t('copilot_quota.remaining', { percent })}</span>
        </div>
      </div>
      <div
        role="meter"
        aria-label={label}
        aria-valuemin={0}
        aria-valuemax={100}
        aria-valuenow={percent}
      >
        <QuotaMeter percent={percent} classes={classes} index={0} />
      </div>
      {data.remaining !== null && data.entitlement !== null && data.entitlement > 0 && (
        <div className={classes.quotaAmount}>
          {data.remaining} / {data.entitlement}
        </div>
      )}
    </div>
  );
}
