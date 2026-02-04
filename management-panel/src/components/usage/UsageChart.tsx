import { useTranslation } from 'react-i18next';
import type { ChartOptions } from 'chart.js';
import { Line } from 'react-chartjs-2';
import { Card } from '@/components/ui/Card';
import { Button } from '@/components/ui/Button';
import type { ChartData } from '@/utils/usage';
import { getHourChartMinWidth } from '@/utils/usage/chartConfig';
import styles from '@/pages/UsagePage.module.scss';

export interface UsageChartProps {
  title: string;
  period: 'hour' | 'day';
  onPeriodChange: (period: 'hour' | 'day') => void;
  chartData: ChartData;
  chartOptions: ChartOptions<'line'>;
  loading: boolean;
  isMobile: boolean;
  emptyText: string;
}

export function UsageChart({
  title,
  period,
  onPeriodChange,
  chartData,
  chartOptions,
  loading,
  isMobile,
  emptyText
}: UsageChartProps) {
  const { t } = useTranslation();

  return (
    <Card
      title={title}
      extra={
        <div className={styles.periodButtons}>
          <Button
            variant={period === 'hour' ? 'primary' : 'secondary'}
            size="sm"
            onClick={() => onPeriodChange('hour')}
          >
            {t('usage_stats.by_hour')}
          </Button>
          <Button
            variant={period === 'day' ? 'primary' : 'secondary'}
            size="sm"
            onClick={() => onPeriodChange('day')}
          >
            {t('usage_stats.by_day')}
          </Button>
        </div>
      }
    >
      {loading ? (
        <div className={styles.hint}>{t('common.loading')}</div>
      ) : chartData.labels.length > 0 ? (
        <div className={styles.chartWrapper}>
          <div className={styles.chartLegend} aria-label="Chart legend">
            {chartData.datasets.map((dataset, index) => (
              <div
                key={`${dataset.label}-${index}`}
                className={styles.legendItem}
                title={dataset.label}
              >
                <span className={styles.legendDot} style={{ backgroundColor: dataset.borderColor }} />
                <span className={styles.legendLabel}>{dataset.label}</span>
              </div>
            ))}
          </div>
          <div className={styles.chartArea}>
            <div className={styles.chartScroller}>
              <div
                className={styles.chartCanvas}
                style={
                  period === 'hour'
                    ? { minWidth: getHourChartMinWidth(chartData.labels.length, isMobile) }
                    : undefined
                }
              >
                <Line data={chartData} options={chartOptions} />
              </div>
            </div>
          </div>
        </div>
      ) : (
        <div className={styles.hint}>{emptyText}</div>
      )}
    </Card>
  );
}
