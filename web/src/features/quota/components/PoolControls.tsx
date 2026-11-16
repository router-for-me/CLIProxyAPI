import { useId, type ReactElement } from 'react';
import { useTranslation } from 'react-i18next';
import { IconChevronLeft, IconChevronRight } from '@/components/ui/icons';
import { SegmentedControl, type SegmentedOption } from '@/components/ui/SegmentedControl';
import { Select } from '@/components/ui/Select';
import styles from '@/features/quota/components/PoolControls.module.scss';
import {
  BURN_WINDOWS,
  FUTURE_USAGES,
  POOL_METRICS,
  type BurnWindowHours,
  type FutureUsage,
  type PoolMetric,
} from '@/features/quota/pool/constants';
import { formatMonthDay } from '@/features/quota/pool/format';
import type { PoolRange } from '@/features/quota/pool/types';

export interface PoolControlsProps {
  accounts: boolean;
  burnWindow: BurnWindowHours;
  disabled?: boolean;
  futureUsage: FutureUsage;
  locale: string;
  metric: PoolMetric;
  onAccountsChange: (value: boolean) => void;
  onBurnWindowChange: (value: BurnWindowHours) => void;
  onFutureUsageChange: (value: FutureUsage) => void;
  onMetricChange: (value: PoolMetric) => void;
  onNext: () => void;
  onPrev: () => void;
  onToday: () => void;
  range: PoolRange;
  timeZone: string;
}

/** The row above the pool cards: chart metric, burn window, future usage, Accounts and the date range. */
export const PoolControls = ({
  accounts,
  burnWindow,
  disabled = false,
  futureUsage,
  locale,
  metric,
  onAccountsChange,
  onBurnWindowChange,
  onFutureUsageChange,
  onMetricChange,
  onNext,
  onPrev,
  onToday,
  range,
  timeZone,
}: PoolControlsProps): ReactElement => {
  const { t } = useTranslation();
  const burnLabelId = useId();
  const futureLabelId = useId();
  const metricOptions: SegmentedOption<PoolMetric>[] = POOL_METRICS.map((value) => ({
    label: t(`quota_pool.metric_${value}`),
    value,
  }));
  const burnOptions: SegmentedOption<BurnWindowHours>[] = BURN_WINDOWS.map((hours) => ({
    label: t('quota_pool.burn_window', { hours }),
    value: hours,
  }));
  const futureOptions = FUTURE_USAGES.map((value) => ({
    label: t(`quota_pool.future_${value}`),
    value,
  }));
  const rangeText = t('quota_pool.range', {
    end: formatMonthDay(range.end - 1, locale, timeZone),
    start: formatMonthDay(range.start, locale, timeZone),
  });

  const handleFutureUsageChange = (value: string) => {
    const next = FUTURE_USAGES.find((usage) => usage === value);

    if (next) onFutureUsageChange(next);
  };

  return (
    <div className={styles.row}>
      <SegmentedControl
        ariaLabel={t('quota_pool.metric_label')}
        disabled={disabled}
        onChange={onMetricChange}
        options={metricOptions}
        value={metric}
      />
      <div className={styles.pair}>
        <span className={styles.label} id={burnLabelId}>
          {t('quota_pool.burn_label')}
        </span>
        <SegmentedControl
          ariaLabelledBy={burnLabelId}
          disabled={disabled}
          onChange={onBurnWindowChange}
          options={burnOptions}
          value={burnWindow}
        />
      </div>
      <div className={styles.pair}>
        <span className={styles.label} id={futureLabelId}>
          {t('quota_pool.future_label')}
        </span>
        <Select
          ariaLabelledBy={futureLabelId}
          className={styles.futureSelect}
          disabled={disabled}
          fullWidth={false}
          onChange={handleFutureUsageChange}
          options={futureOptions}
          size="sm"
          value={futureUsage}
        />
      </div>
      <button
        aria-pressed={accounts}
        className={styles.toggle}
        disabled={disabled}
        onClick={() => onAccountsChange(!accounts)}
        type="button"
      >
        {t('quota_pool.accounts_toggle')}
      </button>
      <div className={styles.range}>
        <span className={styles.rangeText}>{rangeText}</span>
        <button
          aria-label={t('quota_pool.range_prev')}
          className={styles.iconButton}
          disabled={disabled || !range.canPrev}
          onClick={onPrev}
          type="button"
        >
          <IconChevronLeft size={16} />
        </button>
        <button
          aria-current={range.isToday ? 'date' : undefined}
          className={styles.today}
          disabled={disabled}
          onClick={onToday}
          type="button"
        >
          {t('quota_pool.range_today')}
        </button>
        <button
          aria-label={t('quota_pool.range_next')}
          className={styles.iconButton}
          disabled={disabled || !range.canNext}
          onClick={onNext}
          type="button"
        >
          <IconChevronRight size={16} />
        </button>
      </div>
    </div>
  );
};
