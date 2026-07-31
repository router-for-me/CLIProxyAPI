import React, { useState, useMemo, useCallback, useEffect, useRef } from 'react';
import {
  getModelHealth,
  getModelHealthLog,
  getModelHealthLogEntry,
  getModelHealthModels,
  clearModelHealthLog,
  getModelHealthSettings,
  putModelHealthSettings,
  runModelHealthCheckNow,
  runModelHealthProbe,
} from '../api/client.js';
import { useAsync } from '../hooks/useAsync.js';
import { useAutoRefresh } from '../hooks/useAutoRefresh.js';
import { Spinner, ErrorBanner, EmptyState, Modal } from '../components/Primitives.jsx';
import Pager from '../components/Pager.jsx';
import { useToast } from '../components/Toast.jsx';
import { loadTimezone } from './usageShared.jsx';

// ModelHealthPage surfaces the outcome of periodic per-model inference
// probes: status (operational/degraded/unavailable), response time, tokens per
// second, and token usage for every live (Global) model id. Operators can
// configure the probe cadence + excluded models in the Settings modal and
// trigger an immediate sweep with "Run check now". A curated, unauthenticated
// rollup is also published at GET /v0/model-health/uptime.
//
// The page mirrors the Cooldown Providers page (live snapshot + autorefresh)
// for the latest-status table, and the Upstream Providers page (filtered /
// paged history with a detail modal) for the trend view beneath.

const AUTO_REFRESH_INTERVAL_MS = 15 * 1000;
const AUTOREFRESH_STORAGE = 'nixllm.dashboard.modelHealthAutorefresh';
const PAGE_SIZE = 50;

// MAX_TOKENS_PER_PROBE mirrors store.MaxModelHealthTokens on the backend. The
// input is bounded by it so an operator never types a value the server will
// reject; validation in handleSave converts a stray out-of-range value into a
// toast rather than silently clamping it away.
const MAX_TOKENS_PER_PROBE = 1024;

// History retention bounds — mirror the backend store constants so the Settings
// inputs validate client-side before submitting. See model_health.go.
const MIN_RETENTION_DAYS = 1;   // 0 = disabled (handled separately)
const MAX_RETENTION_DAYS = 365;
const MIN_MAX_LOG_ROWS = 10;     // 0 = unlimited (handled separately)
const MAX_MAX_LOG_ROWS = 100000;

const STATUS_OPTIONS = [
  { value: '', label: 'All statuses' },
  { value: 'operational', label: 'Operational' },
  { value: 'degraded', label: 'Degraded' },
  { value: 'unavailable', label: 'Unavailable' },
];

function readAutoRefresh() {
  try { return localStorage.getItem(AUTOREFRESH_STORAGE) !== '0'; }
  catch { return true; }
}

