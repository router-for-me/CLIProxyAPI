import { useTranslation } from 'react-i18next';
import type { PluginQuotaMetric, PluginQuotaState } from '@/types';
import { QuotaMeter } from '../../components/QuotaMeter';
import type { QuotaBodyProps } from '../../types';

const formatReset = (value: string | undefined): string => {
  if (!value) return '-';
  const date = new Date(value);
  return Number.isNaN(date.getTime()) ? value : date.toLocaleString();
};

const formatMetric = (metric: PluginQuotaMetric): string => {
  const value =
    metric.format === 'currency' && metric.currency
      ? new Intl.NumberFormat(undefined, { style: 'currency', currency: metric.currency }).format(metric.value)
      : new Intl.NumberFormat(undefined, { maximumFractionDigits: 2 }).format(metric.value);
  return metric.unit ? `${value} ${metric.unit}` : value;
};

export function PluginQuotaBody({ quota, classes }: QuotaBodyProps<PluginQuotaState>) {
  const { t } = useTranslation();
  const plan = quota.subscription?.tierName || quota.subscription?.plan || quota.subscription?.tierId;

  return (
    <>
      {(plan || quota.summary.length > 0) && (
        <div className={classes.codexPlan}>
          {plan && (
            <span className={classes.codexPlanItem}>
              <span className={classes.codexPlanLabel}>{t('plugin_quota.plan_label')}</span>
              <span className={classes.codexPlanValue}>{plan}</span>
            </span>
          )}
          {quota.summary.map((metric) => (
            <span key={metric.key} className={classes.codexPlanItem}>
              <span className={classes.codexPlanLabel}>{metric.label}</span>
              <span className={classes.codexPlanValue}>{formatMetric(metric)}</span>
            </span>
          ))}
        </div>
      )}
      {!plan && quota.summary.length === 0 && quota.groups.length === 0 ? (
        <div className={classes.quotaMessage}>{t('plugin_quota.empty_data')}</div>
      ) : (
        quota.groups.map((group) => (
          <div key={group.id} className={classes.antigravityQuotaGroup}>
            <div className={classes.antigravityQuotaGroupHeader}>
              <span className={classes.antigravityQuotaGroupTitle}>{group.label}</span>
              {group.description && (
                <span className={classes.antigravityQuotaGroupDescription}>{group.description}</span>
              )}
            </div>
            {group.buckets.map((bucket, index) => {
              const percent = Math.round(Math.max(0, Math.min(1, bucket.remainingFraction)) * 100);
              return (
                <div key={bucket.id} className={classes.quotaRow}>
                  <div className={classes.quotaRowHeader}>
                    <span className={classes.quotaModel} title={bucket.description}>
                      {bucket.label}
                    </span>
                    <div className={classes.quotaMeta}>
                      <span className={classes.quotaPercent}>
                        {t('plugin_quota.remaining_percent', { percent })}
                      </span>
                      <span className={classes.quotaReset}>{formatReset(bucket.resetTime)}</span>
                    </div>
                  </div>
                  <QuotaMeter percent={percent} classes={classes} index={index} />
                </div>
              );
            })}
          </div>
        ))
      )}
    </>
  );
}
