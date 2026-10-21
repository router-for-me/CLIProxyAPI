/**
 * Token cost distribution for a credential.
 *
 * Renders the canonical, non-overlapping split between input tokens (uncached,
 * cache read, cache write) and output tokens (non-reasoning, and thinking /
 * reasoning when the provider reports it).
 */

import { useTranslation } from 'react-i18next';
import { formatCompactNumber } from '@/utils/format';
import type { TokenCostBreakdown } from '../tokenUsage';
import styles from './TokenCostDistribution.module.scss';

export type TokenCostDistributionProps = {
  breakdown: TokenCostBreakdown;
  /** Compact variant keeps the bar and total but drops the legend. */
  compact?: boolean;
};

type Segment = {
  key: string;
  label: string;
  value: number;
  tone: string;
};

export function TokenCostDistribution({ breakdown, compact = false }: TokenCostDistributionProps) {
  const { t } = useTranslation();

  const segments: Segment[] = [
    {
      key: 'input',
      label: t('quota_tokens.input'),
      value: breakdown.inputUncached,
      tone: styles.toneInput,
    },
    {
      key: 'cache_read',
      label: t('quota_tokens.cache_read'),
      value: breakdown.inputCacheRead,
      tone: styles.toneCacheRead,
    },
    {
      key: 'cache_write',
      label: t('quota_tokens.cache_write'),
      value: breakdown.inputCacheWrite,
      tone: styles.toneCacheWrite,
    },
    {
      key: 'output',
      label: t('quota_tokens.output'),
      value: breakdown.outputNonReasoning,
      tone: styles.toneOutput,
    },
  ];

  // Thinking tokens are only shown when the provider actually reports them.
  if (breakdown.outputReasoning > 0) {
    segments.push({
      key: 'thinking',
      label: t('quota_tokens.thinking'),
      value: breakdown.outputReasoning,
      tone: styles.toneThinking,
    });
  }

  const visible = segments.filter((segment) => segment.value > 0);
  const total = Math.max(
    breakdown.total,
    visible.reduce((sum, segment) => sum + segment.value, 0)
  );
  if (total <= 0) return null;

  return (
    <div className={`${styles.root} ${compact ? styles.compact : ''}`.trim()}>
      <div className={styles.header}>
        <span className={styles.title}>{t('quota_tokens.title')}</span>
        <span className={styles.total}>{formatCompactNumber(total)}</span>
      </div>
      <div className={styles.bar} role="img" aria-label={t('quota_tokens.title')}>
        {visible.map((segment) => (
          <span
            key={segment.key}
            className={`${styles.segment} ${segment.tone}`}
            style={{ width: `${(segment.value / total) * 100}%` }}
          />
        ))}
      </div>
      <ul className={styles.legend}>
        {visible.map((segment) => (
          <li key={segment.key} className={styles.legendItem}>
            <span className={`${styles.dot} ${segment.tone}`} aria-hidden="true" />
            <span className={styles.legendLabel}>{segment.label}</span>
            <span className={styles.legendValue}>{formatCompactNumber(segment.value)}</span>
          </li>
        ))}
      </ul>
    </div>
  );
}
