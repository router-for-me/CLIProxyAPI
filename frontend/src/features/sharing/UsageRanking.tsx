import { memo, useMemo, useState } from 'react';
import { useTranslation } from 'react-i18next';
import { formatCompactNumber } from '@/utils/format';
import { number } from './hooks';
import type { OverviewRankRow } from './types';
import type { UsageDimension } from './usageBreakdown';
import { percent } from './usageFormat';
import styles from './Usage.module.scss';

const COMPACT_COUNT = 5;

export const UsageRanking = memo(function UsageRanking({
  rank,
  dimensions,
  active,
  compact,
  onSelect,
}: {
  /** Server-computed top rows per dimension; empty while loading. */
  rank?: Partial<Record<UsageDimension, OverviewRankRow[]>>;
  /** Phones show the top entries with an expand toggle instead of a scroll box. */
  compact: boolean;
  dimensions: UsageDimension[];
  /** Currently applied value per dimension, highlighted in the list. */
  active: Partial<Record<UsageDimension, string>>;
  onSelect: (dimension: UsageDimension, value: string) => void;
}) {
  const { t } = useTranslation();
  const [dimension, setDimension] = useState<UsageDimension>(dimensions[0]);
  const current = dimensions.includes(dimension) ? dimension : dimensions[0];

  const entries = useMemo(() => rank?.[current] ?? [], [rank, current]);
  const metric = entries.some((entry) => entry.value.total_tokens > 0) ? 'tokens' : 'calls';
  const valueOf = (row: OverviewRankRow) =>
    metric === 'tokens' ? row.value.total_tokens : row.value.executions;
  const sum = entries.reduce((total, entry) => total + valueOf(entry), 0) || 1;
  const top = entries.length ? valueOf(entries[0]) || 1 : 1;
  const [expanded, setExpanded] = useState(false);
  const visible = compact && !expanded ? entries.slice(0, COMPACT_COUNT) : entries;

  const label = (row: OverviewRankRow) =>
    row.label ? (current === 'consumer' ? `@${row.label}` : row.label) : t('sharing.unknown');

  return (
    <section className={styles.panel} aria-labelledby="usage-ranking-title">
      <header className={styles.panelHead}>
        <h2 className={styles.panelTitle} id="usage-ranking-title">
          {t('sharing.ranking_title')}
        </h2>
        {dimensions.length > 1 && (
          <div className={styles.miniTabs} role="tablist" aria-label={t('sharing.ranking_title')}>
            {dimensions.map((item) => (
              <button
                key={item}
                type="button"
                role="tab"
                aria-selected={current === item}
                className={`${styles.miniTab} ${current === item ? styles.miniTabActive : ''}`}
                onClick={() => {
                  setDimension(item);
                  setExpanded(false);
                }}
              >
                {t(`sharing.ranking_${item}`)}
              </button>
            ))}
          </div>
        )}
      </header>
      {!entries.length ? (
        <p className={styles.panelEmpty}>{t('sharing.trend_empty')}</p>
      ) : (
        <ol className={styles.rankList}>
          {visible.map((row, index) => {
            const value = row.value;
            const share = (valueOf(row) / sum) * 100;
            const success =
              value.executions > 0
                ? ((value.executions - value.failures) / value.executions) * 100
                : null;
            const selected = active[current] !== undefined && active[current] === row.key;
            const latencyCount = value.latency_count ?? 0;
            return (
              <li key={row.key || '∅'}>
                <button
                  type="button"
                  className={`${styles.rankRow} ${selected ? styles.rankRowActive : ''}`}
                  onClick={() => onSelect(current, row.key)}
                  aria-pressed={selected}
                  title={t('sharing.ranking_filter_hint')}
                >
                  <span className={styles.rankIndex}>{index + 1}</span>
                  <span className={styles.rankBody}>
                    <span className={styles.rankTop}>
                      <span
                        className={`${styles.rankName} ${current !== 'consumer' ? styles.mono : ''}`}
                      >
                        {label(row)}
                      </span>
                      <span className={styles.rankValue} title={number(valueOf(row))}>
                        {metric === 'tokens'
                          ? formatCompactNumber(value.total_tokens)
                          : number(value.executions)}
                        <span className={styles.rankShare}>{percent(share)}</span>
                      </span>
                    </span>
                    <span className={styles.rankTrack} aria-hidden="true">
                      <span
                        className={styles.rankBar}
                        style={{ width: `${Math.max(1.5, (valueOf(row) / top) * 100)}%` }}
                      />
                    </span>
                    <span className={styles.rankMeta}>
                      <span>{t('sharing.ranking_calls', { count: number(value.executions) })}</span>
                      {success !== null && (
                        <span className={success < 95 ? styles.rankMetaWarn : undefined}>
                          {t('sharing.ranking_success', { rate: percent(success) })}
                        </span>
                      )}
                      {latencyCount > 0 && (
                        <span>
                          {t('sharing.ranking_latency', {
                            value: `${((value.latency_ms ?? 0) / latencyCount / 1000).toFixed(1)}s`,
                          })}
                        </span>
                      )}
                    </span>
                  </span>
                </button>
              </li>
            );
          })}
        </ol>
      )}
      {compact && entries.length > COMPACT_COUNT && (
        <button
          type="button"
          className={styles.panelMore}
          aria-expanded={expanded}
          onClick={() => setExpanded((value) => !value)}
        >
          {expanded
            ? t('sharing.ranking_collapse')
            : t('sharing.ranking_show_all', { count: number(entries.length) })}
        </button>
      )}
    </section>
  );
});
