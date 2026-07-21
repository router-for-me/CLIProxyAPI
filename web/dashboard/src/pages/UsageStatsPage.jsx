import React, { useState, useMemo, useEffect, useCallback } from 'react';
import {
  getUsageTotals, getUsageTimeSeries, getUsageTop,
  getUsageEvents, getUsageEvent, getUsageErrors, getUsageError, getUsageFilterOptions,
} from '../api/client.js';
import { useAsync } from '../hooks/useAsync.js';
import { useAutoRefresh } from '../hooks/useAutoRefresh.js';
import {
  Spinner, ErrorBanner, EmptyState, Modal,
} from '../components/Primitives.jsx';
import { Sparkline, BarChart, MultiBarChart } from '../components/Charts.jsx';
import Pager from '../components/Pager.jsx';
import { useToast } from '../components/Toast.jsx';

// Preset time windows the dashboard offers at the top of the page. Each
// preset computes from/to in UTC using relative offsets from now.
const PRESETS = [
  { label: 'Last 15m', duration: 15, unit: 'minute', interval: 'minute' },
  { label: 'Last 1h', duration: 1, unit: 'hour', interval: 'minute' },
  { label: 'Last 6h', duration: 6, unit: 'hour', interval: 'hour' },
  { label: 'Last 24h', duration: 24, unit: 'hour', interval: 'hour' },
  { label: 'Last 7d', duration: 7, unit: 'day', interval: 'day' },
  { label: 'Last 30d', duration: 30, unit: 'day', interval: 'day' },
];

function presetToRange(preset) {
  const now = new Date();
  const from = new Date(now);
  if (preset.unit === 'minute') from.setMinutes(from.getMinutes() - preset.duration);
  if (preset.unit === 'hour') from.setHours(from.getHours() - preset.duration);
  if (preset.unit === 'day') from.setDate(from.getDate() - preset.duration);
  return { from: from.toISOString(), to: now.toISOString(), interval: preset.interval };
}

const EVENTS_PAGE_SIZE = 10;
const AUTO_REFRESH_INTERVAL_MS = 60 * 1000;
const AUTOREFRESH_STORAGE = 'nixllm.dashboard.usageAutorefresh';

function readAutoRefresh() {
  try { return localStorage.getItem(AUTOREFRESH_STORAGE) !== '0'; }
  catch { return true; }
}

// Token breakdown segments rendered left-to-right in the stacked bar. Order
// is deliberate: input-side tokens (input / cached / cache creation) first,
// then output-side tokens (output / reasoning). Provider parsers normalize
// the persisted fields so each segment is non-overlapping:
//   - input_tokens          = billable non-cached prompt tokens (OpenAI/Gemini
//                            subtract cached from prompt_tokens at parse time)
//   - cached_tokens         = cache-read tokens (strictly; never cache-creation)
//   - cache_creation_tokens = cache-write tokens
// For post-fix rows the segment sum matches total_tokens exactly. Pre-fix
// historical rows may still drift (cached_tokens used to fall back to
// cache-creation for Anthropic); the "different by N" hint surfaces that.
//
// Each segment also carries its cost_breakdown field name so the cost bar
// can attribute dollars to the same slices. The mapping mirrors Go's
// SegmentCosts: cached_tokens → cached_read, cache_creation_tokens →
// cache_creation (cache-write surcharge).
const TOKEN_SEGMENTS = [
  { key: 'input_tokens', costKey: 'input', label: 'Input', short: 'in', color: 'var(--accent)' },
  { key: 'cached_tokens', costKey: 'cached_read', label: 'Cached', short: 'cch', color: 'var(--success)' },
  { key: 'cache_creation_tokens', costKey: 'cache_creation', label: 'Cache write', short: 'ccw', color: '#38bdf8' },
  { key: 'output_tokens', costKey: 'output', label: 'Output', short: 'out', color: 'var(--warning)' },
  { key: 'reasoning_tokens', costKey: 'reasoning', label: 'Reasoning', short: 're', color: '#a78bfa' },
];

function tokenSegments(e) {
  return TOKEN_SEGMENTS.map((s) => ({ ...s, value: Number(e[s.key] || 0) }));
}

function costSegments(e) {
  const b = e.cost_breakdown || {};
  return TOKEN_SEGMENTS.map((s) => ({ ...s, value: Number(b[s.costKey] || 0) }));
}

// fmtUSD renders a per-segment dollar amount with enough precision that the
// operator can tell sub-cent slices apart. Zero is shown as "$0" so absent
// (no pricing row) and free both read cleanly.
function fmtUSD(v) {
  if (!v || v <= 0) return '$0';
  if (v < 0.01) return `$${v.toFixed(6)}`;
  return `$${v.toFixed(4)}`;
}

// fmtUSDCompact renders a per-segment dollar amount with the minimum number
// of digits needed to stay readable inside the dense table cell. Always
// groups thousands-separated for legibility.
function fmtUSDCompact(v) {
  if (!v || v <= 0) return '0';
  if (v < 0.01) return v.toFixed(6);
  return v.toFixed(4);
}

// LegendDot renders the colored square that precedes each segment label in
// the compact and modal token-breakdown legends. The color comes from the
// segment data (not a fixed class) so the palette stays centralized in
// TOKEN_SEGMENTS.
function LegendDot({ color, className = '' }) {
  return (
    <span
      className={`tokbar__legend-dot ${className}`}
      style={{ background: color }}
    />
  );
}

