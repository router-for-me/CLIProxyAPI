import React, { useState, useEffect, useMemo } from 'react';
import { listAutoRouterDecisions, getAutoRouterDecision } from '../../api/client.js';
import { useAsync } from '../../hooks/useAsync.js';
import { Spinner, ErrorBanner, EmptyState } from '../Primitives.jsx';

const TIER_OPTIONS = ['simple', 'medium', 'complex', 'reasoning'];

// fmtMs renders a millisecond count compactly.
function fmtMs(v) {
  const n = Number(v || 0);
  if (n >= 1000) return `${(n / 1000).toFixed(1)}s`;
  return `${Math.round(n)}ms`;
}

// DecisionDetail is the expanded panel for one replayed decision: the full
// stored snapshot (score fields, rules, chain) plus event metrics.
function DecisionDetail({ routerId, requestId, onClose }) {
  const detail = useAsync(() => getAutoRouterDecision(routerId, requestId), [routerId, requestId]);
  const snap = detail.data?.decision || null;
  const ev = detail.data?.event || {};

  return (
    <div className="card" style={{ marginTop: 12, borderColor: 'var(--accent, #7ca6c9)' }}>
      <div className="row row--between" style={{ marginBottom: 8 }}>
        <h4 className="card__title" style={{ margin: 0 }}>
          Replay · <span className="mono">{requestId}</span>
        </h4>
        <button onClick={onClose}>Close</button>
      </div>
      {detail.loading && <Spinner label="Loading decision…" />}
      {detail.error && <ErrorBanner error={detail.error} onRetry={detail.reload} />}
      {detail.data && !snap && <EmptyState title="Decision snapshot missing for this event" />}
      {snap && (
        <>
          <div className="stats-grid" style={{ marginBottom: 12 }}>
            <div className="stat-card">
              <div className="stat-card__label">Score total</div>
              <div className="stat-card__value">{Number(snap.score_total ?? 0).toFixed(4)}</div>
              <div className="stat-card__hint">{snap.reasoning_markers} reasoning markers</div>
            </div>
            <div className="stat-card">
              <div className="stat-card__label">Scored → Effective</div>
              <div className="stat-card__value mono" style={{ fontSize: 18 }}>
                {snap.scored_tier} → {snap.effective_tier}
              </div>
              <div className="stat-card__hint">cause: {snap.decision_cause || '—'}</div>
            </div>
            <div className="stat-card">
              <div className="stat-card__label">Mapping → Target</div>
              <div className="stat-card__value mono" style={{ fontSize: 18 }}>
                {snap.mapping_tier || '—'} → <span style={{ fontSize: 12 }}>{snap.target_model || '—'}</span>
              </div>
              <div className="stat-card__hint">
                profile v{snap.profile_version} · {(snap.profile_hash || '').slice(0, 15)}…
              </div>
            </div>
            <div className="stat-card">
              <div className="stat-card__label">Event</div>
              <div className="stat-card__value">{fmtMs(ev.latency_ms)}</div>
              <div className="stat-card__hint">
                ttft {fmtMs(ev.ttft_ms)} · {Number(ev.total_tokens || 0).toLocaleString()} tok · ${Number(ev.cost_usd || 0).toFixed(4)}
                {ev.failed ? ` · FAILED ${ev.fail_status_code || ''}` : ''}
              </div>
            </div>
          </div>

          <h4 style={{ margin: '8px 0' }}>Score fields</h4>
          <div className="row gap-sm" style={{ flexWrap: 'wrap', marginBottom: 12 }}>
            {Object.entries(snap.score_fields || {}).map(([k, v]) => (
              <span key={k} className="mono dim" style={{ fontSize: 12 }}>
                {k}: {Number(v).toFixed(3)}
              </span>
            ))}
          </div>

          {(snap.matched_rules || []).length > 0 && (
            <>
              <h4 style={{ margin: '8px 0' }}>Matched keyword rules</h4>
              <div style={{ overflowX: 'auto', marginBottom: 12 }}>
                <table className="table">
                  <thead><tr><th>Rule</th><th>Tier</th><th>Keywords</th></tr></thead>
                  <tbody>
                    {snap.matched_rules.map((r, i) => (
                      <tr key={i}>
                        <td className="mono">{r.id}</td>
                        <td className="mono">{r.tier}</td>
                        <td className="mono">{(r.keywords || []).join(', ')}</td>
                      </tr>
                    ))}
                  </tbody>
                </table>
              </div>
            </>
          )}

          {(snap.fallback_chain || []).length > 0 && (
            <div className="dim" style={{ fontSize: 12 }}>
              fallback chain: <span className="mono">{snap.fallback_chain.join(' → ')}</span>
            </div>
          )}
        </>
      )}
    </div>
  );
}

