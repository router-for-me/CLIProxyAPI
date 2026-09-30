// ============================================================================
// Upstream provider editor — Logs tab
// ============================================================================
//
// LogsTab is the only NEW tab in PR 2 (the other five tabs are verbatim lifts
// from previous editor surfaces). Per the design doc it embeds filtered
// views of the Analysis → Upstream Providers / Recent Events / Model Health
// pages inside the Logs tab so an operator can investigate a single provider
// without bouncing between three top-level routes.
//
// Each sub-section fetches its own data on mount and renders inline; each has
// its own `↻ Refresh` button + loading/error state. The three call sites are:
//
//   1. Sync events   — GET /v0/management/upstream-sync-log?provider=:id
//                      (supported filter, narrow server-side).
//   2. Recent events — GET /v0/management/usage-stats/events?provider=:id
//                      (supported filter, narrow server-side).
//   3. Model health  — GET /v0/management/model-health/log  (model filter
//                      only, NOT provider; we fetch a recent page and
//                      client-side filter the rows by `provider === :id`).
//
// `providerId` comes from the route param (the editor is mounted at
// /upstream-providers/:id/:tab) so the section-level fetches re-fire when
// the operator navigates between providers.
//
// Each section is collapsed by default after the operator opens it once
// (state held per-section in component-local useState). The plan calls for
// a `Collapsible` + `EventsTable` helper pair; both live in this file since
// they're only consumed by this tab.

import React, { useState, useMemo } from 'react';
import { useParams } from 'react-router-dom';
import {
  getUpstreamSyncLog,
  getUsageEvents,
  getModelHealthLog,
} from '../../api/client.js';
import { useAsync } from '../../hooks/useAsync.js';
import { ErrorBanner } from '../../components/Primitives.jsx';
import { useEditorState } from './useEditorState.jsx';

// Maximum rows embedded per sub-section. The plan asks for `limit: 50`;
// getUpstreamSyncLog + getModelHealthLog take `page_size`, getUsageEvents
// takes `page_size` too. The dashboard's own pages default to 10/25/50 so
// 50 keeps the embedded tables dense enough to be useful without flooding
// the tab body.
const LOGS_LIMIT = 50;

// --- Shared helpers --------------------------------------------------------

// Collapsible renders a single sub-section card with a header that toggles
// the body open/closed and a small `↻ Refresh` action. The body slot is
// always rendered so loading/error/empty/data states can share the same
// card layout; only the body visibility is toggled.
//
// Props:
//   - title:        the section heading shown in the header bar
//   - hint:         one-line description (rendered dim, right of title)
//   - expanded:     controlled open state
//   - onToggle:     () => void — toggles open
//   - onRefresh:    () => void — triggers a refetch
//   - loading:      boolean — drives the spinner state inside the body
//   - error:        Error | null — drives the error banner inside the body
//   - children:     body content (passed through, only shown when expanded)
//   - rowCount:     optional small badge next to the refresh button
function Collapsible({
  title,
  hint,
  expanded,
  onToggle,
  onRefresh,
  loading,
  error,
  rowCount,
  children,
}) {
  function handleRefresh(e) {
    // Stop the click from also toggling the section (the refresh button sits
    // inside the toggleable header row).
    e.stopPropagation();
    if (onRefresh) onRefresh();
  }

  return (
    <div className="card" style={{ marginTop: 16 }}>
      <div
        className="row row--between"
        style={{ cursor: 'pointer', userSelect: 'none' }}
        onClick={onToggle}
        role="button"
        aria-expanded={expanded}
        aria-controls={`logs-section-${title.replace(/\s+/g, '-')}`}
      >
        <div style={{ display: 'flex', alignItems: 'center', gap: 8 }}>
          <span aria-hidden="true" className="mono dim" style={{ width: 12, display: 'inline-block' }}>
            {expanded ? '▾' : '▸'}
          </span>
          <h3 className="card__title" style={{ margin: 0 }}>{title}</h3>
          {hint && <span className="dim" style={{ fontSize: 12 }}>{hint}</span>}
        </div>
        <div className="row gap-sm" style={{ alignItems: 'center' }}>
          {typeof rowCount === 'number' && (
            <span className="mono dim" style={{ fontSize: 12 }}>
              {rowCount.toLocaleString()} row{rowCount === 1 ? '' : 's'}
            </span>
          )}
          <button
            type="button"
            onClick={handleRefresh}
            disabled={loading}
            title="Refresh this section"
            aria-label={`Refresh ${title}`}
          >
            {loading ? '↻ …' : '↻ Refresh'}
          </button>
        </div>
      </div>
      {expanded && (
        <div id={`logs-section-${title.replace(/\s+/g, '-')}`} style={{ marginTop: 12 }}>
          {children}
        </div>
      )}
    </div>
  );
}

