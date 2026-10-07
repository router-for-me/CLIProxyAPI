import React, { useState, useMemo, useEffect, useCallback, useRef } from 'react';
import {
  getUsageErrors, getUsageError, getUsageErrorBodies, getUsageFilterOptions,
  getUsageTotals, getErrorSummary, getErrorTimeline, getErrorGroups,
} from '../api/client.js';
import { useAsync } from '../hooks/useAsync.js';
import { useAutoRefresh } from '../hooks/useAutoRefresh.js';
import {
  Spinner, ErrorBanner, EmptyState, Modal,
} from '../components/Primitives.jsx';
import Pager from '../components/Pager.jsx';
import { useToast } from '../components/Toast.jsx';
import {
  PRESETS, presetToRange, toUTC, toUTCFromTZ, EVENTS_PAGE_SIZE,
  TIMEZONES, loadTimezone, saveTimezone, formatInTZ, tzAbbreviation,
  TokenBreakdownCard, ChartSkeleton,
  FilterSelect, DetailRow, CopyButton, FailoverHistory,
  EventBodiesSection,
} from './usageShared.jsx';
import { ERROR_CLASSES, classLabel, classTone } from './errorClass.js';
import ErrorGroupsPanel from './ErrorGroupsPanel.jsx';

// ErrorsPage renders the failed-attempt stream (usage_errors table) as a
// first-class sibling to Usage Stats. It was promoted from a tab on the
// Usage Stats page into its own sidebar entry so operators can triage errors
// without losing the events view. The filter / preset chrome is shared with
// Usage Stats so the same time window and provider/key/model filters behave
// identically across the two pages.

const AUTO_REFRESH_INTERVAL_MS = 60 * 1000;
const AUTOREFRESH_STORAGE = 'nixllm.dashboard.errorsAutorefresh';
const ERRORS_TAB_STORAGE = 'nixllm.dashboard.errorsTab';

function readAutoRefresh() {
  try { return localStorage.getItem(AUTOREFRESH_STORAGE) !== '0'; }
  catch { return true; }
}

function readErrorsTab() {
  try {
    const v = localStorage.getItem(ERRORS_TAB_STORAGE);
    if (v === 'failed' || v === 'patterns') return v;
  } catch { /* ignore */ }
  return 'failed';
}

