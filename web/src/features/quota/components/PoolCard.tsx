import type { TFunction } from 'i18next';
import { useId, type CSSProperties, type ReactElement, type ReactNode } from 'react';
import { useTranslation } from 'react-i18next';
import { getAuthFileIcon, getTypeLabel } from '@/features/authFiles/constants';
import { MaskedName } from '@/features/quota/components/MaskedName';
import styles from '@/features/quota/components/PoolCard.module.scss';
import { PoolChart } from '@/features/quota/components/PoolChart';
import type { PoolMetric, PoolProvider } from '@/features/quota/pool/constants';
import {
  formatMonthDay,
  formatPoolPercent,
  formatRate,
  formatWhen,
  splitDuration,
} from '@/features/quota/pool/format';
import type { PoolCardModel } from '@/features/quota/pool/types';
import type { ResolvedTheme } from '@/types';
import { getQuotaDisplayName } from '@/utils/quota/identity';
import { MINUTE_MS } from '@/utils/time/durations';

export interface PoolCardProps {
  /** Fixed chart width for tests and static rendering. */
  chartWidth?: number;
  locale: string;
  metric: PoolMetric;
  model: PoolCardModel;
  resolvedTheme: ResolvedTheme;
  showEmails: boolean;
  timeZone: string;
}

interface StatsProps {
  locale: string;
  model: PoolCardModel;
  showEmails: boolean;
  timeZone: string;
}

const SERIES_COLOR: Record<PoolProvider, string> = {
  claude: 'var(--viz-claude)',
  codex: 'var(--viz-codex)',
};

/** "15h 49m"-style text, or null when the duration rounds to zero minutes. */
const formatDuration = (t: TFunction, ms: number): string | null => {
  const { d, h, m } = splitDuration(ms);

  if (d >= 1) return t('quota_pool.duration_dh', { d, h });
  if (h >= 1) return t('quota_pool.duration_hm', { h, m });
  if (m >= 1) return t('quota_pool.duration_m', { m });

  return null;
};

const Stat = ({ children, label }: { children: ReactNode; label: string }): ReactElement => (
  <div>
    <dt>{label}</dt>
    <dd>{children}</dd>
  </div>
);

const Unknown = (): ReactElement => {
  const { t } = useTranslation();

  return <span title={t('quota_pool.needs_history')}>{t('quota_pool.unknown')}</span>;
};

const PoolStats = ({ locale, model, showEmails, timeZone }: StatsProps): ReactElement => {
  const { t } = useTranslation();
  const { burn, now, stats } = model;
  const notBy = (horizonEnd: number) => (
    <span className={styles.muted}>
      {t('quota_pool.not_by', { date: formatMonthDay(horizonEnd, locale, timeZone) })}
    </span>
  );

  const renderPoolEmpty = (): ReactNode => {
    const stat = stats.poolEmpty;

    switch (stat.kind) {
      case 'at':
        return formatWhen(stat.at, now, locale, timeZone);
      case 'now':
        return t('quota_pool.empty_now');
      case 'never':
        return notBy(stat.horizonEnd);
      case 'unknown':
        return <Unknown />;
    }
  };

  const renderFirstEmpty = (): ReactNode => {
    const stat = stats.firstEmpty;

    if (stat.kind === 'never') return notBy(stat.horizonEnd);
    if (stat.kind === 'unknown') return <Unknown />;

    const member = model.members.find(({ key }) => key === stat.key);
    // stat.at is a whole minute; count from the minute now shows, as the clock text does.
    const nowMinute = Math.floor(now / MINUTE_MS) * MINUTE_MS;
    const duration = stat.kind === 'at' ? formatDuration(t, stat.at - nowMinute) : null;

    return (
      <>
        {member && (
          <MaskedName
            label={getQuotaDisplayName(member.file)}
            masked={!showEmails}
            ordinal={member.ordinal}
          />
        )}{' '}
        {duration ? t('quota_pool.in_duration', { duration }) : t('quota_pool.now_short')}
      </>
    );
  };

  const renderNextReset = (): ReactNode => {
    const stat = stats.nextReset;

    if (stat.kind === 'none') return <Unknown />;

    const gainKey = stat.lowerBound ? 'quota_pool.gain_at_least' : 'quota_pool.gain';

    return (
      <>
        <strong className={styles.gain}>{t(gainKey, { value: stat.gain })}</strong>{' '}
        <span className={styles.muted}>{formatWhen(stat.at, now, locale, timeZone)}</span>
      </>
    );
  };

  const renderBurn = (): ReactNode => {
    if (burn.value === null) return <Unknown />;

    const value = formatRate(burn.value);

    if (!burn.approximate) return t('quota_pool.burn_value', { value });

    const duration = formatDuration(t, burn.coveredMs) ?? t('quota_pool.now_short');

    return (
      <span title={t('quota_pool.burn_approx_hint', { duration })}>
        {t('quota_pool.burn_approx', { value })}
      </span>
    );
  };

  return (
    <dl className={styles.stats}>
      <Stat label={t('quota_pool.stat_pool_empty')}>{renderPoolEmpty()}</Stat>
      <Stat label={t('quota_pool.stat_first_empty')}>{renderFirstEmpty()}</Stat>
      <Stat label={t('quota_pool.stat_next_reset')}>{renderNextReset()}</Stat>
      <Stat label={t('quota_pool.stat_burn', { hours: burn.hours })}>{renderBurn()}</Stat>
    </dl>
  );
};

/** One provider's pooled quota: headline, projected stats and the pool chart. */
export const PoolCard = ({
  chartWidth,
  locale,
  metric,
  model,
  resolvedTheme,
  showEmails,
  timeZone,
}: PoolCardProps): ReactElement => {
  const { t } = useTranslation();
  const titleId = useId();
  const { provider } = model;
  const iconSrc = getAuthFileIcon(provider, resolvedTheme);
  const cardStyle = { '--series': SERIES_COLOR[provider] } as CSSProperties;

  return (
    <section aria-labelledby={titleId} className={styles.card} style={cardStyle}>
      <header className={styles.header}>
        {iconSrc && <img alt="" className={styles.icon} src={iconSrc} />}
        <h2 className={styles.name} id={titleId}>
          {getTypeLabel(t, provider)}
        </h2>
        <span className={styles.count}>
          {t('quota_pool.accounts_count', { count: model.memberCount })}
          {model.noDataCount > 0 &&
            ` · ${t('quota_pool.no_data_count', { count: model.noDataCount })}`}
        </span>
      </header>
      <div className={styles.headline}>
        <p className={styles.figureRow}>
          <span className={styles.figure}>{formatPoolPercent(model.poolNow)}%</span>
          <span className={styles.note}>
            {t('quota_pool.of_capacity', { capacity: formatPoolPercent(model.capacity) })}
          </span>
        </p>
        <PoolStats locale={locale} model={model} showEmails={showEmails} timeZone={timeZone} />
      </div>
      <PoolChart
        locale={locale}
        metric={metric}
        model={model}
        timeZone={timeZone}
        width={chartWidth}
      />
    </section>
  );
};