// EventsTable renders a rows array under a fixed column set, with a small
// skeleton row when loading (caller wraps it with ErrorBanner when needed),
// an "empty" message when the rows array is empty, and the rows themselves
// otherwise. Each row's `cells` array matches `columnKeys.length` order.
//
// Props:
//   - rows:        Array of arbitrary row objects (any keys).
//   - columnKeys:  Array of { key, label, mono? } describing the rendered
//                  columns. Each row's cell text is taken from `row[key]`.
//   - emptyHint:   String shown when rows is empty (after loading finishes).
//   - loading:     Boolean — adds skeleton rows (caller still owns errors).
function EventsTable({ rows, columnKeys, emptyHint, loading }) {
  const safeRows = Array.isArray(rows) ? rows : [];
  return (
    <div style={{ overflowX: 'auto' }}>
      <table className="table">
        <thead>
          <tr>
            {columnKeys.map((c) => (
              <th key={c.key}>{c.label}</th>
            ))}
          </tr>
        </thead>
        <tbody>
          {loading && safeRows.length === 0 && Array.from({ length: 3 }).map((_, i) => (
            <tr key={`skel-${i}`} className="skeleton-row">
              {columnKeys.map((__, j) => (
                <td key={j}><span className="skeleton-line" /></td>
              ))}
            </tr>
          ))}
          {!loading && safeRows.length === 0 && (
            <tr>
              <td colSpan={columnKeys.length} style={{ textAlign: 'center', padding: '20px 8px' }}>
                {emptyHint || 'No rows.'}
              </td>
            </tr>
          )}
          {!loading && safeRows.map((row, i) => (
            <tr key={row.id ?? i}>
              {columnKeys.map((c, j) => (
                <td key={c.key} className={c.mono ? 'mono' : undefined} style={j === 0 ? { whiteSpace: 'nowrap' } : undefined}>
                  {row[c.key] == null || row[c.key] === '' ? <span className="dim">—</span> : String(row[c.key])}
                </td>
              ))}
            </tr>
          ))}
        </tbody>
      </table>
    </div>
  );
}

// formatTimestamp renders an ISO string in the operator's local time using
// a stable, compact format. Mirrors the compact-mode renders used by the
// dashboard's other tables — no timezone picker on this embedded view; the
// standalone pages already expose one.
function formatTimestamp(iso) {
  if (!iso) return '';
  const d = new Date(iso);
  if (Number.isNaN(d.getTime())) return String(iso);
  // YYYY-MM-DD HH:MM:SS in the operator's local timezone — short enough to
  // scan 50 rows in a Logs sub-section without wrapping.
  const pad = (n) => String(n).padStart(2, '0');
  return `${d.getFullYear()}-${pad(d.getMonth() + 1)}-${pad(d.getDate())} `
    + `${pad(d.getHours())}:${pad(d.getMinutes())}:${pad(d.getSeconds())}`;
}

// Pre-shape a row for EventsTable: the helpers below normalize each
// backend row into { id, ...columnKeys } so the table component stays
// generic. Returning a new object keeps the underlying fetch payload
// untouched for any future modal/drill-down wiring.

// rowToSyncEvent normalizes a /upstream-sync-log row for the "Sync events"
// sub-section. The page_size-then-callback pattern is preserved by
// SyncEventsSection.
function rowToSyncEvent(row) {
  const kind = row.success ? 'success' : 'failure';
  const trigger = row.trigger ? ` · ${row.trigger}` : '';
  const duration = row.duration_ms != null ? ` · ${row.duration_ms}ms` : '';
  const message = row.error_message || `auth=${row.auth_id || '—'}`;
  return {
    id: row.id ?? `${row.occurred_at || ''}-${row.auth_id || ''}-${Math.random()}`,
    timestamp: formatTimestamp(row.occurred_at),
    kind: `${kind}${trigger}${duration}`,
    message,
  };
}

