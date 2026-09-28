import React from 'react';
import { Spinner, ErrorBanner, EmptyState } from '../Primitives.jsx';

// Runtime defaults applied when a router does not set its own. These mirror
// jevDefaultMinConfidence / jevDefaultTimeout in
// sdk/api/handlers/handlers_auto_router.go: the API returns the stored knobs
// as-is (0 = unset) because the client must not import that package, so the
// defaults are applied here for display.
const DEFAULT_MIN_CONFIDENCE = 0.35;
const DEFAULT_TIMEOUT_MS = 400;

// Published TypeSafe System One input price, USD per million tokens. A router
// pointed at a self-hosted base URL will not match this; the card labels the
// figure as an estimate for that reason.
const JEV_INPUT_USD_PER_MTOK = 0.042;

// Below this many decided verdicts the confidence distribution is too thin to
// tune the floor against: classifier confidence drifts by 0.01-0.04 between
// runs on identical input (measured on a 60-case corpus), so a floor moved on
// a handful of samples is fitted to noise. The card says so instead of
// inviting the change.
const MIN_SAMPLE_FOR_FLOOR_GUIDANCE = 100;

// Tier order and colours, matching the rest of the analysis page.
const TIER_ORDER = ['simple', 'medium', 'complex', 'reasoning'];
const TIER_LABEL = { simple: 'Simple', medium: 'Medium', complex: 'Complex', reasoning: 'Reasoning' };

// Verdict outcomes as the gate reports them, keyed by their JSON field names
// on AutoRouterJevStats (they are the wire contract, not display labels).
const VERDICTS = [
  { key: 'accepted', label: 'Accepted', hint: 'classifier tier routed' },
  { key: 'low_confidence', label: 'Below floor', hint: 'heuristic tier kept' },
  { key: 'errors', label: 'Call failed', hint: 'heuristic tier kept' },
  { key: 'breaker_open', label: 'Breaker open', hint: 'classifier not called' },
];

function pct(part, whole) {
  if (!whole) return 0;
  return Math.round((Number(part) / Number(whole)) * 100);
}

// ConfidenceHistogram draws the 20-bucket classifier confidence distribution
// with the effective floor marked, so an operator can see how much of the mass
// the floor is discarding.
function ConfidenceHistogram({ histogram, floor }) {
  const width = 720;
  const height = 150;
  const pad = { top: 12, right: 8, bottom: 22, left: 30 };
  const plotW = width - pad.left - pad.right;
  const plotH = height - pad.top - pad.bottom;
  const maxCount = Math.max(1, ...histogram.map((b) => b.count));
  const barW = plotW / histogram.length;
  const xScore = (score) => pad.left + score * plotW;

  return (
    <svg viewBox={`0 0 ${width} ${height}`} style={{ width: '100%', height: 'auto' }} role="img" aria-label="Classifier confidence histogram">
      {floor > 0 && (
        <g>
          <line x1={xScore(floor)} x2={xScore(floor)} y1={pad.top} y2={pad.top + plotH} stroke="currentColor" strokeDasharray="3 3" opacity="0.5" />
          <text x={xScore(floor) + 3} y={pad.top + 10} fontSize="10" opacity="0.75">{`floor ${floor.toFixed(2)}`}</text>
        </g>
      )}
      {histogram.map((b) => {
        const h = (b.count / maxCount) * plotH;
        const accepted = (b.bucket + 0.5) * 0.05 >= floor;
        return (
          <rect
            key={b.bucket}
            x={pad.left + b.bucket * barW + 1}
            y={pad.top + plotH - h}
            width={Math.max(1, barW - 2)}
            height={h}
            fill={accepted ? '#7cc9a1' : '#d9b45c'}
            opacity="0.85"
          >
            <title>{`[${(b.bucket * 0.05).toFixed(2)}–${((b.bucket + 1) * 0.05).toFixed(2)}): ${b.count}`}</title>
          </rect>
        );
      })}
      <line x1={pad.left} x2={width - pad.right} y1={pad.top + plotH} y2={pad.top + plotH} stroke="currentColor" opacity="0.3" />
      {[0, 0.25, 0.5, 0.75, 1].map((v) => (
        <text key={v} x={xScore(v)} y={height - 6} fontSize="10" textAnchor="middle" opacity="0.6">{v}</text>
      ))}
    </svg>
  );
}

