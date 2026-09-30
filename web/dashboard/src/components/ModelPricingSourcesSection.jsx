import React, { useState, useMemo } from 'react';
import {
  previewPricingSync, applyPricingSync, listPricingSources,
} from '../api/client.js';
import { useAsync } from '../hooks/useAsync.js';
import { Spinner, ErrorBanner, EmptyState } from './Primitives.jsx';

// ModelPricingSourcesSection — per-source pricing view for one model id.
//
// Lists every pricing source (bundled catalog + operator external sources,
// including disabled ones), showing each source's suggested prices for this
// model (all five components + notes) and whether they differ from the
// model's current pricing. The per-row "Sync" button applies that source's
// values to the model (overwrites model_pricing) via applyPricingSync.
//
// Props:
//   - modelId: the model id to preview.
//   - onApplied(): called after a successful sync so the parent can reload
//     the model (and refresh its Pricing card).
const PRICE_KEYS = [
  'input_per_1m_usd',
  'output_per_1m_usd',
  'cached_input_per_1m_usd',
  'cached_read_per_1m_usd',
  'reasoning_per_1m_usd',
];

const COMPONENTS = [
  { key: 'input_per_1m_usd', label: 'Input' },
  { key: 'output_per_1m_usd', label: 'Output' },
  { key: 'cached_input_per_1m_usd', label: 'Cached input' },
  { key: 'cached_read_per_1m_usd', label: 'Cached read' },
  { key: 'reasoning_per_1m_usd', label: 'Reasoning' },
];

