import { useTranslation } from 'react-i18next';
import type { QoderQuotaState } from '@/types';
import { CountQuotaBody } from '../../components/CountQuotaBody';
import type { QuotaBodyProps } from '../../types';

export function QoderQuotaBody({ quota, classes }: QuotaBodyProps<QoderQuotaState>) {
  const { t } = useTranslation();
  return (
    <>
      {quota.plan && (
        <div className={classes.codexPlan}>
          <span className={classes.codexPlanItem}>
            <span className={classes.codexPlanLabel}>{t('qoder_quota.plan')}</span>
            <span className={classes.codexPlanValue}>{quota.plan}</span>
          </span>
        </div>
      )}
      <CountQuotaBody rows={quota.rows ?? []} provider="qoder" classes={classes} />
    </>
  );
}
