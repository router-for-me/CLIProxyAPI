import { memo, useMemo, useState } from 'react';
import { useTranslation } from 'react-i18next';
import { formatCompactNumber } from '@/utils/format';
import { number } from './hooks';
import type { Summary } from './types';
import { rankMetric, rankUsage, type UsageDimension, type UsageRankEntry } from './usageBreakdown';
import { percent } from './usageFormat';
import styles from './Usage.module.scss';

const COMPACT_COUNT = 5;

export const UsageRanking = memo(function UsageRanking({
  groups,
  dimensions,
  active,
  compact,
  onSelect,
}: {
  groups: Summary[];
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

  const entries = useMemo(() => rankUsage(groups, current), [groups, current]);
  const metric = rankMetric(entries);
  const valueOf = (entry: UsageRankEntry) =>
    metric === 'tokens' ? entry.total_tokens : entry.executions;
  const sum = entries.reduce((total, entry) => total + valueOf(entry), 0) || 1;
  const top = entries.length ? valueOf(entries[0]) || 1 : 1;
  const [expanded, setExpanded] = useState(false);
  const visible = compact && !expanded ? entries.slice(0, COMPACT_COUNT) : entries;

  const label = (entry: UsageRankEntry) =>
    entry.label ? (current === 'consumer' ? `@${entry.label}` : entry.label) : t('sharing.unknown');

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
          {visible.map((entry, index) => {
            const share = (valueOf(entry) / sum) * 100;
            const success =
              entry.executions > 0
                ? ((entry.executions - entry.failures) / entry.executions) * 100
                : null;
            const selected = active[current] !== undefined && active[current] === entry.key;
            return (
              <li key={entry.key || '∅'}>
                <button
                  type="button"
                  className={`${styles.rankRow} ${selected ? styles.rankRowActive : ''}`}
                  onClick={() => onSelect(current, entry.key)}
                  aria-pressed={selected}
                  title={t('sharing.ranking_filter_hint')}
                >
                  <span className={styles.rankIndex}>{index + 1}</span>
                  <span className={styles.rankBody}>
                    <span className={styles.rankTop}>
                      <span
                        className={`${styles.rankName} ${current !== 'consumer' ? styles.mono : ''}`}
                      >
                        {label(entry)}
                      </span>
                      <span className={styles.rankValue} title={number(valueOf(entry))}>
                        {metric === 'tokens'
                          ? formatCompactNumber(entry.total_tokens)
                          : number(entry.executions)}
                        <span className={styles.rankShare}>{percent(share)}</span>
                      </span>
                    </span>
                    <span className={styles.rankTrack} aria-hidden="true">
                      <span
                        className={styles.rankBar}
                        style={{ width: `${Math.max(1.5, (valueOf(entry) / top) * 100)}%` }}
                      />
                    </span>
                    <span className={styles.rankMeta}>
                      <span>{t('sharing.ranking_calls', { count: number(entry.executions) })}</span>
                      {success !== null && (
                        <span className={success < 95 ? styles.rankMetaWarn : undefined}>
                          {t('sharing.ranking_success', { rate: percent(success) })}
                        </span>
                      )}
                      {entry.latency_count > 0 && (
                        <span>
                          {t('sharing.ranking_latency', {
                            value: `${(entry.latency_ms / entry.latency_count / 1000).toFixed(1)}s`,
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