export default function ModelHealthPage() {
  const toast = useToast();
  const tz = useMemo(loadTimezone, []);
  const [autoRefresh, setAutoRefresh] = useState(() => readAutoRefresh());

  // Latest snapshot + settings (top section).
  const snapshot = useAsync(() => getModelHealth(), []);

  // History table (filter + pager).
  const [page, setPage] = useState(1);
  const [modelFilter, setModelFilter] = useState('');
  const [statusFilter, setStatusFilter] = useState('');
  const [selectedLogId, setSelectedLogId] = useState(null);
  const [clearing, setClearing] = useState(false);

  // Settings modal state.
  const [settingsOpen, setSettingsOpen] = useState(false);
  const [settingsSaving, setSettingsSaving] = useState(false);

  // Run-now state (dashboard shows "running…" until the next refresh lands
  // a newer last_run_at).
  const [runPending, setRunPending] = useState(false);

  // Per-model "Check now" state: the set of model ids currently being probed
  // synchronously via POST /model-health/probe/:model. A Set (rather than a
  // single pending id) so an operator can check several models in quick
  // succession; each row shows its own spinner + disabled button until its
  // probe resolves. The backend de-duplicates a duplicate click on the same
  // row (409 probe_in_flight) — this state mirrors that client-side so the
  // button is disabled while a request is in flight even without the round
  // trip.
  const [probingModels, setProbingModels] = useState(() => new Set());

  const filterKey = JSON.stringify({ modelFilter, statusFilter });
  const modelsReq = useAsync(() => getModelHealthModels(), []);

  const listParams = useMemo(() => ({
    model_id: modelFilter,
    status: statusFilter,
    page,
    page_size: PAGE_SIZE,
  }), [modelFilter, statusFilter, page]);

  const list = useAsync(() => getModelHealthLog(listParams), [
    JSON.stringify(listParams),
  ]);

  const reloadAll = useCallback(() => {
    snapshot.reload();
    list.reload();
  }, [snapshot, list]);
  const reloadList = useCallback(() => { list.reload(); }, [list]);
  useAutoRefresh(reloadAll, AUTO_REFRESH_INTERVAL_MS, autoRefresh);

  // Reset to page 1 whenever a filter changes so the operator never lands on
  // an empty out-of-range page after tightening a filter.
  useEffect(() => { setPage(1); }, [filterKey]);

  // Clear the "running…" chip once a fresh sweep completes (last_run_at advanced).
  const prevRunAtRef = useRef(snapshot.data?.last_run_at);
  useEffect(() => {
    const at = snapshot.data?.last_run_at;
    if (at && at !== prevRunAtRef.current && prevRunAtRef.current !== undefined) {
      setRunPending(false);
    }
    prevRunAtRef.current = at;
  }, [snapshot.data?.last_run_at]);

  function toggleAutoRefresh() {
    setAutoRefresh((v) => {
      const next = !v;
      try { localStorage.setItem(AUTOREFRESH_STORAGE, next ? '1' : '0'); } catch { /* ignore */ }
      return next;
    });
  }

  function handleRefresh() {
    reloadAll();
    toast.info('Model health refreshed');
  }

  async function handleRunNow() {
    setRunPending(true);
    try {
      await runModelHealthCheckNow();
      toast.success('Health check triggered — results will appear shortly');
      // Nudge the snapshot reload after a short delay so the in-flight sweep
      // has time to record outcomes. The autorefresh will keep converging.
      setTimeout(reloadAll, 2500);
    } catch (err) {
      setRunPending(false);
      toast.error(err?.message || 'Failed to trigger health check');
    }
  }

  // Probe a single model id synchronously (the per-row "Check now" action).
  // The backend blocks until the probe completes (~1s) and returns the
  // recorded row, so we reload the snapshot immediately on success to reflect
  // the new status / response time / checked_at for that model. A duplicate
  // click is rejected by the backend (409 probe_in_flight) and surfaced as a
  // non-blocking info toast rather than the generic error tone.
  async function handleCheckModel(modelID) {
    if (!modelID || probingModels.has(modelID)) return;
    setProbingModels((cur) => {
      const next = new Set(cur);
      next.add(modelID);
      return next;
    });
    try {
      await runModelHealthProbe(modelID);
      // Snapshot carries the latest status row for this model; reload it so
      // the row updates inline without waiting for the 15s autorefresh.
      snapshot.reload();
      toast.success(`Probe complete for ${modelID}`);
    } catch (err) {
      // 409 = a probe for this model was already in flight (double-click or a
      // concurrent sweep on the same id); surface as info rather than error.
      const status = err?.status;
      if (status === 409) {
        toast.info(err?.message || `A check for ${modelID} is already running`);
      } else {
        toast.error(err?.message || `Failed to probe ${modelID}`);
      }
    } finally {
      setProbingModels((cur) => {
        const next = new Set(cur);
        next.delete(modelID);
        return next;
      });
    }
  }

  // Toggle the scheduled sweep on/off. Sends a partial PATCH so only `enabled`
  // changes — the rest of the settings are untouched server-side. The snapshot
  // response carries `settings`, so reloading it after a successful toggle
  // keeps the header chip + the Settings modal's "Sweep enabled" checkbox in
  // sync without a separate fetch. An in-flight sweep (if one is running) is
  // allowed to finish gracefully: the sweep loop checks `enabled` per model,
  // so turning it off halts further ticks but never cuts a probe mid-flight.
  const [enabledPending, setEnabledPending] = useState(false);
  async function toggleEnabled() {
    const next = !settings.enabled;
    setEnabledPending(true);
    try {
      await putModelHealthSettings({ enabled: next });
      await snapshot.reload();
      toast.success(next ? 'Model health sweep enabled' : 'Model health sweep stopped');
    } catch (err) {
      toast.error(err?.message || 'Failed to toggle model health sweep');
    } finally {
      setEnabledPending(false);
    }
  }

  async function handleClearLog() {
    if (!window.confirm('Clear all model health history rows? Latest snapshots are preserved. This cannot be undone.')) return;
    setClearing(true);
    try {
      const res = await clearModelHealthLog();
      toast.success(`Cleared ${Number(res?.deleted || 0).toLocaleString()} history row(s)`);
      setPage(1);
      modelsReq.reload();
      reloadList();
    } catch (err) {
      toast.error(err?.message || 'Failed to clear history');
    } finally {
      setClearing(false);
    }
  }

  const snapshots = snapshot.data?.snapshots || [];
  const settings = snapshot.data?.settings || {
    enabled: true, interval_seconds: 900, excluded_models: [], max_tokens: 1,
  };
  const lastRunAt = snapshot.data?.last_run_at;
  const lastRunSummary = snapshot.data?.last_run_summary;
  const total = list.data?.total || 0;
  const totalPages = Math.max(1, Math.ceil(total / PAGE_SIZE));

  // KPIs computed from the latest snapshot set.
  const operationalCount = snapshots.filter((s) => s.status === 'operational').length;
  const degradedCount = snapshots.filter((s) => s.status === 'degraded').length;
  const unavailableCount = snapshots.filter((s) => s.status === 'unavailable').length;
  const measured = snapshots.filter((s) => s.tokens_per_second != null);
  const avgTps = measured.length
    ? measured.reduce((a, s) => a + (s.tokens_per_second || 0), 0) / measured.length
    : null;
  const avgRt = snapshots.length
    ? snapshots.reduce((a, s) => a + (s.response_time_ms || 0), 0) / snapshots.length
    : null;

  const modelOptions = (modelsReq.data?.models || []).map((m) => ({ value: m, label: m }));

  return (
    <>
      <div className="main__header">
        <div>
          <h1 className="main__title">Model Health</h1>
          <div className="main__subtitle">
            Periodic inference probes (status, response time, tokens per second)
            for every live Global model id. A curated, unauthenticated rollup is
            published at <code>GET /v0/model-health/uptime</code>. Source:{' '}
            <code>model_health</code> + <code>model_health_log</code> tables
            (30-day retention). Auto-refreshes every 15s.
          </div>
        </div>
        <div className="row gap-sm">
          <button
            className={`autorefresh-chip ${settings.enabled ? '' : 'autorefresh-chip--off'}`}
            onClick={toggleEnabled}
            disabled={enabledPending}
            title={
              enabledPending
                ? 'Updating…'
                : settings.enabled
                  ? 'Sweep is running on a schedule — click to stop'
                  : 'Sweep is stopped — click to enable'
            }
            aria-pressed={settings.enabled}
          >
            <span className="autorefresh-chip__dot" />
            {settings.enabled ? 'Running' : 'Stopped'}
          </button>
          <button
            className={`autorefresh-chip ${autoRefresh ? '' : 'autorefresh-chip--off'}`}
            onClick={toggleAutoRefresh}
            title={autoRefresh ? 'Auto-refresh every 15s — click to pause' : 'Auto-refresh paused — click to resume'}
          >
            <span className="autorefresh-chip__dot" />
            {autoRefresh ? 'Live' : 'Paused'}
          </button>
          <button onClick={() => setSettingsOpen(true)}>Settings</button>
          <button onClick={handleRunNow} disabled={runPending}>
            {runPending ? 'Running…' : 'Run check now'}
          </button>
          <button onClick={handleRefresh}>Refresh</button>
        </div>
      </div>

      {/* KPI strip */}
      <div className="stats-grid">
        <div className="stat-card">
          <div className="stat-card__label">Operational</div>
          <div className="stat-card__value">{operationalCount.toLocaleString()}</div>
          <div className="stat-card__hint">models responding healthy</div>
        </div>
        <div className="stat-card">
          <div className="stat-card__label">Degraded</div>
          <div className="stat-card__value">{degradedCount.toLocaleString()}</div>
          <div className="stat-card__hint">slow or low-TPS</div>
        </div>
        <div className="stat-card">
          <div className="stat-card__label">Unavailable</div>
          <div className="stat-card__value">{unavailableCount.toLocaleString()}</div>
          <div className="stat-card__hint">probes failed</div>
        </div>
        <div className="stat-card">
          <div className="stat-card__label">Avg response time</div>
          <div className="stat-card__value">{avgRt != null ? Math.round(avgRt).toLocaleString() + 'ms' : '—'}</div>
          <div className="stat-card__hint">across {snapshots.length} model(s)</div>
        </div>
        <div className="stat-card">
          <div className="stat-card__label">Avg tokens/sec</div>
          <div className="stat-card__value">{avgTps != null ? avgTps.toFixed(1) : '—'}</div>
          <div className="stat-card__hint">{measured.length} measured</div>
        </div>
      </div>

      {lastRunAt && (
        <div className="dim" style={{ fontSize: 12, marginTop: 4 }}>
          {runPending ? 'Sweep in progress…' : 'Last sweep:'}{' '}
          {formatInTz(lastRunAt, tz)}
          {lastRunSummary ? ` — ${lastRunSummary}` : ''}
          {!settings.enabled && ' · (sweep disabled in Settings)'}
        </div>
      )}

      {snapshot.error && <ErrorBanner error={snapshot.error} onRetry={reloadAll} />}

      {/* Latest snapshot table */}
      <div className="card" style={{ marginTop: 16 }}>
        <div className="row row--between" style={{ marginBottom: 12 }}>
          <h3 className="card__title" style={{ margin: 0 }}>Latest status</h3>
          <span className="mono" style={{ fontSize: 12, color: 'var(--text-dim)' }}>
            {snapshots.length} model(s) · interval {Math.round((settings.interval_seconds || 900) / 60)}m
          </span>
        </div>
        <SnapshotTableBody
          loading={snapshot.loading}
          error={snapshot.error}
          rows={snapshots}
          tz={tz}
          onCheck={handleCheckModel}
          probingModels={probingModels}
        />
      </div>

      {/* History filter bar */}
      <div className="card" style={{ marginTop: 16 }}>
        <div className="row gap-sm" style={{ flexWrap: 'wrap', alignItems: 'center' }}>
          <label className="filter-label">
            Model
            <select value={modelFilter} onChange={(e) => setModelFilter(e.target.value)}>
              <option value="">All models</option>
              {modelOptions.map((o) => (
                <option key={o.value} value={o.value}>{o.label}</option>
              ))}
            </select>
          </label>
          <label className="filter-label">
            Status
            <select value={statusFilter} onChange={(e) => setStatusFilter(e.target.value)}>
              {STATUS_OPTIONS.map((o) => (
                <option key={o.value} value={o.value}>{o.label}</option>
              ))}
            </select>
          </label>
          <button
            onClick={handleClearLog}
            disabled={clearing || total === 0}
            style={{ marginLeft: 'auto' }}
          >
            {clearing ? 'Clearing…' : 'Clear history'}
          </button>
        </div>
      </div>

      {/* History table */}
      <div className="card" style={{ marginTop: 16 }}>
        <div className="row row--between" style={{ marginBottom: 12 }}>
          <h3 className="card__title" style={{ margin: 0 }}>History</h3>
          <span className="mono" style={{ fontSize: 12, color: 'var(--text-dim)' }}>
            {total.toLocaleString()} row(s) matching filter
          </span>
        </div>
        <HistoryTableBody
          loading={list.loading}
          error={list.error}
          events={list.data?.events || []}
          tz={tz}
          onRowClick={setSelectedLogId}
        />
        <Pager
          page={page}
          totalPages={totalPages}
          total={total}
          pageSize={PAGE_SIZE}
          onPageChange={setPage}
        />
      </div>

      {settingsOpen && (
        <SettingsModal
          settings={settings}
          modelOptions={modelOptions}
          saving={settingsSaving}
          toast={toast}
          onClose={() => setSettingsOpen(false)}
          onSave={async (next) => {
            setSettingsSaving(true);
            try {
              const res = await putModelHealthSettings(next);
              toast.success('Model health settings saved');
              setSettingsOpen(false);
              // Refresh both the snapshot (carries settings) and the
              // modelsReq dropdown so newly excluded/added models reflect.
              snapshot.reload();
              modelsReq.reload();
              void res;
            } catch (err) {
              toast.error(err?.message || 'Failed to save settings');
            } finally {
              setSettingsSaving(false);
            }
          }}
        />
      )}

      {selectedLogId != null && (
        <LogEntryModal id={selectedLogId} tz={tz} onClose={() => setSelectedLogId(null)} />
      )}
    </>
  );
}