// ChoiceMatrix shows what the classifier chose (columns) given the tier the
// heuristic would have routed (rows). The diagonal is agreement; cells to the
// right are over-routing (money spent on headroom) and to the left
// under-routing (capability risk). Every measured classifier error on the
// evaluation corpus was over-routing, so this is the view that shows it.
function ChoiceMatrix({ cells }) {
  const rank = (t) => TIER_ORDER.indexOf(t);
  return (
    <div style={{ overflowX: 'auto' }}>
      <table className="table">
        <thead>
          <tr>
            <th>Heuristic tier</th>
            {TIER_ORDER.map((c) => (
              <th key={c} style={{ textAlign: 'right' }}>{TIER_LABEL[c]}</th>
            ))}
          </tr>
        </thead>
        <tbody>
          {TIER_ORDER.map((heuristic) => (
            <tr key={heuristic}>
              <td>{TIER_LABEL[heuristic]}</td>
              {TIER_ORDER.map((choice) => {
                const n = Number(cells[`${choice}|${heuristic}`] || 0);
                const delta = rank(choice) - rank(heuristic);
                const tint = delta === 0 ? undefined : delta > 0 ? 'var(--warn, #b45309)' : '#c97c7c';
                return (
                  <td key={choice} className="mono" style={{ textAlign: 'right', color: n ? tint : undefined, opacity: n ? 1 : 0.35 }}>
                    {n ? n.toLocaleString() : '·'}
                  </td>
                );
              })}
            </tr>
          ))}
        </tbody>
      </table>
      <p className="muted" style={{ marginTop: 8, marginBottom: 0 }}>
        Rows are the tier the heuristic scored; columns are what the classifier
        chose. The diagonal is agreement, the right of it is over-routing
        (headroom bought), the left is under-routing (capability risk). Only
        accepted verdicts appear here — a verdict below the floor routes
        nothing.
      </p>
    </div>
  );
}

