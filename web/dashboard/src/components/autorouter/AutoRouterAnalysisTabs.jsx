import React, { useState, useMemo } from 'react';
import { listAutoRouters, getAutoRouterStats, getAutoRouterDecisionStats, getAutoRouterTierPerformance, getAutoRouterJevStats, getUsageFilterOptions } from '../../api/client.js';
import { useAsync } from '../../hooks/useAsync.js';
import { Spinner, ErrorBanner, EmptyState } from '../Primitives.jsx';
import DecisionDistributionTab from './DecisionDistributionTab.jsx';
import JevTab from './JevTab.jsx';
import SimulationTab from './SimulationTab.jsx';
import ReplayTab from './ReplayTab.jsx';
import { buildStatsParams } from './statsParams.js';
import { loadTimezone } from '../../pages/usageShared.jsx';

// Time window options offered on this page (subset of the Usage Stats presets).
const RANGE_OPTIONS = [
  { label: 'Last 7d', days: 7 },
  { label: 'Last 30d', days: 30 },
];

const DEFAULT_LIMIT = 100;

// Canonical tier order — the backend always returns all 4 tiers, but keeping a
// fixed order here makes the cards render in the same sequence regardless of
// response ordering and lets us surface any tier the backend ever omits.
export const TIERS = [
  { key: 'simple', label: 'Simple' },
  { key: 'medium', label: 'Medium' },
  { key: 'complex', label: 'Complex' },
  { key: 'reasoning', label: 'Reasoning' },
];

// Range state: selected preset + optional custom from/to. from/to sent to the
// server are always RFC3339 UTC (toISOString); the datetime-local inputs stay
// wall-clock local time.
function useRange(initialDays = 7) {
  const [preset, setPreset] = useState(initialDays);
  const [customFrom, setCustomFrom] = useState('');
  const [customTo, setCustomTo] = useState('');
  const [useCustomRange, setUseCustomRange] = useState(false);

  const range = useMemo(() => {
    if (useCustomRange) {
      const from = customFrom ? new Date(customFrom).toISOString() : '';
      const to = customTo ? new Date(customTo).toISOString() : '';
      if (from || to) return { from, to };
    }
    const now = new Date();
    const from = new Date(now);
    from.setDate(from.getDate() - preset);
    return { from: from.toISOString(), to: now.toISOString() };
  }, [preset, customFrom, customTo, useCustomRange]);

  return {
    range,
    preset,
    setPreset,
    customFrom,
    setCustomFrom,
    customTo,
    setCustomTo,
    useCustomRange,
    setUseCustomRange,
  };
}

// TABS lists the analysis views in display order. The active tab is held by
// the page; every tab shares the controls row (router / key / range).
const TABS = [
  { key: 'overview', label: 'Overview' },
  { key: 'distribution', label: 'Decision distribution' },
  // Jev AI sits next to the decision distribution because both explain the
  // same routing decision from different angles: the distribution covers the
  // heuristic scorer, this covers the classifier layered on top of it.
  { key: 'jev', label: 'Jev AI' },
  { key: 'simulation', label: 'Simulation' },
  { key: 'replay', label: 'Replay' },
];