// SnapshotTableBody renders the latest per-model health status. Headers are
// always shown (even when empty) so the table shape stays stable across polls.
// The trailing Actions column carries the per-row "Check now" button that
// triggers a synchronous single-model probe (POST /model-health/probe/:model).
function SnapshotTableBody({ loading, error, rows, tz, onCheck, probingModels }) {
  if (loading) {
    return (
      <TableShell cols={8} actions>
        {Array.from({ length: 4 }).map((_, i) => (
          <tr key={i} className="skeleton-row">
            {Array.from({ length: 8 }).map((__, j) => (
              <td key={j}><span className="skeleton-line" /></td>
            ))}
          </tr>
        ))}
      </TableShell>
    );
  }
  if (error) return <ErrorBanner error={error} />;
  return (
    <TableShell cols={8} actions>
      {rows.length === 0 && (
        <tr>
          <td colSpan={8} style={{ textAlign: 'center', padding: '24px 8px' }}>
            No probes recorded yet. Click <strong>Run check now</strong> or wait for the next scheduled sweep.
          </td>
        </tr>
      )}
      {rows.map((r, i) => {
        const id = r.model_id || '';
        const probing = !!id && probingModels.has(id);
        return (
          <tr key={`${id}|${i}`}>
            <td className="mono">{id || '—'}</td>
            <td><HealthStatusBadge status={r.status} /></td>
            <td className="mono">{r.tokens_per_second != null ? r.tokens_per_second.toFixed(1) : '—'}</td>
            <td className="mono">{r.response_time_ms ? r.response_time_ms.toLocaleString() + 'ms' : '—'}</td>
            <td className="mono">{r.prompt_tokens || 0}/{r.completion_tokens || 0}</td>
            <td className="mono">{r.provider || '—'}</td>
            <td className="mono" title={r.checked_at ? formatInTz(r.checked_at, tz) : ''}>
              {r.checked_at ? formatInTz(r.checked_at, tz) : '—'}
            </td>
            <td>
              <button
                className="ghost"
                onClick={() => onCheck?.(id)}
                disabled={!id || probing}
                title={probing ? `Checking ${id}…` : `Probe ${id} now`}
              >
                {probing ? 'Checking…' : 'Check now'}
              </button>
            </td>
          </tr>
        );
      })}
    </TableShell>
  );
}

