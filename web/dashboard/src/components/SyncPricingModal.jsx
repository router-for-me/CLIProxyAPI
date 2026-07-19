import React, { useState, useMemo, useEffect, useCallback } from 'react';
import {
  previewPricingSync, applyPricingSync,
} from '../api/client.js';
import { Modal, Spinner, ErrorBanner } from './Primitives.jsx';

// SyncPricingModal — review and accept suggested pricing from bundled
// upstream sources (openai-official, anthropic-official, openrouter,
// cloudflare, google-official, etc).
//
// Flow:
//   1. On open, the modal POSTs sync-pricing-preview to fetch one row per
//      catalog model with current_pricing + a `suggestions[]` array of
//      per-source prices.
//   2. The operator selects one source per row (or skips), then clicks
//      "Apply N selections". The selection is POSTed to sync-pricing-apply
//      and committed to model_pricing.
//   3. Rows without any suggestions are shown greyed-out ("not in catalog")
//      and cannot be selected.
export default function SyncPricingModal({ onClose, onApplied }) {
  const [rows, setRows] = useState(null);
  const [sources, setSources] = useState([]);
  const [loading, setLoading] = useState(true);
  const [error, setError] = useState('');
  const [sourceFilter, setSourceFilter] = useState('');
  const [search, setSearch] = useState('');
  // selections: map<model_id, source_name>. Source == '' means "skip".
  const [selections, setSelections] = useState({});
  const [applying, setApplying] = useState(false);
  const [applyResult, setApplyResult] = useState(null);

  useEffect(() => {
    let cancelled = false;
    setLoading(true);
    setError('');
    previewPricingSync()
      .then((data) => {
        if (cancelled) return;
        setRows(data?.rows || []);
        setSources(data?.sources || []);
        // Pre-select the first suggestion for every model that has one,
        // preferring "official" sources when multiple are present.
        const initial = {};
        for (const row of data?.rows || []) {
          if (row.suggestions && row.suggestions.length > 0) {
            const pick = pickPreferredSource(row.suggestions);
            initial[row.model_id] = pick;
          }
        }
        setSelections(initial);
      })
      .catch((err) => {
        if (cancelled) return;
        setError(err.message || 'Failed to load pricing preview');
      })
      .finally(() => { if (!cancelled) setLoading(false); });
    return () => { cancelled = true; };
  }, []);

  const filteredRows = useMemo(() => {
    if (!rows) return [];
    const q = search.trim().toLowerCase();
    return rows.filter((r) => {
      if (sourceFilter && !r.suggestions?.some((s) => s.source === sourceFilter)) {
        return false;
      }
      if (q && !r.model_id.toLowerCase().includes(q) &&
          !(r.provider || '').toLowerCase().includes(q)) {
        return false;
      }
      return true;
    });
  }, [rows, sourceFilter, search]);

  const selectedCount = useMemo(() => {
    if (!rows) return 0;
    let n = 0;
    for (const row of rows) {
      const sel = selections[row.model_id];
      if (sel) n++;
    }
    return n;
  }, [rows, selections]);

  const setSelection = useCallback((modelId, source) => {
    setSelections((prev) => ({ ...prev, [modelId]: source }));
  }, []);

  async function handleApply(e) {
    e.preventDefault();
    if (applying) return;
    setApplying(true);
    setError('');
    setApplyResult(null);
    const picks = [];
    for (const row of rows || []) {
      const source = selections[row.model_id];
      if (!source) continue;
      picks.push({ model_id: row.model_id, source });
    }
    if (picks.length === 0) {
      setError('No models selected. Pick at least one source to apply.');
      setApplying(false);
      return;
    }
    try {
      const result = await applyPricingSync(picks);
      setApplyResult(result);
      onApplied?.(result);
    } catch (err) {
      setError(err.message || 'Apply failed');
    } finally {
      setApplying(false);
    }
  }

  return (
    <Modal title="Sync Pricing from Global Catalog" onClose={onClose}>
      {loading && <Spinner label="Loading pricing suggestions…" />}
      <ErrorBanner error={error} />
      {applyResult && (
        <div className="card" style={{ padding: 12, marginBottom: 12, background: 'var(--bg)' }}>
          <strong>Applied {applyResult.applied}</strong> of {applyResult.requested} pricing rows.
          <ul className="list-bare" style={{ marginTop: 6 }}>
            {(applyResult.results || []).slice(0, 5).map((r, i) => (
              <li key={i} className="dim mono" style={{ fontSize: 12 }}>
                {r.model_id}: {r.status}{r.source ? ` (${r.source})` : ''}{r.reason ? ` — ${r.reason}` : ''}
              </li>
            ))}
            {(applyResult.results || []).length > 5 && (
              <li className="dim" style={{ fontSize: 12 }}>… and {(applyResult.results || []).length - 5} more</li>
            )}
          </ul>
        </div>
      )}
      {!loading && !applyResult && rows && rows.length > 0 && (
        <>
          <p className="form__hint" style={{ marginBottom: 12 }}>
            For every model in the catalog, the table below shows suggested
            prices from known upstream sources (OpenAI, Anthropic, OpenRouter,
            Cloudflare, etc). Pick a source per model, then click
            "Apply&nbsp;{selectedCount}&nbsp;selections" to commit.
          </p>
          <div className="row gap-lg" style={{ flexWrap: 'wrap', marginBottom: 12 }}>
            <input
              type="text" placeholder="Search model…"
              value={search}
              onChange={(e) => setSearch(e.target.value)}
              style={{ width: 240 }}
            />
            <select
              value={sourceFilter}
              onChange={(e) => setSourceFilter(e.target.value)}
              style={{ width: 200 }}
            >
              <option value="">All sources</option>
              {sources.map((s) => <option key={s} value={s}>{s}</option>)}
            </select>
            <button
              onClick={() => {
                const next = { ...selections };
                for (const row of filteredRows) {
                  if (row.suggestions?.length > 0) {
                    next[row.model_id] = pickPreferredSource(row.suggestions);
                  }
                }
                setSelections(next);
              }}
              style={{ padding: '6px 12px', fontSize: 12 }}
            >
              Select all visible
            </button>
            <button
              onClick={() => {
                const next = { ...selections };
                for (const row of filteredRows) {
                  delete next[row.model_id];
                }
                setSelections(next);
              }}
              style={{ padding: '6px 12px', fontSize: 12 }}
            >
              Clear visible
            </button>
          </div>
          <div style={{ maxHeight: '52vh', overflowY: 'auto', border: '1px solid var(--border)', borderRadius: 'var(--radius-sm)' }}>
            <table className="table">
              <thead style={{ position: 'sticky', top: 0, background: 'var(--bg-card)' }}>
                <tr>
                  <th style={{ width: 40 }}></th>
                  <th>Model</th>
                  <th>Current</th>
                  <th>Source</th>
                  <th>Suggested (I/O/Cache)</th>
                  <th>Notes</th>
                </tr>
              </thead>
              <tbody>
                {filteredRows.map((row) => {
                  const hasSuggestions = row.suggestions && row.suggestions.length > 0;
                  const sel = selections[row.model_id] || '';
                  const picked = hasSuggestions
                    ? row.suggestions.find((s) => s.source === sel)
                    : null;
                  return (
                    <tr key={row.model_id}>
                      <td>
                        <input
                          type="checkbox"
                          disabled={!hasSuggestions}
                          checked={!!sel}
                          onChange={(e) => {
                            if (e.target.checked && hasSuggestions) {
                              setSelection(row.model_id, pickPreferredSource(row.suggestions));
                            } else {
                              setSelection(row.model_id, '');
                            }
                          }}
                          style={{ width: 'auto' }}
                        />
                      </td>
                      <td>
                        <div className="mono">{row.model_id}</div>
                        {row.provider && (
                          <div className="dim" style={{ fontSize: 11 }}>{row.provider}</div>
                        )}
                      </td>
                      <td>{formatPricingSummary(row.current_pricing)}</td>
                      <td>
                        {hasSuggestions ? (
                          <select
                            value={sel}
                            onChange={(e) => setSelection(row.model_id, e.target.value)}
                            style={{ width: '100%', padding: '4px 6px', fontSize: 12 }}
                          >
                            <option value="">— skip —</option>
                            {row.suggestions.map((s) => (
                              <option key={s.source} value={s.source}>{s.source}</option>
                            ))}
                          </select>
                        ) : (
                          <span className="dim" style={{ fontSize: 12 }}>no catalog</span>
                        )}
                      </td>
                      <td>
                        {picked ? formatSuggestionSummary(picked) : (
                          <span className="dim" style={{ fontSize: 12 }}>—</span>
                        )}
                      </td>
                      <td>
                        {picked?.notes && (
                          <span className="dim" style={{ fontSize: 11 }}>{picked.notes}</span>
                        )}
                      </td>
                    </tr>
                  );
                })}
              </tbody>
            </table>
          </div>
        </>
      )}
      {!loading && !applyResult && rows && rows.length === 0 && (
        <p className="muted" style={{ padding: 12 }}>
          No models in the catalog. Sync from <code>/v1/models</code> first
          (use the "Sync now" button) to populate models_catalog, then come
          back here to set pricing.
        </p>
      )}
      {!loading && !applyResult && (
        <div className="form__actions">
          <button onClick={onClose} disabled={applying}>Cancel</button>
          <button className="primary" onClick={handleApply} disabled={applying || selectedCount === 0}>
            {applying ? 'Applying…' : `Apply ${selectedCount} selection${selectedCount === 1 ? '' : 's'}`}
          </button>
        </div>
      )}
      {applyResult && (
        <div className="form__actions">
          <button className="primary" onClick={onClose}>Done</button>
        </div>
      )}
    </Modal>
  );
}

