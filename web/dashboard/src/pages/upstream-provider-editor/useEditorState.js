// ============================================================================
// Upstream provider editor — EditorStateProvider context
// ============================================================================
//
// Lifts every useState / handler / effect from the previous
// ProviderEditorForm (./index.jsx) into a React Context provider so the
// tabbed detail page (PR 2) can split the form body across multiple
// tab components without re-fetching the row or duplicating validation.
//
// NO REFACTOR, NO API CHANGE: every state shape, setter, and effect here
// mirrors the original ProviderEditorForm byte-for-byte so the upcoming
// rewire of ./index.jsx can swap its local hooks for context reads without
// changing observable behavior. The plan's sketch (docs/plans/...pr2-plan.md)
// simplifies a few details (providerType as derived, touched as Set, etc.) —
// those are deliberately not adopted here.
//
// Exposed context value:
//   state,                 // current form object (same shape as today's state)
//   setState,              // raw form setter (matches the old setForm)
//   providerType,          // separate useState (matches the old useState)
//   setProviderType,       // setter used by the type picker + the section dropdown
//   isEdit,                // boolean
//   isEntryBearing,        // boolean (isEntryBearingType(providerType))
//   schema,                // schemas[providerType] || { sections: [] }
//   errors,                // derived via validate(state, schema, providerType, siblingNames, isEdit)
//   touched,               // object { [fieldName]: true } (matches the old useState)
//   setTouched,            // raw setter
//   dirty,                 // JSON.stringify(state) !== initialSnapshot
//   saving,                // boolean
//   savingError,           // string | null (renamed from serverError for context parity)
//   oauthConnected,        // boolean (create-mode OAuth connect flow state)
//   setOauthConnected,     // setter used by OAuthConnectSection.onCompleted
//   oauthConnectable,      // boolean (oauth:* with a web auth-url endpoint)
//   oauthChannel,          // string (e.g. 'claude', '' for non-oauth)
//   setField(name, value), // updates state, marks touched, clears savingError
//   setModels(arr),        // convenience wrapper around setField('models', arr)
//   setEntries(arr),       // convenience wrapper around setField('api_key_entries', arr)
//   touch(path),           // marks a field as touched
//   save(),                // POST or PUT, then toast; edit mode stays on the page
//   reset(),               // restore initial state via buildForm(providerType, initial)
//   liveStatus,            // /v0/management/upstream-providers/live-status (PR 1)
//   attemptBack(),         // window.confirm guard + navigate('/upstream-providers')
//   handleOAuthCompleted(authData), // create-mode OAuth callback (merged authData)
//   prevTypeRef,           // ref used by the provider_type switch effect (advanced)
//
// What lives outside the context (still in the parent ./index.jsx):
//   - The actual <form> JSX, including the type-picker step (create mode only).
//   - The sibling-names fetch (page shell, shared across navigations).
//   - The proxyPools list (page shell).
//   - The modal/page chrome (sticky header, error banner).
// The next PR 2 task rewires ./index.jsx to consume the context; this file
// is the dependency that makes that rewire a mechanical swap.

import React, {
  createContext,
  useCallback,
  useContext,
  useEffect,
  useMemo,
  useRef,
  useState,
} from 'react';
import { useNavigate } from 'react-router-dom';
import {
  ApiError,
  createUpstreamProvider,
  getUpstreamProvider,
  listUpstreamProviderLiveStatus,
  oauthChannelToAuthProvider,
  seedUpstreamProviderModels,
  updateUpstreamProvider,
} from '../../api/client.js';
import { coerceLiveStatusResponse } from '../../api/liveStatus.js';
import { useToast } from '../../components/Toast.jsx';
import { buildForm, buildPayload, formatRFC3339, validate } from './form.js';
import {
  isEntryBearingType,
  isOAuth,
  isOpenCodeGo,
  TYPE_LABEL,
  buildSchemas,
  OPENCODE_GO_BASE_URL,
} from './schemas.js';

const EditorStateContext = createContext(null);

// ---------------------------------------------------------------------------
// Provider
// ---------------------------------------------------------------------------

