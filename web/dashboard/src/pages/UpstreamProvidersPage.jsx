import React, { useState, useMemo, useRef, useEffect } from 'react';
import {
  listUpstreamProviders,
  createUpstreamProvider,
  updateUpstreamProvider,
  deleteUpstreamProvider,
  requestOAuthUrl,
  submitOAuthCallback,
  oauthChannelToAuthProvider,
  listAuthFiles,
  getAuthFileModels,
  fetchAuthFileJSON,
} from '../api/client.js';
import { ApiError } from '../api/client.js';
import { useAsync } from '../hooks/useAsync.js';
import { Spinner, ErrorBanner, EmptyState, Modal } from '../components/Primitives.jsx';
import { useToast } from '../components/Toast.jsx';
import {
  Field,
  PasswordInput,
  ToggleRow,
  ChipListEditor,
  KeyValueEditor,
  ModelListEditor,
} from './manage-cpa/FormPrimitives.jsx';
import FetchModelsInline from './manage-cpa/FetchModelsInline.jsx';

// ============================================================================
// Provider type catalog
// ============================================================================

const API_KEY_TYPES = [
  { value: 'gemini-api-key', label: 'Gemini (API Key)', simple: 'gemini' },
  { value: 'interactions-api-key', label: 'Interactions (API Key)', simple: 'interactions' },
  { value: 'codex-api-key', label: 'Codex (API Key)', simple: 'codex' },
  { value: 'xai-api-key', label: 'xAI (API Key)', simple: 'xai' },
  { value: 'claude-api-key', label: 'Claude (API Key)', simple: 'claude' },
  { value: 'vertex-api-key', label: 'Vertex (API Key)', simple: 'vertex' },
  { value: 'openai-compatibility', label: 'OpenAI Compatibility', simple: 'openai' },
];
const OAUTH_TYPES = [
  { value: 'oauth:claude', label: 'Claude (OAuth)', simple: 'claude' },
  { value: 'oauth:codex', label: 'Codex (OAuth)', simple: 'codex' },
  { value: 'oauth:kimi', label: 'Kimi (OAuth)', simple: 'kimi' },
  { value: 'oauth:xai', label: 'xAI (OAuth)', simple: 'xai' },
  { value: 'oauth:vertex', label: 'Vertex (OAuth)', simple: 'vertex' },
  { value: 'oauth:aistudio', label: 'AI Studio (OAuth)', simple: 'aistudio' },
  { value: 'oauth:antigravity', label: 'Antigravity (OAuth)', simple: 'antigravity' },
];
const ALL_TYPES = [...API_KEY_TYPES, ...OAUTH_TYPES];
const TYPE_LABEL = Object.fromEntries(ALL_TYPES.map((t) => [t.value, t.label]));
const TYPE_SIMPLE = Object.fromEntries(ALL_TYPES.map((t) => [t.value, t.simple]));

const isOAuth = (t) => String(t || '').startsWith('oauth:');
const isOpenAI = (t) => t === 'openai-compatibility';
const isClaude = (t) => t === 'claude-api-key' || t === 'oauth:claude';

// Types where the FetchModelsInline probe is meaningful (has a base_url +
// api_key the operator can probe, or an auth-file the registry tracks).
const FETCHABLE_TYPES = new Set([
  'gemini-api-key', 'interactions-api-key', 'codex-api-key', 'xai-api-key',
  'claude-api-key', 'vertex-api-key', 'openai-compatibility',
]);

// URL_RE matches the *start* of a value: a real URL scheme, or the literal
// "direct"/"none" proxy bypass sentinels. Empty values are allowed (they
// mean "use the provider default" for base_url, or "use the global proxy"
// for proxy_url). The $ anchor is intentionally omitted so a full URL like
// "https://api.example.com" passes — only the prefix is checked.
const URL_RE = /^(https?:\/\/|socks5h?:\/\/|direct|none)/i;

// ============================================================================
// Page
// ============================================================================

