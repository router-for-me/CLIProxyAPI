import { memo, useMemo, useState } from 'react';
import { useTranslation } from 'react-i18next';
import { formatCompactNumber } from '@/utils/format';
import { number } from './hooks';
import type { TrendGranularity, TrendPoint } from './usageBreakdown';
import styles from './Usage.module.scss';

type TrendMetric = 'tokens' | 'calls';

const pad = (value: number) => String(value).padStart(2, '0');

const pointLabel = (start: number, granularity: TrendGranularity, withDate: boolean) => {
  const date = new Date(start);
  const day = `${pad(date.getMonth() + 1)}-${pad(date.getDate())}`;
  if (granularity === 'day') return day;
  const hour = `${pad(date.getHours())}:00`;
  return withDate ? `${day} ${hour}` : hour;
};

export const UsageTrend = memo(function UsageTrend({
  points,
  granularity,
}: {
  points: TrendPoint[];
  granularity: TrendGranularity;
}) {
  const { t } = useTranslation();
  const [metric, setMetric] = useState<TrendMetric>('tokens');
  const [active, setActive] = useState<number | null>(null);

  const hasTokens = points.some((point) => point.tokens > 0);
  const effective: TrendMetric = metric === 'tokens' && !hasTokens ? 'calls' : metric;
  const value = (point: TrendPoint) => (effective === 'tokens' ? point.tokens : point.calls);
  const max = useMemo(
    () =>
      Math.max(1, ...points.map((point) => (effective === 'tokens' ? point.tokens : point.calls))),
    [points, effective]
  );
  // Hourly series crossing midnight need the date, otherwise "00:00" is ambiguous.
  const spansDays =
    granularity === 'hour' &&
    points.length > 1 &&
    new Date(points[0].start).getDate() !== new Date(points[points.length - 1].start).getDate();

  const focus = active !== null ? points[active] : null;
  const totals = useMemo(
    () =>
      points.reduce(
        (sum, point) => ({
          calls: sum.calls + point.calls,
          tokens: sum.tokens + point.tokens,
          failures: sum.failures + point.failures,
        }),
        { calls: 0, tokens: 0, failures: 0 }
      ),
    [points]
  );
  const shown = focus ?? totals;

  // Show ~6 axis ticks regardless of series length.
  const tickEvery = Math.max(1, Math.ceil(points.length / 6));

  return (
    <section className={styles.panel} aria-labelledby="usage-trend-title">
      <header className={styles.panelHead}>
        <h2 className={styles.panelTitle} id="usage-trend-title">
          {t(granularity === 'hour' ? 'sharing.trend_hourly' : 'sharing.trend_daily')}
        </h2>
        <div className={styles.miniTabs} role="tablist" aria-label={t('sharing.trend_metric')}>
          {(['tokens', 'calls'] as const).map((item) => (
            <button
              key={item}
              type="button"
              role="tab"
              aria-selected={effective === item}
              disabled={item === 'tokens' && !hasTokens}
              className={`${styles.miniTab} ${effective === item ? styles.miniTabActive : ''}`}
              onClick={() => setMetric(item)}
            >
              {t(item === 'tokens' ? 'sharing.pill_tokens' : 'sharing.requests_label')}
            </button>
          ))}
        </div>
      </header>
      {!points.length ? (
        <p className={styles.panelEmpty}>{t('sharing.trend_empty')}</p>
      ) : (
        <>
          <div className={styles.trendReadout} aria-live="polite">
            <span className={styles.trendReadoutLabel}>
              {focus ? pointLabel(focus.start, granularity, true) : t('sharing.trend_range_total')}
            </span>
            <span className={styles.trendReadoutValue}>
              <strong title={number(shown.tokens)}>{formatCompactNumber(shown.tokens)}</strong>{' '}
              Token
            </span>
            <span className={styles.trendReadoutValue}>
              <strong>{number(shown.calls)}</strong> {t('sharing.trend_requests_unit')}
            </span>
            {shown.failures > 0 && (
              <span className={`${styles.trendReadoutValue} ${styles.trendReadoutFail}`}>
                <strong>{number(shown.failures)}</strong> {t('sharing.trend_failures_unit')}
              </span>
            )}
          </div>
          <div
            className={styles.trendChart}
            onMouseLeave={() => setActive(null)}
            role="img"
            aria-label={t('sharing.trend_aria', {
              count: points.length,
              tokens: number(totals.tokens),
              calls: number(totals.calls),
            })}
          >
            <div className={styles.trendGrid} aria-hidden="true">
              <span>{formatCompactNumber(max)}</span>
              <span>{formatCompactNumber(max / 2)}</span>
              <span>0</span>
            </div>
            <div className={styles.trendBars}>
              {points.map((point, index) => {
                const height = (value(point) / max) * 100;
                const failShare =
                  effective === 'calls' && point.calls > 0
                    ? (point.failures / point.calls) * 100
                    : 0;
                return (
                  <button
                    type="button"
                    key={point.start}
                    className={`${styles.trendBar} ${active === index ? styles.trendBarActive : ''}`}
                    onMouseEnter={() => setActive(index)}
                    onFocus={() => setActive(index)}
                    onBlur={() => setActive(null)}
                    onClick={() => setActive(active === index ? null : index)}
                    aria-label={`${pointLabel(point.start, granularity, true)} · ${number(
                      point.tokens
                    )} Token · ${number(point.calls)} ${t('sharing.trend_requests_unit')}`}
                  >
                    <span
                      className={styles.trendFill}
                      style={{
                        height: `${value(point) > 0 ? Math.max(2, height) : 0}%`,
                        animationDelay: `${Math.min(index * 8, 320)}ms`,
                      }}
                    >
                      {failShare > 0 && (
                        <span className={styles.trendFail} style={{ height: `${failShare}%` }} />
                      )}
                    </span>
                  </button>
                );
              })}
            </div>
          </div>
          <div className={styles.trendAxis} aria-hidden="true">
            {points.map((point, index) => (
              <span key={point.start}>
                {index % tickEvery === 0
                  ? pointLabel(point.start, granularity, spansDays && index === 0)
                  : ''}
              </span>
            ))}
          </div>
        </>
      )}
    </section>
  );
});
