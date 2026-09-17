// Quota — live per-window quota panel for upstream provider pools.
//
// Round-2 of the OmniRoute study (docs/plans/2026-09-17-...). Reads from
// the new /v0/management/pools/:key/quota endpoint (Task 7) which is a
// pure read aggregation over usage_windows. Pool membership is derived
// from the upstream_providers list (round-1 PoolBreaker /
// PoolStrategyForProviderKeys use the same (channel:rowID) compound key).
//
// Layout:
//   - Top section: one card per pool, each rendering a headroom bar per
//     window size (1m / 1h / 1d).
//   - Drill-down: clicking a pool expands to show per-auth brief rows
//     (auth_id / channel / pool_strategy) + the per-model 1h rollup.
//
// Auto-refreshes every 10s using the existing useAutoRefresh hook (same
// pattern as AlertsPage). The helpers exported below are pure and
// unit-tested by Quota.test.jsx; the React render path is exercised
// manually and by `npm run build`.

import React, { useCallback, useEffect, useMemo, useState } from 'react';
import { listUpstreamProviders, getPoolQuota } from '../api/client.js';
import { useAsync } from '../hooks/useAsync.js';
import { useAutoRefresh } from '../hooks/useAutoRefresh.js';
import { Spinner, ErrorBanner, EmptyState } from '../components/Primitives.jsx';

// --- Pure helpers (exported for tests) ------------------------------------

const WARN_THRESHOLD_PCT = 20; // below this, color the bar warning-orange
const OVER_THRESHOLD_PCT = 5;  // below this + over_limit, color the bar red

// formatHeadroomPct renders a numeric percent with one decimal. null /
// undefined / NaN render as an em-dash so a missing field never surfaces
// as "NaN%" in the UI.
export function formatHeadroomPct(pct) {
  if (pct == null || Number.isNaN(pct)) return '—';
  return `${Number(pct).toFixed(1)}%`;
}

// pickOverLimit is the page-level "anything on fire?" flag used to color
// the pool card border. Tolerates missing arrays so the caller can hand
// it `data?.windows` directly.
export function pickOverLimit(windows) {
  if (!Array.isArray(windows)) return false;
  return windows.some((w) => w && w.over_limit);
}

// poolKeyForProvider builds the (channel:rowID) compound key the quota
// endpoint expects, mirroring how PoolBreaker / PoolStrategyForProviderKeys
// canonicalize pool identity. Returns null when the row is missing the
// fields so callers can filter with a single compact call.
export function poolKeyForProvider(provider) {
  if (!provider) return null;
  const id = provider.id;
  const type = provider.provider_type;
  if (!id || !type) return null;
  return `${type}:${id}`;
}

// extractPoolKeys maps the upstream_providers list to one pool key per
// non-disabled row. Stable order: input order. Always returns a fresh
// array (never the input) so callers can use it as a React dep safely.
export function extractPoolKeys(providers) {
  if (!Array.isArray(providers)) return [];
  const out = [];
  const seen = new Set();
  for (const p of providers) {
    if (!p || p.disabled) continue;
    const key = poolKeyForProvider(p);
    if (!key || seen.has(key)) continue;
    seen.add(key);
    out.push(key);
  }
  return out;
}

// severityForWindow classifies one window into a UI tier ('ok' | 'warn'
// | 'over'). The thresholds intentionally match the design doc's
// over_limit semantics so a window crossing the cap flips immediately.
export function severityForWindow(w) {
  if (!w) return 'ok';
  if (w.over_limit) return 'over';
  const pct = Number(w.headroom_pct);
  if (!Number.isFinite(pct)) return 'ok';
  if (pct < OVER_THRESHOLD_PCT) return 'over';
  if (pct < WARN_THRESHOLD_PCT) return 'warn';
  return 'ok';
}

