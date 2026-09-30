import React, { useState, useMemo, useEffect, useCallback } from 'react';
import {
  getUsageEvents, getUsageFilterOptions, getUsageTotals,
} from '../api/client.js';
import { useAsync } from '../hooks/useAsync.js';
import { useAutoRefresh } from '../hooks/useAutoRefresh.js';
import {
  ErrorBanner, EmptyState,
} from '../components/Primitives.jsx';
import { useToast } from '../components/Toast.jsx';
import {
  PRESETS, presetToRange, toUTCFromTZ, EVENTS_PAGE_SIZE,
  TIMEZONES, loadTimezone, saveTimezone,
  FilterSelect, ChartSkeleton,
  EventsTableBody, EventDetailModal,
} from './usageShared.jsx';

// RecentEventsPage renders the events stream (usage_events table) as a
// first-class sibling to Usage Stats and Errors. It was split out of the Usage
// Stats page — where it used to live as a "Recent events" card at the bottom —
// so operators can page through and search the event log without scrolling
// past the aggregate KPIs/charts. The filter / preset / timezone / Request ID
// search chrome is shared with Usage Stats and Errors so the same time window
// and provider/key/model filters behave identically across the three pages.

const AUTO_REFRESH_INTERVAL_MS = 60 * 1000;
const AUTOREFRESH_STORAGE = 'nixllm.dashboard.eventsAutorefresh';

function readAutoRefresh() {
  try { return localStorage.getItem(AUTOREFRESH_STORAGE) !== '0'; }
  catch { return true; }
}