// rowToUsageEvent normalizes a /usage-stats/events row for the "Recent
// events" sub-section. Compact view: timestamp · model alias · latency ·
// outcome. Client-side filter applied upstream in RecentEventsSection.
function rowToUsageEvent(row) {
  const outcome = row.failed ? 'failed' : (row.fail_status_code ? `fail ${row.fail_status_code}` : 'ok');
  return {
    id: row.id ?? row.request_id ?? `${row.requested_at || ''}-${Math.random()}`,
    timestamp: formatTimestamp(row.requested_at),
    model: row.alias || row.model || '—',
    latency: row.latency_ms != null ? `${row.latency_ms}ms` : '—',
    outcome,
  };
}

// rowToHealthEvent normalizes a /model-health/log row for the "Model health"
// sub-section. Compact view: timestamp · model id · status · response time.
// Client-side filter applied upstream in ModelHealthSection.
function rowToHealthEvent(row) {
  return {
    id: row.id ?? `${row.checked_at || ''}-${row.model_id || ''}-${Math.random()}`,
    timestamp: formatTimestamp(row.checked_at),
    model: row.model_id || '—',
    status: row.status || (row.success ? 'operational' : 'unavailable'),
    response_time: row.response_time_ms != null ? `${row.response_time_ms}ms` : '—',
  };
}

// --- Sub-sections ----------------------------------------------------------

// useProviderKeys resolves the two provider identifiers the backend uses so
// the embedded Logs sections can filter by the value that actually appears in
// each data source. The bare `executor_key` (e.g. "claude", "openai-compatible-opencode")
// is what executors stamp into usage_events.provider and the auth manager
// stamps into upstream_sync_log.provider; `provider_key` (e.g. "claude:42") is
// the compound routing/registry key model-health rows may carry. Falls back to
// the raw route id so the sections still narrow server-side when the provider
// row has not loaded yet.
function useProviderKeys(fallbackId) {
  const { initial } = useEditorState();
  return useMemo(() => {
    const executorKey = initial?.executor_key || '';
    const providerKey = initial?.provider_key || '';
    return {
      executorKey,
      providerKey,
      // Server-side filters: prefer the derived keys, else the raw id.
      eventProvider: executorKey || fallbackId,
      syncProvider: executorKey || fallbackId,
      // Model-health is matched client-side against any of these.
      healthKeys: [executorKey, providerKey].filter(Boolean),
    };
  }, [initial, fallbackId]);
}

function SyncEventsSection({ providerFilter }) {
  const [expanded, setExpanded] = useState(true);
  const listParams = useMemo(
    () => ({ provider: providerFilter, page: 1, page_size: LOGS_LIMIT }),
    [providerFilter],
  );
  const asyncReq = useAsync(() => getUpstreamSyncLog(listParams), [providerFilter]);
  const rows = useMemo(
    () => (asyncReq.data?.events || []).map(rowToSyncEvent),
    [asyncReq.data],
  );
  return (
    <Collapsible
      title="Sync events"
      hint="upstream OAuth/auth token refresh outcomes for this provider"
      expanded={expanded}
      onToggle={() => setExpanded((v) => !v)}
      onRefresh={asyncReq.reload}
      loading={asyncReq.loading}
      error={asyncReq.error}
      rowCount={rows.length}
    >
      {asyncReq.error && <ErrorBanner error={asyncReq.error} onRetry={asyncReq.reload} />}
      <EventsTable
        rows={rows}
        loading={asyncReq.loading}
        emptyHint="No sync events for this provider."
        columnKeys={[
          { key: 'timestamp', label: 'Time', mono: true },
          { key: 'kind', label: 'Kind', mono: true },
          { key: 'message', label: 'Message' },
        ]}
      />
    </Collapsible>
  );
}

