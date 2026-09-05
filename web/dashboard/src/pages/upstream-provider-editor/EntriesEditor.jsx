// ============================================================================
// Upstream provider editor — API key entries editor
// ============================================================================

// APIKeyEntriesEditor — multi-row editor for OpenAI Compatibility and Claude
// (API Key) api_key_entries. Each row carries an optional normalised
// identity (also known as the entry provider key), a masked API key, an
// optional proxy URL (override of the row-level proxy when filled), an
// optional weight for weighted round-robin, and an optional selection tier
// (priority) for fill-first pools. The persisted child-row id is
// round-tripped so the backend can update rows in place rather than
// deleting and reinserting them. Rendered by the routed editor page
// (./index.jsx) via the renderInput field engine.

import React, { useMemo } from 'react';
import { PasswordInput } from '../manage-cpa/FormPrimitives.jsx';
import { validateAPIKeyEntries, idHintForIdentity } from './form.js';

export default function APIKeyEntriesEditor({ entries, onChange, error = '' }) {
  const safe = Array.isArray(entries) ? entries : [];
  function update(idx, patch) {
    onChange(safe.map((e, i) => (i === idx ? { ...e, ...patch } : e)));
  }
  function add() {
    onChange([...safe, { api_key: '', proxy_url: '', name: '', id: 0, weight: '', priority: '' }]);
  }
  function remove(idx) { onChange(safe.filter((_, i) => i !== idx)); }

  // Entry identity rules mirror the backend's normalizeUpstreamProviderEntryName.
  // Errors are surfaced inline, never include the secret, and the raw API key
  // is never used as a label or placeholder.
  const errors = useMemo(() => validateAPIKeyEntries(safe), [safe]);

  function rowKey(e, idx) {
    const id = Number(e && e.id) || 0;
    return id > 0 ? `entry-${id}` : `entry-new-${idx}`;
  }

  return (
    <div className="list-editor">
      {safe.length === 0 && <div className="list-editor__empty">No API key entries. Click "+ Add key".</div>}
      {error && (
        <div className="error-banner" role="alert" style={{ marginTop: 4 }}>{error}</div>
      )}
      {safe.map((e, idx) => {
        const id = Number(e && e.id) || 0;
        const rowErr = errors[idx] || {};
        const hint = id > 0
          ? `Persisted as entry #${id}. ${idHintForIdentity(e)}`
          : 'Blank identity will become key-<id> after save.';
        return (
          <div className="list-editor__rowgroup" key={rowKey(e, idx)}>
            <div className="list-editor__row">
              <input
                type="text"
                value={e.name || ''}
                onChange={(ev) => update(idx, { name: ev.target.value })}
                placeholder="identity (optional, e.g. team-a)"
                spellCheck={false}
                aria-label="API key entry identity"
                aria-invalid={!!rowErr.name}
                data-testid={`api-key-entry-name-${idx}`}
              />
              <PasswordInput
                value={e.api_key}
                onChange={(v) => update(idx, { api_key: v })}
                placeholder="api key"
              />
              <input
                type="text"
                value={e.proxy_url || ''}
                onChange={(ev) => update(idx, { proxy_url: ev.target.value })}
                placeholder="proxy url (optional)"
                spellCheck={false}
                aria-label="API key entry proxy URL"
              />
              <input
                type="text"
                inputMode="numeric"
                value={e.weight ?? ''}
                onChange={(ev) => update(idx, { weight: ev.target.value })}
                placeholder="weight"
                title="Positive integer 1..1000000. Blank = default."
                spellCheck={false}
                aria-label="API key entry weight"
                aria-invalid={!!rowErr.weight}
                data-testid={`api-key-entry-weight-${idx}`}
              />
              <input
                type="text"
                inputMode="numeric"
                value={e.priority ?? ''}
                onChange={(ev) => update(idx, { priority: ev.target.value })}
                placeholder="priority"
                title="Selection tier within this pool. Blank = inherit the row priority. Higher tiers are served first and descend on cooldown."
                spellCheck={false}
                aria-label="API key entry priority"
                aria-invalid={!!rowErr.priority}
                data-testid={`api-key-entry-priority-${idx}`}
              />
              <button
                type="button"
                className="list-editor__remove"
                onClick={() => remove(idx)}
                aria-label="Remove entry"
                title="Remove"
              >×</button>
            </div>
            <div className="list-editor__rowhint muted" style={{ fontSize: 11 }}>
              {hint}
            </div>
            {rowErr.name && (
              <div className="form__error" role="alert">{rowErr.name}</div>
            )}
            {rowErr.weight && (
              <div className="form__error" role="alert">{rowErr.weight}</div>
            )}
            {rowErr.priority && (
              <div className="form__error" role="alert">{rowErr.priority}</div>
            )}
          </div>
        );
      })}
      <button type="button" className="list-editor__add" onClick={add}>+ Add key</button>
    </div>
  );
}