export default function UpstreamProvidersPage() {
  const toast = useToast();
  const { data, error, loading, reload } = useAsync(() => listUpstreamProviders(), []);
  const [search, setSearch] = useState('');
  const [typeFilter, setTypeFilter] = useState('');
  const [editing, setEditing] = useState(null);
  const [confirmDelete, setConfirmDelete] = useState(null);

  const providers = data?.providers || [];

  const filtered = useMemo(() => {
    let out = providers;
    if (typeFilter) out = out.filter((p) => p.provider_type === typeFilter);
    if (search.trim()) {
      const q = search.trim().toLowerCase();
      out = out.filter((p) =>
        [p.provider_type, p.name, p.label, p.email, p.file_name, p.base_url]
          .filter(Boolean).some((v) => v.toLowerCase().includes(q)));
    }
    return out;
  }, [providers, search, typeFilter]);

  const counts = useMemo(() => ({
    total: providers.length,
    apiKeys: providers.filter((p) => !isOAuth(p.provider_type)).length,
    oauth: providers.filter((p) => isOAuth(p.provider_type)).length,
  }), [providers]);

  const siblingNames = useMemo(
    () => providers.filter((p) => isOpenAI(p.provider_type)).map((p) => p.name).filter(Boolean),
    [providers],
  );

  const handleDelete = async (p) => {
    try {
      await deleteUpstreamProvider(p.id);
      toast.success(`Deleted ${TYPE_LABEL[p.provider_type] || p.provider_type}`);
      setConfirmDelete(null);
      reload();
    } catch (err) {
      toast.error(err.message || 'Delete failed');
    }
  };

  return (
    <>
      <div className="main__header">
        <div>
          <h1 className="main__title">Upstream Providers</h1>
          <div className="main__subtitle">
            Normalized source of truth for every upstream provider credential.
            Edits re-render <code>config.yaml</code> + auth-dir artifacts and
            reload clients automatically.
          </div>
        </div>
        <div className="row gap-sm">
          <button onClick={() => { reload(); toast.info('Providers refreshed'); }}>Refresh</button>
          <button className="primary" onClick={() => setEditing({})}>+ New Provider</button>
        </div>
      </div>

      <div className="row gap-sm" style={{ marginBottom: 12 }}>
        <Stat label="Total" value={counts.total} />
        <Stat label="API Keys" value={counts.apiKeys} />
        <Stat label="OAuth" value={counts.oauth} />
      </div>

      <div className="card">
        <div className="catalog-toolbar">
          <input
            className="search-input"
            type="text"
            value={search}
            onChange={(e) => setSearch(e.target.value)}
            placeholder="Search by name, type, email, base URL…"
            aria-label="Search upstream providers"
          />
          <select
            className="search-input"
            value={typeFilter}
            onChange={(e) => setTypeFilter(e.target.value)}
            aria-label="Filter by provider type"
            style={{ maxWidth: 220 }}
          >
            <option value="">All provider types</option>
            <optgroup label="API Key">{API_KEY_TYPES.map((t) => <option key={t.value} value={t.value}>{t.label}</option>)}</optgroup>
            <optgroup label="OAuth">{OAUTH_TYPES.map((t) => <option key={t.value} value={t.value}>{t.label}</option>)}</optgroup>
          </select>
        </div>

        {loading ? (
          <Spinner label="Loading upstream providers…" />
        ) : error ? (
          <ErrorBanner error={error} onRetry={reload} />
        ) : filtered.length === 0 ? (
          <EmptyState
            title="No upstream providers"
            hint={search || typeFilter
              ? 'No providers match the current filters.'
              : 'Create one with "+ New Provider", or seed from config.yaml on first PG boot.'}
          />
        ) : (
          <table className="table">
            <thead>
              <tr>
                <th>Provider</th><th>Identifier</th><th>Priority</th>
                <th>Base URL</th><th>Status</th><th>Models</th>
                <th>Updated</th><th aria-label="Actions" />
              </tr>
            </thead>
            <tbody>
              {filtered.map((p) => {
                const ident = p.name || p.label || p.email || p.file_name || '—';
                return (
                  <tr key={p.id} className="clickable-row" onClick={() => setEditing(p)} style={{ cursor: 'pointer' }}>
                    <td>
                      <div className="cell-stack">
                        <span className="cell-stack__main">{TYPE_LABEL[p.provider_type] || p.provider_type}</span>
                        <span className="badge badge--muted" style={{ fontSize: 10 }}>
                          {isOAuth(p.provider_type) ? 'oauth' : 'api-key'}
                        </span>
                      </div>
                    </td>
                    <td>{ident}</td>
                    <td>{p.priority}</td>
                    <td className="truncate-cell">{p.base_url || <span className="dim">—</span>}</td>
                    <td>
                      <span className={`badge ${p.disabled ? 'badge--disabled' : 'badge--active'}`}>
                        {p.disabled ? 'disabled' : 'active'}
                      </span>
                    </td>
                    <td>{(p.models || []).length}</td>
                    <td className="dim">{p.updated_at ? formatTime(p.updated_at) : '—'}</td>
                    <td>
                      <button className="btn-icon" title="Delete" aria-label="Delete provider"
                        onClick={(e) => { e.stopPropagation(); setConfirmDelete(p); }}>✕</button>
                    </td>
                  </tr>
                );
              })}
            </tbody>
          </table>
        )}
      </div>

      {editing && (
        <UpstreamProviderEditor
          provider={editing}
          siblingNames={siblingNames}
          onClose={() => setEditing(null)}
          onSaved={() => { setEditing(null); reload(); }}
        />
      )}

      {confirmDelete && (
        <Modal title="Delete upstream provider?" size="sm"
          onClose={() => setConfirmDelete(null)}
          footer={<>
            <button onClick={() => setConfirmDelete(null)}>Cancel</button>
            <button className="danger" onClick={() => handleDelete(confirmDelete)}>Delete</button>
          </>}
        >
          <p>
            Permanently delete{' '}
            <strong>{TYPE_LABEL[confirmDelete.provider_type] || confirmDelete.provider_type}</strong>
            {' ('}{confirmDelete.name || confirmDelete.label || confirmDelete.file_name}{')'}?
            Config.yaml and auth-dir artifacts will be re-rendered and the in-memory
            clients reloaded.
          </p>
        </Modal>
      )}
    </>
  );
}

function Stat({ label, value }) {
  return (
    <div className="stat">
      <div className="stat__label">{label}</div>
      <div className="stat__value">{value}</div>
    </div>
  );
}

// ============================================================================
// Schema definitions (declarative, per provider_type)
// ============================================================================

