// EventsLive — Live events tab for the dashboard logs page.
//
// Round-2 (docs/plans/2026-09-17-omniroute-round-2-design.md, Workstream 3).
// Subscribes to GET /v0/management/events/stream (SSE) for live event frames;
// when the EventSource errors (network/server down), the tab falls back to
// polling GET /v0/management/events every 10s.
//
// The buffer is capped at 500 newest entries so a long-lived dashboard tab
// never grows unbounded; filters `type` + `auth` mirror the
// ?type=&auth= query params on both endpoints.
//
// The pure helpers (URL build, SSE frame parse, buffer cap, status) are
// exported for node:test coverage in EventsLive.test.jsx; the React render
// path is exercised manually + by `npm run build`.

import React, { useCallback, useEffect, useMemo, useRef, useState } from 'react';
import { useAutoRefresh } from '../hooks/useAutoRefresh.js';
import { Spinner, EmptyState } from '../components/Primitives.jsx';

// Maximum in-memory events kept in the dashboard tab. Caps memory growth on
// long-lived tabs; round-2 ring on the server is 5000 so we never see more
// than the server is willing to remember.
const MAX_EVENTS = 500;

// Polling fallback interval when the SSE EventSource fails.
const POLL_INTERVAL_MS = 10 * 1000;

// Default polling page size when SSE is down.
const POLL_LIMIT = 100;

// --- Pure helpers (exported for tests) -----------------------------------

// buildEventsStreamURL composes the SSE endpoint URL with optional filter
// query params. Empty filters are omitted so the URL stays canonical when
// both are blank.
export function buildEventsStreamURL(filter) {
  const qs = eventsFilterQuery(filter || {});
  const params = new URLSearchParams();
  if (qs.type) params.set('type', qs.type);
  if (qs.auth) params.set('auth', qs.auth);
  const suffix = params.toString();
  return suffix ? `/v0/management/events/stream?${suffix}` : '/v0/management/events/stream';
}

// buildEventsPollingParams composes URLSearchParams for the polling
// fallback. limit is clamped to >=1 (the server treats 0 as "use default"
// so we don't rely on that).
export function buildEventsPollingParams(filter, limit) {
  const safeLimit = Number.isFinite(limit) && limit > 0 ? Math.floor(limit) : 1;
  const qs = eventsFilterQuery(filter || {});
  const params = new URLSearchParams();
  if (qs.type) params.set('type', qs.type);
  if (qs.auth) params.set('auth', qs.auth);
  params.set('limit', String(safeLimit));
  return params;
}

// parseSSEFrame parses a single SSE frame's raw text into a
// { type, data } object, or null when the frame is a keep-alive comment /
// blank / unparseable. The format follows the WHATWG spec:
//
//   field: value\n
//   field: value\n
//   \n
//
// Frames are terminated by a blank line. `data:` lines are joined by
// '\n'. `event:` sets the event name (default: "message"). Lines starting
// with ':' are comments.
export function parseSSEFrame(raw) {
  if (typeof raw !== 'string' || raw.length === 0) return null;
  const lines = raw.split('\n');
  let eventName = 'message';
  const dataLines = [];
  for (const line of lines) {
    if (line.length === 0) continue;
    if (line.startsWith(':')) continue;
    const idx = line.indexOf(':');
    if (idx < 0) continue;
    const field = line.slice(0, idx);
    // WHATWG: a single leading space after the colon is stripped.
    let value = line.slice(idx + 1);
    if (value.startsWith(' ')) value = value.slice(1);
    if (field === 'event') {
      eventName = value;
    } else if (field === 'data') {
      dataLines.push(value);
    }
    // Other fields (id, retry) are ignored — the dashboard does not
    // surface them.
  }
  if (dataLines.length === 0) return null;
  const payload = dataLines.join('\n');
  let parsed;
  try {
    parsed = JSON.parse(payload);
  } catch {
    return null;
  }
  return { type: eventName, data: parsed };
}

// appendEventCapped prepends a new event to a buffer and trims the buffer
// to the given cap (newest-first ordering). Returns a fresh array — never
// mutates the input.
export function appendEventCapped(buffer, next, cap) {
  const safeCap = Number.isFinite(cap) && cap > 0 ? Math.floor(cap) : 1;
  const out = [next, ...(Array.isArray(buffer) ? buffer : [])];
  if (out.length > safeCap) out.length = safeCap;
  return out;
}