// HistoryTableBody renders the paged model_health_log rows. Clicking a row
// opens the detail modal with the full (unsealed) error_message.
function HistoryTableBody({ loading, error, events, tz, onRowClick }) {
  if (loading) {
    return (
      <TableShell cols={7}>
        {Array.from({ length: 4 }).map((_, i) => (
          <tr key={i} className="skeleton-row">
            {Array.from({ length: 7 }).map((__, j) => (
              <td key={j}><span className="skeleton-line" /></td>
            ))}
          </tr>
        ))}
      </TableShell>
    );
  }
  if (error) return <ErrorBanner error={error} />;
  return (
    <TableShell cols={7} clickable>
      {events.length === 0 && (
        <tr>
          <td colSpan={7} style={{ textAlign: 'center', padding: '24px 8px' }}>
            No history rows match the current filter.
          </td>
        </tr>
      )}
      {events.map((e, i) => (
        <tr key={`${e.id || i}`} onClick={() => onRowClick?.(e.id)} style={{ cursor: 'pointer' }}>
          <td className="mono">{e.model_id || '—'}</td>
          <td><HealthStatusBadge status={e.status} /></td>
          <td className="mono">{e.tokens_per_second != null ? e.tokens_per_second.toFixed(1) : '—'}</td>
          <td className="mono">{e.response_time_ms ? e.response_time_ms.toLocaleString() + 'ms' : '—'}</td>
          <td className="mono">{e.prompt_tokens || 0}/{e.completion_tokens || 0}</td>
          <td className="mono">{e.provider || '—'}</td>
          <td className="mono" title={e.checked_at ? formatInTz(e.checked_at, tz) : ''}>
            {e.checked_at ? formatInTz(e.checked_at, tz) : '—'}
          </td>
        </tr>
      ))}
    </TableShell>
  );
}