function buildSchemas() {
  const commonEndpoint = [
    { name: 'base_url', label: 'Base URL', type: 'text', placeholder: 'https://api.example.com',
      hint: 'Upstream API base URL. Leave blank to use the provider default.',
      validate: (v) => (v && !URL_RE.test(v) ? 'Must start with http://, https://, socks5://, or be "direct"/"none".' : '') },
    { name: 'proxy_url', label: 'Proxy URL', type: 'text', placeholder: 'socks5://user:pass@host:1080',
      hint: 'Per-key proxy override. Use "direct"/"none" to bypass global proxy.',
      validate: (v) => (v && !URL_RE.test(v) ? 'Must start with http://, https://, socks5://, or be "direct"/"none".' : '') },
    { name: 'prefix', label: 'Model prefix', type: 'text', placeholder: 'teamA/',
      hint: 'Optional namespace prepended to every model this entry serves.' },
  ];

  const commonRouting = [
    { name: 'priority', label: 'Priority', type: 'number', min: 0, placeholder: '0',
      hint: 'Higher value is preferred when multiple credentials match.' },
    { name: 'models', label: 'Models', type: 'models',
      hint: 'Map client-facing aliases to upstream model names.' },
    { name: 'excluded_models', label: 'Excluded models', type: 'chips',
      placeholder: 'model-id or wildcard', hint: 'Models that should never route through this entry. Supports * wildcards.' },
  ];

  const commonBehavior = [
    { name: 'headers', label: 'Custom headers', type: 'headers',
      hint: 'Extra HTTP headers attached to every request using this entry.' },
    { name: 'disabled', label: 'Disabled', type: 'toggle',
      hint: 'When on, this entry is excluded from routing without removing it.' },
    { name: 'disable_cooling', label: 'Disable cooldown', type: 'toggle',
      hint: 'Skip the cooldown schedule when this credential hits an error.' },
  ];

  // API-key provider schemas share most structure.
  const apiKeyBase = (extraBehavior = [], extraSections = []) => ({
    sections: [
      { title: 'Identity', fields: [
        { name: 'api_key', label: 'API key', type: 'password', placeholder: 'sk-…', required: true,
          hint: 'The credential that authenticates requests to this provider.' },
      ]},
      { title: 'Endpoint', fields: commonEndpoint },
      { title: 'Routing', fields: commonRouting, fetchModels: true },
      { title: 'Behavior', fields: [...commonBehavior, ...extraBehavior] },
      ...extraSections,
    ],
  });

  const claudeCloakSection = {
    title: 'Cloak',
    hint: 'Disguise API requests to appear as the official Claude Code CLI (Claude only).',
    fields: [
      { name: 'cloak_mode', label: 'Cloak mode', type: 'select',
        options: [
          { value: '', label: 'auto (default)' },
          { value: 'always', label: 'always' },
          { value: 'never', label: 'never' },
        ],
        hint: 'auto: cloak only for non-Claude-Code clients. always: always cloak. never: never cloak.' },
      { name: 'cloak_strict_mode', label: 'Strict mode', type: 'toggle',
        hint: 'Strip all user system messages; keep only the Claude Code prompt.' },
      { name: 'cloak_sensitive_words', label: 'Sensitive words', type: 'chips',
        placeholder: 'word to obfuscate', hint: 'Words obfuscated with zero-width characters to bypass content filters.' },
      { name: 'cloak_cache_user_id', label: 'Cache user ID', type: 'toggle',
        hint: 'Cache Claude user_id per API key. Off = fresh random per request.' },
    ],
  };

  const schemaMap = {
    'gemini-api-key': apiKeyBase(),
    'interactions-api-key': apiKeyBase(),
    'codex-api-key': apiKeyBase([
      { name: 'websockets', label: 'WebSockets', type: 'toggle',
        hint: 'Use the Responses API websocket transport for this entry.' },
    ]),
    'xai-api-key': apiKeyBase([
      { name: 'websockets', label: 'WebSockets', type: 'toggle',
        hint: 'Use the Responses API websocket transport for this entry.' },
    ]),
    'claude-api-key': apiKeyBase(
      [
        { name: 'rebuild_mid_system_message', label: 'Rebuild mid system message', type: 'toggle',
          hint: 'Move role=system messages into the top-level system field.' },
        { name: 'experimental_cch_signing', label: 'Experimental CCH signing', type: 'toggle',
          hint: 'Opt-in final-body cch signing for cloaked Claude /v1/messages requests.' },
      ],
      [claudeCloakSection],
    ),
    'vertex-api-key': apiKeyBase(),
    'openai-compatibility': {
      sections: [
        { title: 'Identity', fields: [
          { name: 'name', label: 'Provider name', type: 'text', placeholder: 'openrouter', required: true,
            hint: 'Unique identifier for this OpenAI-compatible provider.' },
          { name: 'base_url', label: 'Base URL', type: 'text', placeholder: 'https://openrouter.ai/api/v1',
            required: true, hint: 'The external OpenAI-compatible API endpoint.',
            validate: (v) => (v && !URL_RE.test(v) ? 'Must start with http://, https://, or socks5://.' : '') },
        ]},
        { title: 'Endpoint', fields: [
          { name: 'proxy_url', label: 'Proxy URL', type: 'text', placeholder: 'direct',
            hint: 'Per-provider proxy override.',
            validate: (v) => (v && !URL_RE.test(v) ? 'Must start with http://, https://, socks5://, or be "direct"/"none".' : '') },
          { name: 'prefix', label: 'Model prefix', type: 'text', placeholder: 'teamA/',
            hint: 'Optional namespace prepended to every model this provider serves.' },
        ]},
        { title: 'Routing', fields: [
          { name: 'priority', label: 'Priority', type: 'number', min: 0, placeholder: '0',
            hint: 'Higher value is preferred when multiple providers match.' },
          { name: 'models', label: 'Models', type: 'openai_models',
            hint: 'Map client-facing aliases to upstream model names. Supports image + modality flags.' },
          { name: 'excluded_models', label: 'Excluded models', type: 'chips',
            placeholder: 'model-id or wildcard', hint: 'Models that should never route through this provider.' },
        ], fetchModels: true },
        { title: 'Behavior', fields: [
          { name: 'headers', label: 'Custom headers', type: 'headers',
            hint: 'Extra HTTP headers attached to every request to this provider.' },
          { name: 'disabled', label: 'Disabled', type: 'toggle',
            hint: 'When on, this provider is excluded from routing.' },
          { name: 'disable_cooling', label: 'Disable cooldown', type: 'toggle',
            hint: 'Skip the cooldown schedule when this provider hits an error.' },
          { name: 'api_key_entries', label: 'API key entries', type: 'api_key_entries',
            hint: 'Multiple keys form a round-robin pool for this provider.' },
        ]},
      ],
    },
  };

  // OAuth provider schemas — share a common identity/token/behavior shape,
  // with cloak added for oauth:claude.
  for (const oauthType of OAUTH_TYPES.map((t) => t.value)) {
    const sections = [
      { title: 'Identity', fields: [
        { name: 'email', label: 'Account email', type: 'text', placeholder: 'user@example.com',
          hint: 'The OAuth account email (read from the auth file).' },
        { name: 'file_name', label: 'File name', type: 'text', placeholder: 'claude-account-1', required: !false,
          hint: 'Auth JSON file id (without .json). Used to match the auth-dir file.' },
        { name: 'label', label: 'Label', type: 'text', placeholder: 'Work account',
          hint: 'Optional human-readable label.' },
      ]},
      { title: 'OAuth token', hint: 'These fields are normally managed automatically by the token refresh pipeline. Edit only when manually seeding or correcting a token.',
        fields: [
          { name: 'token_access_token', label: 'Access token', type: 'password',
            hint: 'Current OAuth access token. Refreshed automatically.' },
          { name: 'token_refresh_token', label: 'Refresh token', type: 'password',
            hint: 'OAuth refresh token (used to obtain new access tokens).' },
          { name: 'token_expiry', label: 'Token expiry', type: 'text', placeholder: '2026-08-01T12:00:00Z',
            hint: 'RFC3339 timestamp. Leave blank if unknown.' },
          { name: 'token_expired', label: 'Token expired', type: 'toggle',
            hint: 'Mark the token as expired to force a refresh on next use.' },
        ]},
      { title: 'Behavior', fields: [
        { name: 'disabled', label: 'Disabled', type: 'toggle',
          hint: 'When on, this auth is excluded from routing.' },
        { name: 'prefix', label: 'Model prefix', type: 'text', placeholder: 'teamA/',
          hint: 'Optional namespace prepended to every model this auth serves.' },
      ]},
    ];
    if (oauthType === 'oauth:claude') sections.push(claudeCloakSection);
    schemaMap[oauthType] = { sections };
  }

  return schemaMap;
}