// eventsFilterQuery normalizes a UI filter object (string-typed fields,
// possibly blank/undefined) into the canonical { type, auth } shape both
// buildEventsStreamURL and buildEventsPollingParams consume.
export function eventsFilterQuery(filter) {
  const safe = filter || {};
  return {
    type: (safe.type || '').trim(),
    auth: (safe.auth || '').trim(),
  };
}

// eventsStreamStatus derives the user-visible status badge from the SSE
// + polling state. Pure so the test asserts the full mapping table.
export function eventsStreamStatus({ sseError, pollError }) {
  if (pollError) return 'error';
  if (sseError) return 'polling';
  return 'live';
}

// --- Component ------------------------------------------------------------

function formatTime(iso) {
  if (!iso) return '—';
  const d = new Date(iso);
  if (Number.isNaN(d.getTime())) return '—';
  return d.toLocaleTimeString();
}

function summarizePayload(payload) {
  if (!payload || typeof payload !== 'object') return '';
  // Compact, single-line rendering of the most useful payload keys.
  const interesting = ['waited_ms', 'failures', 'model', 'auth', 'strategy', 'breaker', 'attempts'];
  const parts = [];
  for (const k of interesting) {
    if (payload[k] != null) parts.push(`${k}=${payload[k]}`);
  }
  if (parts.length > 0) return parts.join(' ');
  try {
    return JSON.stringify(payload);
  } catch {
    return '';
  }
}