// TokBar renders a stacked horizontal bar of the given segments. Each
// segment width is its share of `sum`; segments with a non-zero value get a
// minimum width so they remain visible. The `size` variant controls the bar
// height only.
function TokBar({ segs, sum, size = 'sm', dim = false }) {
  const pct = (v, base) => (base === 0 ? 0 : (v / base) * 100);
  return (
    <div className={`tokbar tokbar--${size}`}>
      {segs.map((s) => s.value > 0 && (
        <div
          key={s.key}
          className="tokbar__seg tokbar__seg--min"
          style={{
            width: `${pct(s.value, sum)}%`,
            background: s.color,
            opacity: dim ? 0.7 : 1,
          }}
        />
      ))}
    </div>
  );
}

// CompactTokenBreakdown renders the stacked token bar plus a single-line
// numeric summary. Designed for the dense Recent Events table cell — hover
// the bar or the row for a title tooltip with every segment's value. When
// the event carries a cost_breakdown (include=cost_breakdown on the events
// request), a second stacked bar attributes dollars to the same slices.
function CompactTokenBreakdown({ e }) {
  const segs = tokenSegments(e);
  const sum = segs.reduce((a, s) => a + s.value, 0);
  const title = segs.map((s) => `${s.label}: ${s.value.toLocaleString()}`).join(' · ');

  const cSegs = costSegments(e);
  const cSum = cSegs.reduce((a, s) => a + s.value, 0);
  const hasCost = Boolean(e.cost_breakdown);

  return (
    <div title={`${title} · Total: ${Number(e.total_tokens || 0).toLocaleString()}${hasCost ? ` · Cost: ${fmtUSD(e.cost_usd || 0)}` : ''}`}>
      <TokBar segs={segs} sum={sum} size="sm" />
      <div className="mono tokbar__legend tokbar__legend--sm">
        {segs.map((s) => (
          <span key={s.key} className="tokbar__legend-item tokbar__legend-item--sm">
            <LegendDot color={s.color} />
            <span className="dim">{s.short}</span>{' '}{s.value.toLocaleString()}
          </span>
        ))}
        <span className="dim tokbar__legend-sum">
          · Σ {(e.total_tokens || 0).toLocaleString()}
        </span>
      </div>
      {hasCost && (
        <>
          <div style={{ marginTop: 4 }}>
            <TokBar segs={cSegs} sum={cSum} size="md" dim />
          </div>
          <div className="mono tokbar__legend tokbar__legend--sm">
            {cSegs.map((s) => (
              <span key={s.key} className="tokbar__legend-item tokbar__legend-item--sm">
                <LegendDot color={s.color} />
                <span className="dim">{s.short}</span>{' '}{fmtUSDCompact(s.value)}
              </span>
            ))}
            <span className="dim tokbar__legend-sum">
              · Σ {fmtUSDCompact(e.cost_usd || 0)}
            </span>
          </div>
        </>
      )}
    </div>
  );
}

// TokenBreakdownCard is the richer, full-width breakdown shown in the event
// detail modal: stacked bar + legend with values, percentages, and the
// persisted total for reconciliation. When the event carries a
// cost_breakdown (always present on the single-event endpoint), a second bar
// attributes dollar cost to the same slices with a parallel legend.
function TokenBreakdownCard({ e }) {
  const segs = tokenSegments(e);
  const sum = segs.reduce((a, s) => a + s.value, 0);
  const pct = (v, base) => (base === 0 ? 0 : (v / base) * 100);
  const total = Number(e.total_tokens || 0);

  const cSegs = costSegments(e);
  const cSum = cSegs.reduce((a, s) => a + s.value, 0);
  const hasCost = Boolean(e.cost_breakdown);

  return (
    <div className="detail-row__block">
      <div className="detail-row__block-label">Token breakdown</div>
      <TokBar segs={segs} sum={sum} size="lg" />
      <div className="tokbar__legend" style={{ marginTop: 10 }}>
        {segs.map((s) => (
          <span key={s.key} className="tokbar__legend-item">
            <LegendDot color={s.color} />
            <span className="dim">{s.label}</span>
            <span className="mono">{s.value.toLocaleString()}</span>
            <span className="dim" style={{ fontSize: 11 }}>{pct(s.value, sum).toFixed(1)}%</span>
          </span>
        ))}
        <span className="dim mono tokbar__legend-sum">
          sum {sum.toLocaleString()} · total {total.toLocaleString()}
          {sum !== total ? ` (different by ${(sum - total).toLocaleString()})` : ''}
        </span>
      </div>

      {hasCost && (
        <>
          <div className="detail-row__block-label" style={{ marginTop: 14 }}>
            Cost breakdown <span style={{ fontSize: 10 }}>(at-rest pricing)</span>
          </div>
          <TokBar segs={cSegs} sum={cSum} size="lg" dim />
          <div className="tokbar__legend" style={{ marginTop: 10 }}>
            {cSegs.map((s) => (
              <span key={s.key} className="tokbar__legend-item">
                <LegendDot color={s.color} />
                <span className="dim">{s.label}</span>
                <span className="mono">{fmtUSD(s.value)}</span>
                <span className="dim" style={{ fontSize: 11 }}>{pct(s.value, cSum).toFixed(1)}%</span>
              </span>
            ))}
            <span className="dim mono tokbar__legend-sum">
              sum {fmtUSD(cSum)} · total {fmtUSD(e.cost_usd || 0)}
              {Math.abs(cSum - (e.cost_usd || 0)) > 1e-9 ? ` (different by ${fmtUSD(cSum - (e.cost_usd || 0))})` : ''}
            </span>
          </div>
        </>
      )}
    </div>
  );
}

