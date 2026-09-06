// ============================================================================
// Upstream provider editor — routed page
// ============================================================================

// UpstreamProviderEditorPage renders the provider editor as a dedicated page
// (provider-editor-page plan, Task 4), replacing the list page's editor
// modal. One route element serves both modes:
//   /upstream-providers/new  → create (type-picker step, then the form)
//   /upstream-providers/:id  → edit (fetch the row, then the form)
//
// The page shell resolves the mode + fetches the edit row (and the sibling
// names for the OpenAI-compat uniqueness check), then mounts the ported
// editor body — a sticky header replaces the modal chrome so the Save button
// stays visible while scrolling the long form. The editor body is ported
// from the former modal: `provider` is available at mount time (exactly the
// modal's prop contract), so the form hydration logic is unchanged.

import React, { useEffect, useMemo, useRef, useState } from 'react';
import { useParams, useNavigate } from 'react-router-dom';
import {
  getUpstreamProvider,
  listUpstreamProviders,
  createUpstreamProvider,
  updateUpstreamProvider,
  listProxyPools,
  oauthChannelToAuthProvider,
} from '../../api/client.js';
import { ApiError } from '../../api/client.js';
import { Spinner, ErrorBanner } from '../../components/Primitives.jsx';
import { useToast } from '../../components/Toast.jsx';
import {
  Field,
  PasswordInput,
  ToggleRow,
  ChipListEditor,
  KeyValueEditor,
  ModelListEditor,
} from '../manage-cpa/FormPrimitives.jsx';
import FetchModelsInline from '../manage-cpa/FetchModelsInline.jsx';
import {
  buildSchemas,
  API_KEY_TYPES,
  OAUTH_TYPES,
  TYPE_LABEL,
  TYPE_SIMPLE,
  isOAuth,
  isOpenAI,
} from './schemas.js';
import {
  buildForm,
  buildPayload,
  validate,
  formatRFC3339,
} from './form.js';
import OAuthConnectSection from './OAuthConnect.jsx';
import APIKeyEntriesEditor from './EntriesEditor.jsx';

// Types where the FetchModelsInline probe is meaningful (has a base_url +
// api_key the operator can probe, or an auth-file the registry tracks).
const FETCHABLE_TYPES = new Set([
  'gemini-api-key', 'interactions-api-key', 'codex-api-key', 'xai-api-key',
  'claude-api-key', 'vertex-api-key', 'openai-compatibility',
]);

// resolveEditorMode classifies the :id route param. 'new' means create;
// anything else is a numeric PG row id (the router guarantees the segment is
// non-empty). Exported as a pure function so the mode contract is testable
// without a DOM.
export function resolveEditorMode(id) {
  const isCreate = id === 'new';
  return { isCreate, providerId: isCreate ? null : id };
}