export default function AutoRouterAnalysisPage() {
  // selectedRouter keeps the full row ({id, model_id, name, display_name}):
  // stats endpoints key on model_id while per-router endpoints (profile,
  // simulate, decisions) key on the PK id — both are needed.
  const [selectedRouter, setSelectedRouter] = useState(null);
  const [apiKeyId, setApiKeyId] = useState('');
  const [tab, setTab] = useState('overview');
  // focusRequestId lets other tabs (simulation samples) open a replay detail.
  const [focusRequestId, setFocusRequestId] = useState('');
  const rng = useRange(7);
  // Display timezone for the custom-range labels (shared with Usage Stats via
  // localStorage), so operators see that the inputs are local wall-clock.
  const [timezone] = useState(() => loadTimezone());

  // Router dropdown options (all auto routers; the CRUD list endpoint returns
  // id + name + display_name + model_id per router).
  const routers = useAsync(() => listAutoRouters({ page: 1, pageSize: 500 }), []);

  // API-key dropdown options — same source as the Usage Stats page so the
  // operator sees the same key identifiers (id + alias) in both surfaces.
  const filterOptions = useAsync(() => getUsageFilterOptions({}), []);

  const params = useMemo(
    () => buildStatsParams(selectedRouter, apiKeyId, rng.range),
    [selectedRouter, apiKeyId, rng.range.from, rng.range.to],
  );

  // Fetch both overview views only once a router is selected. The backend
  // guarantees tier stats always carry the 4 canonical tiers (zero-valued when
  // empty); model stats are ordered by cost descending server-side.
  const statsKey = JSON.stringify(params);
  const tiers = useAsync(() => getAutoRouterStats({ ...params, top: 'tier' }), [statsKey]);
  const models = useAsync(
    () => getAutoRouterStats({ ...params, top: 'model', limit: DEFAULT_LIMIT }),
    [statsKey],
  );
  // Enrichment feeds: cause distribution and tier performance (Overview), also
  // reused as the mismatch/threshold context of other tabs.
  const decisionStats = useAsync(
    () => getAutoRouterDecisionStats(params),
    [statsKey, tab === 'overview' || tab === 'distribution' ? 'active' : 'idle'],
  );
  const performance = useAsync(
    () => getAutoRouterTierPerformance(params),
    [statsKey, tab === 'overview' ? 'active' : 'idle'],
  );
  // Classifier rollup + the router's configured knobs. Fetched for the Jev tab
  // and for Overview, which surfaces the classifier's share of decisions.
  const jevStats = useAsync(
    () => getAutoRouterJevStats(params),
    [statsKey, tab === 'jev' || tab === 'overview' || tab === 'distribution' ? 'active' : 'idle'],
  );

  const routerOptions = routers.data?.auto_routers || [];
  const keyOptions = filterOptions.data?.api_keys || [];
  const tierRows = tiers.data?.tiers || [];
  const modelRows = models.data?.models || [];

  const hasSelection = !!selectedRouter;
  const totalRequests = tierRows.reduce((a, t) => a + Number(t.request_count || 0), 0);
  const totalCost = tierRows.reduce((a, t) => a + Number(t.cost_usd || 0), 0);
  // The backend always returns all 4 tier rows (zero-valued when a tier has no
  // requests), so "no data" is detected from the actual totals rather than the
  // row count: no requests across any tier and no model rows = empty range.
  const noData = hasSelection && totalRequests === 0 && modelRows.length === 0;

  function handleSelectRouter(e) {
    const modelId = e.target.value;
    if (!modelId) {
      setSelectedRouter(null);
      return;
    }
    const row = routerOptions.find((r) => r.model_id === modelId) || null;
    setSelectedRouter(row);
  }

  return (
    <>
      <div className="main__header">
        <div>
          <h1 className="main__title">Auto Router Analysis</h1>
          <div className="main__subtitle">
            Volume, tokens, and cost per complexity tier; the decision
            distribution behind them; a dry-run simulator for scoring-profile
            changes; and per-request decision replay.
          </div>
        </div>
      </div>

      {/* Controls: router + API key + time range */}
      <div className="card">
        <div className="grid grid--3">
          <div className="form__row" style={{ marginBottom: 0 }}>
            <label className="form__label">Auto Router</label>
            <select
              value={selectedRouter?.model_id || ''}
              onChange={handleSelectRouter}
              disabled={routers.loading}
            >
              <option value="">Select a router…</option>
              {routerOptions.map((r) => (
                <option key={r.id} value={r.model_id}>
                  {r.display_name || r.name} ({r.model_id})
                </option>
              ))}
            </select>
          </div>
          <div className="form__row" style={{ marginBottom: 0 }}>
            <label className="form__label">API Key</label>
            <select
              value={apiKeyId}
              onChange={(e) => setApiKeyId(e.target.value)}
              disabled={filterOptions.loading}
            >
              <option value="">All keys</option>
              {keyOptions.map((k) => (
                <option key={k.id} value={k.id}>{k.alias || k.id}</option>
              ))}
            </select>
          </div>
          <div className="form__row" style={{ marginBottom: 0 }}>
            <label className="form__label">Time range</label>
            <div className="row gap-sm" style={{ alignItems: 'center' }}>
              <select
                value={rng.preset}
                onChange={(e) => { rng.setPreset(Number(e.target.value)); rng.setUseCustomRange(false); }}
                disabled={rng.useCustomRange}
              >
                {RANGE_OPTIONS.map((r) => (
                  <option key={r.days} value={r.days}>{r.label}</option>
                ))}
              </select>
              <label className="row gap-sm" style={{ cursor: 'pointer' }}>
                <input
                  type="checkbox"
                  checked={rng.useCustomRange}
                  onChange={(e) => rng.setUseCustomRange(e.target.checked)}
                  style={{ width: 'auto' }}
                />
                <span className="form__label" style={{ margin: 0 }}>Custom</span>
              </label>
            </div>
            {rng.useCustomRange && (
              <div className="usage-toolbar__range" style={{ marginTop: 6 }}>
                <div className="row gap-sm">
                  <span className="usage-toolbar__range-label">From ({timezone})</span>
                  <input
                    type="datetime-local"
                    value={rng.customFrom}
                    onChange={(e) => rng.setCustomFrom(e.target.value)}
                  />
                </div>
                <div className="row gap-sm">
                  <span className="usage-toolbar__range-label">To ({timezone})</span>
                  <input
                    type="datetime-local"
                    value={rng.customTo}
                    onChange={(e) => rng.setCustomTo(e.target.value)}
                  />
                </div>
              </div>
            )}
          </div>
        </div>
      </div>

      {/* Tab bar — visible once a router is selected. */}
      {hasSelection && (
        <div className="row gap-sm" style={{ marginTop: 16 }}>
          {TABS.map((t) => (
            <button
              key={t.key}
              className={tab === t.key ? 'primary' : ''}
              onClick={() => setTab(t.key)}
            >
              {t.label}
            </button>
          ))}
        </div>
      )}

      {/* No router selected yet */}
      {!hasSelection && (
        <EmptyState
          title="Select a router and API key"
          hint="Choose an auto router above to see its per-tier breakdown, decision distribution, profile simulator, and decision replay."
        />
      )}

      {hasSelection && tab === 'overview' && (
        <OverviewTab
          tiers={{ loading: tiers.loading, error: tiers.error, reload: tiers.reload, rows: tierRows }}
          models={{ loading: models.loading, error: models.error, reload: models.reload, rows: modelRows }}
          performance={{
            loading: performance.loading,
            error: performance.error,
            rows: performance.data?.performance || [],
          }}
          decisionStats={{
            loading: decisionStats.loading,
            error: decisionStats.error,
            data: decisionStats.data?.decision_stats || null,
          }}
          jevStats={{
            loading: jevStats.loading,
            error: jevStats.error,
            data: jevStats.data?.jev_stats || null,
          }}
          // The stats response's router block is authoritative; the list row is
          // the fallback so the card still names the configuration if the block
          // was omitted (an unresolvable router).
          routerConfig={jevStats.data?.router || routerOptions.find(
            (r) => r.model_id === selectedRouter?.model_id,
          ) || null}
          totalRequests={totalRequests}
          totalCost={totalCost}
          noData={noData}
        />
      )}

      {hasSelection && tab === 'jev' && (
        <JevTab
          jevStats={{
            loading: jevStats.loading,
            error: jevStats.error,
            reload: jevStats.reload,
            data: jevStats.data?.jev_stats || null,
          }}
          routedTotal={totalRequests}
          // The stats response's router block is authoritative and always
          // present when the router resolves; the list row is the fallback so
          // the tab still names the configuration if the block was omitted.
          routerConfig={jevStats.data?.router || selectedRouter}
        />
      )}

      {hasSelection && tab === 'distribution' && (
        <DecisionDistributionTab
          router={selectedRouter}
          decisionStats={{
            loading: decisionStats.loading,
            error: decisionStats.error,
            reload: decisionStats.reload,
            data: decisionStats.data?.decision_stats || null,
          }}
          onOpenReplay={(requestId) => {
            setFocusRequestId(requestId || '');
            setTab('replay');
          }}
        />
      )}

      {hasSelection && tab === 'simulation' && (
        <SimulationTab
          router={selectedRouter}
          range={rng.range}
          apiKeyId={apiKeyId}
          onOpenReplay={(requestId) => {
            setFocusRequestId(requestId || '');
            setTab('replay');
          }}
        />
      )}

      {hasSelection && tab === 'replay' && (
        <ReplayTab
          router={selectedRouter}
          apiKeyId={apiKeyId}
          focusRequestId={focusRequestId}
        />
      )}
    </>
  );
}

