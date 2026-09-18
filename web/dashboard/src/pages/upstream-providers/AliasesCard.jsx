// AliasesCard — extracted from UpstreamProvidersPage.jsx (Task 17).
//
// Owns the "Global OAuth Model Aliases" card (per-channel rows editor that
// writes the top-level `oauth-model-alias` block in config.yaml). Self-
// contained: local state for the per-channel rows, the in-editor save /
// remove / add-channel handlers, and the static channel list + sanitize
// helpers. The page-level wiring (where this card sits in the layout) stays
// in UpstreamProvidersPage.jsx.

import React, { useState, useRef, useEffect, useCallback } from 'react';
import { Spinner, ErrorBanner, EmptyState } from '../../components/Primitives.jsx';
import { useToast } from '../../components/Toast.jsx';
import {
  getOAuthModelAlias,
  patchOAuthModelAlias,
  deleteOAuthModelAlias,
  getModelDefinitions,
} from '../../api/client.js';

// ============================================================================
// Channels
// ============================================================================

// Channels the global oauth-model-alias config block supports. The backend
// sanitizer accepts arbitrary lowercased keys, but the runtime only honors
// these documented channels.
const OAUTH_ALIAS_CHANNELS = [
  { value: 'claude', label: 'Claude' },
  { value: 'codex', label: 'Codex' },
  { value: 'kimi', label: 'Kimi' },
  { value: 'xai', label: 'xAI' },
  { value: 'vertex', label: 'Vertex' },
  { value: 'aistudio', label: 'AI Studio' },
  { value: 'antigravity', label: 'Antigravity' },
];

// ============================================================================
// Normalization helpers
// ============================================================================

// Normalizes a fetched global alias map into a stable shape the editor uses.
// API returns { "oauth-model-alias": { <channel>: [{name,alias,fork,display-name,force-mapping}] } }.
function normalizeAliasMap(raw) {
  const map = (raw && raw['oauth-model-alias']) || {};
  const out = {};
  Object.entries(map).forEach(([channel, aliases]) => {
    out[String(channel).toLowerCase()] = (Array.isArray(aliases) ? aliases : []).map((a) => ({
      'name': a.name || '',
      'alias': a.alias || '',
      'fork': !!a.fork,
      'display-name': a['display-name'] || a.displayName || '',
      'force-mapping': !!a['force-mapping'] || !!a.forceMapping,
    }));
  });
  return out;
}

// Serializes an editor channel's rows back into the API's kebab-case shape
// (dropping empty rows so the global map stays clean).
function serializeAliasRows(rows) {
  return (rows || [])
    .filter((r) => r && (r.name || r.alias))
    .map((r) => {
      const entry = {
        name: (r.name || '').trim(),
        alias: (r.alias || '').trim(),
      };
      if (r['display-name']) entry['display-name'] = (r['display-name'] || '').trim();
      if (r['fork']) entry['fork'] = true;
      if (r['force-mapping']) entry['force-mapping'] = true;
      return entry;
    });
}

// ============================================================================
// Main component
// ============================================================================

