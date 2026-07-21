import React, { useState, useMemo, useEffect, useCallback, useRef } from 'react';
import {
  listPricingSources, createPricingSource, updatePricingSource,
  deletePricingSource, refreshPricingSource, refreshAllPricingSources,
  uploadPricingSourceFile,
} from '../api/client.js';
import { Modal, Spinner, ErrorBanner } from './Primitives.jsx';

// PricingSourcesModal — manage operator-managed external pricing catalogs
// (LiteLLM-format JSON URLs / uploaded files). Sources are merged into the
// pricingsource registry on refresh and surface as suggestions in the
// Sync Pricing modal alongside the bundled catalog.
//
// Operations:
//   - Add a remote URL or upload a local LiteLLM JSON file.
//   - Toggle enabled (disabled sources drop out of the match index).
//   - Refresh one or all sources on demand.
//   - Delete (also removes the uploaded file from disk).
const EXAMPLE_URL = 'https://raw.githubusercontent.com/abilfida/litellm/refs/heads/litellm_internal_staging/model_prices_and_context_window.json';

export default function PricingSourcesModal({ onClose, onChanged }) {
  const [sources, setSources] = useState(null);
  const [loading, setLoading] = useState(true);
  const [error, setError] = useState('');
  const [busy, setBusy] = useState(null); // id | 'all' | 'create'
  const [creating, setCreating] = useState(false);
  const [draft, setDraft] = useState({ name: '', source_type: 'url', url: EXAMPLE_URL, enabled: true });
  const [uploadFile, setUploadFile] = useState(null);
  const fileInputRef = useRef(null);

  const reload = useCallback(async () => {
    setLoading(true);
    setError('');
    try {
      const data = await listPricingSources();
      setSources(data?.sources || []);
    } catch (err) {
      setError(err.message || 'Failed to load pricing sources');
    } finally {
      setLoading(false);
    }
  }, []);

  useEffect(() => { reload(); }, [reload]);

  async function handleCreate(e) {
    e.preventDefault();
    if (creating) return;
    if (!draft.name.trim()) { setError('Name is required'); return; }
    setCreating(true);
    setError('');
    try {
      // When uploading a file, create the source first with source_type=file
      // but no file_path, then POST the multipart upload.
      const body = {
        name: draft.name.trim(),
        source_type: draft.source_type,
        enabled: !!draft.enabled,
      };
      if (draft.source_type === 'url') {
        body.url = draft.url || '';
      }
      const created = await createPricingSource(body);
      if (draft.source_type === 'file' && uploadFile) {
        await uploadPricingSourceFile(created.id, uploadFile);
      }
      setDraft({ name: '', source_type: 'url', url: EXAMPLE_URL, enabled: true });
      setUploadFile(null);
      if (fileInputRef.current) fileInputRef.current.value = '';
      await reload();
      onChanged?.();
    } catch (err) {
      setError(err.message || 'Create failed');
    } finally {
      setCreating(false);
    }
  }

  async function handleToggle(src) {
    setBusy(src.id);
    setError('');
    try {
      await updatePricingSource(src.id, {
        name: src.name, source_type: src.source_type, url: src.url || '',
        format: src.format || 'litellm', enabled: !src.enabled,
      });
      await reload();
      onChanged?.();
    } catch (err) {
      setError(err.message || 'Toggle failed');
    } finally {
      setBusy(null);
    }
  }

  async function handleRefresh(src) {
    setBusy(src.id);
    setError('');
    try {
      await refreshPricingSource(src.id);
      await reload();
      onChanged?.();
    } catch (err) {
      setError(err.message || 'Refresh failed');
    } finally {
      setBusy(null);
    }
  }

  async function handleRefreshAll() {
    setBusy('all');
    setError('');
    try {
      await refreshAllPricingSources();
      await reload();
      onChanged?.();
    } catch (err) {
      setError(err.message || 'Refresh-all failed');
    } finally {
      setBusy(null);
    }
  }

  async function handleDelete(src) {
    if (!window.confirm(`Delete pricing source "${src.name}"? This removes its suggestions from the catalog.`)) return;
    setBusy(src.id);
    setError('');
    try {
      await deletePricingSource(src.id);
      await reload();
      onChanged?.();
    } catch (err) {
      setError(err.message || 'Delete failed');
    } finally {
      setBusy(null);
    }
  }

  async function handleUploadExisting(src, file) {
    if (!file) return;
    setBusy(src.id);
    setError('');
    try {
      await uploadPricingSourceFile(src.id, file);
      await reload();
      onChanged?.();
    } catch (err) {
      setError(err.message || 'Upload failed');
    } finally {
      setBusy(null);
    }
  }

  const totalEntries = useMemo(() => {
    if (!sources) return 0;
    let n = 0;
    for (const s of sources) n += s.entry_count || 0;
    return n;
  }, [sources]);

  return (
    <Modal title="Pricing Sources" onClose={onClose}>
      {loading && <Spinner label="Loading pricing sources…" />}
      <ErrorBanner error={error} />
      {!loading && sources && (
        <>
          <p className="form__hint" style={{ marginBottom: 12 }}>
            External pricing catalogs (LiteLLM-format JSON). Enabled sources
            contribute pricing suggestions to the <em>Sync Pricing</em> modal,
            filling gaps the bundled catalog does not cover.
          </p>

          <div className="row gap-sm" style={{ marginBottom: 12, flexWrap: 'wrap' }}>
            <button className="primary" onClick={handleRefreshAll} disabled={busy !== null}>
              {busy === 'all' ? 'Refreshing all…' : 'Refresh all'}
            </button>
            <span className="dim" style={{ marginLeft: 'auto', alignSelf: 'center' }}>
              {sources.length} source(s) · {totalEntries} total entries
            </span>
          </div>

          {sources.length === 0 && (
            <p className="muted" style={{ padding: 12, fontSize: 13 }}>
              No pricing sources configured. Add one below to surface
              suggestions from a private LiteLLM fork or uploaded catalog.
            </p>
          )}

          {sources.length > 0 && (
            <div style={{ maxHeight: '40vh', overflowY: 'auto', border: '1px solid var(--border)', borderRadius: 'var(--radius-sm)', marginBottom: 16 }}>
              <table className="table">
                <thead style={{ position: 'sticky', top: 0, background: 'var(--bg-card)' }}>
                  <tr>
                    <th>Name</th>
                    <th>Type</th>
                    <th>Target</th>
                    <th>Entries</th>
                    <th>Last fetch</th>
                    <th></th>
                  </tr>
                </thead>
                <tbody>
                  {sources.map((src) => (
                    <PricingSourceRow
                      key={src.id}
                      src={src}
                      busy={busy === src.id}
                      onToggle={() => handleToggle(src)}
                      onRefresh={() => handleRefresh(src)}
                      onDelete={() => handleDelete(src)}
                      onUpload={(file) => handleUploadExisting(src, file)}
                    />
                  ))}
                </tbody>
              </table>
            </div>
          )}

          {/* Add new source */}
          <form onSubmit={handleCreate} className="card" style={{ padding: 12, background: 'var(--bg)' }}>
            <div className="form__label" style={{ marginBottom: 6 }}>Add pricing source</div>
            <div className="row gap-sm" style={{ flexWrap: 'wrap', marginBottom: 8 }}>
              <input
                type="text" placeholder="name (e.g. litellm-fork)"
                value={draft.name}
                onChange={(e) => setDraft({ ...draft, name: e.target.value })}
                style={{ width: 200 }}
                required
              />
              <select
                value={draft.source_type}
                onChange={(e) => setDraft({ ...draft, source_type: e.target.value })}
                style={{ width: 120 }}
              >
                <option value="url">URL</option>
                <option value="file">Upload</option>
              </select>
              <label className="row gap-sm" style={{ cursor: 'pointer' }}>
                <input
                  type="checkbox"
                  checked={draft.enabled}
                  onChange={(e) => setDraft({ ...draft, enabled: e.target.checked })}
                  style={{ width: 'auto' }}
                />
                <span className="dim" style={{ fontSize: 12 }}>enabled</span>
              </label>
            </div>
            {draft.source_type === 'url' ? (
              <input
                type="url" placeholder="https://…/model_prices_and_context_window.json"
                value={draft.url}
                onChange={(e) => setDraft({ ...draft, url: e.target.value })}
                style={{ width: '100%' }}
              />
            ) : (
              <input
                type="file"
                accept="application/json,.json"
                ref={fileInputRef}
                onChange={(e) => setUploadFile(e.target.files?.[0] || null)}
                style={{ width: '100%' }}
              />
            )}
            <div className="form__actions">
              <button type="button" onClick={onClose}>Close</button>
              <button type="submit" className="primary" disabled={creating}>
                {creating ? 'Adding…' : 'Add source'}
              </button>
            </div>
          </form>
        </>
      )}
    </Modal>
  );
}

