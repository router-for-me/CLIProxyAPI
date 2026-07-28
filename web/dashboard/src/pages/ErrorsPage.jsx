import React, { useState, useMemo, useEffect, useCallback } from 'react';
import {
  getUsageErrors, getUsageError, getUsageFilterOptions, getUsageTotals,
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
  FilterSelect, DetailRow,
} from './usageShared.jsx';

// ErrorsPage renders the failed-attempt stream (usage_errors table) as a
// first-class sibling to Usage Stats. It was promoted from a tab on the
// Usage Stats page into its own sidebar entry so operators can triage errors
// without losing the events view. The filter / preset chrome is shared with
// Usage Stats so the same time window and provider/key/model filters behave
// identically across the two pages.

const AUTO_REFRESH_INTERVAL_MS = 60 * 1000;
const AUTOREFRESH_STORAGE = 'nixllm.dashboard.errorsAutorefresh';

function readAutoRefresh() {
  try { return localStorage.getItem(AUTOREFRESH_STORAGE) !== '0'; }
  catch { return true; }
}

export default function ErrorsPage() {
  const toast = useToast();
  const [presetIdx, setPresetIdx] = useState(3); // "Last 24h" default
  const [filter, setFilter] = useState({
    api_key_id: '',
    provider: '',
    model: '',
    request_id: '',
    customFrom: '',
    customTo: '',
    useCustomRange: false,
    interval: 'hour',
  });
  const [autoRefresh, setAutoRefresh] = useState(() => readAutoRefresh());
  // Selected display timezone (shared with Usage Stats via localStorage).
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

  // Totals power the KPI strip — failure_count and failure_rate come back
  // from the same totals endpoint that Usage Stats uses, scoped to the same
  // filter so the two pages agree on what "this window" means.
  const totals = useAsync(() => getUsageTotals(baseFilter), [JSON.stringify(baseFilter)]);
  const filterOptions = useAsync(
    () => getUsageFilterOptions(baseFilter),
    [JSON.stringify({ from: baseFilter.from, to: baseFilter.to })],
  );

  const [errorsPage, setErrorsPage] = useState(1);
  const errors = useAsync(
    () => getUsageErrors({ ...baseFilter, page: errorsPage, page_size: EVENTS_PAGE_SIZE, include: 'cost_breakdown' }),
    [JSON.stringify(baseFilter), errorsPage],
  );
  useEffect(() => { setErrorsPage(1); }, [JSON.stringify(baseFilter)]);

  const [selectedErrorId, setSelectedErrorId] = useState(null);

  const reloadAll = useCallback(() => {
    totals.reload();
    filterOptions.reload();
    errors.reload();
  }, [totals, filterOptions, errors]);

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

  const failedCount = totals.data?.totals?.failed_count || 0;
  const failureRate = totals.data?.failure_rate ?? 0;
  const totalErrors = errors.data?.total || 0;

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
          <div className="stat-card__hint">across the current page filter</div>
        </div>
        <div className="stat-card">
          <div className="stat-card__label">Failed Requests</div>
          <div className="stat-card__value">{failedCount.toLocaleString()}</div>
          <div className="stat-card__hint">recorded in usage_events for this window</div>
        </div>
        <div className="stat-card">
          <div className="stat-card__label">Failure Rate</div>
          <div className="stat-card__value">{failureRate.toFixed(1)}%</div>
          <div className="stat-card__hint">failed / total requests</div>
        </div>
      </div>

      {totals.error && <ErrorBanner error={totals.error} onRetry={totals.reload} />}

      {/* Errors table */}
      <div className="card" style={{ marginTop: 16 }}>
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
  return (
    <Modal title={`Error #${id}`} onClose={onClose} size="lg">
      {detail.loading && <Spinner label="Loading…" />}
      {detail.error && <ErrorBanner error={detail.error} />}
      {!detail.loading && !detail.error && e && (
        <div className="grid grid--2" style={{ gap: '6px 24px' }}>
          <DetailRow label={`Time (${tzAbbreviation(e.requested_at, timezone)})`} value={e.requested_at ? formatInTZ(e.requested_at, timezone) : '—'} mono />
          <DetailRow label="Time (UTC)" value={e.requested_at ? new Date(e.requested_at).toISOString() : '—'} mono />
          <DetailRow label="Request ID" value={e.request_id || '—'} mono />
          <DetailRow label="API Key ID" value={e.api_key_id || '—'} mono />
          <DetailRow label="Key Alias" value={e.key_alias || '—'} mono />
          <DetailRow label="Provider Official" value={e.official_provider || e.provider || '—'} />
          <DetailRow label="Provider (internal)" value={e.provider || '—'} mono />
          <DetailRow label="Model Alias" value={e.alias || e.model || '—'} mono />
          <DetailRow label="Model (resolved)" value={e.model || '—'} mono />
          <DetailRow label="Route Model" value={e.route_model || '—'} mono />
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
          <DetailRow label="Target URL" value={e.endpoint || '—'} mono />
          <DetailRow label="Client IP" value={e.client_ip || '—'} mono />
          <DetailRow label="Forwarded For" value={e.forwarded_for || '—'} mono />
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