// ============================================================================
// Editor modal (schema-driven)
// ============================================================================

function UpstreamProviderEditor({ provider, siblingNames = [], onClose, onSaved }) {
  const toast = useToast();
  const schemas = useMemo(() => buildSchemas(), []);
  const isEdit = !!(provider && provider.id);

  // provider_type drives the schema. On create, the operator picks it first.
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
      onSaved();
    } catch (err) {
      const msg = err instanceof ApiError ? err.message : (err.message || 'Save failed');
      setServerError(msg);
      toast.error(msg);
    } finally {
      setSaving(false);
    }
  }

  function attemptClose() {
    if (dirty && !saving) {
      const ok = window.confirm('Discard unsaved changes?');
      if (!ok) return;
    }
    onClose();
  }

  const title = isEdit
    ? `Edit ${TYPE_LABEL[providerType] || providerType}`
    : 'New Upstream Provider';

  return (
    <Modal
      title={title}
      size="xl"
      onClose={attemptClose}
      footer={<>
        <button type="button" onClick={attemptClose} disabled={saving}>Cancel</button>
        <button type="button" className="primary" onClick={handleSubmit}
          disabled={saving || (hasErrors && Object.values(touched).some(Boolean))}>
          {saving ? 'Saving…' : isEdit ? 'Save changes' : 'Create provider'}
        </button>
      </>}
    >
      <div>
        {serverError && <div className="error-banner">{serverError}</div>}
        {hasErrors && (
          <div className="error-summary">
            <strong>Please fix {Object.keys(errors).length} field{Object.keys(errors).length === 1 ? '' : 's'}:</strong>
            <ul>{Object.entries(errors).map(([k, v]) => <li key={k}>{v}</li>)}</ul>
          </div>
        )}

        {/* Provider type selector — always rendered first, disabled in edit. */}
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
                    {renderInput(field, form, update, isEdit, providerType)}
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

        {!providerType && (
          <EmptyState title="Select a provider type" hint="Choose a type above to see its configuration fields." />
        )}
      </div>
    </Modal>
  );
}

