// FetchModelsInline — collapsible sub-section that lets the operator
// discover a provider's available models and add the chosen ones to the
// edit-modal's `models` list.
//
// This is the in-modal counterpart of FetchModelsModal. It reuses the
// same fetch helpers from client.js and produces the same normalized
// {id, display_name, ...} shape. Unlike the standalone modal, this
// component never opens a new window and never calls the server-side
// PATCH / PUT on its own — it merges the chosen models into the parent's
// form state so a single submit commits everything together.
//
// Two fetch modes:
//   - 'openai': caller-side probe of <base_url>/v1/models. Used for
//     OpenAI-Compat and for static-key providers whose `api-key` +
//     `base-url` are already filled in (the operator typed a secret).
//   - 'auth': server-side registry view via /v0/management/auth-files/
//     models. Only used in edit mode for OAuth providers that already
//     have a registered auth file. The component looks up the auth-file
//     name from the registry by matching the entry's existing first
//     API key (api-key-entries[0].api-key for OpenAI-Compat,
//     api-key for others).

import React, { useEffect, useMemo, useState, useCallback } from 'react';
import { Spinner, ErrorBanner } from '../../components/Primitives.jsx';
import {
  fetchOpenAICompatModels,
  fetchProviderModelsFromAuth,
  getStoredCallerKey,
  setStoredCallerKey,
  listAuthFiles,
} from '../../api/client.js';

