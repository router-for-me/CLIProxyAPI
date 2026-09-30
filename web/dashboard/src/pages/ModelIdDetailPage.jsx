import React, { useState } from 'react';
import { useParams, Link } from 'react-router-dom';
import { getGlobalModel, getModelProviders, deleteModelEntry } from '../api/client.js';
import { useAsync } from '../hooks/useAsync.js';
import { Spinner, ErrorBanner, EmptyState } from '../components/Primitives.jsx';
import GlobalModelModal from '../components/GlobalModelModal.jsx';

// ModelIdDetailPage — read-only view of one Global Model ID. Lists every
// upstream provider serving the model (catalog rows sharing the id) and the
// canonical attributes/pricing/routing. Attribute/pricing/routing editing is
// delegated to the existing GlobalModelModal via the "Edit" button. The only
// write affordance on this otherwise read-only page is the per-provider Delete
// in the Upstream Providers table (two-step confirm), which removes a single
// (id, provider) catalog row.
export default function ModelIdDetailPage() {
  const { id } = useParams();
  const { data, error, loading, reload } = useAsync(() => getGlobalModel(id), [id]);
  const { data: liveData } = useAsync(() => getModelProviders(id), [id]);
  const [showEdit, setShowEdit] = useState(false);
  const [confirmKey, setConfirmKey] = useState(null);
  const [deleteError, setDeleteError] = useState('');

  async function handleDelete(provider) {
    setDeleteError('');
    try {
      await deleteModelEntry(id, provider);
      setConfirmKey(null);
      reload();
    } catch (err) {
      setDeleteError(err.message || 'Delete failed');
    }
  }

  if (loading) return <Spinner label="Loading model…" />;
  if (error) return <ErrorBanner error={error} onRetry={reload} />;
  if (!data) return <div className="card">Model not found.</div>;

  const canonical = data.canonical || {};
  const rows = data.rows || [];
  const liveKeys = new Set((liveData?.providers || []).map((p) => String(p).toLowerCase()));
  function providerIsLive(r) {
    const prov = String(r.provider || '').toLowerCase();
    const official = String(r.official_provider || '').toLowerCase();
    return liveKeys.has(prov) || liveKeys.has(official)
      || liveKeys.has(prov.split(':')[0]) || liveKeys.has(official.split(':')[0]);
  }

  return (
    <>
      <div className="main__header">
        <div>
          <div className="dim"><Link to="/models">← Models Catalog</Link></div>
          <h1 className="main__title mono">{data.model_id}</h1>
          <div className="main__subtitle">
            {data.live ? 'Live in registry' : 'Not currently in registry'} ·{' '}
            {data.provider_count} upstream provider{data.provider_count === 1 ? '' : 's'}
          </div>
        </div>
        <div className="row gap-sm">
          <button onClick={reload}>Refresh</button>
          <button className="primary" onClick={() => setShowEdit(true)}>Edit</button>
        </div>
      </div>

      <div className="card">
        <h2 className="card__title">Upstream providers</h2>
        {deleteError && <div className="error-banner">{deleteError}</div>}
        {rows.length === 0 ? (
          <EmptyState title="No providers" hint="No catalog rows share this model id." />
        ) : (
          <table className="table">
            <thead>
              <tr>
                <th>Provider</th><th>Official provider</th><th>Display name</th>
                <th>Type</th><th>User-defined</th><th>Updated</th><th></th>
              </tr>
            </thead>
            <tbody>
              {rows.map((r, i) => {
                const rowKey = `${r.provider}|${i}`;
                const live = providerIsLive(r);
                return (
                  <tr key={rowKey}>
                    <td>
                      <span className="row gap-sm">
                        <span
                          className={`live-dot ${live ? 'live-dot--on' : 'live-dot--off'}`}
                          title={live ? 'Live in registry' : 'Not live right now'}
                        />
                        <span className="mono">{r.provider}</span>
                      </span>
                    </td>
                    <td><span className="badge badge--info">{r.official_provider || '—'}</span></td>
                    <td>{r.display_name || '—'}</td>
                    <td>{r.type || '—'}</td>
                    <td>{r.user_defined ? 'yes' : 'no'}</td>
                    <td className="dim">{r.updated_at ? new Date(r.updated_at).toLocaleString() : '—'}</td>
                    <td>
                      <div className="row-actions">
                        {confirmKey === rowKey ? (
                          <>
                            <button
                              className="row-actions__btn row-actions__btn--danger"
                              onClick={() => handleDelete(r.provider)}
                            >Confirm</button>
                            <button
                              className="row-actions__btn"
                              onClick={() => setConfirmKey(null)}
                            >Cancel</button>
                          </>
                        ) : (
                          <button
                            className="row-actions__btn row-actions__btn--danger"
                            onClick={() => setConfirmKey(rowKey)}
                            title="Delete this provider's catalog row for this model id"
                          >Delete</button>
                        )}
                      </div>
                    </td>
                  </tr>
                );
              })}
            </tbody>
          </table>
        )}
        {liveKeys.size > 0 && (
          <div className="form__hint" style={{ marginTop: 8 }}>
            Live routing keys:{' '}
            {Array.from(liveKeys).map((k) => <code key={k} style={{ marginRight: 6 }}>{k}</code>)}
            <div className="dim" style={{ marginTop: 4, fontSize: 11 }}>
              Live keys are registry routing keys; the provider names above are catalog owners.
              Live badges are best-effort.
            </div>
          </div>
        )}
      </div>

      <div className="card">
        <h2 className="card__title">Attributes (canonical)</h2>
        <div className="row-expanded__grid">
          <DetailField label="Display name" value={canonical.display_name || '—'} />
          <DetailField label="Object" value={canonical.object || '—'} />
          <DetailField label="Name" value={canonical.name || '—'} />
          <DetailField label="Version" value={canonical.version || '—'} />
          <DetailField label="Context length" value={fmtInt(canonical.context_length)} />
          <DetailField label="Max completion tokens" value={fmtInt(canonical.max_completion_tokens)} />
          <DetailField label="Input token limit" value={fmtInt(canonical.input_token_limit)} />
          <DetailField label="Output token limit" value={fmtInt(canonical.output_token_limit)} />
          <DetailField label="Supports web search" value={canonical.supports_web_search ? 'yes' : 'no'} />
          <DetailField label="Input modalities" value={(canonical.input_modalities || []).join(', ') || '—'} />
          <DetailField label="Output modalities" value={(canonical.output_modalities || []).join(', ') || '—'} />
          <DetailField label="User-defined" value={canonical.user_defined ? 'yes' : 'no'} />
        </div>
      </div>

      <div className="card">
        <h2 className="card__title">Pricing (global per model id)</h2>
        {data.pricing ? (
          <div className="row-expanded__grid">
            <DetailField label="Input / 1M" value={`$${fmtUSD(data.pricing.input_per_1m_usd)}`} />
            <DetailField label="Output / 1M" value={`$${fmtUSD(data.pricing.output_per_1m_usd)}`} />
            <DetailField label="Cached input / 1M" value={`$${fmtUSD(data.pricing.cached_input_per_1m_usd)}`} />
            <DetailField label="Cached read / 1M" value={`$${fmtUSD(data.pricing.cached_read_per_1m_usd)}`} />
            <DetailField label="Reasoning / 1M" value={`$${fmtUSD(data.pricing.reasoning_per_1m_usd)}`} />
          </div>
        ) : (
          <div className="muted">No pricing set. Use Edit to set pricing for this model.</div>
        )}
      </div>

      <div className="card">
        <h2 className="card__title">Routing (global per model id)</h2>
        {data.routing && Array.isArray(data.routing.providers) && data.routing.providers.length > 0 ? (
          <div>
            <div className="dim" style={{ marginBottom: 6 }}>
              Strategy: {data.routing.strategy || 'default'} · {data.routing.providers.length} pinned provider(s)
            </div>
            <div className="row gap-sm" style={{ flexWrap: 'wrap' }}>
              {data.routing.providers.map((p, i) => (
                <span key={`${p}|${i}`} className="badge badge--muted mono">{p}</span>
              ))}
            </div>
          </div>
        ) : (
          <div className="muted">Default routing (no pinned providers).</div>
        )}
      </div>

      {showEdit && (
        <GlobalModelModal
          modelId={id}
          onClose={() => setShowEdit(false)}
          onSaved={() => { setShowEdit(false); reload(); }}
        />
      )}
    </>
  );
}

function DetailField({ label, value }) {
  return (
    <div className="row-expanded__field">
      <div className="row-expanded__label">{label}</div>
      <div className="row-expanded__value">{value}</div>
    </div>
  );
}

function fmtInt(n) {
  if (n == null || n === '') return '—';
  return Number(n).toLocaleString();
}

function fmtUSD(v) {
  if (v == null || v === 0) return '0';
  if (v < 0.01) return v.toFixed(4);
  if (v < 1) return v.toFixed(3);
  return v.toFixed(2);
}