// OverviewTab is the original analysis page content, enriched with cause
// badges on the tier cards and latency/error columns on the model table.
function OverviewTab({ tiers, models, performance, decisionStats, jevStats, routerConfig, totalRequests, totalCost, noData }) {
  // Join performance rows onto the model table by model id.
  const perfByModel = useMemo(() => {
    const map = new Map();
    for (const p of performance.rows || []) {
      if (!map.has(p.model)) map.set(p.model, p);
    }
    return map;
  }, [performance.rows]);

  const causeCounts = decisionStats.data?.cause_counts || {};
  const keywordCount = Number(causeCounts.literal_keyword_match || 0);
  const scorerCount = Number(causeCounts.complexity_scorer || 0);
  // The classifier causes are part of the same denominator: omitting them
  // would overstate every heuristic share once the gate is on.
  const jevAccepted = Number(causeCounts.jev_classifier || 0);
  const jevLowConf = Number(causeCounts.jev_low_confidence || 0);
  const jevFallback = Number(causeCounts.jev_fallback_heuristic || 0);
  const jevCount = jevAccepted + jevLowConf + jevFallback;
  const causeTotal = keywordCount + scorerCount + jevCount;
  const keywordShare = causeTotal > 0 ? Math.round((keywordCount / causeTotal) * 100) : 0;
  const jevShare = causeTotal > 0 ? Math.round((jevCount / causeTotal) * 100) : 0;
  // Over-routing is the number worth watching: the classifier's errors on the
  // evaluation corpus were all over-routing, which buys headroom rather than
  // costing capability.
  const jevOverrouted = Number(jevStats?.data?.overrouted || 0);
  const jevConfigured = !!routerConfig?.jev_enabled;

  return (
    <>
      {/* View 1 — Requests per tier */}
      <div className="card" style={{ marginTop: 16 }}>
        <div className="row row--between" style={{ marginBottom: 12 }}>
          <h3 className="card__title" style={{ margin: 0 }}>Requests per tier</h3>
          <span className="dim" style={{ fontSize: 12 }}>
            {totalRequests.toLocaleString()} req · ${totalCost.toFixed(4)}
            {causeTotal > 0 && ` · ${keywordShare}% keyword-routed`}
            {jevCount > 0 && ` · ${jevShare}% classifier-consulted`}
          </span>
        </div>
        {tiers.loading && <Spinner label="Loading tier stats…" />}
        {tiers.error && <ErrorBanner error={tiers.error} onRetry={tiers.reload} />}
        {!tiers.loading && !tiers.error && (
          <div className="stats-grid">
            {TIERS.map((t) => {
              const row = tiers.rows.find((r) => r.tier === t.key) || { request_count: 0, total_tokens: 0, cost_usd: 0 };
              return (
                <div key={t.key} className="stat-card">
                  <div className="stat-card__label">{t.label}</div>
                  <div className="stat-card__value">{Number(row.request_count || 0).toLocaleString()}</div>
                  <div className="stat-card__hint">
                    {Number(row.total_tokens || 0).toLocaleString()} tokens · ${Number(row.cost_usd || 0).toFixed(4)}
                  </div>
                </div>
              );
            })}
          </div>
        )}
      </div>

      {/* View 2 — Jev AI classifier summary. Shown only when the router is
          opted in or the classifier actually ran, so a deployment that never
          enabled it sees the page it always saw. */}
      {(jevConfigured || jevCount > 0) && (
        <div className="card" style={{ marginTop: 16 }}>
          <div className="row row--between" style={{ marginBottom: 12 }}>
            <h3 className="card__title" style={{ margin: 0 }}>Jev AI classification</h3>
            <span className="dim" style={{ fontSize: 12 }}>
              {jevConfigured ? 'router opted in' : 'not opted in for this router'}
            </span>
          </div>
          <div className="stats-grid">
            <div className="stat-card">
              <div className="stat-card__label">Consulted</div>
              <div className="stat-card__value">{Number(jevStats?.data?.consulted || 0).toLocaleString()}</div>
              <div className="stat-card__hint">
                {causeTotal > 0 ? `${jevShare}% of decisions in range` : '—'}
              </div>
            </div>
            <div className="stat-card">
              <div className="stat-card__label">Accepted</div>
              <div className="stat-card__value">{jevAccepted.toLocaleString()}</div>
              <div className="stat-card__hint">classifier tier routed</div>
            </div>
            <div className="stat-card">
              <div className="stat-card__label">Over-routed</div>
              <div className="stat-card__value">{jevOverrouted.toLocaleString()}</div>
              <div className="stat-card__hint">
                {Number(jevStats?.data?.underrouted || 0).toLocaleString()} under-routed
              </div>
            </div>
            <div className="stat-card">
              <div className="stat-card__label">Fallbacks</div>
              <div className="stat-card__value">{(jevLowConf + jevFallback).toLocaleString()}</div>
              <div className="stat-card__hint">heuristic tier kept</div>
            </div>
          </div>
          <p className="muted" style={{ marginTop: 12, marginBottom: 0 }}>
            Full classifier detail — verdict split, confidence distribution
            against the floor, and per-tier over-routing — is on the Jev AI tab.
          </p>
        </div>
      )}

      {/* View 3 — Cost + performance per target model */}
      <div className="card" style={{ marginTop: 16, padding: 0 }}>
        <div className="row row--between" style={{ padding: '14px 16px 8px' }}>
          <h3 className="card__title" style={{ margin: 0 }}>Cost & performance per target model</h3>
          <span className="dim" style={{ fontSize: 12 }}>
            p50/p95 latency in ms · err = failed share
          </span>
        </div>
        {models.loading && <Spinner label="Loading model stats…" />}
        {models.error && <ErrorBanner error={models.error} onRetry={models.reload} />}
        {!models.loading && !models.error && models.rows.length === 0 && (
          <EmptyState title="No data for this range" />
        )}
        {!models.loading && !models.error && models.rows.length > 0 && (
          <div style={{ overflowX: 'auto' }}>
            <table className="table">
              <thead>
                <tr>
                  <th>Target model</th>
                  <th style={{ textAlign: 'right' }}>Requests</th>
                  <th style={{ textAlign: 'right' }}>Total tokens</th>
                  <th style={{ textAlign: 'right' }}>Cost (USD)</th>
                  <th style={{ textAlign: 'right' }}>Avg cost / request</th>
                  <th style={{ textAlign: 'right' }}>p50</th>
                  <th style={{ textAlign: 'right' }}>p95</th>
                  <th style={{ textAlign: 'right' }}>err</th>
                </tr>
              </thead>
              <tbody>
                {models.rows.map((m, i) => {
                  const perf = perfByModel.get(m.model);
                  return (
                    <tr key={`${m.model}-${i}`}>
                      <td className="mono">{m.model || '—'}</td>
                      <td className="mono" style={{ textAlign: 'right' }}>{Number(m.request_count || 0).toLocaleString()}</td>
                      <td className="mono" style={{ textAlign: 'right' }}>{Number(m.total_tokens || 0).toLocaleString()}</td>
                      <td className="mono" style={{ textAlign: 'right' }}>${Number(m.cost_usd || 0).toFixed(4)}</td>
                      <td className="mono" style={{ textAlign: 'right' }}>${Number(m.avg_cost_per_request || 0).toFixed(4)}</td>
                      <td className="mono" style={{ textAlign: 'right' }}>{perf ? Math.round(perf.p50_latency_ms) : '—'}</td>
                      <td className="mono" style={{ textAlign: 'right' }}>{perf ? Math.round(perf.p95_latency_ms) : '—'}</td>
                      <td className="mono" style={{ textAlign: 'right' }}>
                        {perf ? `${(perf.error_rate * 100).toFixed(1)}%` : '—'}
                      </td>
                    </tr>
                  );
                })}
              </tbody>
            </table>
          </div>
        )}
      </div>

      {noData && (
        <EmptyState title="No data for this range" />
      )}
    </>
  );
}
