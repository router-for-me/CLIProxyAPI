/**
 * prox additions to the Quota page:
 * - LiveStrip: routing mode, next-session account, traffic, burn, cache share, cost.
 * - PoolSummary (All tab): one pooled card per provider.
 * - PoolPanel (provider tab): pool chart with recorded/projected and drain rate.
 */

import { useState } from 'react';
import { ProviderArtwork } from '@/components/common/ProviderArtwork';
import { getAuthFileIcon } from '@/features/authFiles/constants';
import { useThemeStore } from '@/stores';
import { useProx } from '../api';
import {
  pct,
  providerColor,
  providerLabel,
  relative,
  usd,
  weightNote,
  when,
  WINDOW_LABELS,
} from '../format';
import type { Overview, Pool, PoolChart as PoolChartData, PoolCharts, Usage } from '../types';
import { DrainChart } from './DrainChart';
import { PoolChart } from './PoolChart';
import styles from './PoolOverview.module.scss';

const levelClass = (remaining: number) =>
  remaining >= 70 ? styles.high : remaining >= 30 ? styles.medium : styles.low;

function ProviderIcon({ provider }: { provider: string }) {
  const theme = useThemeStore((s) => s.resolvedTheme);
  return (
    <ProviderArtwork
      provider={provider}
      src={getAuthFileIcon(provider, theme)}
      className={styles.icon}
    >
      <span className={styles.iconFallback}>{providerLabel(provider).slice(0, 1)}</span>
    </ProviderArtwork>
  );
}

/* ---------- live telemetry ---------- */

export function LiveStrip({ charts }: { charts: PoolChartData[] }) {
  const overview = useProx<Overview>('/overview', 15_000);
  const usage = useProx<Usage>('/usage?range=6h', 30_000);
  const pool = useProx<Pool>('/pool', 30_000);
  if (overview.error && !overview.data) {
    return (
      <div className={styles.strip} data-off="">
        <span className={styles.stripLabel}>prox</span>
        <span className={styles.stripMuted}>Scheduler off or unreachable ({overview.error})</span>
      </div>
    );
  }
  const o = overview.data;
  const totals = Object.values(usage.data?.providers ?? {}).reduce(
    (acc, p) => {
      acc.requests += p.totals.requests;
      acc.failovers += p.totals.failovers;
      acc.affinity += p.totals.affinity_hits;
      return acc;
    },
    { requests: 0, failovers: 0, affinity: 0 }
  );
  const claudeCache = usage.data?.providers?.claude?.cache_read_share;
  const codexCache = usage.data?.providers?.codex?.cache_read_share;
  const cost = (pool.data?.providers ?? []).reduce((s, p) => s + p.cost_today_usd, 0);
  const burns = charts.filter((c) => c.burn_last_3h_percent_per_hour > 0);

  return (
    <div className={styles.strip}>
      <div className={styles.stripItem}>
        <span className={styles.stripLabel}>Routing</span>
        <span className={styles.stripValue}>
          <span className={styles.dot} data-mode={o?.mode ?? 'off'} />
          {o
            ? `${o.mode === 'active' ? 'Active' : 'Shadow'} · ${o.policy.replace(/_/g, ' ')}`
            : '--'}
        </span>
      </div>
      {(o?.next_up ?? []).map((n) => (
        <div key={n.provider} className={styles.stripItem} title={n.reason}>
          <span className={styles.stripLabel}>Next {providerLabel(n.provider)} session</span>
          <span className={styles.stripValue}>
            {n.account || n.auth_id || (n.fallback ? 'CPA default' : '--')}
          </span>
        </div>
      ))}
      <div className={styles.stripItem}>
        <span className={styles.stripLabel}>Requests, 6h</span>
        <span className={styles.stripValue}>
          {totals.requests.toLocaleString()}
          <span className={styles.stripMuted}>
            {' '}
            · {totals.affinity.toLocaleString()} sticky · {totals.failovers} failover
            {totals.failovers === 1 ? '' : 's'}
          </span>
        </span>
      </div>
      <div
        className={styles.stripItem}
        title="Cache reads as a share of prompt tokens. A drop means sessions are losing affinity."
      >
        <span className={styles.stripLabel}>Cache read</span>
        <span className={styles.stripValue}>
          <CacheShare label="Claude" value={claudeCache} />
          <CacheShare label="Codex" value={codexCache} />
        </span>
      </div>
      <div className={styles.stripItem}>
        <span className={styles.stripLabel}>Burn, last 3h</span>
        <span className={styles.stripValue}>
          {burns.length === 0
            ? '0%/h'
            : burns.map((c) => (
                <span
                  key={c.provider}
                  className={styles.burn}
                  style={{ color: providerColor(c.provider) }}
                >
                  <span className={styles.stripMuted}>{providerLabel(c.provider)}</span>{' '}
                  {c.burn_last_3h_percent_per_hour.toFixed(1)}%/h
                </span>
              ))}
        </span>
      </div>
      <div className={styles.stripItem} title="API-equivalent cost of today's traffic">
        <span className={styles.stripLabel}>Cost today</span>
        <span className={styles.stripValue}>{usd(cost)}</span>
      </div>
    </div>
  );
}