function TableShell({ children, cols, clickable, actions }) {
  return (
    <div style={{ overflowX: 'auto' }}>
      <table className={`table${clickable ? ' table--clickable' : ''}`}>
        <thead>
          <tr>
            <th>Model</th>
            <th>Status</th>
            <th>Tokens/sec</th>
            <th>Response time</th>
            <th>Tokens (in/out)</th>
            <th>Provider</th>
            <th>Checked</th>
            {actions && <th style={{ textAlign: 'right' }}>Actions</th>}
          </tr>
        </thead>
        <tbody>{children}</tbody>
      </table>
    </div>
  );
}

// HealthStatusBadge renders a colored badge for a model health status. Uses
// the existing badge variants so it matches the rest of the dashboard.
function HealthStatusBadge({ status }) {
  const cls = {
    operational: 'badge--active',
    degraded: 'badge--expired',
    unavailable: 'badge--revoked',
    unknown: 'badge--muted',
  }[status] || 'badge--muted';
  return <span className={`badge ${cls}`}>{status || 'unknown'}</span>;
}

// SettingsModal edits the model health sweep configuration: enable/disable,
// probe interval (minutes), max_tokens per probe, and the excluded-models
// multiselect populated from models seen in the log + current snapshot.
function SettingsModal({ settings, modelOptions, saving, toast, onClose, onSave }) {
  const [enabled, setEnabled] = useState(!!settings.enabled);
  const [intervalMin, setIntervalMin] = useState(
    Math.max(1, Math.round((settings.interval_seconds || 900) / 60)),
  );
  const [maxTokens, setMaxTokens] = useState(Math.max(1, settings.max_tokens || 1));
  // History retention. retention_days=0 disables time-based pruning;
  // max_log_rows=0 disables the per-model row cap.
  const [retentionDays, setRetentionDays] = useState(
    settings.retention_days != null ? Math.max(0, settings.retention_days) : 30,
  );
  const [maxLogRows, setMaxLogRows] = useState(
    settings.max_log_rows != null ? Math.max(0, settings.max_log_rows) : 1000,
  );
  // Excluded-model entries with add/remove. Initialized from the current
  // settings; the operator can also pick from known models.
  const [excluded, setExcluded] = useState(() => (settings.excluded_models || []).slice());
  const [pending, setPending] = useState('');

  function addExcluded(value) {
    const v = (value || '').trim();
    if (!v) return;
    if (excluded.some((m) => m.toLowerCase() === v.toLowerCase())) return;
    setExcluded((cur) => [...cur, v]);
    setPending('');
  }
  function removeExcluded(value) {
    setExcluded((cur) => cur.filter((m) => m !== value));
  }

  const knownModels = modelOptions.map((o) => o.value);

  function handleSave() {
    // Validate max_tokens client-side so an out-of-range value surfaces as a
    // toast instead of being silently clamped by the backend (or rejected with
    // a 400). The input is bounded by MAX_TOKENS_PER_PROBE, but a typed value
    // can still exceed it, so guard before saving.
    const parsedTokens = Number(maxTokens);
    if (!Number.isFinite(parsedTokens) || parsedTokens < 1 || parsedTokens > MAX_TOKENS_PER_PROBE) {
      toast?.error(`Max tokens per probe must be between 1 and ${MAX_TOKENS_PER_PROBE}`);
      return;
    }
    // retention_days: 0 disables time-based pruning; non-zero must fall in
    // [MIN_RETENTION_DAYS, MAX_RETENTION_DAYS]. Negative is invalid.
    const parsedRetention = Number(retentionDays);
    if (!Number.isFinite(parsedRetention) || parsedRetention < 0 ||
        (parsedRetention > 0 && (parsedRetention < MIN_RETENTION_DAYS || parsedRetention > MAX_RETENTION_DAYS))) {
      toast?.error(`Retention days must be 0 (disabled) or between ${MIN_RETENTION_DAYS} and ${MAX_RETENTION_DAYS}`);
      return;
    }
    // max_log_rows: 0 = unlimited; non-zero must fall in
    // [MIN_MAX_LOG_ROWS, MAX_MAX_LOG_ROWS]. Negative is invalid.
    const parsedRows = Number(maxLogRows);
    if (!Number.isFinite(parsedRows) || parsedRows < 0 ||
        (parsedRows > 0 && (parsedRows < MIN_MAX_LOG_ROWS || parsedRows > MAX_MAX_LOG_ROWS))) {
      toast?.error(`Max log rows must be 0 (unlimited) or between ${MIN_MAX_LOG_ROWS} and ${MAX_MAX_LOG_ROWS.toLocaleString()}`);
      return;
    }
    onSave({
      enabled,
      interval_seconds: Math.max(5, Math.round(intervalMin)) * 60,
      max_tokens: Math.round(parsedTokens),
      retention_days: Math.round(parsedRetention),
      max_log_rows: Math.round(parsedRows),
      excluded_models: excluded,
    });
  }

  return (
    <Modal
      title="Model Health settings"
      onClose={onClose}
      size="lg"
      footer={
        <>
          <button onClick={onClose}>Cancel</button>
          <button onClick={handleSave} disabled={saving}>
            {saving ? 'Saving…' : 'Save'}
          </button>
        </>
      }
    >
      <div className="row gap-sm" style={{ flexWrap: 'wrap', alignItems: 'center', marginBottom: 16 }}>
        <label className="filter-label" style={{ flex: '0 0 auto' }}>
          <input
            type="checkbox"
            checked={enabled}
            onChange={(e) => setEnabled(e.target.checked)}
            style={{ marginRight: 6 }}
          />
          Sweep enabled
        </label>
        <label className="filter-label">
          Interval (minutes)
          <input
            type="number"
            min={5}
            value={intervalMin}
            onChange={(e) => setIntervalMin(e.target.value)}
            style={{ width: 90 }}
          />
        </label>
        <label className="filter-label">
          Max tokens per probe
          <input
            type="number"
            min={1}
            max={MAX_TOKENS_PER_PROBE}
            value={maxTokens}
            onChange={(e) => setMaxTokens(e.target.value)}
            style={{ width: 90 }}
          />
        </label>
      </div>
      <div className="dim" style={{ fontSize: 12, marginBottom: 12 }}>
        Each probe sends one tiny inference request (generating {Math.max(1, Number(maxTokens) || 0)} token(s)
        per model) to measure response time + tokens-per-second. Interval floor is 5 minutes;
        max tokens per probe is capped at {MAX_TOKENS_PER_PROBE.toLocaleString()}.
      </div>

      <label className="filter-label" style={{ marginBottom: 6 }}>History retention</label>
      <div className="row gap-sm" style={{ flexWrap: 'wrap', alignItems: 'center', marginBottom: 4 }}>
        <label className="filter-label">
          Retention (days)
          <input
            type="number"
            min={0}
            value={retentionDays}
            onChange={(e) => setRetentionDays(e.target.value)}
            style={{ width: 90 }}
            title="0 disables time-based pruning; else rows older than this are purged hourly"
          />
        </label>
        <label className="filter-label">
          Max rows per model
          <input
            type="number"
            min={0}
            value={maxLogRows}
            onChange={(e) => setMaxLogRows(e.target.value)}
            style={{ width: 110 }}
            title="0 keeps unlimited history; else each model is trimmed to its newest N rows hourly"
          />
        </label>
      </div>
      <div className="dim" style={{ fontSize: 12, marginBottom: 12 }}>
        History older than the retention window, or beyond the per-model row cap, is auto-cleaned
        hourly. Set retention days to <strong>0</strong> to disable time-based pruning, or max rows
        to <strong>0</strong> for unlimited history. Floors: {MIN_RETENTION_DAYS} day /{' '}
        {MIN_MAX_LOG_ROWS} rows.
      </div>

      <label className="filter-label" style={{ marginBottom: 6 }}>Excluded models</label>
      <div className="row gap-sm" style={{ alignItems: 'center', marginBottom: 8 }}>
        <select
          value={pending}
          onChange={(e) => setPending(e.target.value)}
          style={{ flex: 1, minWidth: 200 }}
        >
          <option value="">Pick a known model…</option>
          {knownModels
            .filter((m) => !excluded.some((x) => x.toLowerCase() === m.toLowerCase()))
            .map((m) => (
              <option key={m} value={m}>{m}</option>
            ))}
        </select>
        <button onClick={() => addExcluded(pending)} disabled={!pending}>Add</button>
      </div>
      <input
        type="text"
        placeholder="…or type any model id and press Enter"
        value={pending}
        onChange={(e) => setPending(e.target.value)}
        onKeyDown={(e) => {
          if (e.key === 'Enter') {
            e.preventDefault();
            addExcluded(pending);
          }
        }}
        style={{ width: '100%', marginBottom: 12 }}
      />
      {excluded.length === 0 ? (
        <div className="dim" style={{ fontSize: 12 }}>No models excluded — every live model id is probed.</div>
      ) : (
        <div className="row gap-sm" style={{ flexWrap: 'wrap' }}>
          {excluded.map((m) => (
            <span key={m} className="badge badge--muted" style={{ display: 'inline-flex', alignItems: 'center', gap: 6 }}>
              <span className="mono">{m}</span>
              <button
                onClick={() => removeExcluded(m)}
                style={{ padding: '0 4px', lineHeight: 1, fontSize: 14 }}
                title={`Remove ${m}`}
              >×</button>
            </span>
          ))}
        </div>
      )}
    </Modal>
  );
}