export default function UpstreamProviderEditorPage() {
  // Active proxy pools feed the row-level + per-entry pickers below. The
  // list is fetched once on mount; a stale binding (pool deleted elsewhere)
  // still shows its raw id so the operator can clear it.
  const [proxyPools, setProxyPools] = useState([]);
  useEffect(() => {
    let cancelled = false;
    listProxyPools()
      .then((res) => { if (!cancelled) setProxyPools(Array.isArray(res?.pools) ? res.pools : []); })
      .catch(() => { /* picker degrades to manual URL only */ });
    return () => { cancelled = true; };
  }, []);

  const { id } = useParams();
  const { isCreate, providerId } = resolveEditorMode(id);

  // Edit row (edit mode only). The editor body is not mounted until the
  // row arrives, mirroring the modal's mount-time `provider` prop contract.
  const [provider, setProvider] = useState(null);
  const [fetchError, setFetchError] = useState(null);

  // Sibling OpenAI-compat provider names for the name-uniqueness check —
  // needed in both modes (the modal received the same list from the list
  // page). Fetched once; a failure is non-fatal (validation falls back to
  // the server's own 409 on save).
  const [siblingNames, setSiblingNames] = useState([]);

  useEffect(() => {
    let cancelled = false;
    if (!isCreate) {
      setFetchError(null);
      // Clear the previous row before refetching: navigating from one
      // provider straight to another (same Route, no wrapper remount) would
      // otherwise mount the keyed editor with the STALE provider — hydrating
      // the old row's data into the new route's form. Resetting to null
      // re-arms the loading gate below until the fresh row arrives.
      setProvider(null);
      getUpstreamProvider(providerId)
        .then((row) => { if (!cancelled) setProvider(row); })
        .catch((err) => { if (!cancelled) setFetchError(err); });
    }
    listUpstreamProviders()
      .then((res) => {
        if (cancelled) return;
        setSiblingNames(((res && res.providers) || [])
          .filter((p) => isOpenAI(p.provider_type))
          .map((p) => p.name)
          .filter(Boolean));
      })
      .catch(() => { /* non-fatal — server 409 still guards duplicates */ });
    return () => { cancelled = true; };
  }, [isCreate, providerId]);

  if (!isCreate && fetchError) {
    return (
      <div className="card">
        <ErrorBanner error={fetchError} />
        <div className="row gap-sm" style={{ marginTop: 12 }}>
          <BackToListButton />
        </div>
      </div>
    );
  }
  // Derived gate: render the editor only when the loaded row matches the
  // route id. This is timing-independent — a provider-to-provider navigation
  // cannot flash the old row's editor (or its error banner) for a commit,
  // regardless of effect ordering between the shell and the keyed child.
  const providerMatchesRoute = !isCreate && provider && String(provider.id) === providerId;
  if (!isCreate && !providerMatchesRoute) {
    return <Spinner label="Loading provider…" />;
  }

  // Keyed by the route id so a provider-to-provider navigation remounts a
  // fresh editor once the derived gate above releases it.
  return (
    <ProviderEditorForm
      key={id}
      provider={isCreate ? null : provider}
      siblingNames={siblingNames}
    />
  );
}

function BackToListButton() {
  const navigate = useNavigate();
  return (
    <button type="button" onClick={() => navigate('/upstream-providers')}>
      ← Back to providers
    </button>
  );
}

// ============================================================================
// Editor body (ported from the list page's editor modal)
// ============================================================================