// stages: 'idle' | 'fetching' | 'ready' | 'error'
// We deliberately keep this minimal — the inline component shares the
// page's spinner / error styles instead of inventing its own.
export default function FetchModelsInline({
  // The form's current field values, read for context (which API key to
  // probe with, which base URL to use). The component never mutates
  // these directly.
  form,
  // provider id (one of PROVIDER_KEY_KINDS[].id).
  provider,
  // isEdit: when true, the section is allowed to look up an existing
  // auth-file for the credential being edited and use the registry view.
  isEdit,
  // siblingNames for OpenAI-Compat; passed to getStoredCallerKey fallback.
  siblingNames = [],
  // Called when the operator clicks "Add selected". The parent appends
  // the picked models to its own form.models list.
  onAddModels,
  // Optional: hide the section entirely (e.g. for providers we don't
  // support fetch on — currently we support all of them, so this stays
  // for future gating).
  disabled = false,
}) {
  const [open, setOpen] = useState(false);
  const [stage, setStage] = useState('idle');
  const [error, setError] = useState('');
  const [fetched, setFetched] = useState([]);
  const [selected, setSelected] = useState({});
  const [filter, setFilter] = useState('');
  const [callerKey, setCallerKey] = useState(() => getStoredCallerKey() || '');
  const [rememberKey, setRememberKey] = useState(false);

  // authFileName: the first auth-file that matches this provider type
  // (used for the 'auth' mode during edit). Loaded lazily when the
  // section first opens so we don't fire a network request for nothing.
  const [authFileName, setAuthFileName] = useState('');

  const providerAuthType = useMemo(() => providerIdToAuthType(provider), [provider]);

  // Decide which fetch mode to use when the operator clicks Fetch.
  // The form's URL + credential always wins — the operator typed them
  // explicitly into the form they're editing, and that's the canonical
  // source of truth for "what does this entry point at".
  //   1. form has base_url (+ optional api_key) -> caller-side probe
  //      against the form's values, regardless of add vs edit mode.
  //   2. form has no base_url (only happens in Add mode without a
  //      typed base_url) -> fall back to the registry view IF an
  //      auth-file matches; otherwise show a friendly hint.
  // The form's stored key comes in several shapes depending on the caller:
  //   - The form's own `api_key` scalar (the typed-in secret) — canonical
  //     for New entries.
  //   - `api_key_entries[*].api_key` (underscore) — produced by the
  //     UpstreamProvidersPage buildForm/buildPayload editor.
  //   - `api_key_entries[*]['api-key']` (underscore container + kebab key)
  //     and `['api-key-entries'][*]['api-key']` (full kebab) — shapes seen
  //     from ProviderKeyEditModal / wire JSON where the Go tag is
  //     `api-key-entries`.
  // Read all shapes defensively; the first non-empty value wins.
  const formApiKey =
    form?.api_key?.trim()
    || (Array.isArray(form?.api_key_entries) && (form.api_key_entries[0]?.['api-key']?.trim?.() || form.api_key_entries[0]?.api_key?.trim?.()))
    || (Array.isArray(form?.['api-key-entries']) && form['api-key-entries'][0]?.['api-key']?.trim?.())
    || '';
  // The form's `name` (for OpenAI-Compat) lets the server resolve the
  // per-provider credential from cfg.OpenAICompatibility server-side,
  // bypassing any JS-side JSON path ambiguity.
  const formName =
    (typeof form?.name === 'string' && form.name.trim())
    || '';
  const formBaseUrl = form?.base_url?.trim() || '';
  const hasFormBaseUrl = !!formBaseUrl;
  const canFetch = !disabled && (hasFormBaseUrl || isEdit);

  const doFetch = useCallback(async () => {
    setStage('fetching');
    setError('');
    setFetched([]);
    setSelected({});
    try {
      // Priority 1: always prefer the form's URL + credential. This is
      // what the operator is editing; even in Edit mode the registry view
      // is only a fallback (the auth-file may belong to a different
      // credential than the one this entry actually points at).
      if (hasFormBaseUrl) {
        const list = await fetchOpenAICompatModels({
          baseUrl: formBaseUrl,
          // The form's API key is the source of truth — the operator
          // typed it for this exact entry. The callerKey from localStorage
          // is only a fallback when the form is empty (Add mode, no key
          // typed yet) so we still surface the proxy's last-used token.
          apiKey: formApiKey || callerKey,
          // Forward the entry's name so the server can resolve the
          // per-provider credential itself, regardless of whether the
          // dashboard could parse api-key-entries[*].api-key out of the
          // wire JSON (the Go json tag is `api-key-entries`, which the
          // browser keeps as a kebab-case string).
          name: formName,
        });
        setFetched(list);
        setStage('ready');
        return;
      }
      // Priority 2: registry view as fallback (Edit mode without base_url).
      if (isEdit && authFileName) {
        const list = await fetchProviderModelsFromAuth(authFileName);
        setFetched(list);
        setStage('ready');
        return;
      }
      // No path available — surface a helpful hint.
      setError(
        'Add a Base URL in the Endpoint section above, then click Fetch. '
        + 'The probe needs an upstream endpoint to call /v1/models.',
      );
      setStage('error');
    } catch (err) {
      setError(err.message || 'Failed to fetch models.');
      setStage('error');
    }
  }, [
    isEdit,
    authFileName,
    hasFormBaseUrl,
    formBaseUrl,
    formApiKey,
    formName,
    callerKey,
  ]);

  // Lazy-load the auth-files list when the section opens in edit mode,
  // so we can pick an auth-file for the registry view.
  useEffect(() => {
    if (!open || !isEdit) return;
    let cancelled = false;
    listAuthFiles()
      .then((res) => {
        if (cancelled) return;
        const files = Array.isArray(res?.files) ? res.files : [];
        // Pick the first active (non-disabled) auth-file whose type matches
        // this provider. Falls back to any matching file if none active.
        const match = files.find(
          (f) => (f?.type || f?.provider) === providerAuthType && !f.disabled,
        ) || files.find((f) => (f?.type || f?.provider) === providerAuthType);
        if (match) setAuthFileName(match.name || match.id || '');
      })
      .catch(() => { /* non-fatal — fall through to remote probe */ });
    return () => { cancelled = true; };
  }, [open, isEdit, providerAuthType]);

  // When the section closes, reset transient state so reopening starts fresh.
  function toggleOpen() {
    const next = !open;
    setOpen(next);
    if (!next) {
      setStage('idle');
      setError('');
      setFetched([]);
      setSelected({});
      setFilter('');
    }
  }

  const filtered = useMemo(() => {
    if (!filter) return fetched;
    const q = filter.toLowerCase();
    return fetched.filter((m) =>
      [m.id, m.display_name, m.owned_by, m.type]
        .filter(Boolean)
        .some((s) => String(s).toLowerCase().includes(q)),
    );
  }, [fetched, filter]);

  const allFilteredSelected =
    filtered.length > 0 && filtered.every((m) => selected[m.id]);
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

  function handleAdd() {
    const chosen = fetched.filter((m) => selected[m.id]);
    if (chosen.length === 0) return;
    if (rememberKey && callerKey) setStoredCallerKey(callerKey);
    onAddModels(chosen);
    // Clear the picker so the operator can refetch and add more if needed.
    setSelected({});
  }

  return (
    <div
      className="form-section"
      style={{
        background: 'var(--bg-elevated)',
        borderStyle: 'dashed',
        marginTop: 0,
      }}
    >
      <div
        className="row row--between"
        style={{ cursor: 'pointer' }}
        onClick={toggleOpen}
        role="button"
        tabIndex={0}
        onKeyDown={(e) => { if (e.key === 'Enter' || e.key === ' ') { e.preventDefault(); toggleOpen(); } }}
      >
        <div>
          <div className="form-section__title" style={{ marginBottom: 0 }}>
            <span style={{ marginRight: 6, color: 'var(--accent)' }}>{open ? '▾' : '▸'}</span>
            Discover models from this form's endpoint
          </div>
          <div className="form-section__hint" style={{ margin: '4px 0 0' }}>
            Probe <code className="mono">&lt;base_url&gt;</code>/v1/models using the
            Base URL + API key from this form. The auth-file registry is only
            used as a fallback when no Base URL is set.
          </div>
        </div>
        <div className="row gap-sm" onClick={(e) => e.stopPropagation()}>
          {open && (
            // type="button" is critical here: this button lives inside the
            // parent's <form> (the modal uses a <form> for accessibility),
            // and an untyped <button> defaults to type="submit" which would
            // fire handleSubmit and close the modal. The same applies to
            // every other <button> in this file.
            <button type="button" onClick={doFetch} disabled={stage === 'fetching' || !canFetch}>
              {stage === 'fetching' ? 'Fetching…' : 'Fetch'}
            </button>
          )}
        </div>
      </div>

      {open && (
        <div style={{ marginTop: 12 }}>
          {!isEdit && (
            <div className="form-section__hint" style={{ marginBottom: 8 }}>
              {hasFormBaseUrl
                ? 'The server probes <base_url>/v1/models on your behalf — no CORS issue from the browser.'
                : 'Add a Base URL in the Endpoint section above before fetching.'}
            </div>
          )}

          {/* Bearer-token input — visible for remote-probe mode. */}
          {!isEdit && hasFormBaseUrl && (
            <div className="form__row" style={{ marginBottom: 8 }}>
              <label className="form__label">Bearer token (optional override)</label>
              <input
                type="password"
                value={callerKey}
                onChange={(e) => setCallerKey(e.target.value)}
                placeholder={
                  form?.api_key || form?.api_key_entries?.[0]?.['api-key'] || form?.api_key_entries?.[0]?.api_key
                    ? 'Use the form\'s API key'
                    : 'sk-… (some providers require auth)'
                }
                spellCheck={false}
              />
              <label className="row gap-sm" style={{ marginTop: 4, fontSize: 11, color: 'var(--text-dim)' }}>
                <input
                  type="checkbox"
                  checked={rememberKey}
                  onChange={(e) => setRememberKey(e.target.checked)}
                  style={{ width: 'auto' }}
                />
                <span>Remember this token for future fetches.</span>
              </label>
            </div>
          )}

          {stage === 'fetching' && (
            <Spinner label="Querying upstream…" />
          )}

          {error && <div className="error-banner">{error}</div>}

          {stage === 'ready' && fetched.length > 0 && (
            <>
              <div className="row gap-sm" style={{ marginBottom: 6 }}>
                <input
                  type="text"
                  value={filter}
                  onChange={(e) => setFilter(e.target.value)}
                  placeholder="Filter by id, owner, type…"
                  style={{ flex: 1 }}
                />
                <button onClick={toggleAll} type="button">
                  {allFilteredSelected ? 'Clear filter' : 'Select all visible'}
                </button>
              </div>

              <div
                className="list-editor"
                style={{ maxHeight: 280, overflowY: 'auto' }}
              >
                {filtered.length === 0 && (
                  <div className="list-editor__empty">
                    No models match "{filter}".
                  </div>
                )}
                {filtered.map((m) => {
                  const checked = !!selected[m.id];
                  return (
                    <label
                      key={m.id}
                      className="toggle-row"
                      style={{
                        cursor: 'pointer',
                        borderBottom: '1px solid var(--border)',
                        padding: '6px 8px',
                      }}
                    >
                      <div style={{ minWidth: 0, flex: 1 }}>
                        <div
                          className="toggle-row__label mono"
                          style={{ overflow: 'hidden', textOverflow: 'ellipsis' }}
                        >
                          {m.id}
                        </div>
                        <div
                          className="toggle-row__hint"
                          style={{ display: 'flex', gap: 8, flexWrap: 'wrap' }}
                        >
                          {m.owned_by && (
                            <span className="dim">owned_by: {m.owned_by}</span>
                          )}
                          {m.type && <span className="dim">type: {m.type}</span>}
                          {m.context_length ? (
                            <span className="dim">ctx: {m.context_length.toLocaleString()}</span>
                          ) : null}
                          {m.display_name ? (
                            <span className="dim">{m.display_name}</span>
                          ) : null}
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

              <div className="row row--between" style={{ marginTop: 8 }}>
                <span className="dim" style={{ fontSize: 12 }}>
                  {selectedCount > 0
                    ? `${selectedCount} of ${fetched.length} selected — clicking Add appends them to the Models list above.`
                    : `${fetched.length} discovered — pick one or more to add.`}
                </span>
                <button
                  type="button"
                  className="primary"
                  onClick={handleAdd}
                  disabled={selectedCount === 0}
                >
                  Add {selectedCount > 0 ? `(${selectedCount})` : 'selected'} to models
                </button>
              </div>
            </>
          )}

          {stage === 'ready' && fetched.length === 0 && !error && (
            <div className="dim" style={{ textAlign: 'center', padding: 8 }}>
              The upstream returned no models. The provider may be unavailable
              or the bearer token is wrong.
            </div>
          )}

          {stage === 'idle' && (
            <div className="dim" style={{ fontSize: 12 }}>
              Click <strong>Fetch</strong> above to discover what this provider
              currently serves. The result picker stays in this section; nothing
              is committed until you click <strong>Add entry</strong> /{' '}
              <strong>Save changes</strong> at the bottom of the modal.
            </div>
          )}
        </div>
      )}
    </div>
  );
}

// providerIdToAuthType — mirrors the helper in ProvidersTab.jsx. We
// duplicate it here rather than import to keep the modal self-contained
// (the modal already pulls form helpers from ./FormPrimitives.jsx, no
// ./hooks.js dependency).
function providerIdToAuthType(id) {
  const map = {
    claude: 'claude',
    codex: 'codex',
    xai: 'xai',
    vertex: 'vertex',
    gemini: 'gemini',
    interactions: 'gemini',
  };
  return map[id] || id;
}