export default function UsageStatsPage() {
  const toast = useToast();
  const [presetIdx, setPresetIdx] = useState(3); // "Last 24h" default
  const [filter, setFilter] = useState({
    api_key_id: '',
    provider: '',
    model: '',
    // from/to/interval are auto-derived from the preset unless the user
    // types custom values.
    customFrom: '',
    customTo: '',
    useCustomRange: false,
    interval: 'hour',
  });
  const [autoRefresh, setAutoRefresh] = useState(() => readAutoRefresh());

  const rangeParams = useMemo(() => {
    if (filter.useCustomRange && (filter.customFrom || filter.customTo)) {
      return {
        from: filter.customFrom ? toUTC(filter.customFrom) : undefined,
        to: filter.customTo ? toUTC(filter.customTo) : undefined,
        interval: filter.interval || 'hour',
      };
    }
    return presetToRange(PRESETS[presetIdx]);
  }, [presetIdx, filter.useCustomRange, filter.customFrom, filter.customTo, filter.interval]);

  const baseFilter = useMemo(() => ({
    api_key_id: filter.api_key_id || undefined,
    provider: filter.provider || undefined,
    model: filter.model || undefined,
    from: rangeParams.from,
    to: rangeParams.to,
  }), [filter.api_key_id, filter.provider, filter.model, rangeParams.from, rangeParams.to]);

  const totals = useAsync(() => getUsageTotals(baseFilter), [JSON.stringify(baseFilter)]);
  const ts = useAsync(() => getUsageTimeSeries(baseFilter, rangeParams.interval), [JSON.stringify(baseFilter), rangeParams.interval]);
  const topModels = useAsync(() => getUsageTop({ dimension: 'model', metric: 'cost_usd', limit: 10, ...baseFilter }), [JSON.stringify(baseFilter)]);
  const topKeys = useAsync(() => getUsageTop({ dimension: 'api_key_id', metric: 'request_count', limit: 10, ...baseFilter }), [JSON.stringify(baseFilter)]);
  const topProviders = useAsync(() => getUsageTop({ dimension: 'provider', metric: 'request_count', limit: 10, ...baseFilter }), [JSON.stringify(baseFilter)]);

  // Filter dropdown options. Refetched whenever the time range changes so the
  // operator sees only values that actually appear in the window.
  const filterOptions = useAsync(
    () => getUsageFilterOptions(baseFilter),
    [JSON.stringify({ from: baseFilter.from, to: baseFilter.to })],
  );

  const [eventsPage, setEventsPage] = useState(1);
  const events = useAsync(
    () => getUsageEvents({ ...baseFilter, page: eventsPage, page_size: EVENTS_PAGE_SIZE, include: 'cost_breakdown' }),
    [JSON.stringify(baseFilter), eventsPage],
  );
  // Reset the events pager back to page 1 whenever the filter changes so the
  // operator doesn't end up staring at an out-of-range page on a new window.
  useEffect(() => { setEventsPage(1); }, [JSON.stringify(baseFilter)]);

  // Failed-attempt records are persisted in a separate usage_errors table.
  // The operator can flip between the "Events" (successful responses) and
  // "Errors" tabs at the bottom of the page; both share the same filter.
  const [eventsTab, setEventsTab] = useState('events'); // 'events' | 'errors'
  const [errorsPage, setErrorsPage] = useState(1);
  const errors = useAsync(
    () => getUsageErrors({ ...baseFilter, page: errorsPage, page_size: EVENTS_PAGE_SIZE, include: 'cost_breakdown' }),
    [JSON.stringify(baseFilter), errorsPage],
  );
  useEffect(() => { setErrorsPage(1); }, [JSON.stringify(baseFilter)]);

  const [selectedEventId, setSelectedEventId] = useState(null);
  const [selectedErrorId, setSelectedErrorId] = useState(null);

  const reloadAll = useCallback(() => {
    totals.reload();
    ts.reload();
    topModels.reload();
    topKeys.reload();
    topProviders.reload();
    filterOptions.reload();
    events.reload();
    errors.reload();
  }, [totals, ts, topModels, topKeys, topProviders, filterOptions, events, errors]);

  // Auto-refresh: re-fetch every metric every 60s while the page is visible
  // and the toggle is on. Mirrors the Models Catalog auto-refresh pattern.
  useAutoRefresh(reloadAll, AUTO_REFRESH_INTERVAL_MS, autoRefresh);

  function handleRefresh() {
    reloadAll();
    toast.info('Stats refreshed');
  }

  function toggleAutoRefresh() {
    setAutoRefresh((v) => {
      const next = !v;
      try { localStorage.setItem(AUTOREFRESH_STORAGE, next ? '1' : '0'); } catch { /* ignore */ }
      return next;
    });
  }

  function updateFilter(partial) {
    setFilter((f) => ({ ...f, ...partial }));
  }

  const tsPoints = ts.data?.points || [];
  const tsData = useMemo(() => tsPoints.map((p) => ({
    label: p.bucket,
    value: p.request_count,
    a: p.input_tokens,
    b: p.output_tokens,
    cost: p.cost_usd,
  })), [tsPoints]);

  const totalTokens = totals.data?.totals?.total_tokens || 0;
  const failureRate = totals.data?.failure_rate ?? 0;
  const reqCount = totals.data?.totals?.request_count || 0;

  return (
    <>
      <div className="main__header">
        <div>
          <h1 className="main__title">Usage Statistics</h1>
          <div className="main__subtitle">
            Aggregate request volume, tokens, cost, per-key / per-model
            leaderboards, single-event drill-down, and dropdown-driven filters
            from the PG usage_events table.
          </div>
        </div>
        <div className="row gap-sm">
          <button
            className={`autorefresh-chip ${autoRefresh ? '' : 'autorefresh-chip--off'}`}
            onClick={toggleAutoRefresh}
            title={autoRefresh ? 'Auto-refresh every 60s — click to pause' : 'Auto-refresh paused — click to resume'}
          >
            <span className="autorefresh-chip__dot" />
            {autoRefresh ? 'Live' : 'Paused'}
          </button>
          <button onClick={handleRefresh}>Refresh</button>
        </div>
      </div>

      {/* Preset + filter bar */}
      <div className="card">
        <div className="usage-toolbar">
          <div className="seg-group" role="tablist" aria-label="Time window">
            {PRESETS.map((p, i) => (
              <button
                key={p.label}
                type="button"
                className={`seg-btn ${i === presetIdx && !filter.useCustomRange ? 'seg-btn--active' : ''}`}
                onClick={() => { setPresetIdx(i); updateFilter({ useCustomRange: false }); }}
              >
                {p.label}
              </button>
            ))}
          </div>
          <div className="catalog-toolbar__spacer" />
          <label className="row gap-sm" style={{ cursor: 'pointer' }}>
            <input
              type="checkbox"
              checked={filter.useCustomRange}
              onChange={(e) => updateFilter({ useCustomRange: e.target.checked })}
              style={{ width: 'auto' }}
            />
            <span className="form__label" style={{ margin: 0 }}>Custom range</span>
          </label>
        </div>
        <div className="grid grid--4">
          <div className="form__row" style={{ marginBottom: 0 }}>
            <label className="form__label">API Key</label>
            <FilterSelect
              loading={filterOptions.loading}
              options={(filterOptions.data?.api_keys || []).map((k) => ({ value: k.id, label: k.alias || k.id }))}
              value={filter.api_key_id}
              onChange={(v) => updateFilter({ api_key_id: v })}
              anyLabel="any key"
            />
          </div>
          <div className="form__row" style={{ marginBottom: 0 }}>
            <label className="form__label">Provider</label>
            <FilterSelect
              loading={filterOptions.loading}
              options={(filterOptions.data?.providers || []).map((p) => ({ value: p, label: p }))}
              value={filter.provider}
              onChange={(v) => updateFilter({ provider: v })}
              anyLabel="any provider"
            />
          </div>
          <div className="form__row" style={{ marginBottom: 0 }}>
            <label className="form__label">Model</label>
            <FilterSelect
              loading={filterOptions.loading}
              options={(filterOptions.data?.models || []).map((m) => ({ value: m, label: m }))}
              value={filter.model}
              onChange={(v) => updateFilter({ model: v })}
              anyLabel="any model"
            />
          </div>
          <div className="form__row" style={{ marginBottom: 0 }}>
            <label className="form__label">Interval</label>
            <select
              value={rangeParams.interval}
              onChange={(e) => updateFilter({ interval: e.target.value })}
              disabled={!filter.useCustomRange}
            >
              <option value="minute">minute</option>
              <option value="hour">hour</option>
              <option value="day">day</option>
            </select>
          </div>
        </div>
        {filter.useCustomRange && (
          <div className="usage-toolbar__range">
            <div className="row gap-sm">
              <span className="usage-toolbar__range-label">From (UTC)</span>
              <input type="datetime-local" value={filter.customFrom} onChange={(e) => updateFilter({ customFrom: e.target.value })} />
            </div>
            <div className="row gap-sm">
              <span className="usage-toolbar__range-label">To (UTC)</span>
              <input type="datetime-local" value={filter.customTo} onChange={(e) => updateFilter({ customTo: e.target.value })} />
            </div>
          </div>
        )}
      </div>

      {/* KPI cards */}
      <div className="stats-grid">
        <div className="stat-card">
          <div className="stat-card__label">Total Requests</div>
          <div className="stat-card__value">{reqCount.toLocaleString()}</div>
          <div className="stat-card__hint">
            {(totals.data?.totals?.failed_count || 0).toLocaleString()} failed ({failureRate.toFixed(1)}%)
          </div>
        </div>
        <div className="stat-card">
          <div className="stat-card__label">Total Tokens</div>
          <div className="stat-card__value">{totalTokens.toLocaleString()}</div>
          <div className="stat-card__hint">
            in {(totals.data?.totals?.input_tokens || 0).toLocaleString()} · out {(totals.data?.totals?.output_tokens || 0).toLocaleString()}
          </div>
        </div>
        <div className="stat-card">
          <div className="stat-card__label">Total Cost</div>
          <div className="stat-card__value">${(totals.data?.totals?.cost_usd || 0).toFixed(4)}</div>
          <div className="stat-card__hint">
            avg ${reqCount > 0 ? ((totals.data?.totals?.cost_usd || 0) / reqCount).toFixed(4) : '0.0000'}/req
          </div>
        </div>
        <div className="stat-card">
          <div className="stat-card__label">Active Models</div>
          <div className="stat-card__value">{String(topModels.data?.entries?.length || 0)}</div>
          <div className="stat-card__hint">{topProviders.data?.entries?.length || 0} providers</div>
        </div>
      </div>

      {totals.error && <ErrorBanner error={totals.error} onRetry={totals.reload} />}

      {/* Time-series charts */}
      <div className="grid grid--2" style={{ marginTop: 16 }}>
        <div className="card">
          <div className="row row--between" style={{ marginBottom: 12 }}>
            <h3 className="card__title" style={{ margin: 0 }}>Request volume</h3>
            <span className="dim" style={{ fontSize: 12 }}>{rangeParams.interval}</span>
          </div>
          {ts.loading && <ChartSkeleton />}
          {!ts.loading && tsData.length > 0 && (
            <BarChart data={tsData} />
          )}
          {!ts.loading && tsData.length === 0 && (
            <EmptyState title="No data for this window" />
          )}
        </div>
        <div className="card">
          <div className="row row--between" style={{ marginBottom: 12 }}>
            <h3 className="card__title" style={{ margin: 0 }}>Token usage</h3>
            <span className="row gap-sm" style={{ fontSize: 11 }}>
              <span className="tokbar__legend-dot" style={{ background: 'var(--accent)' }} /> input
              <span className="tokbar__legend-dot" style={{ background: 'var(--warning)', marginLeft: 6 }} /> output
            </span>
          </div>
          {ts.loading && <ChartSkeleton />}
          {!ts.loading && tsData.length > 0 && (
            <MultiBarChart data={tsData} />
          )}
          {!ts.loading && tsData.length === 0 && (
            <EmptyState title="No data for this window" />
          )}
        </div>
      </div>

      <div className="card">
        <div className="row row--between" style={{ marginBottom: 12 }}>
          <h3 className="card__title" style={{ margin: 0 }}>Cost trend</h3>
          <span className="dim" style={{ fontSize: 12 }}>{rangeParams.interval}</span>
        </div>
        {ts.loading && <ChartSkeleton />}
        {!ts.loading && tsData.length > 0 && (
          <Sparkline values={tsData.map((d) => d.cost)} />
        )}
        {!ts.loading && tsData.length === 0 && (
          <EmptyState title="No cost data for this window" />
        )}
      </div>

      {/* Leaderboards */}
      <div className="grid grid--3" style={{ marginTop: 16 }}>
        <LeaderboardCard
          title="Top models by cost"
          entries={topModels.data?.entries}
          loading={topModels.loading}
          error={topModels.error}
          onReload={topModels.reload}
          metric="cost_usd"
        />
        <LeaderboardCard
          title="Top API keys by requests"
          entries={topKeys.data?.entries}
          loading={topKeys.loading}
          error={topKeys.error}
          onReload={topKeys.reload}
          metric="request_count"
        />
        <LeaderboardCard
          title="Top providers"
          entries={topProviders.data?.entries}
          loading={topProviders.loading}
          error={topProviders.error}
          onReload={topProviders.reload}
          metric="request_count"
        />
      </div>

      {/* Recent events / errors table — operator can flip between the two.
          Failed attempts live in a separate usage_errors table and are never
          mixed into the success-side events listing. */}
      <div className="card" style={{ marginTop: 16 }}>
        <div className="row row--between" style={{ marginBottom: 12 }}>
          <div className="seg-group" role="tablist" aria-label="Events / Errors">
            <button
              type="button"
              className={`seg-btn ${eventsTab === 'events' ? 'seg-btn--active' : ''}`}
              onClick={() => setEventsTab('events')}
            >
              Events
            </button>
            <button
              type="button"
              className={`seg-btn ${eventsTab === 'errors' ? 'seg-btn--active' : ''}`}
              onClick={() => setEventsTab('errors')}
            >
              Errors
            </button>
          </div>
        </div>
        {eventsTab === 'events' ? (
          <EventsTableBody
            events={events}
            page={eventsPage}
            pageSize={EVENTS_PAGE_SIZE}
            onPage={setEventsPage}
            onRowClick={setSelectedEventId}
          />
        ) : (
          <ErrorsTableBody
            errors={errors}
            page={errorsPage}
            pageSize={EVENTS_PAGE_SIZE}
            onPage={setErrorsPage}
            onRowClick={setSelectedErrorId}
          />
        )}
      </div>

      {selectedEventId != null && (
        <EventDetailModal id={selectedEventId} onClose={() => setSelectedEventId(null)} />
      )}
      {selectedErrorId != null && (
        <ErrorDetailModal id={selectedErrorId} onClose={() => setSelectedErrorId(null)} />
      )}
    </>
  );
}