// ============================================================================
// Input renderer (dispatches by field.type)
// ============================================================================

function renderInput(field, form, update, isEdit, providerType) {
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
    case 'api_key_entries':
      return <APIKeyEntriesEditor entries={value || []} onChange={(v) => update(field.name, v)} />;
    case 'text':
    default:
      return <input id={id} type="text" value={value || ''} onChange={(e) => update(field.name, e.target.value)}
        placeholder={field.placeholder} required={field.required} />;
  }
}

// ============================================================================
// OAuthConnectSection — the connect workflow step for oauth:* providers
// ============================================================================

// OAuthConnectSection renders the OAuth authorization workflow:
//   1. Generate authorize URL (calls /<provider>-auth-url)
//   2. Auto-open the URL in a new tab (+ copy button fallback)
//   3. Operator completes login in their browser
//   4. Operator pastes the callback redirect URL (or raw code+state)
//   5. Submit calls /oauth-callback to complete the token exchange
//
// After a successful callback, onCompleted() is called so the parent can
// reveal the full identity/token settings. This section is shown ABOVE the
// schema-driven sections for oauth:* types, so the operator follows the
// connect flow first before configuring further details.
function OAuthConnectSection({ providerType, onCompleted }) {
  const toast = useToast();
  const channel = providerType.replace(/^oauth:/, '');
  const authProvider = oauthChannelToAuthProvider(channel);

  const [stage, setStage] = useState('idle'); // idle | loading | ready | submitting | done | error
  const [error, setError] = useState('');
  const [authUrl, setAuthUrl] = useState('');
  const [oauthState, setOauthState] = useState('');
  const [flow, setFlow] = useState('web');
  const [userCode, setUserCode] = useState('');
  const [callbackUrl, setCallbackUrl] = useState('');
  const [copied, setCopied] = useState(false);

  const handleGenerate = () => {
    setStage('loading');
    setError('');
    setAuthUrl('');
    setCallbackUrl('');
    requestOAuthUrl(authProvider, { isWebUI: true })
      .then((res) => {
        setStage('ready');
        setAuthUrl(res?.url || '');
        setOauthState(res?.state || '');
        setFlow(res?.flow || 'web');
        setUserCode(res?.user_code || '');
        // Auto-open the authorize URL in a new tab so the operator can start
        // the browser login immediately.
        if (res?.url) {
          window.open(res.url, '_blank', 'noopener,noreferrer');
        }
      })
      .catch((err) => {
        setStage('error');
        setError(err.message || 'Failed to generate authorize URL');
      });
  };

  const handleSubmitCallback = () => {
    const trimmed = callbackUrl.trim();
    if (!trimmed) {
      toast.error('Paste the callback redirect URL first.');
      return;
    }
    setStage('submitting');
    setError('');
    submitOAuthCallback({ provider: authProvider, redirectUrl: trimmed })
      .then(() => {
        setStage('fetching');
        toast.info('OAuth callback accepted. Fetching credential details…');
        // The server persists the auth file asynchronously + a background
        // waiter exchanges the code for tokens. Poll listAuthFiles until the
        // new auth file for this provider channel appears, then extract the
        // identity (email/file_name/label) and discover its models.
        pollForAuthFile(0)
          .then((authData) => {
            setStage('done');
            toast.success('OAuth flow completed. Provider details populated.');
            onCompleted?.(authData);
          })
          .catch((err) => {
            // Even if polling fails/times out, mark as done so the operator
            // can proceed manually — the credential may still be settling.
            setStage('done');
            toast.info(err.message || 'Could not auto-fetch details; fill them manually.');
            onCompleted?.(null);
          });
      })
      .catch((err) => {
        setStage('error');
        setError(err.message || 'Callback submission failed');
        toast.error(err.message || 'Callback submission failed');
      });
  };

  // pollForAuthFile polls listAuthFiles up to maxAttempts (every 1.5s) until
  // it finds a non-disabled auth file whose type matches the OAuth channel.
  // Once found, it fetches the file's models via getAuthFileModels and
  // returns {file_name, email, label, models}.
  const pollForAuthFile = (attempt) => {
    const maxAttempts = 8;
    const delayMs = 1500;
    return new Promise((resolve, reject) => {
      const tryFetch = (n) => {
        listAuthFiles()
          .then((res) => {
            const files = res?.files || [];
            // Match by provider type (channel). The auth-file "type" field
            // from the server is the channel: claude, codex, xai, kimi,
            // antigravity. Skip disabled entries.
            const match = files.find((f) => {
              const ft = String(f?.type || f?.provider || '').toLowerCase();
              return ft === channel && !f?.disabled && !f?.unavailable;
            });
            if (!match) {
              if (n >= maxAttempts) {
                reject(new Error('Timed out waiting for the auth file to appear.'));
                return;
              }
              setTimeout(() => tryFetch(n + 1), delayMs);
              return;
            }
            // Auth file found — fetch the raw JSON (for tokens + all fields),
            // then fetch its models, then resolve with everything.
            const fileName = match.name || match.id || '';
            const email = match.email || '';
            const label = match.label || '';
            Promise.all([
              fetchAuthFileJSON(fileName).catch(() => null),
              getAuthFileModels(fileName).catch(() => null),
            ]).then(([rawJson, mres]) => {
              const modelList = (mres?.models || []).map((m) => {
                const model = { name: m.id || m.name || '' };
                if (m.display_name) model.display_name = m.display_name;
                return model;
              }).filter((m) => m.name);

              // Extract all available fields from the raw auth JSON so they
              // can be persisted into the upstream_providers row.
              const authData = {
                file_name: fileName,
                email: email || strVal(rawJson, 'email'),
                label,
                models: modelList,
              };
              if (rawJson) {
                // Token fields (nested "token" object OR flat top-level).
                const tokenObj = rawJson.token || {};
                authData.token_access_token =
                  strVal(rawJson, 'access_token') || strVal(tokenObj, 'access_token');
                authData.token_refresh_token =
                  strVal(rawJson, 'refresh_token') || strVal(tokenObj, 'refresh_token');
                authData.token_token_type =
                  strVal(rawJson, 'token_type') || strVal(tokenObj, 'token_type');
                authData.token_scope =
                  strVal(rawJson, 'scope') || strVal(tokenObj, 'scope');
                authData.token_expiry =
                  strVal(rawJson, 'expires_at') || strVal(rawJson, 'expire') ||
                  strVal(rawJson, 'expired') || strVal(tokenObj, 'expiry');
                if (rawJson.expired === true) authData.token_expired = true;
                // Cloak fields (Claude OAuth).
                authData.cloak_mode = strVal(rawJson, 'cloak_mode');
                if (rawJson.cloak_strict_mode === true) authData.cloak_strict_mode = true;
                if (Array.isArray(rawJson.cloak_sensitive_words)) {
                  authData.cloak_sensitive_words = rawJson.cloak_sensitive_words;
                }
                if (rawJson.cloak_cache_user_id === true || rawJson.cloak_cache_user_id === false) {
                  authData.cloak_cache_user_id = rawJson.cloak_cache_user_id;
                }
                // Pass-through extras.
                if (rawJson.disable_cooling === true) authData.disable_cooling = true;
                if (rawJson.request_retry != null) authData.request_retry = rawJson.request_retry;
                if (rawJson.tool_prefix_disabled === true) authData.tool_prefix_disabled = true;
                if (rawJson.prefix) authData.prefix = rawJson.prefix;
              }
              resolve(authData);
            });
          })
          .catch(() => {
            if (n >= maxAttempts) {
              reject(new Error('Failed to fetch auth files.'));
              return;
            }
            setTimeout(() => tryFetch(n + 1), delayMs);
          });
      };
      tryFetch(attempt);
    });
  };

  const handleCopy = () => {
    if (!authUrl) return;
    navigator.clipboard?.writeText(authUrl).then(() => {
      setCopied(true);
      setTimeout(() => setCopied(false), 1500);
    });
  };

  return (
    <div className="form-section">
      <div className="form-section__title">Connect {TYPE_LABEL[providerType] || providerType}</div>
      <div className="form-section__hint">
        Start the OAuth login flow in your browser, then paste the callback URL
        you land on after authorizing. This exchanges the code for tokens so the
        provider is ready to route requests.
      </div>

      {stage === 'idle' && (
        <div className="form__row">
          <button type="button" className="primary" onClick={handleGenerate}>
            Generate authorize URL
          </button>
        </div>
      )}

      {stage === 'loading' && <Spinner label="Requesting authorize URL…" />}

      {stage === 'fetching' && <Spinner label="Fetching credential details from server…" />}

      {stage === 'error' && (
        <>
          <div className="error-banner">{error}</div>
          <button type="button" onClick={handleGenerate}>Try again</button>
        </>
      )}

      {(stage === 'ready' || stage === 'submitting' || stage === 'done') && (
        <>
          {stage === 'done' ? (
            <div className="success-banner">
              ✓ OAuth flow completed. Save the provider to persist the credential.
            </div>
          ) : (
            <div className="form__row">
              <label className="form__label">Authorize URL</label>
              <div className="copyable" style={{ wordBreak: 'break-all', fontSize: 12 }}>{authUrl}</div>
              <div className="row gap-sm" style={{ marginTop: 6 }}>
                <button type="button" onClick={() => window.open(authUrl, '_blank', 'noopener,noreferrer')}>
                  Re-open in new tab
                </button>
                <button type="button" onClick={handleCopy}>{copied ? '✓ Copied' : 'Copy URL'}</button>
              </div>
              {flow === 'device' && userCode && (
                <div className="form__hint" style={{ marginTop: 8 }}>
                  Device code: <strong>{userCode}</strong>
                </div>
              )}
              <div className="form__hint" style={{ marginTop: 8 }}>
                Complete the login in the browser tab. After authorizing, you will
                be redirected to a callback URL — copy it from the browser address
                bar and paste it below.
              </div>
            </div>
          )}

          {stage !== 'done' && (
            <div className="form__row">
              <label className="form__label">Callback URL</label>
              <input
                type="text"
                value={callbackUrl}
                onChange={(e) => setCallbackUrl(e.target.value)}
                placeholder="https://your-server/anthropic/callback?code=…&state=…"
                spellCheck={false}
              />
              <div className="form__hint">
                Paste the full redirect URL from the browser address bar after
                completing the login. The server extracts the code and state
                automatically.
              </div>
              <div className="row gap-sm" style={{ marginTop: 8 }}>
                <button
                  type="button"
                  className="primary"
                  onClick={handleSubmitCallback}
                  disabled={stage === 'submitting' || !callbackUrl.trim()}
                >
                  {stage === 'submitting' ? 'Submitting…' : 'Complete OAuth'}
                </button>
              </div>
            </div>
          )}
        </>
      )}
    </div>
  );
}