function ProviderEditorForm({ provider, siblingNames = [] }) {
  const toast = useToast();
  const navigate = useNavigate();
  const schemas = useMemo(() => buildSchemas(), []);
  const isEdit = !!(provider && provider.id);

  // provider_type drives the schema. On create, the operator picks it first
  // via the type picker; '' means the picker is still shown.
  const [providerType, setProviderType] = useState(() => provider?.provider_type || '');
  const schema = schemas[providerType] || { sections: [] };

  // OAuth connect state: for oauth:* types that have a web auth-url endpoint,
  // the operator must complete the OAuth flow (generate URL → browser login →
  // paste callback URL) before the token fields are meaningful. We track
  // whether that flow has completed in this editor session. In edit mode the
  // provider is already connected, so we start connected=true (skip the
  // connect step, reveal the token section immediately).
  const oauthChannel = isOAuth(providerType) ? providerType.replace(/^oauth:/, '') : '';
  const oauthConnectable = !!oauthChannelToAuthProvider(oauthChannel);
  const [oauthConnected, setOauthConnected] = useState(isEdit && oauthConnectable);

  const [form, setForm] = useState(() => buildForm(providerType, provider));
  const [touched, setTouched] = useState({});
  const [saving, setSaving] = useState(false);
  const [serverError, setServerError] = useState('');
  const [initialSnapshot] = useState(() => JSON.stringify(form));
  const prevTypeRef = useRef(providerType);

  // When the provider_type changes (create mode), re-build the form to match
  // the new schema while preserving the provider_type itself + sensible
  // carry-overs (priority, prefix).
  useEffect(() => {
    if (prevTypeRef.current === providerType) return;
    prevTypeRef.current = providerType;
    setForm(buildForm(providerType, provider, form));
    setTouched({});
    setServerError('');
    setOauthConnected(false);
  }, [providerType]); // eslint-disable-line react-hooks/exhaustive-deps

  const errors = useMemo(
    () => validate(form, schema, providerType, siblingNames, isEdit),
    [form, schema, providerType, siblingNames, isEdit],
  );
  const hasErrors = Object.keys(errors).length > 0;
  const dirty = JSON.stringify(form) !== initialSnapshot;

  // Dirty guard: window.confirm on the Back button + a beforeunload warning
  // on hard navigation (reload / tab close). The app uses BrowserRouter
  // (not a data router), so react-router's useBlocker is unavailable —
  // the design doc settled on the plain browser confirm.
  useEffect(() => {
    if (!dirty) return undefined;
    const onBeforeUnload = (e) => {
      e.preventDefault();
      // returnValue is the legacy contract Chrome/Edge still require to
      // show the browser's own leave-site prompt.
      e.returnValue = '';
    };
    window.addEventListener('beforeunload', onBeforeUnload);
    return () => window.removeEventListener('beforeunload', onBeforeUnload);
  }, [dirty]);

  function update(name, value) {
    setForm((f) => ({ ...f, [name]: value }));
    setTouched((t) => ({ ...t, [name]: true }));
    setServerError('');
  }

  async function handleSubmit(e) {
    e?.preventDefault?.();
    if (hasErrors) {
      setTouched(Object.fromEntries(Object.keys(errors).map((k) => [k, true])));
      return;
    }
    setSaving(true);
    setServerError('');
    try {
      const payload = buildPayload(form, providerType);
      if (isEdit) {
        await updateUpstreamProvider(provider.id, payload);
        toast.success('Provider updated');
      } else {
        await createUpstreamProvider(payload);
        toast.success('Provider created');
      }
      navigate('/upstream-providers');
    } catch (err) {
      const msg = err instanceof ApiError ? err.message : (err.message || 'Save failed');
      setServerError(msg);
      toast.error(msg);
    } finally {
      setSaving(false);
    }
  }

  function attemptBack() {
    if (dirty && !saving) {
      const ok = window.confirm('You have unsaved changes. Leave anyway?');
      if (!ok) return;
    }
    navigate('/upstream-providers');
  }

  const title = isEdit
    ? (provider.name || provider.label || provider.file_name || TYPE_LABEL[providerType] || 'Edit Provider')
    : 'New Provider';

  // Summary banner content for the editor header. Operators editing OAuth
  // accounts care about file_name (auth-dir collision key) and the account
  // email; for API-key providers it's the base_url.
  const summary = isEdit
    ? {
        identifier: provider?.file_name || provider?.name || provider?.label || '',
        secondary: provider?.email || provider?.base_url || '',
      }
    : null;

  // ---- Create mode step 0: type picker ----------------------------------

  if (!providerType) {
    const pickerGroup = (label, types) => (
      <div className="form-section">
        <div className="form-section__title">{label}</div>
        <div className="type-picker__grid">
          {types.map((t) => (
            <button
              type="button"
              key={t.value}
              className="type-picker__card"
              onClick={() => setProviderType(t.value)}
              aria-label={`Choose ${t.label}`}
            >
              <span className="type-picker__label">{t.label}</span>
              <span className="type-picker__value dim">{t.value}</span>
            </button>
          ))}
        </div>
      </div>
    );
    return (
      <>
        <div className="main__header">
          <div>
            <div className="dim"><button type="button" className="linklike" onClick={attemptBack} disabled={saving}>← Back</button></div>
            <h1 className="main__title">New Provider</h1>
            <div className="main__subtitle">Choose a provider type to configure its credentials.</div>
          </div>
        </div>
        <div className="type-picker">
          {pickerGroup('API Key', API_KEY_TYPES)}
          {pickerGroup('OAuth / File-backed', OAUTH_TYPES)}
        </div>
      </>
    );
  }

  // ---- Editor body -------------------------------------------------------

  return (
    <div className="upstream-editor">
      <div className="upstream-editor__header">
        <div className="upstream-editor__header-main">
          <div className="dim">
            <button type="button" className="linklike" onClick={attemptBack} disabled={saving}>← Back</button>
          </div>
          <h1 className="main__title upstream-editor__title">{title}</h1>
        </div>
        <div className="row gap-sm" style={{ alignItems: 'center', flexWrap: 'wrap' }}>
          <span className="badge badge--muted" style={{ fontSize: 10 }}>{providerType}</span>
          <button
            type="button"
            className="primary"
            onClick={handleSubmit}
            disabled={saving || (hasErrors && Object.values(touched).some(Boolean))}
          >
            {saving ? 'Saving…' : isEdit ? 'Save changes' : 'Create provider'}
          </button>
        </div>
      </div>

      <div>
        {serverError && <div className="error-banner">{serverError}</div>}
        {hasErrors && (
          <div className="error-summary">
            <strong>Please fix {Object.keys(errors).length} field{Object.keys(errors).length === 1 ? '' : 's'}:</strong>
            <ul>{Object.entries(errors).map(([k, v]) => <li key={k}>{v}</li>)}</ul>
          </div>
        )}

        {/* Editor summary banner. Surfaces the provider type + identifier
            (file_name for OAuth, name for OpenAI-compat, base_url for API-key)
            and a one-line "save will re-render" hint so the operator never
            has to remember what an upstream provider row actually controls. */}
        {providerType && (
          <div className="editor-summary" role="region" aria-label="Editor summary">
            <div className="editor-summary__head">
              <span className="badge badge--muted" style={{ fontSize: 10 }}>{providerType}</span>
              {summary?.identifier && (
                <code className="editor-summary__id">{summary.identifier}</code>
              )}
              {dirty && (
                <span className="editor-summary__dirty" title="You have unsaved changes">● unsaved changes</span>
              )}
            </div>
            <div className="editor-summary__hint">
              {isOAuth(providerType) && (
                <>Saves write a row to <code>upstream_providers</code> + a normalized JSON file to the auth-dir.</>
              )}
              {isOpenAI(providerType) && (
                <>Saves write a row to <code>upstream_providers</code>; base URL becomes the routing key.</>
              )}
              {!isOAuth(providerType) && !isOpenAI(providerType) && (
                <>Saves write a row to <code>upstream_providers</code> + re-render the matching <code>config.yaml</code> provider block.</>
              )}
              {' '}Reload is triggered automatically.
            </div>
          </div>
        )}

        {/* Provider type selector — still rendered first so the type stays
            switchable in create mode; disabled in edit (the type is the row's
            identity). */}
        <div className="form-section">
          <div className="form-section__title">Provider Type</div>
          <div className="form-section__row">
            <Field label="Type" required>
              <select value={providerType} onChange={(e) => setProviderType(e.target.value)} disabled={isEdit}>
                <option value="">Select a provider type…</option>
                <optgroup label="API Key">{API_KEY_TYPES.map((t) => <option key={t.value} value={t.value}>{t.label}</option>)}</optgroup>
                <optgroup label="OAuth / File-backed">{OAUTH_TYPES.map((t) => <option key={t.value} value={t.value}>{t.label}</option>)}</optgroup>
              </select>
            </Field>
          </div>
        </div>

        {/* OAuth connect workflow — for oauth:* types that support a web
            auth-url flow, show the Connect step first (create mode only) so
            the operator can generate the authorize URL, complete the browser
            login, then paste the callback redirect URL to complete the token
            exchange. In edit mode the provider is already connected, so the
            flow is skipped entirely. After a successful callback, the
            identity/token/models fields are auto-populated from the new auth
            file before the full settings are revealed. */}
        {oauthConnectable && !isEdit && (
          <OAuthConnectSection
            providerType={providerType}
            onCompleted={(authData) => {
              setOauthConnected(true);
              // Auto-populate Identity + token + cloak + models from the auth
              // file the server just created during the OAuth flow. All
              // available fields from the auth JSON are merged so the
              // upstream_providers row persists the complete picture.
              if (authData) {
                setForm((f) => {
                  const next = {
                    ...f,
                    file_name: authData.file_name || f.file_name,
                    email: authData.email || f.email,
                    label: authData.label || f.label,
                  };
                  // Token fields.
                  if (authData.token_access_token) next.token_access_token = authData.token_access_token;
                  if (authData.token_refresh_token) next.token_refresh_token = authData.token_refresh_token;
                  if (authData.token_token_type) next.token_token_type = authData.token_token_type;
                  if (authData.token_scope) next.token_scope = authData.token_scope;
                  if (authData.token_expiry) next.token_expiry = formatRFC3339(authData.token_expiry);
                  if (authData.token_expired) next.token_expired = true;
                  // Cloak fields (Claude OAuth).
                  if (authData.cloak_mode != null) next.cloak_mode = authData.cloak_mode;
                  if (authData.cloak_strict_mode) next.cloak_strict_mode = true;
                  if (Array.isArray(authData.cloak_sensitive_words)) {
                    next.cloak_sensitive_words = authData.cloak_sensitive_words;
                  }
                  if (authData.cloak_cache_user_id === true || authData.cloak_cache_user_id === false) {
                    next.cloak_cache_user_id = authData.cloak_cache_user_id;
                  }
                  // Extra config passthrough.
                  const extra = { ...(f.extra_config || {}) };
                  if (authData.disable_cooling) extra.disable_cooling = true;
                  if (authData.request_retry != null) extra.request_retry = authData.request_retry;
                  if (authData.tool_prefix_disabled) extra.tool_prefix_disabled = true;
                  if (Object.keys(extra).length > 0) next.extra_config = extra;
                  // Prefix.
                  if (authData.prefix) next.prefix = authData.prefix;
                  // Models.
                  if (authData.models && authData.models.length > 0) {
                    next.models = authData.models;
                  }
                  return next;
                });
              }
            }}
          />
        )}

        {/* Schema-driven sections. */}
        {schema.sections.map((section) => {
          // For OAuth providers, hide the OAuth token section until the
          // connect flow is done (those fields are populated by the callback).
          if (oauthConnectable && !oauthConnected && section.title === 'OAuth token') {
            return null;
          }
          return (
          <div className="form-section" key={section.title}>
            <div className="form-section__title">{section.title}</div>
            {section.hint && <div className="form-section__hint">{section.hint}</div>}
            <div className="form-section__row">
              {section.fields.map((field) => {
                const showError = touched[field.name] && errors[field.name];
                return (
                  <Field
                    key={field.name}
                    label={field.label}
                    hint={field.hint}
                    error={showError ? errors[field.name] : ''}
                    required={field.required}
                    htmlFor={`up_${field.name.replace(/[.\s]/g, '_')}`}
                  >
                    {renderInput(field, form, update, isEdit, providerType, proxyPools)}
                  </Field>
                );
              })}
            </div>

            {/* Inline Fetch Models — rendered at the bottom of sections that
                declare fetchModels:true, for fetchable provider types. */}
            {section.fetchModels && FETCHABLE_TYPES.has(providerType) && (
              <FetchModelsInline
                form={form}
                provider={TYPE_SIMPLE[providerType] || providerType}
                isEdit={isEdit}
                siblingNames={siblingNames}
                onAddModels={(picked) => {
                  const existing = Array.isArray(form.models) ? form.models : [];
                  const byName = new Set(existing.map((r) => (r?.name || r?.id || '').trim()).filter(Boolean));
                  const additions = picked
                    .filter((p) => p && (p.id || p.name) && !byName.has(p.id || p.name))
                    .map((p) => {
                      const m = { name: p.id || p.name };
                      // Use camelCase keys for the upstream_providers API.
                      if (p.display_name) m.display_name = p.display_name;
                      return m;
                    });
                  if (additions.length > 0) {
                    // Merge into existing model rows (which use camelCase).
                    const merged = [...existing];
                    for (const a of additions) merged.push(a);
                    update('models', merged);
                  }
                }}
              />
            )}
          </div>
          );
        })}
      </div>
    </div>
  );
}