// ChartSkeleton fills the chart area with shimmer bars instead of a centered
// spinner, matching the loading pattern used across the overhauled pages and
// avoiding layout shift when the data arrives.
function ChartSkeleton() {
  return (
    <div className="chart-skeleton">
      <div className="skeleton-line chart-skeleton__bar" />
    </div>
  );
}

// FilterSelect renders a dropdown populated with distinct values from the
// usage_events table. Includes a leading "any" option that clears the filter.
// Shows a small "loading…" placeholder while the options request is in flight.
function FilterSelect({ options, value, onChange, anyLabel, loading }) {
  return (
    <select value={value || ''} onChange={(e) => onChange(e.target.value)} disabled={loading}>
      <option value="">{anyLabel}</option>
      {options.map((opt) => (
        <option key={opt.value} value={opt.value}>{opt.label}</option>
      ))}
    </select>
  );
}

// EventsTableBody renders the table + pager only (no card). Used by the
// tabbed Events/Errors switcher, which wraps both in a single card.
function EventsTableBody({ events, page, pageSize, onPage, onRowClick }) {
  const total = events.data?.total || 0;
  const rows = events.data?.events || [];
  const totalPages = total === 0 ? 1 : Math.ceil(total / pageSize);
  return (
    <>
      {events.loading && (
        <div style={{ overflowX: 'auto' }}>
          <table className="table" style={{ tableLayout: 'fixed' }}>
            <thead>
              <tr>
                <th>Time (UTC)</th>
                <th>Key / Alias</th>
                <th>Provider</th>
                <th>Model</th>
                <th style={{ minWidth: 260 }}>Token breakdown</th>
                <th style={{ textAlign: 'right' }}>Cost</th>
                <th style={{ textAlign: 'right' }}>Latency</th>
                <th style={{ textAlign: 'right' }}>TTFT</th>
              </tr>
            </thead>
            <tbody>
              {Array.from({ length: 4 }).map((_, i) => (
                <tr key={i} className="skeleton-row">
                  <td><span className="skeleton-line" /></td>
                  <td><span className="skeleton-line" /></td>
                  <td><span className="skeleton-line" /></td>
                  <td><span className="skeleton-line" /></td>
                  <td><span className="skeleton-line" /></td>
                  <td><span className="skeleton-line" /></td>
                  <td><span className="skeleton-line" /></td>
                  <td><span className="skeleton-line" /></td>
                </tr>
              ))}
            </tbody>
          </table>
        </div>
      )}
      {events.error && <ErrorBanner error={events.error} />}
      {!events.loading && !events.error && rows.length === 0 && (
        <EmptyState title="No events for this window" />
      )}
      {!events.loading && !events.error && rows.length > 0 && (
        <>
          <div style={{ overflowX: 'auto' }}>
            <table className="table">
              <thead>
                <tr>
                  <th>Time (UTC)</th>
                  <th>Key / Alias</th>
                  <th>Provider</th>
                  <th>Model</th>
                  <th style={{ minWidth: 260 }}>Token breakdown</th>
                  <th style={{ textAlign: 'right' }}>Cost</th>
                  <th style={{ textAlign: 'right' }}>Latency</th>
                  <th style={{ textAlign: 'right' }}>TTFT</th>
                </tr>
              </thead>
              <tbody>
                {rows.map((e) => (
                  <tr
                    key={String(e.id)}
                    className="row-link"
                    onClick={() => onRowClick(e.id)}
                  >
                    <td className="mono" style={{ whiteSpace: 'nowrap' }}>
                      {e.requested_at ? new Date(e.requested_at).toISOString().replace('T', ' ').replace(/\.\d+Z$/, 'Z') : '—'}
                    </td>
                    <td className="mono">{e.key_alias || e.api_key_id || '—'}</td>
                    <td>{e.provider || '—'}</td>
                    <td className="mono">{e.model || '—'}</td>
                    <td>
                      <CompactTokenBreakdown e={e} />
                    </td>
                    <td className="mono" style={{ textAlign: 'right' }}>${(e.cost_usd || 0).toFixed(4)}</td>
                    <td className="mono" style={{ textAlign: 'right' }}>{e.latency_ms ? `${e.latency_ms} ms` : '—'}</td>
                    <td className="mono" style={{ textAlign: 'right' }}>{e.ttft_ms ? `${e.ttft_ms} ms` : '—'}</td>
                  </tr>
                ))}
              </tbody>
            </table>
          </div>
          <Pager
            page={page}
            totalPages={totalPages}
            total={total}
            pageSize={pageSize}
            onPageChange={onPage}
          />
        </>
      )}
    </>
  );
}

