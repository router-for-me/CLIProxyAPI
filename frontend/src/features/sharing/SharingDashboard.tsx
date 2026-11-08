import { useCallback, useMemo } from 'react';
import { Link } from 'react-router-dom';
import { useTranslation } from 'react-i18next';
import { Card } from '@/components/ui/Card';
import { sharingApi } from '@/services/api/sharing';
import { useAuthStore } from '@/stores';
import { formatCompactNumber } from '@/utils/format';
import { useHeaderRefresh } from '@/hooks/useHeaderRefresh';
import { DailyTrend, LoadState, PageHeader } from './shared';
import { useSharingData, number } from './hooks';
import { formatMS, formatPercent, performanceMetrics } from './marketplace';
import type { PerformanceBucket } from './types';
import styles from './Sharing.module.scss';

const emptyPerformance = (): PerformanceBucket => ({
  start: 0,
  requests: 0,
  failures: 0,
  latency_ms: 0,
  latency_count: 0,
  ttft_ms: 0,
  ttft_count: 0,
  output_tokens: 0,
  generation_ms: 0,
});

export function SharingDashboard() {
  const { t } = useTranslation();
  const account = useAuthStore((state) => state.account);
  const username = account?.username || '';

  // Catalog data is light and renders immediately; the log-derived summaries
  // and the performance window load in their own groups so a slow (or
  // cold-building) index never blocks the rest of the dashboard.
  const {
    data: light,
    loading: lightLoading,
    error: lightError,
    refresh: refreshLight,
  } = useSharingData(
    useCallback(async (signal: AbortSignal) => {
      const [channels, marketplace, keys] = await Promise.all([
        sharingApi.channels(signal),
        sharingApi.marketplace(signal),
        sharingApi.keys(signal),
      ]);
      return { channels, marketplace, keys };
    }, []),
    {
      keepPreviousData: false,
      headerRefresh: false,
      dataKey: username,
      cacheKey: `dashboard-light:${username}`,
    }
  );
  const {
    data: summaries,
    loading: summariesLoading,
    error: summariesError,
    refresh: refreshSummaries,
  } = useSharingData(
    useCallback(
      async (signal: AbortSignal) => {
        const [site, mine, contributed] = await Promise.all([
          sharingApi.summary({}, signal),
          sharingApi.summary({ consumer: username }, signal),
          sharingApi.summary({ owner: username }, signal),
        ]);
        return { site, mine, contributed };
      },
      [username]
    ),
    {
      keepPreviousData: false,
      headerRefresh: false,
      dataKey: username,
      cacheKey: `dashboard-summaries:${username}`,
    }
  );
  const {
    data: performance,
    loading: performanceLoading,
    error: performanceError,
    refresh: refreshPerformance,
  } = useSharingData(
    useCallback((signal: AbortSignal) => sharingApi.marketplacePerformance(signal), []),
    { headerRefresh: false }
  );
  useHeaderRefresh(
    useCallback(async () => {
      await Promise.all([refreshLight(), refreshSummaries(), refreshPerformance()]);
    }, [refreshLight, refreshSummaries, refreshPerformance])
  );

  const siteStats = useMemo(() => {
    const total = summaries?.site.total;
    const channels = light?.channels.channels ?? [];
    const models = light?.marketplace.models ?? [];
    const failureRate =
      total && total.executions > 0 ? ((total.failures / total.executions) * 100).toFixed(1) : null;
    const stats: { key: string; label: string; value: string; title?: string; hint: string }[] = [
      {
        key: 'calls',
        label: t('sharing.routed_calls'),
        value: total ? number(total.calls) : '—',
        hint: failureRate !== null
          ? t('sharing.hint_failures', { rate: failureRate })
          : t('sharing.hint_requests'),
      },
      {
        key: 'tokens',
        label: t('sharing.total_tokens'),
        // BigModel-style: compact headline figure, exact count on hover and in the IO hint.
        value: total ? formatCompactNumber(total.total_tokens) : '—',
        title: total ? number(total.total_tokens) : undefined,
        hint: total
          ? t('sharing.hint_tokens_io', {
              input: number(total.input_tokens),
              output: number(total.output_tokens),
            })
          : '',
      },
      {
        key: 'models',
        label: t('sharing.stat_available_models'),
        value: light ? number(models.filter((model) => model.available).length) : '—',
        hint: light ? t('sharing.stat_models_available_hint', { total: models.length }) : '',
      },
      {
        key: 'channels',
        label: t('sharing.channel'),
        value: light ? number(channels.length) : '—',
        hint: light
          ? t('sharing.stat_channels_hint', {
              published: channels.filter((channel) => channel.published).length,
            })
          : '',
      },
    ];
    return stats;
  }, [light, summaries, t]);

  const last24h = useMemo(() => {
    if (!performance) return null;
    const merged = emptyPerformance();
    performance.models.forEach((entry) => {
      merged.requests += entry.totals.requests;
      merged.failures += entry.totals.failures;
      merged.latency_ms += entry.totals.latency_ms;
      merged.latency_count += entry.totals.latency_count;
      merged.ttft_ms += entry.totals.ttft_ms;
      merged.ttft_count += entry.totals.ttft_count;
      merged.output_tokens += entry.totals.output_tokens;
      merged.generation_ms += entry.totals.generation_ms;
    });
    return performanceMetrics(merged);
  }, [performance]);

  const myStats = useMemo(() => {
    const keys = light?.keys.keys ?? [];
    const channels = light?.channels.channels ?? [];
    const owned = channels.filter((channel) => channel.owner === username);
    return {
      keysActive: keys.filter((key) => !key.disabled).length,
      keysTotal: keys.length,
      channelsOwned: owned.length,
      channelsPublished: owned.filter((channel) => channel.published).length,
    };
  }, [light, username]);

  return (
    <div className={styles.page}>
      <PageHeader
        title={t('nav.dashboard')}
        hint={t('sharing.dashboard_hint', { name: username })}
      />
      <LoadState
        loading={lightLoading || summariesLoading || performanceLoading}
        error={summariesError || performanceError || lightError}
      />

      <div className={styles.sectionHead}>
        <h2>{t('sharing.dashboard_site_section')}</h2>
      </div>
      <div className={styles.stats}>
        {siteStats.map((stat) => (
          <Card key={stat.key}>
            <div className={styles.stat}>
              {stat.label}
              <strong title={stat.title}>{stat.value}</strong>
              {stat.hint && <span className={styles.statHint}>{stat.hint}</span>}
            </div>
          </Card>
        ))}
      </div>
      <Card title={t('sharing.performance_window')}>
        {!performance ? (
          <LoadState loading={performanceLoading} error={performanceError} />
        ) : last24h && last24h.requests > 0 ? (
          <div className={styles.statCards}>
            <div className={styles.statCard}>
              <span className={styles.statLabel}>{t('sharing.requests_label')}</span>
              <strong className={styles.statValue}>{number(last24h.requests)}</strong>
            </div>
            <div className={styles.statCard}>
              <span className={styles.statLabel}>{t('sharing.success_rate')}</span>
              <strong className={styles.statValue}>{formatPercent(last24h.successRate)}</strong>
            </div>
            <div className={styles.statCard}>
              <span className={styles.statLabel}>{t('sharing.avg_latency')}</span>
              <strong className={styles.statValue}>{formatMS(last24h.avgLatencyMS)}</strong>
            </div>
            <div className={styles.statCard}>
              <span className={styles.statLabel}>{t('sharing.avg_ttft')}</span>
              <strong className={styles.statValue}>{formatMS(last24h.avgTTFTMS)}</strong>
            </div>
          </div>
        ) : (
          <p className={styles.chartEmpty}>{t('sharing.no_performance')}</p>
        )}
      </Card>

      <div className={styles.sectionHead}>
        <h2>{t('sharing.dashboard_my_section')}</h2>
      </div>
      <div className={styles.split}>
        <Card title={t('sharing.my_usage')} className={styles.splitCard}>
          <div className={styles.splitBody}>
            <div className={styles.statRow}>
              <div className={styles.stat}>
                {t('sharing.requests_label')}
                <strong>{summaries ? number(summaries.mine.total.calls) : '—'}</strong>
              </div>
              <div className={styles.stat}>
                {t('sharing.total_tokens')}
                <strong title={summaries ? number(summaries.mine.total.total_tokens) : undefined}>
                  {summaries ? formatCompactNumber(summaries.mine.total.total_tokens) : '—'}
                </strong>
              </div>
            </div>
            <Link to="/usage" className="btn btn-secondary">
              {t('sharing.view_usage')}
            </Link>
          </div>
        </Card>
        <Card title={t('sharing.my_contribution')} className={styles.splitCard}>
          <div className={styles.splitBody}>
            <div className={styles.statRow}>
              <div className={styles.stat}>
                {t('sharing.requests_label')}
                <strong>{summaries ? number(summaries.contributed.total.calls) : '—'}</strong>
              </div>
              <div className={styles.stat}>
                {t('sharing.total_tokens')}
                <strong
                  title={summaries ? number(summaries.contributed.total.total_tokens) : undefined}
                >
                  {summaries ? formatCompactNumber(summaries.contributed.total.total_tokens) : '—'}
                </strong>
              </div>
            </div>
            <p className="hint">{t('sharing.contribution_hint')}</p>
            <Link to="/usage?scope=contributed" className="btn btn-secondary">
              {t('sharing.view_usage')}
            </Link>
          </div>
        </Card>
        <Card title={t('sharing.keys')} className={styles.splitCard}>
          <div className={styles.splitBody}>
            <div className={styles.stat}>
              {t('sharing.enabled')}
              <strong>{light ? number(myStats.keysActive) : '—'}</strong>
              {light && (
                <span className={styles.statHint}>
                  {t('sharing.stat_keys_hint', { total: myStats.keysTotal })}
                </span>
              )}
            </div>
            <Link to="/keys" className="btn btn-secondary">
              {t('sharing.manage_keys')}
            </Link>
          </div>
        </Card>
        <Card title={t('sharing.my_channels')} className={styles.splitCard}>
          <div className={styles.splitBody}>
            <div className={styles.stat}>
              {t('sharing.channel')}
              <strong>{light ? number(myStats.channelsOwned) : '—'}</strong>
              {light && (
                <span className={styles.statHint}>
                  {t('sharing.stat_channels_hint', { published: myStats.channelsPublished })}
                </span>
              )}
            </div>
            <Link to="/channels" className="btn btn-secondary">
              {t('sharing.manage_channels')}
            </Link>
          </div>
        </Card>
      </div>
      <Card title={t('sharing.my_trend_title')}>
        {summaries ? <DailyTrend groups={summaries.mine.groups} maxDays={7} /> : null}
      </Card>

      <Card title={t('sharing.start')}>
        <div className={styles.splitBody}>
          <p className="hint">{t('sharing.start_hint')}</p>
          <div className={styles.actions}>
            <Link to="/keys" className="btn btn-primary">
              {t('sharing.create_key')}
            </Link>
            <Link to="/marketplace" className="btn btn-secondary">
              {t('sharing.marketplace')}
            </Link>
            <Link to="/channels" className="btn btn-secondary">
              {t('sharing.create_channel')}
            </Link>
          </div>
        </div>
      </Card>
    </div>
  );
}