function CacheShare({ label, value }: { label: string; value: number | null | undefined }) {
  if (value === null || value === undefined) return null;
  const v = value * 100;
  return (
    <span className={styles.cache} data-low={v < 50 ? '' : undefined}>
      {label} {v.toFixed(0)}%
    </span>
  );
}

/* ---------- pool charts ---------- */

export function PoolSummary({
  charts,
  onSelect,
}: {
  charts: PoolChartData[];
  onSelect: (provider: string) => void;
}) {
  if (!charts.length) return null;
  return (
    <div className={styles.summary}>
      {charts.map((c) => {
        const accounts = c.accounts ?? [];
        return (
          <button
            key={c.provider}
            type="button"
            className={styles.summaryCard}
            onClick={() => onSelect(c.provider)}
          >
            <div className={styles.summaryHead}>
              <ProviderIcon provider={c.provider} />
              <span className={styles.summaryName}>{providerLabel(c.provider)}</span>
              <span className={styles.summaryCount}>
                {accounts.length} credential{accounts.length === 1 ? '' : 's'}
              </span>
            </div>
            <div className={styles.summaryWindow}>{WINDOW_LABELS[c.window] ?? c.window} pool</div>
            <div className={styles.summaryValue}>
              <strong>{Math.round(c.current_percent)}%</strong>
              <span>of {Math.round(c.capacity_percent)}%</span>
              {weightNote(c.weights) && (
                <span className={styles.weightNote}>{weightNote(c.weights)}</span>
              )}
            </div>
            <div className={styles.segments}>
              {accounts.map((a) => (
                <span
                  key={a.id}
                  className={styles.segment}
                  style={{ flexGrow: a.weight || 1 }}
                  title={`${a.label}: ${pct(a.remaining_percent)}`}
                >
                  <span
                    className={`${styles.segmentFill} ${levelClass(a.remaining_percent)}`}
                    style={{ width: `${a.remaining_percent}%` }}
                  />
                </span>
              ))}
            </div>
            <div className={styles.summaryReset}>
              {c.next_reset ? (
                <>
                  <strong>{relative(c.next_reset.at)}</strong> · {when(c.next_reset.at)} · +
                  {Math.round(c.next_reset.gain)}%
                </>
              ) : (
                'No reset pending'
              )}
            </div>
          </button>
        );
      })}
    </div>
  );
}

type PanelTab = 'remaining' | 'drain';