// ErrorsTableBody mirrors EventsTableBody but renders failed-attempt rows
// (usage_errors). Surfaces fail_status_code as a status badge and the
// error_message truncated, with the full text in the detail modal.
function ErrorsTableBody({ errors, page, pageSize, onPage, onRowClick }) {
  const total = errors.data?.total || 0;
  const rows = errors.data?.errors || [];
  const totalPages = total === 0 ? 1 : Math.ceil(total / pageSize);
  return (
    <>
      {errors.loading && (
        <div style={{ overflowX: 'auto' }}>
          <table className="table" style={{ tableLayout: 'fixed' }}>
            <thead>
              <tr>
                <th>Time (UTC)</th>
                <th>Key / Alias</th>
                <th>Provider</th>
                <th>Model</th>
                <th style={{ textAlign: 'right' }}>Status</th>
                <th>Error message</th>
                <th style={{ textAlign: 'right' }}>Latency</th>
              </tr>
            </thead>
            <tbody>
              {Array.from({ length: 4 }).map((_, i) => (
                <tr key={i} className="skeleton-row">
                  <td><span className="skeleton-line" /></td>
                  <td><span className="skeleton-line" /></td>
                  <td><span className="skeleton-line" /></td>
                  <td><span className="skeleton-line" /></td>
                  <td><span className="skeleton-line" /></td>
                  <td><span className="skeleton-line" /></td>
                  <td><span className="skeleton-line" /></td>
                </tr>
              ))}
            </tbody>
          </table>
        </div>
      )}
      {errors.error && <ErrorBanner error={errors.error} />}
      {!errors.loading && !errors.error && rows.length === 0 && (
        <EmptyState title="No failed attempts for this window" />
      )}
      {!errors.loading && !errors.error && rows.length > 0 && (
        <>
          <div style={{ overflowX: 'auto' }}>
            <table className="table">
              <thead>
                <tr>
                  <th>Time (UTC)</th>
                  <th>Key / Alias</th>
                  <th>Provider</th>
                  <th>Model</th>
                  <th style={{ textAlign: 'right' }}>Status</th>
                  <th>Error message</th>
                  <th style={{ textAlign: 'right' }}>Latency</th>
                </tr>
              </thead>
              <tbody>
                {rows.map((e) => (
                  <tr
                    key={String(e.id)}
                    className="row-link"
                    onClick={() => onRowClick(e.id)}
                  >
                    <td className="mono" style={{ whiteSpace: 'nowrap' }}>
                      {e.requested_at ? new Date(e.requested_at).toISOString().replace('T', ' ').replace(/\.\d+Z$/, 'Z') : '—'}
                    </td>
                    <td className="mono">{e.key_alias || e.api_key_id || '—'}</td>
                    <td>{e.provider || '—'}</td>
                    <td className="mono">{e.model || '—'}</td>
                    <td style={{ textAlign: 'right' }} className="mono">
                      {e.fail_status_code ? String(e.fail_status_code) : '—'}
                    </td>
                    <td style={{ maxWidth: 420, overflow: 'hidden', textOverflow: 'ellipsis', whiteSpace: 'nowrap' }}>
                      {e.error_message || '—'}
                    </td>
                    <td className="mono" style={{ textAlign: 'right' }}>{e.latency_ms ? `${e.latency_ms} ms` : '—'}</td>
                  </tr>
                ))}
              </tbody>
            </table>
          </div>
          <Pager
            page={page}
            totalPages={totalPages}
            total={total}
            pageSize={pageSize}
            onPageChange={onPage}
          />
        </>
      )}
    </>
  );
}