export default function RecentEventsPage() {
  const toast = useToast();
  const [presetIdx, setPresetIdx] = useState(3); // "Last 24h" default
  const [filter, setFilter] = useState({
    api_key_id: '',
    provider: '',
    model: '',
    request_id: '',
    // from/to/interval are auto-derived from the preset unless the user
    // types custom values.
    customFrom: '',
    customTo: '',
    useCustomRange: false,
    interval: 'hour',
  });
  const [autoRefresh, setAutoRefresh] = useState(() => readAutoRefresh());
  // Selected display timezone (shared with Usage Stats and Errors via
  // localStorage). The server always receives UTC from/to; this only affects
  // how wall-clock columns and the custom-range inputs are displayed.
  const [timezone, setTimezone] = useState(() => loadTimezone());

  function changeTimezone(tz) {
    setTimezone(tz);
    saveTimezone(tz);
  }

  const rangeParams = useMemo(() => {
    if (filter.useCustomRange && (filter.customFrom || filter.customTo)) {
      return {
        from: filter.customFrom ? toUTCFromTZ(filter.customFrom, timezone) : undefined,
        to: filter.customTo ? toUTCFromTZ(filter.customTo, timezone) : undefined,
        interval: filter.interval || 'hour',
      };
    }
    return presetToRange(PRESETS[presetIdx]);
  }, [presetIdx, filter.useCustomRange, filter.customFrom, filter.customTo, filter.interval, timezone]);

  const baseFilter = useMemo(() => ({
    api_key_id: filter.api_key_id || undefined,
    provider: filter.provider || undefined,
    model: filter.model || undefined,
    request_id: filter.request_id.trim() || undefined,
    from: rangeParams.from,
    to: rangeParams.to,
  }), [filter.api_key_id, filter.provider, filter.model, filter.request_id, rangeParams.from, rangeParams.to]);

  // Totals power the KPI strip (event count + failure rate for this window).
  // Same totals endpoint Usage Stats uses, scoped to the same filter so the
  // two pages agree on what "this window" means.
  const totals = useAsync(() => getUsageTotals(baseFilter), [JSON.stringify(baseFilter)]);
  // Filter dropdowns are listed with only the time window (never the currently
  // selected api_key/provider/model): scoping them by the active filter would
  // collapse each menu to the one already-selected value after any refresh.
  const filterOptions = useAsync(
    () => getUsageFilterOptions({ from: baseFilter.from, to: baseFilter.to }),
    [baseFilter.from, baseFilter.to],
  );

  const [eventsPage, setEventsPage] = useState(1);
  const events = useAsync(
    () => getUsageEvents({ ...baseFilter, page: eventsPage, page_size: EVENTS_PAGE_SIZE, include: 'cost_breakdown' }),
    [JSON.stringify(baseFilter), eventsPage],
  );
  // Reset the events pager back to page 1 whenever the filter changes so the
  // operator doesn't end up staring at an out-of-range page on a new window.
  useEffect(() => { setEventsPage(1); }, [JSON.stringify(baseFilter)]);

  const [selectedEventId, setSelectedEventId] = useState(null);

  const reloadAll = useCallback(() => {
    totals.reload();
    filterOptions.reload();
    events.reload();
  }, [totals, filterOptions, events]);

  useAutoRefresh(reloadAll, AUTO_REFRESH_INTERVAL_MS, autoRefresh);

  function handleRefresh() {
    reloadAll();
    toast.info('Events refreshed');
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

  const eventCount = totals.data?.totals?.request_count || 0;
  const failedCount = totals.data?.totals?.failed_count || 0;
  const totalAttempts = totals.data?.total_attempts ?? (eventCount + failedCount);
  const failureRate = totals.data?.failure_rate ?? 0;

  return (
    <>
      <div className="main__header">
        <div>
          <h1 className="main__title">Recent Events</h1>
          <div className="main__subtitle">
            Per-request records from the usage_events table, scoped to the same
            time window and provider/key/model filters as Usage Stats. Drill
            into a row to see the full token breakdown, latency, and triage
            fields.
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
          <label className="row gap-sm" style={{ alignItems: 'center' }} title="Display timezone for the Time column and custom range inputs">
            <span className="form__label" style={{ margin: 0 }}>Timezone</span>
            <select
              value={timezone}
              onChange={(e) => changeTimezone(e.target.value)}
              style={{ width: 'auto' }}
            >
              {TIMEZONES.map((tz) => (
                <option key={tz} value={tz}>{tz}</option>
              ))}
            </select>
          </label>
        </div>
        <div className="grid grid--3">
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
        </div>
        <div className="form__row" style={{ marginBottom: 0, marginTop: 8 }}>
          <label className="form__label">Request ID</label>
          <input
            type="text"
            className="search-input"
            placeholder="filter by request id (exact match)"
            value={filter.request_id}
            onChange={(e) => updateFilter({ request_id: e.target.value })}
          />
        </div>
        {filter.useCustomRange && (
          <div className="usage-toolbar__range">
            <div className="row gap-sm">
              <span className="usage-toolbar__range-label">From ({timezone})</span>
              <input type="datetime-local" value={filter.customFrom} onChange={(e) => updateFilter({ customFrom: e.target.value })} />
            </div>
            <div className="row gap-sm">
              <span className="usage-toolbar__range-label">To ({timezone})</span>
              <input type="datetime-local" value={filter.customTo} onChange={(e) => updateFilter({ customTo: e.target.value })} />
            </div>
          </div>
        )}
      </div>

      {/* KPI cards */}
      <div className="stats-grid">
        <div className="stat-card">
          <div className="stat-card__label">Requests in Window</div>
          <div className="stat-card__value">{eventCount.toLocaleString()}</div>
          <div className="stat-card__hint">successful requests in this window</div>
        </div>
        <div className="stat-card">
          <div className="stat-card__label">Failed Attempts</div>
          <div className="stat-card__value">{failedCount.toLocaleString()}</div>
          <div className="stat-card__hint">
            {failureRate.toFixed(1)}% of {totalAttempts.toLocaleString()} attempts
          </div>
        </div>
        <div className="stat-card">
          <div className="stat-card__label">Total Cost</div>
          <div className="stat-card__value">${(totals.data?.totals?.cost_usd || 0).toFixed(4)}</div>
          <div className="stat-card__hint">
            avg ${eventCount > 0 ? ((totals.data?.totals?.cost_usd || 0) / eventCount).toFixed(4) : '0.0000'}/req
          </div>
        </div>
      </div>

      {totals.error && <ErrorBanner error={totals.error} onRetry={totals.reload} />}

      {/* Events table */}
      <div className="card" style={{ marginTop: 16 }}>
        <div className="row row--between" style={{ marginBottom: 12 }}>
          <h3 className="card__title" style={{ margin: 0 }}>Recent events</h3>
        </div>
        <EventsTableBody
          events={events}
          page={eventsPage}
          pageSize={EVENTS_PAGE_SIZE}
          onPage={setEventsPage}
          onRowClick={setSelectedEventId}
          timezone={timezone}
        />
      </div>

      {selectedEventId != null && (
        <EventDetailModal id={selectedEventId} timezone={timezone} onClose={() => setSelectedEventId(null)} />
      )}
    </>
  );
}