export default function JevTab({ jevStats, routedTotal, routerConfig }) {
  const stats = jevStats.data;
  const cfg = routerConfig || {};
  const floor = Number(cfg.jev_min_confidence) > 0
    ? Number(cfg.jev_min_confidence)
    : DEFAULT_MIN_CONFIDENCE;
  const timeout = Number(cfg.jev_timeout_ms) > 0
    ? Number(cfg.jev_timeout_ms)
    : DEFAULT_TIMEOUT_MS;

  if (jevStats.loading) {
    return <div className="card" style={{ marginTop: 16 }}><Spinner label="Loading Jev AI usage…" /></div>;
  }
  if (jevStats.error) {
    return <div className="card" style={{ marginTop: 16 }}><ErrorBanner error={jevStats.error} onRetry={jevStats.reload} /></div>;
  }

  const consulted = Number(stats?.consulted || 0);
  const choices = stats?.choice_counts || {};
  const appliedChoices = stats?.applied_choice_counts || {};
  const decided = Number(stats?.decided_count || 0);
  const overrouted = Number(stats?.overrouted || 0);
  const underrouted = Number(stats?.underrouted || 0);
  const appliedTotal = TIER_ORDER.reduce((a, t) => a + Number(appliedChoices[t] || 0), 0);
  const costUSD = (Number(stats?.input_tokens || 0) / 1_000_000) * JEV_INPUT_USD_PER_MTOK;

  // The global classifier can be on while this router never opts in, or opt in
  // while the global switch (or the API key) is missing — in both cases the
  // rollup below is empty for a reason the operator needs named.
  const routerEnabled = !!cfg.jev_enabled;
  const status = !cfg.id
    ? { tone: 'muted', text: 'Classifier configuration is unavailable for this router; showing observed activity only.' }
    : !routerEnabled
      ? { tone: 'muted', text: 'This router is not opted into Jev AI classification, so the heuristic alone routes its requests. Enable it on the router to use the classifier.' }
      : consulted === 0
        ? { tone: 'warn', text: 'This router is opted in but no request in range carries a classifier verdict. Check the global Jev AI switch and the API key on the Settings page — with either missing, the heuristic routes unchanged.' }
        : { tone: 'ok', text: `Active: floor ${floor.toFixed(2)}, timeout ${timeout} ms${cfg.jev_model_override ? `, model ${cfg.jev_model_override}` : ''}.` };

  const statusColor = status.tone === 'ok' ? '#7cc9a1' : status.tone === 'warn' ? 'var(--warn, #b45309)' : undefined;

  return (
    <>
      <div className="card" style={{ marginTop: 16 }}>
        <h3 className="card__title">Classifier status</h3>
        <p style={{ marginTop: 4, marginBottom: 12, color: statusColor }}>{status.text}</p>
        <div className="stats-grid">
          <div className="stat-card">
            <div className="stat-card__label">Consulted</div>
            <div className="stat-card__value">{consulted.toLocaleString()}</div>
            <div className="stat-card__hint">
              {routedTotal ? `${pct(consulted, routedTotal)}% of ${Number(routedTotal).toLocaleString()} routed` : 'no routed requests in range'}
            </div>
          </div>
          <div className="stat-card">
            <div className="stat-card__label">Cache hits</div>
            <div className="stat-card__value">{Number(stats?.cache_hits || 0).toLocaleString()}</div>
            <div className="stat-card__hint">
              {consulted ? `${pct(stats?.cache_hits, consulted)}% served without a call` : '—'}
            </div>
          </div>
          <div className="stat-card">
            <div className="stat-card__label">Overrides</div>
            <div className="stat-card__value">{Number(stats?.override_count || 0).toLocaleString()}</div>
            <div className="stat-card__hint">
              {appliedTotal ? `${pct(stats?.override_count, appliedTotal)}% of accepted verdicts disagreed` : 'classifier agreed with the heuristic'}
            </div>
          </div>
          <div className="stat-card">
            <div className="stat-card__label">Avg latency</div>
            <div className="stat-card__value">{Math.round(Number(stats?.avg_latency_ms || 0))} ms</div>
            <div className="stat-card__hint">added before the upstream call</div>
          </div>
        </div>
      </div>

      {consulted === 0 ? (
        <div className="card" style={{ marginTop: 16 }}>
          <EmptyState title="No classifier activity in this range" />
        </div>
      ) : (
        <>
          <div className="card" style={{ marginTop: 16 }}>
            <div className="row row--between" style={{ marginBottom: 12 }}>
              <h3 className="card__title" style={{ margin: 0 }}>How the gate resolved</h3>
              <span className="dim" style={{ fontSize: 12 }}>
                {consulted.toLocaleString()} verdict{consulted === 1 ? '' : 's'}
              </span>
            </div>
            <div className="stats-grid">
              {VERDICTS.map((v) => (
                <div key={v.key} className="stat-card">
                  <div className="stat-card__label">{v.label}</div>
                  <div className="stat-card__value">{Number(stats?.[v.key] || 0).toLocaleString()}</div>
                  <div className="stat-card__hint">
                    {pct(stats?.[v.key], consulted)}% · {v.hint}
                  </div>
                </div>
              ))}
            </div>
            <p className="muted" style={{ marginTop: 12, marginBottom: 0 }}>
              A breaker-open verdict means the classifier was not called at all;
              a failed call means it was called and did not answer in time
              ({timeout} ms). Both fall back to the heuristic tier.
            </p>
          </div>

          <div className="card" style={{ marginTop: 16 }}>
            <div className="row row--between" style={{ marginBottom: 12 }}>
              <h3 className="card__title" style={{ margin: 0 }}>Tier the classifier chose</h3>
              <span className="dim" style={{ fontSize: 12 }}>
                wanted vs applied
              </span>
            </div>
            <div className="stats-grid">
              {TIER_ORDER.map((t) => (
                <div key={t} className="stat-card">
                  <div className="stat-card__label">{TIER_LABEL[t]}</div>
                  <div className="stat-card__value">{Number(choices[t] || 0).toLocaleString()}</div>
                  <div className="stat-card__hint">
                    {Number(appliedChoices[t] || 0).toLocaleString()} applied
                  </div>
                </div>
              ))}
            </div>
            <p className="muted" style={{ marginTop: 12, marginBottom: 0 }}>
              &ldquo;Wanted&rdquo; counts every verdict that named a tier;
              &ldquo;applied&rdquo; counts only those the floor accepted. The
              gap is the classifier&apos;s opinion the floor discarded.
            </p>
          </div>

          <div className="card" style={{ marginTop: 16 }}>
            <div className="row row--between" style={{ marginBottom: 12 }}>
              <h3 className="card__title" style={{ margin: 0 }}>Classifier choice vs heuristic tier</h3>
              <span className="dim" style={{ fontSize: 12 }}>
                {overrouted.toLocaleString()} over-routed · {underrouted.toLocaleString()} under-routed
              </span>
            </div>
            {appliedTotal === 0 ? (
              <EmptyState title="No accepted verdicts in this range" />
            ) : (
              <ChoiceMatrix cells={stats?.choice_vs_scored || {}} />
            )}
            {overrouted > 0 && (
              <div style={{ marginTop: 12 }}>
                <div className="form__label" style={{ marginBottom: 4 }}>Over-routing by heuristic tier</div>
                <div className="row gap-sm" style={{ flexWrap: 'wrap' }}>
                  {TIER_ORDER.filter((t) => Number(stats?.overrouted_by_tier?.[t] || 0) > 0).map((t) => (
                    <span key={t} className="muted">
                      {TIER_LABEL[t]}: <strong>{Number(stats.overrouted_by_tier[t]).toLocaleString()}</strong>
                    </span>
                  ))}
                </div>
              </div>
            )}
          </div>

          <div className="card" style={{ marginTop: 16 }}>
            <div className="row row--between" style={{ marginBottom: 8 }}>
              <h3 className="card__title" style={{ margin: 0 }}>Confidence</h3>
              <span className="dim" style={{ fontSize: 12 }}>
                mean {Number(stats?.avg_confidence || 0).toFixed(3)} · p95 {Number(stats?.p95_confidence || 0).toFixed(3)} · {decided.toLocaleString()} decided
              </span>
            </div>
            <ConfidenceHistogram histogram={stats?.confidence_histogram || []} floor={floor} />
            <p className="muted" style={{ marginTop: 8, marginBottom: 0 }}>
              Green bars clear the floor of {floor.toFixed(2)}; amber ones were
              rejected for low confidence. Verdicts that never got an answer
              (failed calls, open breaker) carry no confidence and are excluded.
            </p>
            {decided > 0 && decided < MIN_SAMPLE_FOR_FLOOR_GUIDANCE && (
              <p className="muted" style={{ marginTop: 8, marginBottom: 0, color: 'var(--warn, #b45309)' }}>
                Only {decided.toLocaleString()} decided verdicts in range.
                Classifier confidence drifts by 0.01–0.04 between runs on
                identical input, so a floor moved on this sample would be
                fitted to noise. Gather at least {MIN_SAMPLE_FOR_FLOOR_GUIDANCE}
                before retuning it.
              </p>
            )}
          </div>

          <div className="card" style={{ marginTop: 16 }}>
            <h3 className="card__title">Classifier cost</h3>
            <div className="stats-grid">
              <div className="stat-card">
                <div className="stat-card__label">Input tokens</div>
                <div className="stat-card__value">{Number(stats?.input_tokens || 0).toLocaleString()}</div>
                <div className="stat-card__hint">
                  {Math.round(Number(stats?.avg_input_tokens || 0)).toLocaleString()} per call
                </div>
              </div>
              <div className="stat-card">
                <div className="stat-card__label">Est. spend</div>
                <div className="stat-card__value">${costUSD.toFixed(4)}</div>
                <div className="stat-card__hint">
                  at ${JEV_INPUT_USD_PER_MTOK}/Mtok input
                </div>
              </div>
              <div className="stat-card">
                <div className="stat-card__label">Avg latency</div>
                <div className="stat-card__value">{Math.round(Number(stats?.avg_latency_ms || 0))} ms</div>
                <div className="stat-card__hint">of {timeout} ms allowed</div>
              </div>
              <div className="stat-card">
                <div className="stat-card__label">Accepted share</div>
                <div className="stat-card__value">{pct(stats?.accepted, consulted)}%</div>
                <div className="stat-card__hint">of consulted verdicts</div>
              </div>
            </div>
            <p className="muted" style={{ marginTop: 12, marginBottom: 0 }}>
              Output tokens are free on this classifier, so input is the whole
              bill. The classifier runs before any upstream model connection, so
              its latency adds to end-to-end response time in full — the figure
              above is the added delay, not a total. A cache hit is counted in
              the token sum but not in a separate call, so spend is an upper
              bound when the cache is warm.
            </p>
          </div>
        </>
      )}
    </>
  );
}