// EventDetailModal fetches and renders a single usage event. The sealed
// api_key_principal is intentionally never shown; the operator sees the
// non-secret key_alias and the other fields needed for triage.
function EventDetailModal({ id, onClose }) {
  const detail = useAsync(() => getUsageEvent(id), [id]);
  const e = detail.data?.event;
  return (
    <Modal title={`Event #${id}`} onClose={onClose} size="lg">
      {detail.loading && <Spinner label="Loading…" />}
      {detail.error && <ErrorBanner error={detail.error} />}
      {!detail.loading && !detail.error && e && (
        <div className="grid grid--2" style={{ gap: '6px 24px' }}>
          <DetailRow label="Time (UTC)" value={e.requested_at ? new Date(e.requested_at).toISOString() : '—'} mono />
          <DetailRow label="Request ID" value={e.request_id || '—'} mono />
          <DetailRow label="API Key ID" value={e.api_key_id || '—'} mono />
          <DetailRow label="Key Alias" value={e.key_alias || '—'} mono />
          <DetailRow label="Provider" value={e.provider || '—'} />
          <DetailRow label="Model" value={e.model || '—'} mono />
          <DetailRow label="Alias" value={e.alias || '—'} mono />
          <DetailRow label="Executor" value={e.executor_type || '—'} />
          <DetailRow label="Auth Type" value={e.auth_type || '—'} />
          <DetailRow label="Source" value={e.source || '—'} />
          <DetailRow label="Reasoning Effort" value={e.reasoning_effort || '—'} />
          <DetailRow label="Service Tier" value={e.service_tier || '—'} />
          <DetailRow label="Response Service Tier" value={e.response_service_tier || '—'} />
          <TokenBreakdownCard e={e} />
          <DetailRow label="Cost (USD)" value={`$${(e.cost_usd || 0).toFixed(4)}`} mono />
          <DetailRow label="Latency" value={e.latency_ms ? `${e.latency_ms} ms` : '—'} mono />
          <DetailRow label="TTFT" value={e.ttft_ms ? `${e.ttft_ms} ms` : '—'} mono />
          <DetailRow label="Failed" value={e.failed ? 'yes' : 'no'} mono />
          <DetailRow label="Fail Status" value={e.fail_status_code ? String(e.fail_status_code) : '—'} mono />
          <DetailRow label="Generate" value={e.generate ? 'true' : 'false'} mono />
          <DetailRow label="Endpoint" value={e.endpoint || '—'} mono />
        </div>
      )}
      {!detail.loading && !detail.error && !e && <EmptyState title="Event not found" />}
    </Modal>
  );
}