export default function EventsLive() {
  const [filter, setFilter] = useState({ type: '', auth: '' });
  const [events, setEvents] = useState([]);
  const [sseError, setSseError] = useState(false);
  const [pollError, setPollError] = useState(null);
  const [pollData, setPollData] = useState(null);

  // SSE connection — re-establishes when filters change so a filter edit
  // doesn't leak frames from the previous query.
  useEffect(() => {
    if (typeof window === 'undefined' || typeof window.EventSource === 'undefined') {
      // No EventSource support (older test environments): drop straight
      // to polling.
      setSseError(true);
      return undefined;
    }
    setSseError(false);
    const url = buildEventsStreamURL(filter);
    const es = new EventSource(url, { withCredentials: true });

    const onMessage = (msg) => {
      const frame = parseSSEFrame(msg?.data || '');
      if (!frame) return;
      setEvents((prev) => appendEventCapped(prev, frame.data, MAX_EVENTS));
    };
    const onError = () => {
      es.close();
      setSseError(true);
    };
    es.addEventListener('message', onMessage);
    // Subscribe to a few common event types so their typed frames also
    // land in onMessage. (EventSource fires typed events on the matching
    // listener; "message" is the default for un-typed frames.)
    es.addEventListener('routing.decision', onMessage);
    es.addEventListener('routing.cooldown_wait', onMessage);
    es.addEventListener('routing.attempts_exhausted', onMessage);
    es.addEventListener('breaker.tripped', onMessage);
    es.addEventListener('breaker.probe_ok', onMessage);
    es.addEventListener('breaker.probe_fail', onMessage);
    es.addEventListener('cooldown.reclassified', onMessage);
    es.addEventListener('quota.threshold_cross', onMessage);
    es.addEventListener('error', onError);

    return () => {
      es.removeEventListener('message', onMessage);
      es.removeEventListener('routing.decision', onMessage);
      es.removeEventListener('routing.cooldown_wait', onMessage);
      es.removeEventListener('routing.attempts_exhausted', onMessage);
      es.removeEventListener('breaker.tripped', onMessage);
      es.removeEventListener('breaker.probe_ok', onMessage);
      es.removeEventListener('breaker.probe_fail', onMessage);
      es.removeEventListener('cooldown.reclassified', onMessage);
      es.removeEventListener('quota.threshold_cross', onMessage);
      es.removeEventListener('error', onError);
      es.close();
    };
  }, [filter.type, filter.auth]);

  // Polling fallback — only runs while SSE is in error state. Disabled
  // completely (intervalMs=null contract in useAutoRefresh) once SSE is
  // healthy.
  const pollFetcher = useCallback(async () => {
    const qs = buildEventsPollingParams(filter, POLL_LIMIT);
    const path = `/v0/management/events?${qs.toString()}`;
    const token = (() => {
      try { return localStorage.getItem('nixllm.dashboard.token') || ''; } catch { return ''; }
    })();
    const res = await fetch(`/v0/management${path}`, {
      headers: token ? { Authorization: `Bearer ${token}` } : {},
    });
    if (!res.ok) {
      const text = await res.text().catch(() => '');
      throw new Error(`events poll failed (${res.status}): ${text || res.statusText}`);
    }
    return res.json();
  }, [filter.type, filter.auth]);

  // useAutoRefresh accepts a falsy intervalMs as "disabled" — when SSE is
  // healthy we disable polling entirely.
  useAutoRefresh(async () => {
    try {
      const data = await pollFetcher();
      setPollData(data);
      setPollError(null);
      if (Array.isArray(data?.events)) {
        // Replace the buffer with the polled snapshot (newest-first).
        // SSE re-engagement on filter change already clears the buffer
        // implicitly; polling is a snapshot, not a stream.
        setEvents(data.events.slice(0, MAX_EVENTS));
      }
    } catch (err) {
      setPollError(err);
    }
  }, sseError ? POLL_INTERVAL_MS : null);

  const status = eventsStreamStatus({ sseError, pollError });

  const visible = useMemo(() => (Array.isArray(events) ? events : []), [events]);

  const statusLabel = status === 'live'
    ? 'Live'
    : status === 'polling'
      ? 'Polling (SSE unavailable)'
      : 'Error';
  const statusCls = status === 'live'
    ? 'autorefresh-chip'
    : status === 'polling'
      ? 'autorefresh-chip autorefresh-chip--off'
      : 'autorefresh-chip autorefresh-chip--off';

  return (
    <div className="events-live">
      <div className="card events-live__filters" style={{ marginTop: 16 }}>
        <div className="row gap-sm" style={{ flexWrap: 'wrap', alignItems: 'center' }}>
          <label className="filter-label">
            Type
            <input
              type="search"
              value={filter.type}
              placeholder="e.g. routing.decision"
              onChange={(e) => setFilter((f) => ({ ...f, type: e.target.value }))}
              style={{ minWidth: 200 }}
            />
          </label>
          <label className="filter-label">
            Auth
            <input
              type="search"
              value={filter.auth}
              placeholder="e.g. openai#0"
              onChange={(e) => setFilter((f) => ({ ...f, auth: e.target.value }))}
              style={{ minWidth: 200 }}
            />
          </label>
          <span className={statusCls} title={statusLabel}>
            <span className="autorefresh-chip__dot" />
            {statusLabel}
          </span>
          <span className="mono dim" style={{ fontSize: 12 }}>
            {visible.length.toLocaleString()} event{visible.length === 1 ? '' : 's'}{visible.length >= MAX_EVENTS ? ` (capped at ${MAX_EVENTS.toLocaleString()})` : ''}
          </span>
          {pollError && (
            <span className="logs-status-error" style={{ fontSize: 12 }}>
              {pollError?.message || 'polling failed'}
            </span>
          )}
        </div>
      </div>

      <div className="card events-live__panel" style={{ marginTop: 16 }}>
        {visible.length === 0 ? (
          <div className="logs-panel__empty">
            {status === 'live'
              ? <Spinner label="Waiting for events…" />
              : status === 'polling'
                ? <EmptyState title="No events yet" hint="Polling every 10s. New events will appear here." />
                : <EmptyState title="Events unavailable" hint={pollError?.message || 'Could not load events.'} />}
          </div>
        ) : (
          <div className="events-live__list">
            {visible.map((e, i) => (
              <div key={`${e?.ts || ''}-${i}`} className="events-live__item">
                <span className="events-live__ts mono">{formatTime(e?.ts)}</span>
                <span className="events-live__type mono">{e?.type || 'unknown'}</span>
                <span className="events-live__auth mono">{e?.auth_id || e?.channel || ''}</span>
                <span className="events-live__model mono">{e?.model || ''}</span>
                <span className="events-live__payload mono dim">
                  {summarizePayload(e?.payload)}
                </span>
              </div>
            ))}
          </div>
        )}
      </div>

      <div className="logs-status-bar">
        <span>
          Live event feed from <code>/v0/management/events/stream</code>
          {sseError ? ' — SSE unavailable, polling /v0/management/events every 10s' : ''}
        </span>
        <span>
          Buffer capped at {MAX_EVENTS.toLocaleString()} (newest first)
        </span>
      </div>
    </div>
  );
}