function PricingSourceRow({ src, busy, onToggle, onRefresh, onDelete, onUpload }) {
  const [showUpload, setShowUpload] = useState(false);
  return (
    <tr>
      <td>
        <div className="mono">{src.name}</div>
        {src.enabled ? (
          <span className="badge badge--active">enabled</span>
        ) : (
          <span className="badge badge--muted">disabled</span>
        )}
      </td>
      <td className="muted">{src.source_type}</td>
      <td style={{ maxWidth: 280, overflow: 'hidden', textOverflow: 'ellipsis', whiteSpace: 'nowrap' }}>
        {src.source_type === 'url' ? (
          <span className="mono dim" title={src.url}>{src.url || '—'}</span>
        ) : (
          <span className="mono dim" title={src.file_path}>{src.file_path || 'no file'}</span>
        )}
      </td>
      <td className="mono">{src.entry_count || 0}</td>
      <td className="dim" style={{ fontSize: 11 }}>
        {src.last_fetched_at ? formatRelativeTime(src.last_fetched_at) : '—'}
        {src.last_error && (
          <div className="error-banner" style={{ marginTop: 4, padding: '4px 8px', fontSize: 11 }}>
            {src.last_error}
          </div>
        )}
      </td>
      <td>
        <div className="row gap-sm" style={{ justifyContent: 'flex-end', flexWrap: 'wrap' }}>
          <button onClick={onToggle} disabled={busy} style={{ padding: '4px 8px', fontSize: 12 }}>
            {src.enabled ? 'Disable' : 'Enable'}
          </button>
          <button onClick={onRefresh} disabled={busy || !src.enabled} style={{ padding: '4px 8px', fontSize: 12 }}>
            {busy ? '…' : 'Refresh'}
          </button>
          {src.source_type === 'file' && (
            <button onClick={() => setShowUpload((v) => !v)} disabled={busy} style={{ padding: '4px 8px', fontSize: 12 }}>
              Replace
            </button>
          )}
          <button onClick={onDelete} disabled={busy} style={{ padding: '4px 8px', fontSize: 12 }}>Delete</button>
          {showUpload && src.source_type === 'file' && (
            <input
              type="file" accept="application/json,.json"
              onChange={(e) => { onUpload(e.target.files?.[0]); setShowUpload(false); }}
              style={{ fontSize: 11 }}
            />
          )}
        </div>
      </td>
    </tr>
  );
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
