// FetchModelsModal — discover models for a provider, let the operator
// pick a subset, and apply the selection to a specific config entry.
//
// Two fetch modes, decided by `mode`:
//   - 'auth'    (default): GET /v0/management/auth-files/models?name=<id>
//     — the registry's view of what the upstream currently serves for
//     the chosen auth file. Used for OAuth/static-key providers
//     (gemini, claude, codex, xai, vertex, interactions).
//   - 'openai': caller-side GET <base_url>/v1/models with a bearer
//     token the operator pastes in (or re-uses from localStorage).
//     Used for OpenAI-Compat entries where the proxy has no live
//     auth to probe with.
//
// Apply always goes through the per-provider PATCH or openai-compat PUT
// helper in client.js so the server's sanitize + persist + reload path
// stays the single source of truth.

import React, { useEffect, useMemo, useState } from 'react';
import { Modal } from '../../components/Primitives.jsx';
import { Spinner, ErrorBanner } from '../../components/Primitives.jsx';
import {
  fetchProviderModelsFromAuth,
  fetchOpenAICompatModels,
  applyProviderModels,
  getStoredCallerKey,
} from '../../api/client.js';

// Apply scope — which config entry the selection will be written to.
//   - 'entry-index' for OAuth/static-key providers (each <ProviderKeyCard>.
//     the operator is editing the models for one specific entry, identified
//     by its row index).
//   - 'entry-name'  for OpenAI-Compat (multi-entry; the operator picks
//     which named entry gets the new models).
export default function FetchModelsModal({
  kind,
  // Provider id (one of PROVIDER_KEY_KINDS[].id) — needed so apply() knows
  // which endpoint to call.
  provider,
  // Mode switch.
  mode = 'auth',
  // 'auth' mode: the auth-file id/name to query the registry with.
  authName = '',
  // 'openai' mode: the entry's base URL.
  baseUrl = '',
  // 'openai' mode + 'entry-name' scope: the entry's `name` to apply to.
  entryName = '',
  // 'entry-index' scope: row index to apply to.
  entryIndex = 0,
  // Optional list of existing names (for OpenAI-Compat).
  siblingNames = [],
  onClose,
  onApplied,
}) {
  const isOpenai = mode === 'openai';

  const [stage, setStage] = useState('fetching'); // fetching | ready | error | applying
  const [error, setError] = useState('');
  const [fetched, setFetched] = useState([]);
  const [selected, setSelected] = useState({});
  const [filter, setFilter] = useState('');

  // OpenAI-Compat-only: a bearer token the operator can paste for the
  // remote probe. We pre-fill with the stored caller key (used elsewhere
  // for /v1/models sync) so the modal works out of the box for operators
  // who already configured one.
  const [callerKey, setCallerKey] = useState(() => getStoredCallerKey() || '');
  const [rememberKey, setRememberKey] = useState(false);

  useEffect(() => {
    let cancelled = false;
    setStage('fetching');
    setError('');
    setFetched([]);
    setSelected({});

    const runner = isOpenai
      ? fetchOpenAICompatModels({ baseUrl, apiKey: callerKey }).catch((err) => {
          throw err;
        })
      : fetchProviderModelsFromAuth(authName);

    runner
      .then((list) => {
        if (cancelled) return;
        setFetched(Array.isArray(list) ? list : []);
        setStage('ready');
      })
      .catch((err) => {
        if (cancelled) return;
        setError(err.message || 'Failed to fetch models.');
        setStage('error');
      });
    return () => { cancelled = true; };
    // Re-run when the operator changes the caller key (debounced via button).
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [mode, authName, baseUrl]);

  const filtered = useMemo(() => {
    if (!filter) return fetched;
    const q = filter.toLowerCase();
    return fetched.filter((m) =>
      [m.id, m.display_name, m.owned_by, m.type]
        .filter(Boolean)
        .some((s) => String(s).toLowerCase().includes(q)),
    );
  }, [fetched, filter]);

  const allFilteredSelected = filtered.length > 0 && filtered.every((m) => selected[m.id]);
  const selectedCount = Object.values(selected).filter(Boolean).length;

  function toggleAll() {
    const next = { ...selected };
    if (allFilteredSelected) {
      for (const m of filtered) delete next[m.id];
    } else {
      for (const m of filtered) next[m.id] = true;
    }
    setSelected(next);
  }
  function toggleOne(id) {
    setSelected((s) => ({ ...s, [id]: !s[id] }));
  }

  async function handleApply() {
    const chosen = fetched.filter((m) => selected[m.id]);
    if (chosen.length === 0) {
      setError('Select at least one model before applying.');
      return;
    }
    setStage('applying');
    setError('');
    try {
      await applyProviderModels(provider, {
        index: entryIndex,
        name: entryName,
        models: chosen,
      });
      if (rememberKey && callerKey) {
        // Reuse the same storage helper the models-catalog sync uses.
        try {
          localStorage.setItem('nixllm.dashboard.callerKey', callerKey);
        } catch { /* ignore */ }
      }
      onApplied(chosen.length);
    } catch (err) {
      setError(err.message || 'Apply failed.');
      setStage('ready');
    }
  }

  function retry() {
    // Force a re-fetch by toggling stage.
    setStage('fetching');
    setError('');
    if (isOpenai) {
      fetchOpenAICompatModels({ baseUrl, apiKey: callerKey })
        .then((list) => {
          setFetched(Array.isArray(list) ? list : []);
          setStage('ready');
        })
        .catch((err) => {
          setError(err.message || 'Failed to fetch models.');
          setStage('error');
        });
    } else {
      fetchProviderModelsFromAuth(authName)
        .then((list) => {
          setFetched(Array.isArray(list) ? list : []);
          setStage('ready');
        })
        .catch((err) => {
          setError(err.message || 'Failed to fetch models.');
          setStage('error');
        });
    }
  }

  const title = isOpenai
    ? `Fetch models — ${entryName || 'OpenAI-Compat'}`
    : `Fetch models — ${authName}`;

  const footer = (
    <>
      <span className="dim" style={{ marginRight: 'auto', fontSize: 12 }}>
        {selectedCount > 0
          ? `${selectedCount} of ${fetched.length} selected`
          : fetched.length > 0
            ? `${fetched.length} discovered`
            : ''}
      </span>
      <button onClick={onClose} disabled={stage === 'applying'}>Cancel</button>
      <button
        className="primary"
        onClick={handleApply}
        disabled={stage === 'applying' || selectedCount === 0}
      >
        {stage === 'applying'
          ? 'Applying…'
          : `Apply ${selectedCount > 0 ? `(${selectedCount})` : 'selected'}`}
      </button>
    </>
  );

  return (
    <Modal title={title} size="lg" onClose={onClose} footer={footer}>
      {isOpenai && (
        <div className="form-section" style={{ marginBottom: 12 }}>
          <div className="form-section__title">Remote probe</div>
          <div className="form-section__hint">
            Calls <code className="mono">{normalizeForHint(baseUrl)}/v1/models</code> with
            the bearer token below. Some providers reject the call without auth.
          </div>
          <div className="form-section__row">
            <div className="form__row">
              <label className="form__label">Bearer token (optional)</label>
              <input
                type="password"
                value={callerKey}
                onChange={(e) => setCallerKey(e.target.value)}
                placeholder="sk-… (leave empty to probe anonymously)"
                spellCheck={false}
              />
              <label className="row gap-sm" style={{ marginTop: 6, fontSize: 11, color: 'var(--text-dim)' }}>
                <input
                  type="checkbox"
                  checked={rememberKey}
                  onChange={(e) => setRememberKey(e.target.checked)}
                  style={{ width: 'auto' }}
                />
                <span>Remember this token for future fetches (browser localStorage).</span>
              </label>
            </div>
            <div className="form__row" style={{ alignSelf: 'end' }}>
              <button onClick={retry} disabled={stage === 'fetching' || stage === 'applying'}>
                Refetch
              </button>
            </div>
          </div>
        </div>
      )}

      {error && <div className="error-banner">{error}</div>}

      {stage === 'fetching' && (
        <Spinner label={isOpenai ? `Probing ${normalizeForHint(baseUrl)}/v1/models…` : `Querying registry for ${authName}…`} />
      )}

      {stage === 'error' && (
        <div className="dim" style={{ textAlign: 'center', padding: 16 }}>
          Could not fetch models. Adjust the inputs above and click Refetch, or
          check the dashboard's error log.
        </div>
      )}

      {stage !== 'fetching' && fetched.length > 0 && (
        <>
          <div className="row gap-sm" style={{ marginBottom: 8 }}>
            <input
              type="text"
              value={filter}
              onChange={(e) => setFilter(e.target.value)}
              placeholder="Filter by id, owner, type…"
              style={{ flex: 1 }}
            />
            <button onClick={toggleAll}>
              {allFilteredSelected ? 'Clear filter' : 'Select all visible'}
            </button>
          </div>

          <div className="list-editor" style={{ maxHeight: 420, overflowY: 'auto' }}>
            {filtered.length === 0 && (
              <div className="list-editor__empty">No models match "{filter}".</div>
            )}
            {filtered.map((m) => {
              const checked = !!selected[m.id];
              return (
                <label
                  key={m.id}
                  className="toggle-row"
                  style={{ cursor: 'pointer', borderBottom: '1px solid var(--border)' }}
                >
                  <div style={{ minWidth: 0, flex: 1 }}>
                    <div className="toggle-row__label mono" style={{ overflow: 'hidden', textOverflow: 'ellipsis' }}>
                      {m.id}
                    </div>
                    <div className="toggle-row__hint" style={{ display: 'flex', gap: 8, flexWrap: 'wrap', marginTop: 2 }}>
                      {m.owned_by && <span className="dim">owned_by: {m.owned_by}</span>}
                      {m.type && <span className="dim">type: {m.type}</span>}
                      {m.context_length ? <span className="dim">ctx: {m.context_length.toLocaleString()}</span> : null}
                      {m.display_name ? <span className="dim">{m.display_name}</span> : null}
                    </div>
                  </div>
                  <span className="toggle-switch">
                    <input
                      type="checkbox"
                      checked={checked}
                      onChange={() => toggleOne(m.id)}
                      aria-label={`Select ${m.id}`}
                    />
                    <span className="toggle-switch__slider" />
                  </span>
                </label>
              );
            })}
          </div>
        </>
      )}

      {stage === 'ready' && fetched.length === 0 && !error && (
        <div className="dim" style={{ textAlign: 'center', padding: 16 }}>
          The upstream returned no models. The provider may be unavailable or
          the bearer token is wrong.
        </div>
      )}

      {stage === 'applying' && (
        <div className="dim" style={{ textAlign: 'center', padding: 12 }}>
          Writing to <code className="mono">{kind?.field || provider}</code>…
        </div>
      )}
    </Modal>
  );
}

function normalizeForHint(url) {
  if (!url) return '';
  return String(url).trim().replace(/\/+$/, '');
}