// ErrorDetailModal fetches and renders a single failed-attempt row from the
// usage_errors table. The sealed api_key_principal is intentionally never
// shown. Unlike EventDetailModal, every row here is a failure by construction
// so we surface the status code and full error_message prominently instead of
// a Failed yes/no row.
function ErrorDetailModal({ id, onClose }) {
  const detail = useAsync(() => getUsageError(id), [id]);
  const e = detail.data?.error_event;
  return (
    <Modal title={`Error #${id}`} onClose={onClose} size="lg">
      {detail.loading && <Spinner label="Loading…" />}
      {detail.error && <ErrorBanner error={detail.error} />}
      {!detail.loading && !detail.error && e && (
        <div className="grid grid--2" style={{ gap: '6px 24px' }}>
          <DetailRow label="Time (UTC)" value={e.requested_at ? new Date(e.requested_at).toISOString() : '—'} mono />
          <DetailRow label="Request ID" value={e.request_id || '—'} mono />
          <DetailRow label="API Key ID" value={e.api_key_id || '—'} mono />
          <DetailRow label="Key Alias" value={e.key_alias || '—'} mono />
          <DetailRow label="Provider" value={e.provider || '—'} />
          <DetailRow label="Model" value={e.model || '—'} mono />
          <DetailRow label="Alias" value={e.alias || '—'} mono />
          <DetailRow label="Executor" value={e.executor_type || '—'} />
          <DetailRow label="Auth Type" value={e.auth_type || '—'} />
          <DetailRow label="Source" value={e.source || '—'} />
          <DetailRow label="Reasoning Effort" value={e.reasoning_effort || '—'} />
          <DetailRow label="Service Tier" value={e.service_tier || '—'} />
          <DetailRow label="Response Service Tier" value={e.response_service_tier || '—'} />
          <TokenBreakdownCard e={e} />
          <DetailRow label="Cost (USD)" value={`$${(e.cost_usd || 0).toFixed(4)}`} mono />
          <DetailRow label="Latency" value={e.latency_ms ? `${e.latency_ms} ms` : '—'} mono />
          <DetailRow label="TTFT" value={e.ttft_ms ? `${e.ttft_ms} ms` : '—'} mono />
          <DetailRow label="Status Code" value={e.fail_status_code ? String(e.fail_status_code) : '—'} mono />
          <DetailRow label="Generate" value={e.generate ? 'true' : 'false'} mono />
          <DetailRow label="Endpoint" value={e.endpoint || '—'} mono />
          <div className="detail-row__block">
            <div className="detail-row__block-label">Error message</div>
            <div className="mono" style={{ fontSize: 13, whiteSpace: 'pre-wrap', wordBreak: 'break-all', marginTop: 4 }}>
              {e.error_message || '—'}
            </div>
          </div>
        </div>
      )}
      {!detail.loading && !detail.error && !e && <EmptyState title="Error not found" />}
    </Modal>
  );
}