export default function ReplayTab({ router, apiKeyId, focusRequestId }) {
  const [page, setPage] = useState(1);
  const [effectiveTier, setEffectiveTier] = useState('');
  const [cause, setCause] = useState('');
  const [openRequestId, setOpenRequestId] = useState(focusRequestId || '');

  // When another tab asks us to open a request, surface it.
  useEffect(() => {
    if (focusRequestId) setOpenRequestId(focusRequestId);
  }, [focusRequestId]);

  const params = useMemo(() => ({
    page,
    page_size: 25,
    api_key_id: apiKeyId || undefined,
    effective_tier: effectiveTier || undefined,
    decision_cause: cause || undefined,
  }), [page, apiKeyId, effectiveTier, cause]);

  const list = useAsync(() => listAutoRouterDecisions(router.id, params), [router.id, JSON.stringify(params)]);

  const rows = list.data?.decisions || [];
  const totalPages = list.data?.total_pages || 0;

  return (
    <>
      <div className="card" style={{ marginTop: 16 }}>
        <div className="row row--between" style={{ marginBottom: 8 }}>
          <h3 className="card__title" style={{ margin: 0 }}>Decision replay</h3>
          <span className="dim" style={{ fontSize: 12 }}>
            {Number(list.data?.total ?? 0).toLocaleString()} decisions
          </span>
        </div>
        <div className="row gap-sm" style={{ marginBottom: 8 }}>
          <select value={effectiveTier} onChange={(e) => { setEffectiveTier(e.target.value); setPage(1); }}>
            <option value="">All effective tiers</option>
            {TIER_OPTIONS.map((t) => <option key={t} value={t}>{t}</option>)}
          </select>
          <select value={cause} onChange={(e) => { setCause(e.target.value); setPage(1); }}>
            <option value="">All causes</option>
            <option value="complexity_scorer">complexity_scorer</option>
            <option value="literal_keyword_match">literal_keyword_match</option>
          </select>
        </div>

        {list.loading && <Spinner label="Loading decisions…" />}
        {list.error && <ErrorBanner error={list.error} onRetry={list.reload} />}
        {!list.loading && !list.error && rows.length === 0 && <EmptyState title="No decisions in this range" />}
        {!list.loading && !list.error && rows.length > 0 && (
          <div style={{ overflowX: 'auto' }}>
            <table className="table">
              <thead>
                <tr>
                  <th>Time</th>
                  <th>Scored → Effective</th>
                  <th>Cause</th>
                  <th>Target model</th>
                  <th style={{ textAlign: 'right' }}>Tokens</th>
                  <th style={{ textAlign: 'right' }}>Cost</th>
                  <th>Req id</th>
                </tr>
              </thead>
              <tbody>
                {rows.map((d) => (
                  <tr key={d.id} style={{ cursor: 'pointer' }} onClick={() => setOpenRequestId(d.request_id)}>
                    <td className="mono" style={{ fontSize: 12 }}>
                      {d.requested_at ? new Date(d.requested_at).toLocaleString() : '—'}
                    </td>
                    <td className="mono">
                      {d.scored_tier || '—'} → {d.effective_tier || '—'}
                      {d.mapping_tier && d.mapping_tier !== d.effective_tier && (
                        <span className="dim"> (mapped {d.mapping_tier})</span>
                      )}
                    </td>
                    <td className="mono" style={{ fontSize: 12 }}>{d.decision_cause || '—'}</td>
                    <td className="mono" style={{ fontSize: 12 }}>{d.model || '—'}</td>
                    <td className="mono" style={{ textAlign: 'right' }}>{Number(d.total_tokens || 0).toLocaleString()}</td>
                    <td className="mono" style={{ textAlign: 'right' }}>${Number(d.cost_usd || 0).toFixed(4)}</td>
                    <td className="mono" style={{ fontSize: 12 }} title={d.request_id}>
                      {(d.request_id || '').slice(0, 8) || '—'}
                    </td>
                  </tr>
                ))}
              </tbody>
            </table>
          </div>
        )}
        {totalPages > 1 && (
          <div className="row gap-sm" style={{ marginTop: 8, alignItems: 'center' }}>
            <button disabled={page <= 1} onClick={() => setPage(page - 1)}>← Prev</button>
            <span className="dim" style={{ fontSize: 12 }}>page {page} / {totalPages}</span>
            <button disabled={page >= totalPages} onClick={() => setPage(page + 1)}>Next →</button>
          </div>
        )}
      </div>

      {openRequestId && (
        <DecisionDetail
          routerId={router.id}
          requestId={openRequestId}
          onClose={() => setOpenRequestId('')}
        />
      )}
    </>
  );
}