// EditorStateProvider owns every piece of state previously held inside
// ProviderEditorForm. `initial` is the loaded provider row (edit mode) or
// null (create mode). `siblingNames` is fed in from the page shell — it is
// NOT lifted into context because the page shell fetches it once and shares
// it across navigations.
export function EditorStateProvider({ initial, siblingNames = [], children }) {
  const toast = useToast();
  const navigate = useNavigate();

  // schemas is a stable memo; buildSchemas() is pure but cheap to memoise
  // so the editor's re-renders don't reallocate it on every keystroke.
  const schemas = useMemo(() => buildSchemas(), []);

  const isEdit = !!(initial && initial.id);

  // provider_type drives the schema. On create, the operator picks it first
  // via the type picker; '' means the picker is still shown.
  const [providerType, setProviderType] = useState(() => initial?.provider_type || '');
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

  const [form, setForm] = useState(() => buildForm(providerType, initial));
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
    setForm(buildForm(providerType, initial, form));
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

  // Dirty guard: a beforeunload warning on hard navigation (reload / tab close).
  // The app uses BrowserRouter (not a data router), so react-router's
  // useBlocker is unavailable — the design doc settled on the plain browser
  // confirm / beforeunload prompt.
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

  // PR 1 live-status: /v0/management/upstream-providers/live-status. The
  // editor's tabs (overview badges, entries status pills, quota status)
  // consume this; the provider lives in context so every tab sees the same
  // map without re-fetching. Errors degrade silently — the same shape as
  // the picker/list surfaces.
  const [liveStatus, setLiveStatus] = useState({});
  useEffect(() => {
    let cancelled = false;
    listUpstreamProviderLiveStatus()
      .then((json) => { if (!cancelled) setLiveStatus(coerceLiveStatusResponse(json)); })
      .catch(() => { /* degrade silently — picker/list tolerate {} */ });
    return () => { cancelled = true; };
  }, []);

  // update(name, value) — verbatim from the old ProviderEditorForm. Renamed
  // `setField` in the context API so the plan's contract is met while the
  // implementation preserves the old behavior (touched flag, server-error
  // reset on every keystroke).
  const setField = useCallback((name, value) => {
    setForm((f) => ({ ...f, [name]: value }));
    setTouched((t) => ({ ...t, [name]: true }));
    setServerError('');
  }, []);

  const setModels = useCallback((arr) => setField('models', arr), [setField]);
  const setEntries = useCallback((arr) => setField('api_key_entries', arr), [setField]);
  const touch = useCallback((name) => setTouched((t) => ({ ...t, [name]: true })), []);

  // reset() restores the form to the loaded provider row. Edit mode hits
  // this on a "Discard changes" affordance; create mode drops back to a
  // blank build of the same provider_type.
  const reset = useCallback(() => setForm(buildForm(providerType, initial)), [providerType, initial]);

  // save() runs the same POST/PUT pipeline the old handleSubmit used.
  // Edit mode stays on the current page (the toast confirms the save);
  // create mode navigates to the new row's detail route so the editor
  // re-mounts in edit mode (replacing the create flow).
  const save = useCallback(async (e) => {
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
        // Stay on the editor after saving so the operator can keep tuning
        // the same row; the toast confirms the save.
        await updateUpstreamProvider(initial.id, payload);
        toast.success('Provider updated');
      } else {
        const created = await createUpstreamProvider(payload);
        toast.success('Provider created');
        // OpenCode Go rows start with an empty catalog — seed it once so
        // the operator lands on a usable model list. Best-effort: a seed
        // failure never blocks the create (the panel has a manual
        // "Seed models" button for retries).
        if (created && created.id && isOpenCodeGo(providerType)) {
          try {
            const seeded = await seedUpstreamProviderModels(created.id);
            toast.success(`Seeded ${seeded?.added ?? 0} models`);
          } catch { /* manual seed available in the tools panel */ }
        }
        // The server returns the created row (with its id). Move to the
        // detail route so the same editor re-mounts in edit mode instead
        // of dropping the operator back on the list.
        if (created && created.id) {
          navigate(`/upstream-providers/${created.id}`, { replace: true });
        }
      }
    } catch (err) {
      const msg = err instanceof ApiError ? err.message : (err.message || 'Save failed');
      setServerError(msg);
      toast.error(msg);
    } finally {
      setSaving(false);
    }
  }, [hasErrors, errors, form, providerType, isEdit, initial, toast, navigate]);

  // attemptBack — back navigation with the same dirty/saving guard the
  // modal's Back button had. Edit-mode tabs that want their own Back button
  // call this so the unsaved-changes prompt stays consistent.
  const attemptBack = useCallback(() => {
    if (dirty && !saving) {
      const ok = window.confirm('You have unsaved changes. Leave anyway?');
      if (!ok) return;
    }
    navigate('/upstream-providers');
  }, [dirty, saving, navigate]);

  // handleOAuthCompleted(authData) — verbatim from the old OAuthConnectSection
  // onCompleted callback. Auto-populates Identity + token + cloak + models
  // from the auth JSON the server created during the OAuth flow.
  const handleOAuthCompleted = useCallback((authData) => {
    setOauthConnected(true);
    if (!authData) return;
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
  }, []);

  // pickProviderType(value) — the create-mode type-picker side effect. The
  // picker JSX still lives in the parent for now; this hook centralises the
  // setter + the opencode-go base_url prefill so subsequent tasks can wire
  // the picker to call this.
  const pickProviderType = useCallback((value) => {
    setProviderType(value);
    if (value === 'opencode-go') {
      setForm((f) => ({ ...f, base_url: OPENCODE_GO_BASE_URL }));
    }
  }, []);

  // Reload the row after an opencode-go catalog seed/refresh — verbatim from
  // the old onCatalogChanged callback. Kept here so the next PR 2 task
  // (OpenCodeGoActions rewrite) can call it without duplicating the
  // refetch-and-rehydrate plumbing.
  const reloadRow = useCallback(async (rowId) => {
    const id = rowId || initial?.id;
    if (!id) return;
    try {
      const row = await getUpstreamProvider(id);
      // The page shell owns the row; the editor only rehydrates its own
      // form. The next PR 2 task will add a sibling hook so the page shell
      // picks up the same row refresh.
      setForm(buildForm(providerType, row));
    } catch { /* keep the stale form; save still works */ }
  }, [providerType, initial]);

  // title / summary strings — kept here so the editor's sticky header can
  // pull them from context instead of recomputing per tab.
  const title = isEdit
    ? (initial?.name || initial?.label || initial?.file_name || TYPE_LABEL[providerType] || 'Edit Provider')
    : 'New Provider';
  const summary = isEdit
    ? {
        identifier: initial?.file_name || initial?.name || initial?.label || '',
        secondary: initial?.email || initial?.base_url || '',
      }
    : null;

  const value = useMemo(() => ({
    // Raw state + setters (verbatim lift).
    state: form,
    setState: setForm,
    providerType,
    setProviderType,
    pickProviderType,
    isEdit,
    isEntryBearing: isEntryBearingType(providerType),
    schema,
    errors,
    hasErrors,
    touched,
    setTouched,
    dirty,
    saving,
    savingError: serverError,
    setSavingError: setServerError,
    oauthConnected,
    setOauthConnected,
    oauthConnectable,
    oauthChannel,
    // Handlers (verbatim lift, plus the wrappers the plan contract requires).
    setField,
    setModels,
    setEntries,
    touch,
    save,
    reset,
    attemptBack,
    handleOAuthCompleted,
    reloadRow,
    // Display strings.
    title,
    summary,
    // PR 1 surface.
    liveStatus,
    // Escape hatches for the next PR 2 task's wiring.
    prevTypeRef,
    schemas,
  }), [
    form, providerType, pickProviderType, isEdit, schema, errors, hasErrors,
    touched, dirty, saving, serverError,
    oauthConnected, oauthConnectable, oauthChannel,
    setField, setModels, setEntries, touch, save, reset,
    attemptBack, handleOAuthCompleted, reloadRow,
    title, summary, liveStatus, schemas, initial,
  ]);

  return (
    <EditorStateContext.Provider value={value}>
      {children}
    </EditorStateContext.Provider>
  );
}

// ---------------------------------------------------------------------------
// Hook
// ---------------------------------------------------------------------------

// useEditorState reads the editor's form state + handlers from the nearest
// EditorStateProvider. Throws when used outside a provider so wiring mistakes
// fail loudly during the next PR 2 task.
export function useEditorState() {
  const ctx = useContext(EditorStateContext);
  if (!ctx) {
    throw new Error('useEditorState must be used inside EditorStateProvider');
  }
  return ctx;
}