export function AliasesCard() {
  const toast = useToast();
  const [channels, setChannels] = useState({});
  const [loading, setLoading] = useState(true);
  const [savingChannel, setSavingChannel] = useState('');
  const [error, setError] = useState('');
  // Tracks channels that were just added by the operator (not loaded from the
  // server). These mount expanded so the empty editor is immediately visible,
  // while all other channels default to collapsed for a scannable list.
  const newlyAddedRef = useRef(new Set());

  const reload = useCallback(async () => {
    setLoading(true);
    setError('');
    try {
      const payload = await getOAuthModelAlias();
      setChannels(normalizeAliasMap(payload));
    } catch (err) {
      setError(err.message || 'Failed to load global aliases');
    } finally {
      setLoading(false);
    }
  }, []);

  useEffect(() => { reload(); }, [reload]);

  const updateRows = (channel, rows) => {
    setChannels((prev) => ({ ...prev, [channel]: rows }));
  };

  const addChannel = () => {
    // Find the first channel not yet present in the map and seed it with an
    // empty row so the editor reveals its rows immediately.
    const next = OAUTH_ALIAS_CHANNELS.find((c) => !channels[c.value]);
    if (!next) return;
    newlyAddedRef.current.add(next.value);
    setChannels((prev) => ({ ...prev, [next.value]: [{ name: '', alias: '' }] }));
  };

  const saveChannel = async (channel) => {
    setSavingChannel(channel);
    try {
      const aliases = serializeAliasRows(channels[channel] || []);
      await patchOAuthModelAlias(channel, aliases);
      toast.success(`Saved global aliases for ${channel}`);
      // Reload to pick up the server-sanitized view (empty channels are deleted).
      await reload();
    } catch (err) {
      toast.error(err.message || `Failed to save ${channel}`);
    } finally {
      setSavingChannel('');
    }
  };

  const removeChannel = async (channel) => {
    setSavingChannel(channel);
    try {
      await deleteOAuthModelAlias(channel);
      setChannels((prev) => {
        const next = { ...prev };
        delete next[channel];
        return next;
      });
      toast.success(`Removed ${channel} from global aliases`);
    } catch (err) {
      if (String(err.status) === '404') {
        // Already gone — drop locally.
        setChannels((prev) => {
          const next = { ...prev };
          delete next[channel];
          return next;
        });
      } else {
        toast.error(err.message || `Failed to remove ${channel}`);
      }
    } finally {
      setSavingChannel('');
    }
  };

  const presentChannels = Object.keys(channels).sort();
  const remainingAddable = OAUTH_ALIAS_CHANNELS.filter((c) => !channels[c.value]);
  const channelLabel = (c) => OAUTH_ALIAS_CHANNELS.find((x) => x.value === c)?.label || c;

  return (
    <div className="card" style={{ marginTop: 16 }}>
      <div className="main__header" style={{ marginBottom: 8 }}>
        <div>
          <h2 className="main__title" style={{ fontSize: 18 }}>Global OAuth Model Aliases</h2>
          <div className="main__subtitle">
            Per-channel model alias mappings written to the top-level{' '}
            <code>oauth-model-alias</code> block in <code>config.yaml</code>.
            These apply to every OAuth/file-backed account of that channel as a
            fallback; per-account aliases (edited above) override these.
          </div>
        </div>
        <div className="row gap-sm">
          <button onClick={reload} disabled={loading}>Refresh</button>
        </div>
      </div>

      {loading ? (
        <Spinner label="Loading global aliases…" />
      ) : error ? (
        <ErrorBanner error={error} onRetry={reload} />
      ) : (
        <>
          {presentChannels.length === 0 && remainingAddable.length === 0 && (
            <EmptyState title="No channels"
              hint="All defined channels are configured (or none are applicable)." />
          )}
          {presentChannels.length === 0 && remainingAddable.length > 0 && (
            <EmptyState title="No global aliases yet"
              hint="Add a channel to start mapping client aliases to OAuth upstream models." />
          )}

          {presentChannels.map((channel) => {
            const isNew = newlyAddedRef.current.has(channel);
            // Consume the "newly added" flag after first render so a later
            // reload re-collapses the channel (matching loaded-channel behavior).
            if (isNew) {
              queueMicrotask(() => { newlyAddedRef.current.delete(channel); });
            }
            return (
              <ChannelAliasEditor
                key={channel}
                channel={channel}
                label={channelLabel(channel)}
                rows={channels[channel] || []}
                onChange={(rows) => updateRows(channel, rows)}
                onSave={() => saveChannel(channel)}
                onRemove={() => removeChannel(channel)}
                saving={savingChannel === channel}
                initiallyExpanded={isNew}
              />
            );
          })}

          {remainingAddable.length > 0 && (
            <div className="row gap-sm" style={{ marginTop: 12 }}>
              <label style={{ fontSize: 12 }} className="dim">Add channel:</label>
              <select
                id="oauth_alias_add_channel"
                defaultValue=""
                onChange={(e) => {
                  const v = e.target.value;
                  if (!v) return;
                  newlyAddedRef.current.add(v);
                  setChannels((prev) => ({ ...prev, [v]: [{ name: '', alias: '' }] }));
                  e.target.value = '';
                }}
                aria-label="Add OAuth alias channel"
              >
                <option value="">Select a channel…</option>
                {remainingAddable.map((c) => (
                  <option key={c.value} value={c.value}>{c.label}</option>
                ))}
              </select>
            </div>
          )}
        </>
      )}
    </div>
  );
}