export default function ErrorsPage() {
  const toast = useToast();
  const [presetIdx, setPresetIdx] = useState(3); // "Last 24h" default
  const [filter, setFilter] = useState({
    api_key_id: '',
    provider: '',
    model: '',
    request_id: '',
    error_class: '',
    error_fingerprint: '',
    customFrom: '',
    customTo: '',
    useCustomRange: false,
    interval: 'hour',
  });
  const [autoRefresh, setAutoRefresh] = useState(() => readAutoRefresh());
  // Selected display timezone (shared with Usage Stats via localStorage).
  const [timezone, setTimezone] = useState(() => loadTimezone());
  // Failed attempts vs Error patterns tab. Persisted so a reload keeps the
  // operator on the view they were using, mirroring the auto-refresh pref.
  const [errorsTab, setErrorsTab] = useState(() => readErrorsTab());
  // Grouping dimension for the Error patterns leaderboard.
  const [patternsGroupBy, setPatternsGroupBy] = useState('class');
  // Ref to the Failed attempts card so selecting a pattern can scroll to it.
  const tableRef = useRef(null);

  function changeTab(tab) {
    setErrorsTab(tab);
    try { localStorage.setItem(ERRORS_TAB_STORAGE, tab); } catch { /* ignore */ }
  }

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
    error_class: filter.error_class || undefined,
    error_fingerprint: filter.error_fingerprint || undefined,
    from: rangeParams.from,
    to: rangeParams.to,
  }), [filter.api_key_id, filter.provider, filter.model, filter.request_id, filter.error_class, filter.error_fingerprint, rangeParams.from, rangeParams.to]);

  // Totals power the KPI strip — failure_count and failure_rate come back
  // from the same totals endpoint that Usage Stats uses, scoped to the same
  // filter so the two pages agree on what "this window" means.
  const totals = useAsync(() => getUsageTotals(baseFilter), [JSON.stringify(baseFilter)]);
  // List filter dropdowns by time window only — see RecentEventsPage for why
  // scoping by the active filter would collapse the menus on refresh.
  const filterOptions = useAsync(
    () => getUsageFilterOptions({ from: baseFilter.from, to: baseFilter.to }),
    [baseFilter.from, baseFilter.to],
  );

  const [errorsPage, setErrorsPage] = useState(1);
  const errors = useAsync(
    () => getUsageErrors({ ...baseFilter, page: errorsPage, page_size: EVENTS_PAGE_SIZE, include: 'cost_breakdown' }),
    [JSON.stringify(baseFilter), errorsPage],
  );
  useEffect(() => { setErrorsPage(1); }, [JSON.stringify(baseFilter)]);

  // Error summary powers the class breakdown chip strip below the KPI cards.
  const summary = useAsync(() => getErrorSummary(baseFilter), [JSON.stringify(baseFilter)]);
  // Error timeline powers the mini per-class timeline.
  const timeline = useAsync(() => getErrorTimeline(baseFilter, rangeParams.interval || 'hour'), [JSON.stringify(baseFilter), rangeParams.interval]);

  // Error patterns leaderboard — only fetched when the Error patterns tab is
  // active so we don't waste bandwidth on a hidden view. The guard returns
  // Promise.resolve(null) which useAsync treats as a successful null result.
  const patternsActive = errorsTab === 'patterns';
  const errorGroups = useAsync(
    () => (patternsActive ? getErrorGroups(baseFilter, patternsGroupBy, 50) : Promise.resolve(null)),
    [JSON.stringify(baseFilter), patternsGroupBy, patternsActive],
  );

  const [selectedErrorId, setSelectedErrorId] = useState(null);

  const reloadAll = useCallback(() => {
    totals.reload();
    filterOptions.reload();
    errors.reload();
    summary.reload();
    timeline.reload();
    errorGroups.reload();
  }, [totals, filterOptions, errors, summary, timeline, errorGroups]);

  useAutoRefresh(reloadAll, AUTO_REFRESH_INTERVAL_MS, autoRefresh);

  function handleRefresh() {
    reloadAll();
    toast.info('Errors refreshed');
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

  // Called when a row in the Error patterns leaderboard is clicked. Maps the
  // group's dimension to the corresponding filter param so the Failed-attempts
  // table narrows to matches of that group. Afterwards switches to the table
  // tab and scrolls it into view.
  function onSelectGroup(group) {
    switch (patternsGroupBy) {
      case 'class':
        updateFilter({ error_class: group.key, error_fingerprint: '' });
        break;
      case 'fingerprint':
        updateFilter({ error_fingerprint: group.key, error_class: '' });
        break;
      case 'provider':
        updateFilter({ provider: group.key, error_class: '', error_fingerprint: '' });
        break;
      case 'model':
        updateFilter({ model: group.key, error_class: '', error_fingerprint: '' });
        break;
      case 'status':
        // The listing endpoint does NOT support status-based filtering; as a
        // best-effort fallback we filter by the group's error_class (if any).
        updateFilter({ error_class: group.error_class || '', error_fingerprint: '' });
        break;
      default:
        break;
    }
    changeTab('failed');
    // Scroll the Failed attempts card into view on a brief delay so React has
    // rendered the tab switch first.
    requestAnimationFrame(() => {
      tableRef.current?.scrollIntoView({ behavior: 'smooth', block: 'start' });
    });
  }

  const failedCount = totals.data?.totals?.failed_count || 0;
  const failureRate = totals.data?.failure_rate ?? 0;
  const totalAttempts = totals.data?.total_attempts ?? 0;
  const totalErrors = errors.data?.total || 0;

  // Class counts keyed by slug. The summary is computed over baseFilter, which
  // already carries the active class filter, so when a chip is selected only
  // that class comes back with a non-zero count — the strip then shows one
  // "active" chip rather than a misleading full breakdown.
  const classCounts = useMemo(() => {
    const map = new Map();
    for (const c of summary.data?.by_class || []) {
      map.set(c.error_class || '', (map.get(c.error_class || '') || 0) + (c.count || 0));
    }
    return map;
  }, [summary.data]);

  // Only surface classes the summary actually reported. If a class the
  // backend knows about is absent from this window it is a zero-count chip,
  // which is noise; the strip is driven by real data instead.
  const chipClasses = useMemo(() => {
    const seen = new Set();
    const out = [];
    for (const cls of ERROR_CLASSES) {
      if (classCounts.has(cls.slug)) {
        out.push(cls);
        seen.add(cls.slug);
      }
    }
    // Append any slug the backend returned that errorClass.js does not know
    // yet, so a new class is still visible before the map is updated.
    for (const slug of classCounts.keys()) {
      if (slug && !seen.has(slug)) out.push({ slug, label: classLabel(slug), tone: classTone(slug) });
    }
    return out;
  }, [classCounts]);

  // Flatten the per-class timeline into stacked columns. Each class's bucket
  // sequence is aligned on the union of buckets so the columns line up, and
  // the tallest column drives the percentage heights.
  const timelineModel = useMemo(() => {
    const series = timeline.data?.series || [];
    if (!series.length) return { columns: [], first: '', last: '' };
    const buckets = [];
    const index = new Map();
    for (const s of series) {
      for (const p of s.points || []) {
        if (!index.has(p.bucket)) {
          index.set(p.bucket, buckets.length);
          buckets.push(p.bucket);
        }
      }
    }
    buckets.sort();
    const columns = buckets.map((bucket) => ({ bucket, total: 0, segments: [] }));
    for (const s of series) {
      for (const p of s.points || []) {
        const col = columns[index.get(p.bucket)];
        if (!col) continue;
        col.segments.push({ slug: s.error_class || '', count: p.count || 0 });
        col.total += p.count || 0;
      }
    }
    const max = columns.reduce((m, c) => Math.max(m, c.total), 0) || 1;
    return {
      columns,
      max,
      first: buckets[0] || '',
      last: buckets[buckets.length - 1] || '',
    };
  }, [timeline.data]);

  const classChip = (cls) => {
    const count = classCounts.get(cls.slug) || 0;
    const active = filter.error_class === cls.slug;
    return (
      <button
        key={cls.slug}
        type="button"
        className={`err-class-chip ${active ? 'err-class-chip--active' : ''} ${count === 0 ? 'err-class-chip--empty' : ''}`}
        style={{ '--chip-tone': cls.tone }}
        onClick={() => updateFilter({ error_class: active ? '' : cls.slug })}
        title={active ? 'Clear the class filter' : `Filter to ${cls.label}`}
      >
        <span className="err-class-chip__dot" />
        <span className="err-class-chip__label">{cls.label}</span>
        <span className="err-class-chip__count">{count.toLocaleString()}</span>
      </button>
    );
  };

  return (
    <>
      <div className="main__header">
        <div>
          <h1 className="main__title">Errors</h1>
          <div className="main__subtitle">
            Failed-attempt records from the usage_errors table, scoped to the
            same time window and provider/key/model filters as Usage Stats.
            Drill into a row to see the upstream endpoint (target URL),
            client IP, and the full error message.
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
          <div className="stat-card__label">Errors in Window</div>
          <div className="stat-card__value">{totalErrors.toLocaleString()}</div>
          <div className="stat-card__hint">failed attempts matching the current filter</div>
        </div>
        <div className="stat-card">
          <div className="stat-card__label">Failed Attempts</div>
          <div className="stat-card__value">{failedCount.toLocaleString()}</div>
          <div className="stat-card__hint">
            {totalAttempts > 0 ? `of ${totalAttempts.toLocaleString()} total attempts` : 'in this window'}
          </div>
        </div>
        <div className="stat-card">
          <div className="stat-card__label">Failure Rate</div>
          <div className="stat-card__value">{failureRate.toFixed(1)}%</div>
          <div className="stat-card__hint">failed attempts / total attempts</div>
        </div>
      </div>

      {totals.error && <ErrorBanner error={totals.error} onRetry={totals.reload} />}

      {/* Class breakdown + per-class timeline. Each has its own loading/error
          state so a failing aggregation degrades on its own without taking the
          KPI cards or the table down with it. */}
      {summary.error && <ErrorBanner error={summary.error} onRetry={summary.reload} />}
      {!summary.error && chipClasses.length > 0 && (
        <div className="err-classes">
          {chipClasses.map(classChip)}
          {filter.error_class && (
            <button
              type="button"
              className="err-class-chip"
              style={{ '--chip-tone': 'var(--text-dim)' }}
              onClick={() => updateFilter({ error_class: '' })}
              title="Clear the class filter"
            >
              <span className="err-class-chip__label">Clear ✕</span>
            </button>
          )}
        </div>
      )}

      <div className="err-strip">
        <div className="err-strip__title">Errors over time by class</div>
        {timeline.loading && !timeline.data && <Spinner label="Loading timeline…" />}
        {timeline.error && <ErrorBanner error={timeline.error} onRetry={timeline.reload} />}
        {!timeline.error && !timeline.loading && timelineModel.columns.length === 0 && (
          <div className="err-timeline__empty">No errors in this window</div>
        )}
        {!timeline.error && timelineModel.columns.length > 0 && (
          <>
            <div className="err-timeline">
              {timelineModel.columns.map((col) => (
                <div
                  key={col.bucket}
                  className="err-timeline__col"
                  title={`${formatInTZ(col.bucket, timezone)} — ${col.total.toLocaleString()} error${col.total === 1 ? '' : 's'}`}
                >
                  {col.total === 0
                    ? <div className="err-timeline__seg err-timeline__seg--empty" />
                    : col.segments.map((seg, i) => (
                      <div
                        key={`${seg.slug}-${i}`}
                        className="err-timeline__seg"
                        style={{
                          background: classTone(seg.slug),
                          height: `${(seg.count / timelineModel.max) * 100}%`,
                        }}
                      />
                    ))}
                </div>
              ))}
            </div>
            <div className="err-timeline__foot">
              <span>{formatInTZ(timelineModel.first, timezone)}</span>
              <span>{formatInTZ(timelineModel.last, timezone)}</span>
            </div>
          </>
        )}
      </div>

      {/* Fingerprint hint chip — the Failed attempts table has no fingerprint
          column, so without this an operator can't tell why the list narrowed
          after picking a Message group. Clicking clears the fingerprint. */}
      {filter.error_fingerprint && (
        <div className="err-classes">
          <button
            type="button"
            className="err-class-chip"
            style={{ '--chip-tone': 'var(--purple)' }}
            onClick={() => updateFilter({ error_fingerprint: '' })}
            title="Clear the fingerprint filter"
          >
            <span className="err-class-chip__dot" />
            <span className="err-class-chip__label">
              filtered by fingerprint: {filter.error_fingerprint.slice(0, 12)}…
            </span>
            <span className="err-class-chip__count">Clear ✕</span>
          </button>
        </div>
      )}

      {/* Tab row — Failed attempts (the raw stream) vs Error patterns
          (the grouped leaderboard). */}
      <div className="seg-group" role="tablist" aria-label="Errors view" style={{ marginTop: 16 }}>
        <button
          type="button"
          className={`seg-btn ${errorsTab === 'failed' ? 'seg-btn--active' : ''}`}
          onClick={() => changeTab('failed')}
        >
          Failed attempts
        </button>
        <button
          type="button"
          className={`seg-btn ${errorsTab === 'patterns' ? 'seg-btn--active' : ''}`}
          onClick={() => changeTab('patterns')}
        >
          Error patterns
        </button>
      </div>

      {errorsTab === 'failed' && (
        <div className="card" style={{ marginTop: 16 }} ref={tableRef}>
          <div className="row row--between" style={{ marginBottom: 12 }}>
            <h3 className="card__title" style={{ margin: 0 }}>Failed attempts</h3>
          </div>
          <ErrorsTableBody
            errors={errors}
            page={errorsPage}
            pageSize={EVENTS_PAGE_SIZE}
            onPage={setErrorsPage}
            onRowClick={setSelectedErrorId}
            timezone={timezone}
          />
        </div>
      )}

      {errorsTab === 'patterns' && (
        <div className="card" style={{ marginTop: 16 }}>
          <div className="row row--between" style={{ marginBottom: 12 }}>
            <h3 className="card__title" style={{ margin: 0 }}>Error patterns</h3>
          </div>
          <ErrorGroupsPanel
            groups={errorGroups.data?.groups || []}
            loading={errorGroups.loading}
            error={errorGroups.error}
            groupBy={patternsGroupBy}
            onGroupByChange={setPatternsGroupBy}
            onSelectGroup={onSelectGroup}
            timezone={timezone}
            onRetry={errorGroups.reload}
          />
        </div>
      )}

      {selectedErrorId != null && (
        <ErrorDetailModal id={selectedErrorId} timezone={timezone} onClose={() => setSelectedErrorId(null)} />
      )}
    </>
  );
}