// APIKeyEntriesEditor — multi-row editor for openai-compatibility
// api-key-entries. Each row is a PasswordInput + proxy URL input.
function APIKeyEntriesEditor({ entries, onChange }) {
  const safe = Array.isArray(entries) ? entries : [];
  function update(idx, patch) {
    onChange(safe.map((e, i) => (i === idx ? { ...e, ...patch } : e)));
  }
  function add() { onChange([...safe, { api_key: '', proxy_url: '' }]); }
  function remove(idx) { onChange(safe.filter((_, i) => i !== idx)); }

  return (
    <div className="list-editor">
      {safe.length === 0 && <div className="list-editor__empty">No API key entries. Click "+ Add key".</div>}
      {safe.map((e, idx) => (
        <div className="list-editor__row" key={idx}>
          <PasswordInput value={e.api_key} onChange={(v) => update(idx, { api_key: v })} placeholder="api key" />
          <input type="text" value={e.proxy_url || ''} onChange={(ev) => update(idx, { proxy_url: ev.target.value })}
            placeholder="proxy url (optional)" spellCheck={false} />
          <button type="button" className="list-editor__remove" onClick={() => remove(idx)}
            aria-label="Remove entry" title="Remove">×</button>
        </div>
      ))}
      <button type="button" className="list-editor__add" onClick={add}>+ Add key</button>
    </div>
  );
}