// mergePoolQuotas joins the per-pool fetch results with the requested
// pool keys, preserving the input keys' order. A pool whose fetch
// failed (not present in the Map) is surfaced with error=true so the
// card can render a small "fetch failed" hint without losing the slot.
export function mergePoolQuotas(keys, responses) {
  if (!Array.isArray(keys)) return [];
  return keys.map((poolKey) => {
    const resp = responses && responses.get ? responses.get(poolKey) : null;
    if (!resp) {
      return {
        poolKey,
        windows: [],
        models: [],
        auths: [],
        partial: false,
        error: true,
      };
    }
    return {
      poolKey,
      windows: Array.isArray(resp.windows) ? resp.windows : [],
      models: Array.isArray(resp.models) ? resp.models : [],
      auths: Array.isArray(resp.auths) ? resp.auths : [],
      partial: !!resp.partial,
      error: false,
    };
  });
}

// --- Component ------------------------------------------------------------

const AUTO_REFRESH_INTERVAL_MS = 10 * 1000;

export default function QuotaPage() {
  // Pool discovery: fetch upstream_providers once, derive pool keys from
  // non-disabled rows. The keys list is what drives the per-pool fetch
  // fan-out below.
  const providers = useAsync(() => listUpstreamProviders(), []);
  const poolKeys = useMemo(() => extractPoolKeys(providers.data?.providers), [providers.data]);

  // Per-pool fetch state. We keep a Map<poolKey, QuotaSnapshot> so the
  // refresh cycle can replace just one entry instead of stalling the
  // whole panel when a single pool's PG query times out.
  const [responses, setResponses] = useState(() => new Map());
  const [fetching, setFetching] = useState(false);

  const reload = useCallback(async () => {
    if (poolKeys.length === 0) {
      setResponses(new Map());
      return;
    }
    setFetching(true);
    try {
      const settled = await Promise.allSettled(poolKeys.map((k) => getPoolQuota(k)));
      const next = new Map();
      poolKeys.forEach((k, i) => {
        const r = settled[i];
        if (r && r.status === 'fulfilled' && r.value) {
          next.set(k, r.value);
        }
      });
      setResponses(next);
    } finally {
      setFetching(false);
    }
  }, [poolKeys]);

  // Initial fetch + dependency-driven reload when the pool membership
  // changes (e.g. a new upstream_providers row was added).
  useEffect(() => { reload(); }, [reload]);

  useAutoRefresh(reload, AUTO_REFRESH_INTERVAL_MS, !providers.loading && poolKeys.length > 0);

  const pools = useMemo(() => mergePoolQuotas(poolKeys, responses), [poolKeys, responses]);

  return (
    <>
      <div className="main__header">
        <div>
          <h1 className="main__title">Quota</h1>
          <div className="main__subtitle">
            Live per-window headroom across upstream provider pools. Aggregated
            from <code>usage_windows</code> with a 2s query timeout; a slow
            PG row surfaces as <code>partial: true</code> rather than blocking
            the panel. Auto-refreshes every 10s.
          </div>
        </div>
        <div className="row gap-sm">
          <button onClick={reload} disabled={fetching}>
            {fetching ? 'Refreshing…' : 'Refresh'}
          </button>
        </div>
      </div>

      {providers.error && <ErrorBanner error={providers.error} onRetry={providers.reload} />}

      {providers.loading ? (
        <Spinner label="Loading pools…" />
      ) : poolKeys.length === 0 ? (
        <EmptyState
          title="No upstream providers configured"
          hint="Add an upstream provider row to start seeing live quota headroom bars."
        />
      ) : (
        <div className="quota-grid">
          {pools.map((p) => (
            <PoolCard key={p.poolKey} pool={p} />
          ))}
        </div>
      )}
    </>
  );
}

