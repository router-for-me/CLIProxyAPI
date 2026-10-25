import { useTranslation } from 'react-i18next';
import type { KiroQuotaState } from '@/types';
import { CountQuotaBody } from '../../components/CountQuotaBody';
import type { QuotaBodyProps } from '../../types';

export function KiroQuotaBody({ quota, classes }: QuotaBodyProps<KiroQuotaState>) {
  const { t } = useTranslation();
  return (
    <>
      {quota.plan && (
        <div className={classes.codexPlan}>
          <span className={classes.codexPlanItem}>
            <span className={classes.codexPlanLabel}>{t('kiro_quota.plan')}</span>
            <span className={classes.codexPlanValue}>{quota.plan}</span>
          </span>
        </div>
      )}
      <CountQuotaBody rows={quota.rows ?? []} provider="kiro" classes={classes} />
    </>
  );
}