export function PoolPanel({
  provider,
  weekly,
}: {
  provider: string;
  weekly: PoolChartData | undefined;
}) {
  const [windowKind, setWindowKind] = useState<'weekly' | 'five_hour'>('weekly');
  const [tab, setTab] = useState<PanelTab>('remaining');
  const [drainHours, setDrainHours] = useState(24);
  const fiveHour = useProx<PoolCharts>(
    provider === 'claude' ? '/pool/chart?window=five_hour' : null,
    60_000
  );
  const short = fiveHour.data?.charts.find((c) => c.provider === provider);
  const chart = windowKind === 'five_hour' && short ? short : weekly;
  const color = providerColor(provider);
  if (!chart) return null;
  const since = new Date(chart.now).getTime() - drainHours * 3_600_000;
  const horizon = new Date(chart.horizon).toLocaleDateString(undefined, {
    month: 'short',
    day: 'numeric',
  });

  return (
    <section className={styles.panel}>
      <header className={styles.panelHead}>
        <div className={styles.panelTitle}>
          <ProviderIcon provider={provider} />
          <span>{providerLabel(provider)}</span>
          <span className={styles.summaryCount}>{(chart.accounts ?? []).length} accounts</span>
        </div>
        {short && (
          <div className={styles.segmented} role="tablist" aria-label="Window">
            {(['weekly', 'five_hour'] as const).map((k) => (
              <button
                key={k}
                type="button"
                role="tab"
                aria-selected={windowKind === k}
                onClick={() => setWindowKind(k)}
              >
                {WINDOW_LABELS[k]}
              </button>
            ))}
          </div>
        )}
      </header>
      <div className={styles.headline}>
        <div className={styles.big}>
          <strong style={{ color: tab === 'drain' ? color : undefined }}>
            {tab === 'drain'
              ? `${chart.burn_last_3h_percent_per_hour.toFixed(1)}`
              : Math.round(chart.current_percent)}
            {tab === 'drain' ? '%/h' : '%'}
          </strong>
          <span>
            {tab === 'drain' ? 'burn, last 3h' : `of ${Math.round(chart.capacity_percent)}%`}
            {tab !== 'drain' && weightNote(chart.weights) && (
              <em className={styles.weightNote}>{weightNote(chart.weights)}</em>
            )}
          </span>
        </div>
        <dl className={styles.stats}>
          <div>
            <dt>Pool empty</dt>
            <dd>{chart.pool_empty_at ? when(chart.pool_empty_at) : `Not by ${horizon}`}</dd>
          </div>
          <div>
            <dt>First empty</dt>
            <dd title={chart.first_empty?.label}>
              {chart.first_empty
                ? `${chart.first_empty.label} · ${when(chart.first_empty.at)}`
                : 'None before reset'}
            </dd>
          </div>
          <div>
            <dt>Next reset</dt>
            <dd>
              {chart.next_reset ? (
                <>
                  <span className={styles.gainText}>+{Math.round(chart.next_reset.gain)}%</span>{' '}
                  {when(chart.next_reset.at)}
                </>
              ) : (
                '--'
              )}
            </dd>
          </div>
          <div>
            <dt>Burn, last 3h</dt>
            <dd>{chart.burn_last_3h_percent_per_hour.toFixed(1)}%/h</dd>
          </div>
        </dl>
      </div>
      <div className={styles.tabs} role="tablist">
        <button
          type="button"
          role="tab"
          aria-selected={tab === 'remaining'}
          onClick={() => setTab('remaining')}
        >
          Remaining quota
        </button>
        <button
          type="button"
          role="tab"
          aria-selected={tab === 'drain'}
          onClick={() => setTab('drain')}
        >
          Drain rate
        </button>
        {tab === 'drain' && (
          <select
            className={styles.rangeSelect}
            value={drainHours}
            onChange={(e) => setDrainHours(Number(e.target.value))}
            aria-label="Drain range"
          >
            <option value={6}>Last 6 hours</option>
            <option value={24}>Last 24 hours</option>
            <option value={72}>Last 3 days</option>
          </select>
        )}
      </div>
      {tab === 'remaining' ? (
        <>
          <PoolChart
            recorded={chart.recorded ?? []}
            projected={chart.projected ?? []}
            events={chart.events ?? []}
            capacity={chart.capacity_percent}
            now={chart.now}
            color={color}
            hourly={chart.window === 'five_hour'}
          />
          <div className={styles.legend}>
            <span className={styles.legendTitle}>Pool</span>
            <span>
              <i style={{ background: color }} /> Recorded
            </span>
            <span>
              <i data-dashed="" style={{ borderColor: color }} /> Projected
            </span>
            <span className={styles.legendNote}>
              Each account counts 100% of its plan
              {weightNote(chart.weights) ? ` (${weightNote(chart.weights)})` : ''}. Projection uses
              the last 24h burn.
            </span>
          </div>
        </>
      ) : (
        <>
          <div className={styles.legend}>
            <span>
              <i style={{ background: color }} /> % per hour
            </span>
            <span className={styles.legendNote}>1-hour average</span>
          </div>
          <DrainChart points={chart.drain ?? []} color={color} since={since} />
          <div className={styles.legend}>
            <span className={styles.legendTitle}>
              Peak {chart.drain_peak_percent_per_hour.toFixed(0)}% per hour
            </span>
            <span className={styles.legendNote}>
              All accounts · Resets excluded · Gaps mean missing history
            </span>
          </div>
        </>
      )}
    </section>
  );
}