// ============================================================================
// Per-channel editor row block
// ============================================================================

function ChannelAliasEditor({ channel, label, rows, onChange, onSave, onRemove, saving, initiallyExpanded = false }) {
  const [confirmRemove, setConfirmRemove] = useState(false);
  const [modelOptions, setModelOptions] = useState([]);
  const [modelsLoading, setModelsLoading] = useState(false);
  const [modelsError, setModelsError] = useState('');
  // Channels default to collapsed so a long channel list stays scannable. Only
  // channels the operator just added (initiallyExpanded=true) start open so
  // the empty editor is immediately visible and ready to edit.
  const [collapsed, setCollapsed] = useState(!initiallyExpanded);

  // Fetch the static default model catalog for this OAuth channel once.
  // These IDs populate the "upstream model" dropdown alongside the free-text
  // input, so the operator can either pick a known model or type a custom one
  // (e.g. an internal/off-catalog id).
  useEffect(() => {
    let cancelled = false;
    setModelsError('');
    if (!channel) return;
    setModelsLoading(true);
    getModelDefinitions(channel)
      .then((payload) => {
        if (cancelled) return;
        const models = (payload && payload.models) || [];
        setModelOptions(models.map((m) => ({
          id: m.id || '',
          display: m.display_name || m.id || m.name || '',
        })).filter((m) => m.id));
      })
      .catch((err) => {
        if (cancelled) return;
        setModelsError(err.message || 'Failed to load default models');
      })
      .finally(() => { if (!cancelled) setModelsLoading(false); });
    return () => { cancelled = true; };
  }, [channel]);

  const update = (idx, patch) => {
    const next = rows.map((r, i) => (i === idx ? { ...r, ...patch } : r));
    onChange(next);
  };
  const addRow = () => onChange([...(rows || []), { name: '', alias: '' }]);
  const removeRow = (idx) => {
    if ((rows || []).length <= 1) {
      onChange([{ name: '', alias: '' }]);
      return;
    }
    onChange(rows.filter((_, i) => i !== idx));
  };
  const autoAddOnLastRow = (idx) => {
    if (idx === (rows || []).length - 1 && (rows[idx].name || rows[idx].alias)) {
      addRow();
    }
  };

  const aliasCount = (rows || []).filter((r) => r && (r.name || r.alias)).length;

  return (
    <div
      className="list-editor"
      style={{
        marginBottom: 16,
        paddingTop: 10,
        paddingBottom: collapsed ? 10 : 12,
        borderBottom: '1px solid var(--border)',
      }}
    >
      <div className="row gap-sm" style={{ justifyContent: 'space-between', marginBottom: collapsed ? 0 : 8, alignItems: 'center' }}>
        <button
          type="button"
          className="row gap-sm"
          onClick={() => setCollapsed((v) => !v)}
          aria-label={collapsed ? 'Expand channel' : 'Collapse channel'}
          aria-expanded={!collapsed}
          title={collapsed ? 'Expand' : 'Collapse'}
          style={{ background: 'none', border: 'none', padding: 0, cursor: 'pointer', alignItems: 'center', color: 'inherit' }}
        >
          <span style={{ display: 'inline-block', transition: 'transform 0.15s', transform: collapsed ? 'rotate(-90deg)' : 'rotate(0deg)', fontSize: 12 }} aria-hidden="true">▼</span>
          <span className="cell-stack">
            <span className="cell-stack__main" style={{ fontWeight: 600 }}>{label}</span>
            <span className="badge badge--muted" style={{ fontSize: 10 }}>{channel}</span>
          </span>
          <span className="dim" style={{ fontSize: 11 }}>
            {aliasCount === 0 ? 'no aliases' : `${aliasCount} alias${aliasCount === 1 ? '' : 'es'}`}
          </span>
        </button>
        <div className="row gap-sm">
          <button onClick={onSave} disabled={saving}>Save</button>
          {confirmRemove ? (
            <>
              <button onClick={() => setConfirmRemove(false)}>Cancel</button>
              <button className="danger" onClick={onRemove} disabled={saving}>Confirm delete</button>
            </>
          ) : (
            <button onClick={() => setConfirmRemove(true)} disabled={saving}>Remove channel</button>
          )}
        </div>
      </div>

      {!collapsed && (
        <>
          {modelsError && (
            <div className="dim" style={{ fontSize: 11, marginBottom: 6 }}>
              Default models unavailable: {modelsError}. You can still type a model id manually.
            </div>
          )}
          {modelsLoading && (
            <div className="dim" style={{ fontSize: 11, marginBottom: 6 }}>Loading default models…</div>
          )}

          {(rows || []).length === 0 && (
            <div className="list-editor__empty">No aliases. Click “Add alias”.</div>
          )}
          {(rows || []).map((row, idx) => (
            <div key={idx} style={{ display: 'flex', flexDirection: 'column', gap: 4 }}>
              <div className="list-editor__row">
                {/* Mixed input: free text + dropdown of the channel's default models. */}
                <input
                  list={`oauth_alias_models_${channel}`}
                  type="text"
                  value={row.name || ''}
                  onChange={(e) => update(idx, { name: e.target.value })}
                  onBlur={() => autoAddOnLastRow(idx)}
                  placeholder="upstream model (e.g. gpt-5.3-codex-spark)"
                  spellCheck={false}
                  aria-label="Upstream model"
                  style={{ flex: 1 }}
                />
                <datalist id={`oauth_alias_models_${channel}`}>
                  {modelOptions.map((m) => (
                    <option key={m.id} value={m.id}>{m.display !== m.id ? m.display : ''}</option>
                  ))}
                </datalist>
                <input
                  type="text"
                  value={row.alias || ''}
                  onChange={(e) => update(idx, { alias: e.target.value })}
                  onBlur={() => autoAddOnLastRow(idx)}
                  placeholder="client alias (e.g. gpt-5.5)"
                  spellCheck={false}
                  aria-label="Client alias"
                  style={{ flex: 1 }}
                />
                <button
                  type="button"
                  className="list-editor__remove"
                  onClick={() => removeRow(idx)}
                  aria-label="Remove alias"
                  title="Remove"
                >
                  ×
                </button>
              </div>
              <div className="list-editor__row" style={{ paddingLeft: 0 }}>
                <input
                  type="text"
                  value={row['display-name'] || ''}
                  onChange={(e) => update(idx, { 'display-name': e.target.value })}
                  placeholder="display name (optional)"
                  spellCheck={false}
                  aria-label="Display name"
                  style={{ flex: 1 }}
                />
                <label className="toggle-row" style={{ flex: '0 0 auto', padding: '4px 8px', border: '1px solid var(--border)', borderRadius: 'var(--radius-sm)' }}>
                  <span className="toggle-row__label" style={{ fontSize: 11 }}>fork</span>
                  <span className="toggle-switch">
                    <input
                      type="checkbox"
                      checked={!!row['fork']}
                      onChange={(e) => update(idx, { 'fork': e.target.checked })}
                      aria-label="Fork alias"
                    />
                    <span className="toggle-switch__slider" />
                  </span>
                </label>
                <label className="toggle-row" style={{ flex: '0 0 auto', padding: '4px 8px', border: '1px solid var(--border)', borderRadius: 'var(--radius-sm)' }}>
                  <span className="toggle-row__label" style={{ fontSize: 11 }}>force-mapping</span>
                  <span className="toggle-switch">
                    <input
                      type="checkbox"
                      checked={!!row['force-mapping']}
                      onChange={(e) => update(idx, { 'force-mapping': e.target.checked })}
                      aria-label="Force mapping"
                    />
                    <span className="toggle-switch__slider" />
                  </span>
                </label>
              </div>
            </div>
          ))}
          <button type="button" className="list-editor__add" onClick={addRow}>+ Add alias</button>
        </>
      )}
    </div>
  );
}

export default AliasesCard;