function DetailRow({ label, value, mono }) {
  return (
    <div className="detail-row">
      <div className="detail-row__label">{label}</div>
      <div className={`detail-row__value ${mono ? 'mono' : ''}`}>{value}</div>
    </div>
  );
}

function LeaderboardCard({ title, entries, loading, error, onReload, metric }) {
  const fmt = (v) => {
    if (metric === 'cost_usd') return `$${Number(v).toFixed(4)}`;
    return Number(v).toLocaleString();
  };
  const max = useMemo(() => {
    if (!entries || entries.length === 0) return 1;
    return Math.max(...entries.map((e) => e[metric === 'cost_usd' ? 'cost_usd' : metric]), 1);
  }, [entries, metric]);
  const valueKey = metric === 'cost_usd' ? 'cost_usd' : metric;
  return (
    <div className="card" style={{ padding: 0 }}>
      <div className="row row--between" style={{ padding: '14px 16px 8px' }}>
        <h3 className="card__title" style={{ margin: 0 }}>{title}</h3>
        <button onClick={onReload} style={{ padding: '3px 8px', fontSize: 12 }} title="Reload">↻</button>
      </div>
      {loading && <Spinner label="Loading…" />}
      {error && <ErrorBanner error={error} />}
      {!loading && !error && entries && entries.length === 0 && (
        <EmptyState title="No data" />
      )}
      {!loading && !error && entries && entries.length > 0 && (
        <table className="table">
          <thead>
            <tr>
              <th>Key</th>
              <th style={{ textAlign: 'right' }}>{metricLabel(metric)}</th>
              <th style={{ width: '40%' }}></th>
            </tr>
          </thead>
          <tbody>
            {entries.map((e, i) => (
              <tr key={`${e.key}-${i}`}>
                <td className="mono">{e.key || '—'}</td>
                <td className="mono" style={{ textAlign: 'right' }}>{fmt(e[valueKey])}</td>
                <td>
                  <div className="lb-bar">
                    <div
                      className="lb-bar__fill"
                      style={{ width: `${(e[valueKey] / max * 100)}%` }}
                    />
                  </div>
                </td>
              </tr>
            ))}
          </tbody>
        </table>
      )}
    </div>
  );
}

function metricLabel(metric) {
  if (metric === 'cost_usd') return 'Cost (USD)';
  if (metric === 'total_tokens') return 'Tokens';
  return 'Requests';
}

function toUTC(localValue) {
  if (!localValue) return '';
  // datetime-local input produces "2026-07-19T15:30"; assume the user means
  // local time. Convert to ISO UTC.
  const d = new Date(localValue);
  if (isNaN(d.getTime())) return '';
  return d.toISOString();
}