// PoolCard renders one pool's quota snapshot. Click-to-expand shows the
// per-auth brief + per-model 1h rollup underneath the headroom bars.
function PoolCard({ pool }) {
  const [expanded, setExpanded] = useState(false);
  const overAll = pickOverLimit(pool.windows);
  return (
    <div className={`card quota-card${overAll ? ' quota-card--over' : ''}`}>
      <div
        className="quota-card__head row row--between"
        onClick={() => setExpanded((v) => !v)}
        role="button"
        tabIndex={0}
        onKeyDown={(e) => {
          if (e.key === 'Enter' || e.key === ' ') {
            e.preventDefault();
            setExpanded((v) => !v);
          }
        }}
        aria-expanded={expanded}
        style={{ cursor: 'pointer' }}
      >
        <div>
          <div className="card__title" style={{ margin: 0 }}>{pool.poolKey}</div>
          <div className="dim" style={{ fontSize: 12, marginTop: 2 }}>
            {pool.auths.length} auth{pool.auths.length === 1 ? '' : 's'}
            {pool.partial ? ' · partial' : ''}
            {pool.error ? ' · fetch failed' : ''}
          </div>
        </div>
        <span className="mono" style={{ fontSize: 12, color: 'var(--text-dim)' }}>
          {expanded ? '−' : '+'}
        </span>
      </div>

      {/* Headroom bars — one per window size */}
      <div style={{ marginTop: 12 }}>
        {pool.windows.length === 0 && !pool.error && (
          <div className="dim" style={{ fontSize: 12 }}>No window data yet.</div>
        )}
        {pool.error && (
          <div className="dim" style={{ fontSize: 12 }}>
            Could not load quota for this pool. Refresh to retry.
          </div>
        )}
        {pool.windows.map((w) => (
          <WindowBar key={w.size} window={w} />
        ))}
      </div>

      {expanded && (
        <div style={{ marginTop: 12 }}>
          <PoolDrillDown pool={pool} />
        </div>
      )}
    </div>
  );
}

// WindowBar paints one progress bar for a window's headroom percent.
function WindowBar({ window: w }) {
  const pct = Math.max(0, Math.min(100, Number(w.headroom_pct) || 0));
  const severity = severityForWindow(w);
  const className = `quota-bar quota-bar--${severity}`;
  return (
    <div style={{ marginBottom: 8 }}>
      <div className="row row--between" style={{ marginBottom: 4 }}>
        <span className="mono" style={{ fontSize: 12 }}>{w.size}</span>
        <span className="mono" style={{ fontSize: 12 }}>
          {formatHeadroomPct(w.headroom_pct)}
          {w.over_limit ? ' · over limit' : ''}
        </span>
      </div>
      <div className={className} role="progressbar" aria-valuenow={pct} aria-valuemin={0} aria-valuemax={100}>
        <div className="quota-bar__fill" style={{ width: `${pct}%` }} />
      </div>
    </div>
  );
}

// PoolDrillDown renders the per-auth brief list + per-model 1h rollup
// for an expanded pool card. Auths come from the quota response itself
// (no extra round-trip); models likewise.
function PoolDrillDown({ pool }) {
  if (pool.auths.length === 0 && pool.models.length === 0) {
    return <div className="dim" style={{ fontSize: 12 }}>No drill-down data.</div>;
  }
  return (
    <>
      {pool.auths.length > 0 && (
        <div style={{ marginBottom: 12 }}>
          <div className="quota-card__subtitle">Auths</div>
          <ul style={{ margin: '4px 0 0', paddingLeft: 18, fontSize: 12 }}>
            {pool.auths.map((a) => (
              <li key={a.auth_id} className="mono">
                {a.auth_id}
                {a.pool_strategy ? ` · ${a.pool_strategy}` : ''}
                {a.channel ? ` · ${a.channel}` : ''}
              </li>
            ))}
          </ul>
        </div>
      )}
      {pool.models.length > 0 && (
        <div>
          <div className="quota-card__subtitle">Models (1h)</div>
          <table className="table" style={{ marginTop: 4 }}>
            <thead>
              <tr>
                <th>Model</th>
                <th style={{ textAlign: 'right' }}>Used</th>
                <th style={{ textAlign: 'right' }}>Limit</th>
              </tr>
            </thead>
            <tbody>
              {pool.models.map((m) => (
                <tr key={m.model}>
                  <td className="mono">{m.model}</td>
                  <td className="mono" style={{ textAlign: 'right' }}>{Number(m.used_1h || 0).toLocaleString()}</td>
                  <td className="mono" style={{ textAlign: 'right' }}>{Number(m.limit_1h || 0).toLocaleString()}</td>
                </tr>
              ))}
            </tbody>
          </table>
        </div>
      )}
    </>
  );
}