export default function ModelPricingSourcesSection({ modelId, onApplied }) {
  const {
    data: preview, error: previewError, loading: previewLoading, reload: reloadPreview,
  } = useAsync(() => previewPricingSync(modelId), [modelId]);
  const {
    data: sourcesData, error: sourcesError, loading: sourcesLoading, reload: reloadSources,
  } = useAsync(() => listPricingSources(), []);
  const [applying, setApplying] = useState('');
  const [results, setResults] = useState({}); // source name -> { status, message }

  const row = preview?.rows?.[0] || null;
  const current = row?.current_pricing || null;

  const suggestionBySource = useMemo(() => {
    const map = new Map();
    for (const s of row?.suggestions || []) map.set(s.source, s);
    return map;
  }, [row]);

  // Merge: bundled + enabled-external names from the preview's sources list,
  // unioned with every external row (including disabled) from the sources API.
  const sourceRows = useMemo(() => {
    const externalByName = new Map();
    for (const e of sourcesData?.sources || []) externalByName.set(e.name, e);
    const names = new Set();
    for (const s of preview?.sources || []) names.add(s);
    for (const e of sourcesData?.sources || []) names.add(e.name);
    const list = [];
    for (const name of names) {
      const ext = externalByName.get(name);
      list.push({
        name,
        kind: ext ? 'external' : 'bundled',
        enabled: ext ? !!ext.enabled : true,
        entryCount: ext?.entry_count ?? null,
        lastFetchedAt: ext?.last_fetched_at || null,
        lastError: ext?.last_error || '',
        suggestion: suggestionBySource.get(name) || null,
      });
    }
    list.sort((a, b) => a.name.localeCompare(b.name));
    return list;
  }, [preview, sourcesData, suggestionBySource]);

  // Preview is the primary data: only its failure blocks the table. A failed
  // external-sources fetch degrades to a warning (bundled suggestions still
  // render from the preview).
  const loading = previewLoading || sourcesLoading;
  const blockingError = previewError;

  async function handleSync(name) {
    setApplying(name);
    setResults((r) => ({ ...r, [name]: undefined }));
    try {
      const res = await applyPricingSync([{ model_id: modelId, source: name }]);
      const outcome = res?.results?.[0];
      if (outcome && outcome.status !== 'applied') {
        setResults((r) => ({ ...r, [name]: { status: 'error', message: outcome.reason || outcome.status } }));
      } else {
        setResults((r) => ({ ...r, [name]: { status: 'applied', message: 'Applied' } }));
      }
      reloadPreview();
      onApplied?.();
    } catch (err) {
      setResults((r) => ({ ...r, [name]: { status: 'error', message: err.message || 'Sync failed' } }));
    } finally {
      setApplying('');
    }
  }

  return (
    <div className="card">
      <div className="row row--between" style={{ alignItems: 'center' }}>
        <h2 className="card__title" style={{ margin: 0 }}>Pricing sources</h2>
        <button
          className="row-actions__btn"
          onClick={() => { reloadPreview(); reloadSources(); }}
        >Refresh</button>
      </div>
      <p className="form__hint" style={{ margin: '6px 0 12px' }}>
        Published prices per source for this model. Sync applies a source's
        values to this model's pricing (overwrites the current price).
      </p>
      {loading && <Spinner label="Loading pricing sources…" />}
      <ErrorBanner error={blockingError} onRetry={() => { reloadPreview(); reloadSources(); }} />
      {sourcesError && !blockingError && (
        <div className="dim" style={{ fontSize: 12, marginBottom: 8 }}>
          External source metadata unavailable: {sourcesError.message}
        </div>
      )}
      {!loading && !blockingError && sourceRows.length === 0 && (
        <EmptyState
          title="No pricing sources"
          hint="No bundled or external pricing sources are available."
        />
      )}
      {!loading && !blockingError && sourceRows.length > 0 && (
        <table className="table">
          <thead>
            <tr>
              <th>Source</th>
              <th>Status</th>
              <th>Suggested price (USD / 1M)</th>
              <th>vs current</th>
              <th></th>
            </tr>
          </thead>
          <tbody>
            {sourceRows.map((s) => {
              const result = results[s.name];
              return (
                <tr key={s.name}>
                  <td>
                    <div className="mono">{s.name}</div>
                    <span className={`badge ${s.kind === 'external' ? 'badge--info' : 'badge--muted'}`}>
                      {s.kind}
                    </span>
                  </td>
                  <td>
                    {s.kind === 'external' ? (
                      <>
                        <span className={`badge ${s.enabled ? 'badge--active' : 'badge--muted'}`}>
                          {s.enabled ? 'enabled' : 'disabled'}
                        </span>
                        {s.entryCount != null && (
                          <div className="dim" style={{ fontSize: 11 }}>{s.entryCount} entries</div>
                        )}
                        {s.lastFetchedAt && (
                          <div className="dim" style={{ fontSize: 11 }}>
                            {formatRelativeTime(s.lastFetchedAt)}
                          </div>
                        )}
                        {s.lastError && (
                          <div className="error-banner" style={{ marginTop: 4, padding: '4px 8px', fontSize: 11 }}>
                            {s.lastError}
                          </div>
                        )}
                      </>
                    ) : (
                      <span className="badge badge--muted">bundled</span>
                    )}
                  </td>
                  <td>
                    {s.suggestion ? (
                      <div className="pricing-inline mono">
                        {COMPONENTS.map((c) => (
                          <span key={c.key}>{c.label}: ${fmtUSD(s.suggestion[c.key])}/M</span>
                        ))}
                        {s.suggestion.notes && (
                          <span className="dim" style={{ fontSize: 11 }}>{s.suggestion.notes}</span>
                        )}
                      </div>
                    ) : (
                      <span className="pricing-inline pricing-inline--unset">no suggestion</span>
                    )}
                  </td>
                  <td>
                    {!s.suggestion ? (
                      <span className="dim">—</span>
                    ) : !current ? (
                      <span className="badge badge--muted">no current price</span>
                    ) : priceDiffers(s.suggestion, current) ? (
                      <span className="badge badge--warn">differs</span>
                    ) : (
                      <span className="badge badge--active">same</span>
                    )}
                  </td>
                  <td>
                    <div className="row-actions">
                      <button
                        className="row-actions__btn row-actions__btn--primary"
                        disabled={!s.suggestion || !s.enabled || applying === s.name}
                        title={!s.suggestion
                          ? 'This source has no price for this model'
                          : "Apply this source's price to the model"}
                        onClick={() => handleSync(s.name)}
                      >{applying === s.name ? 'Syncing…' : 'Sync'}</button>
                      {result?.status === 'applied' && (
                        <span className="badge badge--active">applied</span>
                      )}
                      {result?.status === 'error' && (
                        <span className="badge badge--warn" title={result.message}>error</span>
                      )}
                    </div>
                  </td>
                </tr>
              );
            })}
          </tbody>
        </table>
      )}
    </div>
  );
}

// priceDiffers reports whether any of the five components differs between the
// suggestion and the model's current pricing (missing current = differs).
function priceDiffers(suggestion, current) {
  if (!current) return true;
  return PRICE_KEYS.some((k) => Number(suggestion[k] || 0) !== Number(current[k] || 0));
}

function fmtUSD(v) {
  if (v == null || v === 0) return '0';
  if (v < 0.01) return v.toFixed(4);
  if (v < 1) return v.toFixed(3);
  return v.toFixed(2);
}

function formatRelativeTime(iso) {
  if (!iso) return '—';
  const t = new Date(iso);
  const ms = Date.now() - t.getTime();
  if (Number.isNaN(ms)) return '—';
  if (ms < 30000) return 'just now';
  const mins = Math.floor(ms / 60000);
  if (mins < 60) return `${mins}m ago`;
  const hours = Math.floor(mins / 60);
  if (hours < 24) return `${hours}h ago`;
  return `${Math.floor(hours / 24)}d ago`;
}