// ErrorsTableBody mirrors EventsTableBody but renders failed-attempt rows
// (usage_errors). Surfaces fail_status_code as a status badge and the
// error_message truncated, with the full text plus Target URL / IP in the
// detail modal.
function ErrorsTableBody({ errors, page, pageSize, onPage, onRowClick, timezone }) {
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
                <th>Time ({timezone})</th>
                <th>Request ID</th>
                <th>Key / Alias</th>
                <th>Provider Official</th>
                <th>Model Alias</th>
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
                  <th>Time ({timezone})</th>
                  <th>Request ID</th>
                  <th>Key / Alias</th>
                  <th>Provider Official</th>
                  <th>Model Alias</th>
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
                    <td className="mono" style={{ whiteSpace: 'nowrap' }} title={e.requested_at ? new Date(e.requested_at).toISOString() : ''}>
                      {e.requested_at ? formatInTZ(e.requested_at, timezone) : '—'}
                    </td>
                    <td className="mono" style={{ whiteSpace: 'nowrap' }}>{e.request_id || '—'}</td>
                    <td className="mono">{e.key_alias || e.api_key_id || '—'}</td>
                    <td>{e.official_provider || e.provider || '—'}</td>
                    <td className="mono">{e.alias || e.model || '—'}</td>
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

// ErrorDetailModal fetches and renders a single failed-attempt row from the
// usage_errors table. The sealed api_key_principal is intentionally never
// shown. Unlike EventDetailModal, every row here is a failure by construction
// so we surface the status code and full error_message prominently instead of
// a Failed yes/no row. The Target URL (endpoint column — the upstream URL the
// executor actually hit) and the client IP / forwarded-for fields are
// surfaced here so an operator can correlate a failure to a concrete upstream
// path and a concrete source IP without leaving the modal.
function ErrorDetailModal({ id, timezone, onClose }) {
  const detail = useAsync(() => getUsageError(id), [id]);
  const e = detail.data?.error_event;
  const substituted = Boolean(e?.served_model && e?.model && e.served_model !== e.model);
  return (
    <Modal title={`Error #${id}`} onClose={onClose} size="lg">
      {detail.loading && <Spinner label="Loading…" />}
      {detail.error && <ErrorBanner error={detail.error} />}
      {!detail.loading && !detail.error && e && (
        <>
          <div className="grid grid--2" style={{ gap: '6px 24px' }}>
            <DetailRow label={`Time (${tzAbbreviation(e.requested_at, timezone)})`} value={e.requested_at ? formatInTZ(e.requested_at, timezone) : '—'} mono />
            <DetailRow label="Time (UTC)" value={e.requested_at ? new Date(e.requested_at).toISOString() : '—'} mono />
          </div>

          <div className="detail-row__block-label" style={{ marginTop: 14 }}>Routing</div>
          <div className="grid grid--2" style={{ gap: '6px 24px' }}>
            <div className="detail-row">
              <div className="detail-row__label">Request ID</div>
              <div className="detail-row__value mono">
                {e.request_id || '—'}
                <CopyButton value={e.request_id} label="request id" />
              </div>
            </div>
            <DetailRow label="API Key ID" value={e.api_key_id || '—'} mono />
            <DetailRow label="Key Alias" value={e.key_alias || '—'} mono />
            <DetailRow label="Provider Official" value={e.official_provider || e.provider || '—'} />
            <DetailRow label="Provider (internal)" value={e.provider || '—'} mono />
            <DetailRow label="Route Model" value={e.route_model || '—'} mono />
            <DetailRow label="Model Alias" value={e.alias || e.model || '—'} mono />
            <DetailRow label="Model (resolved)" value={e.model || '—'} mono />
            <div className="detail-row">
              <div className="detail-row__label">Model (served)</div>
              <div className="detail-row__value mono">
                {e.served_model || '—'}
                {substituted && <span className="badge badge--warn" title="Upstream served a different model than requested">substituted</span>}
              </div>
            </div>
            <DetailRow label="Executor" value={e.executor_type || '—'} />
            <DetailRow label="Auth Type" value={e.auth_type || '—'} />
            <DetailRow label="Source" value={e.source || '—'} />
            <DetailRow label="Reasoning Effort" value={e.reasoning_effort || '—'} />
            <DetailRow label="Service Tier" value={e.service_tier || '—'} />
            <DetailRow label="Response Service Tier" value={e.response_service_tier || '—'} />
            <DetailRow label="Target URL" value={e.endpoint || '—'} mono />
          </div>

          <div style={{ marginTop: 14 }}>
            <TokenBreakdownCard e={e} />
          </div>

          <div className="detail-row__block-label" style={{ marginTop: 14 }}>Timing & cost</div>
          <div className="grid grid--2" style={{ gap: '6px 24px' }}>
            <DetailRow label="Cost (USD)" value={`$${(e.cost_usd || 0).toFixed(4)}`} mono />
            <DetailRow label="Latency" value={e.latency_ms ? `${e.latency_ms} ms` : '—'} mono />
            <DetailRow label="TTFT" value={e.ttft_ms ? `${e.ttft_ms} ms` : '—'} mono />
            <DetailRow label="Generate" value={e.generate ? 'true' : 'false'} mono />
          </div>

          <div className="detail-row__block-label" style={{ marginTop: 14 }}>Client</div>
          <div className="grid grid--2" style={{ gap: '6px 24px' }}>
            <DetailRow label="Client IP" value={e.client_ip || '—'} mono />
            <DetailRow label="Forwarded For" value={e.forwarded_for || '—'} mono />
            <DetailRow label="Status Code" value={e.fail_status_code ? String(e.fail_status_code) : '—'} mono />
          </div>

          <div className="detail-row__block">
            <div className="detail-row__block-label">Error message</div>
            <div className="mono" style={{ fontSize: 13, whiteSpace: 'pre-wrap', wordBreak: 'break-all', marginTop: 4 }}>
              {e.error_message || '—'}
            </div>
          </div>

          {/* Raw captured upstream response (headers + body) for this failed
              attempt, when body capture was enabled for the provider. Fetched
              through the errors :id/bodies endpoint so the same request_bodies
              row the Events modal shows is surfaced here too. */}
          <EventBodiesSection id={id} fetchBodies={getUsageErrorBodies} />

          <FailoverHistory
            requestId={e.request_id}
            currentId={e.id}
            currentKind="error"
            timezone={timezone}
          />
        </>
      )}
      {!detail.loading && !detail.error && !e && <EmptyState title="Error not found" />}
    </Modal>
  );
}