// ============================================================================
// Form construction & payload building
// ============================================================================

function buildForm(providerType, initial, carryOver) {
  const src = initial || {};
  const carry = carryOver || {};
  const base = {
    provider_type: providerType || src.provider_type || '',
    name: carry.name ?? src.name ?? '',
    label: carry.label ?? src.label ?? '',
    api_key: carry.api_key ?? src.api_key ?? '',
    base_url: carry.base_url ?? src.base_url ?? '',
    proxy_url: carry.proxy_url ?? src.proxy_url ?? '',
    prefix: carry.prefix ?? src.prefix ?? '',
    email: src.email ?? '',
    file_name: src.file_name ?? '',
    priority: carry.priority ?? src.priority ?? 0,
    disabled: src.disabled ?? false,
    websockets: src.websockets ?? false,
    rebuild_mid_system_message: src.rebuild_mid_system_message ?? false,
    experimental_cch_signing: src.experimental_cch_signing ?? false,
    disable_cooling: (src.extra_config && src.extra_config.disable_cooling) ?? false,
    headers: [],
    models: [],
    excluded_models: src.excluded_models ?? [],
    api_key_entries: [],
    // Cloak
    cloak_mode: src.cloak_mode ?? '',
    cloak_strict_mode: src.cloak_strict_mode ?? false,
    cloak_sensitive_words: src.cloak_sensitive_words ?? [],
    cloak_cache_user_id: src.cloak_cache_user_id ?? false,
    // OAuth tokens
    token_access_token: src.token_access_token ?? '',
    token_refresh_token: src.token_refresh_token ?? '',
    token_expiry: src.token_expiry ? formatRFC3339(src.token_expiry) : '',
    token_expired: src.token_expired ?? false,
  };

  // Hydrate models (camelCase shape from the API).
  if (Array.isArray(src.models) && src.models.length > 0) {
    base.models = src.models.map((m) => ({
      name: m.name || '',
      alias: m.alias || '',
      'display-name': m.display_name || m.displayName || '',
      'force-mapping': !!m.force_mapping,
      image: !!m.image,
      'input-modalities': m.input_modalities || m.inputModalities || [],
      'output-modalities': m.output_modalities || m.outputModalities || [],
    }));
  }

  // Hydrate headers map → KeyValueEditor rows.
  if (src.headers && typeof src.headers === 'object') {
    base.headers = Object.entries(src.headers).map(([key, value]) => ({ key, value: String(value) }));
  }

  // Hydrate openai-compat api_key_entries.
  if (Array.isArray(src.api_key_entries)) {
    base.api_key_entries = src.api_key_entries.map((e) => ({
      api_key: e.api_key || '',
      proxy_url: e.proxy_url || '',
    }));
  }

  return base;
}