// ============================================================================
// Input renderer (dispatches by field.type)
// ============================================================================

function renderInput(field, form, update, isEdit, providerType, proxyPools = []) {
  const id = `up_${field.name.replace(/[.\s]/g, '_')}`;
  const value = form[field.name];
  switch (field.type) {
    case 'password':
      return <PasswordInput id={id} value={value} onChange={(v) => update(field.name, v)}
        placeholder={field.placeholder} defaultShown={isEdit} />;
    case 'number':
      return <input id={id} type="number" min={field.min} value={value ?? ''}
        onChange={(e) => update(field.name, e.target.value)} placeholder={field.placeholder} />;
    case 'select':
      return <select id={id} value={value || ''} onChange={(e) => update(field.name, e.target.value)}>
        {field.options.map((o) => <option key={o.value} value={o.value}>{o.label}</option>)}
      </select>;
    case 'toggle':
      return <ToggleRow label={field.label} hint={field.hint} checked={!!value}
        onChange={(v) => update(field.name, v)} />;
    case 'chips':
      return <ChipListEditor values={value || []} onChange={(v) => update(field.name, v)}
        placeholder={field.placeholder} emptyHint={field.emptyHint} />;
    case 'headers':
      return <KeyValueEditor rows={value || []} onChange={(v) => update(field.name, v)} />;
    case 'models':
      return <ModelListEditor rows={value || []} onChange={(v) => update(field.name, v)}
        fieldHints={{ name: 'upstream name', alias: 'client alias', displayName: 'display name' }} />;
    case 'openai_models':
      return <ModelListEditor rows={value || []} onChange={(v) => update(field.name, v)}
        fieldHints={{
          name: 'upstream model (e.g. anthropic/claude-3-5-sonnet)',
          alias: 'client alias (e.g. claude-sonnet)',
          displayName: 'display name (optional)',
        }} />;
    case 'proxy_pool_id': {
      // Row-level pool binding: inherit (empty) / active pools / direct.
      // Selecting a pool clears the manual proxy_url field (the renderer
      // makes them exclusive; keep the form consistent with that).
      const numeric = Number(value);
      const selected = Number.isFinite(numeric) && numeric > 0 ? String(numeric) : '';
      return (
        <select
          id={id}
          value={selected}
          onChange={(e) => {
            const v = e.target.value;
            if (v === '') {
              update(field.name, '');
            } else {
              update(field.name, Number(v));
              update('proxy_url', '');
            }
          }}
          data-testid="up_proxy_pool_id"
        >
          <option value="">inherit: manual proxy URL / global</option>
          {proxyPools.filter((p) => p.is_active).map((p) => (
            <option key={p.id} value={String(p.id)}>{p.name}</option>
          ))}
          <option value="0">none (no pool)</option>
        </select>
      );
    }
    case 'api_key_entries':
      return <APIKeyEntriesEditor
        entries={value || []}
        onChange={(v) => update(field.name, v)}
        proxyPools={proxyPools}
      />;
    case 'text':
    default:
      return <input id={id} type="text" value={value || ''} onChange={(e) => update(field.name, e.target.value)}
        placeholder={field.placeholder} required={field.required} />;
  }
}