// LogEntryModal fetches and shows a single model_health_log row, including the
// full (unsealed) error_message, prompt_message, and completion text.
function LogEntryModal({ id, tz, onClose }) {
  const entry = useAsync(() => getModelHealthLogEntry(id), [id]);
  const [showPrompt, setShowPrompt] = useState(false);
  const [showCompletion, setShowCompletion] = useState(false);
  return (
    <Modal title="Health check detail" onClose={onClose} size="lg">
      {entry.loading && <Spinner />}
      <ErrorBanner error={entry.error} />
      {entry.data && (
        <div>
          <DetailRow label="Model" value={entry.data.model_id} mono />
          <DetailRow label="Status" value={<HealthStatusBadge status={entry.data.status} />} />
          <DetailRow label="Success" value={entry.data.success ? 'yes' : 'no'} />
          <DetailRow label="Response time" value={entry.data.response_time_ms ? entry.data.response_time_ms.toLocaleString() + 'ms' : '—'} mono />
          <DetailRow label="Tokens/sec" value={entry.data.tokens_per_second != null ? entry.data.tokens_per_second.toFixed(1) : '—'} mono />
          <DetailRow label="Tokens (in/out)" value={`${entry.data.prompt_tokens || 0}/${entry.data.completion_tokens || 0}`} mono />
          <DetailRow label="Provider" value={entry.data.provider || '—'} mono />

          <div className="filter-label" style={{ marginTop: 12, marginBottom: 4 }}>Upstream provider</div>
          <div className="row gap-sm" style={{ flexWrap: 'wrap', fontSize: 12 }}>
            <DetailRow label="Provider" value={entry.data.upstream_provider || '—'} mono />
            <DetailRow label="Auth ID" value={entry.data.upstream_auth_id || '—'} mono />
            <DetailRow label="Resolved model" value={entry.data.upstream_model || '—'} mono />
          </div>

          <DetailRow label="Checked" value={entry.data.checked_at ? formatInTz(entry.data.checked_at, tz) : '—'} mono />

          {entry.data.error_message && (
            <div style={{ marginTop: 12 }}>
              <div className="filter-label">Error message</div>
              <pre className="mono" style={{ whiteSpace: 'pre-wrap', wordBreak: 'break-word', background: 'var(--bg-elevated)', padding: 8, borderRadius: 6, fontSize: 12 }}>
                {entry.data.error_message}
              </pre>
            </div>
          )}

          {entry.data.prompt_message != null && entry.data.prompt_message !== '' && (
            <CollapsibleTextBlock
              label="Prompt message"
              text={entry.data.prompt_message}
              open={showPrompt}
              onToggle={() => setShowPrompt((v) => !v)}
            />
          )}
          {entry.data.completion != null && entry.data.completion !== '' && (
            <CollapsibleTextBlock
              label="Completion"
              text={entry.data.completion}
              open={showCompletion}
              onToggle={() => setShowCompletion((v) => !v)}
            />
          )}
        </div>
      )}
    </Modal>
  );
}