function buildPayload(form, providerType) {
  const oauth = isOAuth(providerType);
  const openai = isOpenAI(providerType);
  const claude = isClaude(providerType);

  const headersMap = {};
  (form.headers || []).forEach(({ key, value }) => {
    const k = (key || '').trim();
    if (k) headersMap[k] = value;
  });

  const cleanedModels = (form.models || [])
    .filter((r) => r && (r.name || r.alias))
    .map((r) => {
      const m = {
        name: (r.name || '').trim(),
        alias: (r.alias || '').trim(),
        display_name: (r['display-name'] || '').trim(),
        force_mapping: !!r['force-mapping'],
      };
      if (openai) {
        if (r.image) m.image = true;
        const im = r['input-modalities'];
        if (Array.isArray(im) && im.length) m.input_modalities = im;
        const om = r['output-modalities'];
        if (Array.isArray(om) && om.length) m.output_modalities = om;
      }
      return m;
    });

  // Build extra_config from the form's toggle fields + any pre-existing
  // extra_config keys (e.g. request_retry, tool_prefix_disabled populated
  // during the OAuth connect flow).
  const extra = { ...(typeof form.extra_config === 'object' ? form.extra_config : {}) };
  if (form.disable_cooling) extra.disable_cooling = true;
  // Don't send an empty object — extra_config defaults to '{}' server-side.
  const extraConfig = Object.keys(extra).length > 0 ? extra : {};

  const payload = {
    provider_type: providerType,
    priority: Number(form.priority) || 0,
    disabled: !!form.disabled,
    prefix: (form.prefix || '').trim(),
    base_url: (form.base_url || '').trim(),
    proxy_url: (form.proxy_url || '').trim() || 'none',
    headers: headersMap,
    models: cleanedModels,
    excluded_models: form.excluded_models || [],
    extra_config: extraConfig,
  };

  if (oauth) {
    payload.email = (form.email || '').trim();
    payload.file_name = (form.file_name || '').trim();
    payload.label = (form.label || '').trim();
    payload.token_access_token = form.token_access_token || '';
    payload.token_refresh_token = form.token_refresh_token || '';
    payload.token_expired = !!form.token_expired;
    if (form.token_expiry) {
      const t = new Date(form.token_expiry);
      if (!Number.isNaN(t.getTime())) {
        payload.token_expiry = t.toISOString();
      }
    }
  } else if (openai) {
    payload.name = (form.name || '').trim();
    payload.api_key_entries = (form.api_key_entries || [])
      .filter((e) => e.api_key && e.api_key.trim())
      .map((e) => ({ api_key: e.api_key.trim(), proxy_url: (e.proxy_url || '').trim() }));
  } else {
    // API-key providers.
    payload.api_key = (form.api_key || '').trim();
  }

  if (claude) {
    payload.rebuild_mid_system_message = !!form.rebuild_mid_system_message;
    payload.experimental_cch_signing = !!form.experimental_cch_signing;
    payload.cloak_mode = form.cloak_mode || '';
    payload.cloak_strict_mode = !!form.cloak_strict_mode;
    payload.cloak_sensitive_words = form.cloak_sensitive_words || [];
    payload.cloak_cache_user_id = !!form.cloak_cache_user_id;
  }

  // Codex/xAI websockets.
  if (providerType === 'codex-api-key' || providerType === 'xai-api-key') {
    payload.websockets = !!form.websockets;
  }

  return payload;
}

// ============================================================================
// Validation
// ============================================================================

function validate(form, schema, providerType, siblingNames, isEdit) {
  const errors = {};
  for (const section of schema.sections) {
    for (const field of section.fields) {
      const v = form[field.name];
      if (field.required && !field._skipRequired && (v === '' || v === null || v === undefined ||
          (Array.isArray(v) && v.length === 0 && field.type !== 'chips'))) {
        // For arrays/toggles, empty isn't necessarily invalid. Only flag
        // text/password/number/select required-as-non-empty.
        if (['text', 'password', 'number'].includes(field.type)) {
          if (!v || (typeof v === 'string' && !v.trim())) {
            errors[field.name] = `${field.label} is required.`;
            continue;
          }
        }
      }
      if (field.validate && v) {
        const msg = field.validate(v);
        if (msg) errors[field.name] = msg;
      }
    }
  }
  // OpenAI-compat name uniqueness.
  if (isOpenAI(providerType)) {
    const name = (form.name || '').trim();
    if (name && siblingNames.includes(name)) {
      // In edit mode, the current row's own name is in siblingNames too;
      // we can't distinguish without the editing row's id in the list, so
      // only flag duplicates when there are 2+ occurrences.
      const occurrences = siblingNames.filter((n) => n === name).length;
      if (!isEdit || occurrences > 1) {
        errors.name = `Another provider already uses the name "${name}".`;
      }
    }
  }
  return errors;
}

// ============================================================================
// Helpers
// ============================================================================

function formatRFC3339(t) {
  if (!t) return '';
  try {
    const d = new Date(t);
    if (Number.isNaN(d.getTime())) return '';
    return d.toISOString().replace(/\.\d{3}Z$/, 'Z');
  } catch { return ''; }
}

// strVal safely extracts a trimmed string from an object by key. Returns ''
// for missing/null/non-string values.
function strVal(obj, key) {
  if (!obj || typeof obj !== 'object') return '';
  const v = obj[key];
  if (typeof v === 'string') return v.trim();
  if (typeof v === 'number') return String(v);
  return '';
}

function formatTime(iso) {
  try {
    const d = new Date(iso);
    if (Number.isNaN(d.getTime())) return '—';
    return d.toLocaleString();
  } catch { return '—'; }
}