// pickPreferredSource prefers official publishers over marketplaces so the
// default selection points at the canonical list price rather than a
// reseller markup. Ties are broken alphabetically for stability.
function pickPreferredSource(suggestions) {
  const priority = ['openai-official', 'anthropic-official', 'google-official', 'cloudflare', 'openrouter'];
  for (const p of priority) {
    const match = suggestions.find((s) => s.source === p);
    if (match) return match.source;
  }
  return suggestions[0]?.source || '';
}

function formatPricingSummary(p) {
  if (!p) return <span className="dim" style={{ fontSize: 12 }}>—</span>;
  return (
    <span className="mono" style={{ fontSize: 11 }}>
      {fmt(p.input_per_1m_usd)} / {fmt(p.output_per_1m_usd)} / {fmt(p.cached_read_per_1m_usd)}
    </span>
  );
}

function formatSuggestionSummary(s) {
  return (
    <span className="mono" style={{ fontSize: 11 }}>
      {fmt(s.input_per_1m_usd)} / {fmt(s.output_per_1m_usd)} / {fmt(s.cached_read_per_1m_usd)}
    </span>
  );
}

function fmt(v) {
  if (v == null || v === 0) return '0';
  if (v < 0.01) return v.toFixed(4);
  if (v < 1) return v.toFixed(3);
  return v.toFixed(2);
}