// CollapsibleTextBlock renders a labelled, collapsible block of recorded text
// (prompt_message / completion). Defaults to collapsed since the content can
// be long; the toggle keeps the detail modal scannable until the operator
// wants the verbatim text.
function CollapsibleTextBlock({ label, text, open, onToggle }) {
  return (
    <div style={{ marginTop: 12 }}>
      <button
        onClick={onToggle}
        style={{ padding: '2px 8px', fontSize: 12, marginBottom: 4 }}
        aria-expanded={open}
      >
        {open ? '▾' : '▸'} {label}
      </button>
      {open && (
        <pre className="mono" style={{ whiteSpace: 'pre-wrap', wordBreak: 'break-word', background: 'var(--bg-elevated)', padding: 8, borderRadius: 6, fontSize: 12, marginTop: 0 }}>
          {text}
        </pre>
      )}
    </div>
  );
}

function DetailRow({ label, value, mono }) {
  return (
    <div className="row row--between" style={{ padding: '6px 0', borderBottom: '1px solid var(--border)' }}>
      <span className="dim" style={{ fontSize: 12 }}>{label}</span>
      <span className={mono ? 'mono' : ''}>{value ?? '—'}</span>
    </div>
  );
}

// formatInTz renders an ISO/RFC3339 timestamp in the operator's chosen
// timezone, falling back to the raw value when parsing fails.
function formatInTz(value, tz) {
  if (!value) return '—';
  try {
    const d = new Date(value);
    if (Number.isNaN(d.getTime())) return String(value);
    return d.toLocaleString(undefined, { timeZone: tz || undefined, hour12: false });
  } catch {
    return String(value);
  }
}