function RecentEventsSection({ providerFilter }) {
  const [expanded, setExpanded] = useState(true);
  const listParams = useMemo(
    () => ({ provider: providerFilter, page: 1, page_size: LOGS_LIMIT }),
    [providerFilter],
  );
  const asyncReq = useAsync(() => getUsageEvents(listParams), [providerFilter]);
  const rows = useMemo(
    () => (asyncReq.data?.events || []).map(rowToUsageEvent),
    [asyncReq.data],
  );
  return (
    <Collapsible
      title="Recent events"
      hint="last requests routed through this provider"
      expanded={expanded}
      onToggle={() => setExpanded((v) => !v)}
      onRefresh={asyncReq.reload}
      loading={asyncReq.loading}
      error={asyncReq.error}
      rowCount={rows.length}
    >
      {asyncReq.error && <ErrorBanner error={asyncReq.error} onRetry={asyncReq.reload} />}
      <EventsTable
        rows={rows}
        loading={asyncReq.loading}
        emptyHint="No requests for this provider in the recent window."
        columnKeys={[
          { key: 'timestamp', label: 'Time', mono: true },
          { key: 'model', label: 'Model', mono: true },
          { key: 'latency', label: 'Latency', mono: true },
          { key: 'outcome', label: 'Outcome' },
        ]}
      />
    </Collapsible>
  );
}

function ModelHealthSection({ providerId, healthKeys }) {
  // /model-health/log is keyed on model_id, not provider. The plan instructs
  // to fetch a recent page and client-side filter rows by provider so this
  // section still surfaces the per-provider slice when the operator expects
  // it. Registry keys may be entry-scoped compound keys
  // ("<provider>:<id>:key-<entry>"), so a row matches when its `provider`
  // equals any derived key exactly or starts with "<executor_key>:".
  const [expanded, setExpanded] = useState(true);
  const listParams = useMemo(
    () => ({ page: 1, page_size: LOGS_LIMIT * 2 }),
    [],
  );
  const asyncReq = useAsync(() => getModelHealthLog(listParams), []);
  const rows = useMemo(() => {
    const all = Array.isArray(asyncReq.data?.events) ? asyncReq.data.events : [];
    const keys = healthKeys && healthKeys.length ? healthKeys : (providerId ? [providerId] : []);
    if (keys.length === 0) return [];
    const matches = (rowProvider) => {
      if (rowProvider == null || rowProvider === '') return false;
      const rp = String(rowProvider).toLowerCase();
      return keys.some((k) => {
        const key = String(k).toLowerCase();
        return rp === key || rp.startsWith(`${key}:`);
      });
    };
    return all
      .filter((row) => row && matches(row.provider))
      .slice(0, LOGS_LIMIT)
      .map(rowToHealthEvent);
  }, [asyncReq.data, providerId, healthKeys]);
  return (
    <Collapsible
      title="Model health"
      hint="recent probe outcomes for models on this provider"
      expanded={expanded}
      onToggle={() => setExpanded((v) => !v)}
      onRefresh={asyncReq.reload}
      loading={asyncReq.loading}
      error={asyncReq.error}
      rowCount={rows.length}
    >
      {asyncReq.error && <ErrorBanner error={asyncReq.error} onRetry={asyncReq.reload} />}
      <EventsTable
        rows={rows}
        loading={asyncReq.loading}
        emptyHint="No model-health probe records for this provider."
        columnKeys={[
          { key: 'timestamp', label: 'Checked', mono: true },
          { key: 'model', label: 'Model', mono: true },
          { key: 'status', label: 'Status' },
          { key: 'response_time', label: 'Response time', mono: true },
        ]}
      />
    </Collapsible>
  );
}

// --- Tab ------------------------------------------------------------------

export function LogsTab() {
  const { id } = useParams();
  const { executorKey, eventProvider, syncProvider, healthKeys } = useProviderKeys(id);
  return (
    <section>
      <div className="form-section">
        <div className="form-section__title">Logs</div>
        <div className="form-section__hint">
          Filtered views of the per-provider records captured by the auth
          manager, request router, and periodic health probes. Each section
          fetches and refreshes independently — see{' '}
          <a href="/upstream-sync-log">Upstream Providers</a>,{' '}
          <a href="/recent-events">Recent Events</a>, and{' '}
          <a href="/model-health">Model Health</a> for the full views.
        </div>
      </div>
      {/* id stays stable across renders; useParams already returns a string.
          The three sections re-fire their fetches when id changes (passed
          as the useAsync dep), so navigating to a different provider's
          editor refreshes the embedded tables. Filters use the provider's
          derived executor/channel key so rows actually match. */}
      <SyncEventsSection providerFilter={syncProvider} />
      <RecentEventsSection providerFilter={eventProvider} />
      <ModelHealthSection providerId={id} healthKeys={executorKey ? healthKeys : []} />
    </section>
  );
}

export default LogsTab;
