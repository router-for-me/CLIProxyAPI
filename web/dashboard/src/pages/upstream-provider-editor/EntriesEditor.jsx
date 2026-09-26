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
import { formatRelativeTime } from '../../utils/formatRelativeTime.js';

export default function APIKeyEntriesEditor({ entries, onChange, error = '', proxyPools = [] }) {
  const safe = Array.isArray(entries) ? entries : [];
  const pools = Array.isArray(proxyPools) ? proxyPools : [];
  function update(idx, patch) {
    onChange(safe.map((e, i) => (i === idx ? { ...e, ...patch } : e)));
  }
  function add() {
    onChange([...safe, { api_key: '', proxy_url: '', proxy_pool_id: '', name: '', id: 0, weight: '', priority: '', disabled: false, max_concurrent: '', max_wait_ms: '', auto_disabled: false, auto_disabled_at: '', auto_disabled_reason: '' }]);
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
        const isOff = !!e.disabled;
        const autoOff = !!(e && e.auto_disabled);
        const hint = id > 0
          ? `Persisted as entry #${id}. ${idHintForIdentity(e)}`
          : 'Blank identity will become key-<id> after save.';
        return (
          <div className={`list-editor__rowgroup${(isOff && !autoOff) || autoOff ? ' list-editor__rowgroup--disabled' : ''}`} key={rowKey(e, idx)}>
            <div className="list-editor__row">
              <label
                className="toggle-switch entry-toggle"
                title={autoOff
                  ? 'Entry auto-disabled by the server on a matched upstream error — use Re-enable to bring it back'
                  : isOff ? 'Entry is disabled — excluded from routing' : 'Entry is active'}
                data-testid={`api-key-entry-disabled-${idx}`}
              >
                <input
                  type="checkbox"
                  checked={isOff}
                  onChange={(ev) => update(idx, { disabled: ev.target.checked })}
                  aria-label="Disable entry"
                />
                <span className="toggle-switch__slider" />
              </label>
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
              <select
                value={e.proxy_pool_id ? String(e.proxy_pool_id) : ''}
                onChange={(ev) => {
                  const v = ev.target.value;
                  // Mutually exclusive: picking a pool clears the manual URL;
                  // "none" pins explicit direct egress; inherit keeps both empty.
                  update(idx, v === '' || v === 'none'
                    ? { proxy_pool_id: '', proxy_url: v === 'none' ? 'none' : '' }
                    : { proxy_pool_id: Number(v), proxy_url: '' });
                }}
                aria-label="API key entry proxy pool"
                data-testid={`api-key-entry-proxy-pool-${idx}`}
              >
                <option value="">pool: inherit row</option>
                {pools.filter((p) => p.is_active).map((p) => (
                  <option key={p.id} value={String(p.id)}>{p.name}</option>
                ))}
                <option value="none">direct (no proxy)</option>
              </select>
              <input
                type="text"
                value={e.proxy_pool_id ? '' : (e.proxy_url || '')}
                onChange={(ev) => update(idx, { proxy_url: ev.target.value, proxy_pool_id: ev.target.value ? '' : e.proxy_pool_id })}
                placeholder="proxy url (optional)"
                spellCheck={false}
                aria-label="API key entry proxy URL"
                disabled={!!e.proxy_pool_id}
                title={e.proxy_pool_id ? `Bound to proxy pool #${e.proxy_pool_id} — clear the picker to edit the manual URL` : undefined}
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
              <input
                type="text"
                inputMode="numeric"
                value={e.max_concurrent ?? ''}
                onChange={(ev) => update(idx, { max_concurrent: ev.target.value })}
                placeholder="max concurrent"
                title="In-flight request cap for this entry. Blank / 0 = unlimited."
                spellCheck={false}
                aria-label="API key entry max concurrent"
                aria-invalid={!!rowErr.max_concurrent}
                data-testid={`api-key-entry-max-concurrent-${idx}`}
              />
              <input
                type="text"
                inputMode="numeric"
                value={e.max_wait_ms ?? ''}
                onChange={(ev) => update(idx, { max_wait_ms: ev.target.value })}
                placeholder="max wait ms"
                title="Wait budget in milliseconds before an eligible-but-full entry fails over. Blank = default (200ms)."
                spellCheck={false}
                aria-label="API key entry max wait ms"
                aria-invalid={!!rowErr.max_wait_ms}
                data-testid={`api-key-entry-max-wait-ms-${idx}`}
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
              {isOff && !autoOff && (
                <span className="badge badge--disabled" style={{ marginLeft: 6, fontSize: 10 }}>disabled</span>
              )}
              {autoOff && (
                <AutoDisabledBadge reason={e.auto_disabled_reason} at={e.auto_disabled_at} />
              )}
            </div>
            {autoOff && (
              <div className="list-editor__rowhint" style={{ marginTop: 2 }}>
                <button
                  type="button"
                  className="list-editor__reenable"
                  onClick={() => update(idx, {
                    auto_disabled: false,
                    auto_disabled_at: '',
                    auto_disabled_reason: '',
                    disabled: false,
                  })}
                  title="Clear auto-disabled state. Click Save to persist."
                >
                  Re-enable
                </button>
              </div>
            )}
            {rowErr.name && (
              <div className="form__error" role="alert">{rowErr.name}</div>
            )}
            {rowErr.weight && (
              <div className="form__error" role="alert">{rowErr.weight}</div>
            )}
            {rowErr.priority && (
              <div className="form__error" role="alert">{rowErr.priority}</div>
            )}
            {rowErr.max_concurrent && (
              <div className="form__error" role="alert">{rowErr.max_concurrent}</div>
            )}
            {rowErr.max_wait_ms && (
              <div className="form__error" role="alert">{rowErr.max_wait_ms}</div>
            )}
          </div>
        );
      })}
      <button type="button" className="list-editor__add" onClick={add}>+ Add key</button>
    </div>
  );
}

// AutoDisabledBadge renders the runtime auto-disabled state for an entry:
// the reason (e.g. "401") and, when known, how long ago the entry was
// disabled. Uses the app's shared relative-time util for consistency with
// the rest of the dashboard.
function AutoDisabledBadge({ reason, at }) {
  const when = at ? formatRelativeTime(at) : '';
  const summary = reason
    ? `Auto-disabled${when ? ` ${when}` : ''}${reason ? ` — reason ${reason}` : ''}`
    : `Auto-disabled${when ? ` ${when}` : ''}`;
  return (
    <span
      className="badge badge--disabled auto-disabled-badge"
      style={{ marginLeft: 6, fontSize: 10 }}
      title="Automatically disabled by the server after a matched upstream error. Re-enable to bring the entry back."
      aria-label={summary}
    >
      auto-disabled {reason ? `(${reason})` : ''}{when ? ` · ${when}` : ''}
    </span>
  );
}
