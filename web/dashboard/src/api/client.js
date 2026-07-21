// API client for the NixLLM Dashboard.
//
// All calls target the existing /v0/management routes on the CLIProxyAPI
// Go server. Authentication uses the same scheme as the official management
// UI: `Authorization: Bearer <MANAGEMENT_PASSWORD>`. The password is stored
// in localStorage after the user enters it on the login page; rotating the
// env var on the server invalidates the stored token automatically (the
// next request returns 403 and the app drops back to the login screen).

const STORAGE_KEY = 'nixllm.dashboard.token';
const API_BASE = '/v0/management';

export function getStoredToken() {
  try {
    return localStorage.getItem(STORAGE_KEY) || '';
  } catch {
    return '';
  }
}

export function setStoredToken(token) {
  try {
    if (token) {
      localStorage.setItem(STORAGE_KEY, token);
    } else {
      localStorage.removeItem(STORAGE_KEY);
    }
  } catch {
    /* ignore — private mode / quota */
  }
}

export function clearStoredToken() {
  setStoredToken('');
}

// fetchJSON — thin wrapper around fetch that:
//  - injects the Bearer auth header from localStorage,
//  - serializes JSON bodies,
//  - raises a typed ApiError on non-2xx with the server's message preserved,
//  - returns null on 204 No Content.
async function fetchJSON(path, options = {}) {
  const token = getStoredToken();
  const headers = {
    Accept: 'application/json',
    ...(options.body ? { 'Content-Type': 'application/json' } : {}),
    ...(token ? { Authorization: `Bearer ${token}` } : {}),
    ...(options.headers || {}),
  };

  const res = await fetch(`${API_BASE}${path}`, {
    ...options,
    headers,
  });

  if (res.status === 204) return null;

  const contentType = res.headers.get('content-type') || '';
  const isJSON = contentType.includes('application/json');
  const payload = isJSON ? await res.json().catch(() => null) : null;

  if (!res.ok) {
    const message = extractErrorMessage(payload, res.statusText);
    const err = new ApiError(message, res.status);
    err.payload = payload;
    throw err;
  }

  return payload;
}

function extractErrorMessage(payload, fallback) {
  if (!payload) return fallback || 'Request failed';
  if (typeof payload.error === 'string') return payload.error;
  if (payload.error && typeof payload.error.message === 'string') return payload.error.message;
  if (typeof payload.message === 'string') return payload.message;
  return fallback || 'Request failed';
}

// cpaFetch — variant of fetchJSON used by the "Manage CPA" pages. Differences
// from the private fetchJSON above:
//   - `body` may be a string (e.g. raw YAML) — sent as text/plain without
//     forcing Content-Type: application/json, so the Go server's
//     ShouldBindBody path picks the right decoder per-route.
//   - `raw: true` skips JSON parsing on success and returns the response
//     body as a string (used by /config.yaml).
// All other auth/header/error semantics are identical.
export async function cpaFetch(path, options = {}) {
  const token = getStoredToken();
  const headers = {
    Accept: 'application/json',
    ...(options.body && !options.raw ? { 'Content-Type': 'application/json' } : {}),
    ...(options.body && options.raw
      ? { 'Content-Type': options.contentType || 'text/plain; charset=utf-8' }
      : {}),
    ...(token ? { Authorization: `Bearer ${token}` } : {}),
    ...(options.headers || {}),
  };

  const res = await fetch(`${API_BASE}${path}`, { ...options, headers });

  if (res.status === 204) return null;

  if (options.raw) {
    const text = await res.text();
    if (!res.ok) {
      // try to parse the body as JSON for a friendly error
      let message = res.statusText || 'Request failed';
      try {
        const payload = JSON.parse(text);
        message = extractErrorMessage(payload, message);
      } catch { /* keep statusText */ }
      const err = new ApiError(message, res.status);
      throw err;
    }
    return text;
  }

  const contentType = res.headers.get('content-type') || '';
  const isJSON = contentType.includes('application/json');
  const payload = isJSON ? await res.json().catch(() => null) : null;

  if (!res.ok) {
    const message = extractErrorMessage(payload, res.statusText);
    const err = new ApiError(message, res.status);
    err.payload = payload;
    throw err;
  }

  return payload;
}

export class ApiError extends Error {
  constructor(message, status) {
    super(message);
    this.name = 'ApiError';
    this.status = status;
  }
}

// --- Auth -------------------------------------------------------------------

// The Go server doesn't expose a dedicated "verify token" endpoint, so the
// dashboard probes auth by GET'ing /v0/management/config. A 200 response
// means the stored token is valid; a 403 means it's stale or wrong.
export async function verifyToken() {
  await fetchJSON('/config');
  return true;
}

// --- PG-backed API Keys + Policies ------------------------------------------

export async function listAPIKeys({ page = 1, pageSize = 25, status = '' } = {}) {
  const qs = new URLSearchParams();
  qs.set('page', String(page));
  qs.set('page_size', String(pageSize));
  if (status) qs.set('status', status);
  return fetchJSON(`/api-keys-pg?${qs}`);
}

export async function getAPIKey(id) {
  return fetchJSON(`/api-keys-pg/${encodeURIComponent(id)}`);
}

// createAPIKey creates a new caller-facing API key. user_id is REQUIRED
// (LiteLLM workflow): every key is owned by an Internal User; the server
// returns 400 when it is missing. Per-key policy fields are optional —
// when a budget cap is left unset on the policy, enforcement falls back to
// the owning internal user's max_budget.
export async function createAPIKey({ name, secret, user_id, expires_at, metadata, policy }) {
  return fetchJSON('/api-keys-pg', {
    method: 'POST',
    body: JSON.stringify({ name, secret, user_id, expires_at, metadata, policy }),
  });
}

export async function patchAPIKey(id, patch) {
  return fetchJSON(`/api-keys-pg/${encodeURIComponent(id)}`, {
    method: 'PATCH',
    body: JSON.stringify(patch),
  });
}

export async function putAPIKeyPolicy(id, policy) {
  return fetchJSON(`/api-keys-pg/${encodeURIComponent(id)}/policy`, {
    method: 'PUT',
    body: JSON.stringify(policy),
  });
}

export async function regenerateAPIKey(id) {
  return fetchJSON(`/api-keys-pg/${encodeURIComponent(id)}/regenerate`, {
    method: 'POST',
  });
}

export async function deleteAPIKey(id) {
  await fetchJSON(`/api-keys-pg/${encodeURIComponent(id)}`, { method: 'DELETE' });
}

// --- Usage Stats ------------------------------------------------------------

export async function getUsageStats(params = {}) {
  const qs = new URLSearchParams();
  for (const [k, v] of Object.entries(params)) {
    if (v !== undefined && v !== null && v !== '') qs.set(k, String(v));
  }
  const suffix = qs.toString() ? `?${qs}` : '';
  return fetchJSON(`/usage-stats${suffix}`);
}

// KPI totals — single row of roll-up metrics for a filter window.
// Cheap query (no GROUP BY) — ideal for the dashboard's top-of-page cards.
export async function getUsageTotals(params = {}) {
  const qs = toUsageQS(params);
  return fetchJSON(`/usage-stats/totals${qs}`);
}

// Time-series — one bucket per interval across the filter window.
// interval is 'minute' | 'hour' | 'day'. Each point has bucket (RFC3339),
// bucket_ts (epoch seconds), request_count, token sums, cost_usd.
// Useful for sparkline / line charts over time.
export async function getUsageTimeSeries(params = {}, interval = 'hour') {
  const qs = toUsageQS({ ...params, interval });
  return fetchJSON(`/usage-stats/timeseries${qs}`);
}

// Top-N — leaderboard by dimension (model / provider / api_key_id /
// api_key_principal) and metric (request_count / total_tokens / cost_usd).
export async function getUsageTop({
  dimension = 'model',
  metric = 'request_count',
  limit = 10,
  ...filter
} = {}) {
  const qs = toUsageQS({ ...filter, dimension, metric, limit });
  return fetchJSON(`/usage-stats/top${qs}`);
}

function toUsageQS(params) {
  const qs = new URLSearchParams();
  for (const [k, v] of Object.entries(params)) {
    if (v !== undefined && v !== null && v !== '') qs.set(k, String(v));
  }
  const s = qs.toString();
  return s ? `?${s}` : '';
}

export async function getUsageSummary({ api_key_id, window = 'hourly' }) {
  const qs = new URLSearchParams({ api_key_id, window });
  return fetchJSON(`/usage-stats/summary?${qs}`);
}

export async function getUsageWindows(apiKeyId) {
  return fetchJSON(`/usage-windows/${encodeURIComponent(apiKeyId)}`);
}

// Paginated list of raw usage events. Each row carries the resolved
// non-secret key_alias (in place of the sealed api_key_principal). Used by
// the dashboard's "Recent events" table.
//   - params: same shape as getUsageTotals + page, page_size
//   - params.include may contain "cost_breakdown" to ask the server to attach
//     a per-segment dollar attribution to every row. Off by default.
export async function getUsageEvents(params = {}) {
  const qs = toUsageQS(params);
  return fetchJSON(`/usage-stats/events${qs}`);
}

// Single usage event detail. Used by the dashboard modal when an operator
// clicks a row in the events table. The single-event payload always carries
// the full cost breakdown (server-side guarantee, no include= flag needed).
export async function getUsageEvent(id) {
  return fetchJSON(`/usage-stats/events/${encodeURIComponent(id)}`);
}

// Paginated list of raw failed-attempt records (usage_errors table). Mirrors
// getUsageEvents in shape but surfaces fail_status_code and error_message so
// operators can triage request errors separately from successful responses.
export async function getUsageErrors(params = {}) {
  const qs = toUsageQS(params);
  return fetchJSON(`/usage-stats/errors${qs}`);
}

// Single failed-attempt detail. Used by the dashboard's error detail modal.
export async function getUsageError(id) {
  return fetchJSON(`/usage-stats/errors/${encodeURIComponent(id)}`);
}

// Distinct { api_keys: [{id, alias}], providers, models } observed in the
// filter window. Used by the dashboard to populate dropdown filter menus so
// the operator never types a free-text value (which is impossible against
// the sealed api_key_principal column).
export async function getUsageFilterOptions(params = {}) {
  const qs = toUsageQS(params);
  return fetchJSON(`/usage-stats/filters${qs}`);
}

// --- Internal Users (LiteLLM-style key owners) ----------------------------
//
// Internal Users are proxy-managed entities that own API keys. Budget/RPM
// enforcement runs at the user level (in addition to the per-key path), and
// the dashboard surfaces per-user benchmark metrics (spend, tokens, requests,
// latency, RPM) modeled after LiteLLM's Internal Users feature.

export async function listInternalUsers({
  page = 1,
  pageSize = 25,
  role = '',
  search = '',
  sortBy = 'spend',
  sortOrder = 'desc',
} = {}) {
  const qs = new URLSearchParams();
  qs.set('page', String(page));
  qs.set('page_size', String(pageSize));
  if (role) qs.set('role', role);
  if (search) qs.set('search', search);
  qs.set('sort_by', sortBy);
  qs.set('sort_order', sortOrder);
  return fetchJSON(`/internal-users?${qs}`);
}

// createInternalUser creates a new internal user. When payload.auto_create_key
// is not explicitly false, the server auto-provisions a default API key bound
// to the new user and returns the plaintext secret ONCE in the response body
// (fields `secret` + `api_key`). LiteLLM-equivalent of POST /user/new.
export async function createInternalUser(payload) {
  return fetchJSON('/internal-users', {
    method: 'POST',
    body: JSON.stringify(payload),
  });
}

// reconcileInternalUserSpend recomputes the user's running spend from
// usage_events and writes the SUM back to internal_users.spend. Recovers
// from IncrementSpend failures (transient PG errors during Consume).
// Optional from/to bounds scope the SUM to a window.
export async function reconcileInternalUserSpend(id, { from = '', to = '' } = {}) {
  const qs = new URLSearchParams();
  if (from) qs.set('from', from);
  if (to) qs.set('to', to);
  const suffix = qs.toString() ? `?${qs}` : '';
  return fetchJSON(`/internal-users/${encodeURIComponent(id)}/reconcile-spend${suffix}`, {
    method: 'POST',
  });
}

// reconcileAllSpend runs a bulk reconciliation across every user with events
// newer than `since`. Empty since recomputes the entire history (slow).
export async function reconcileAllSpend(since = '') {
  const qs = new URLSearchParams();
  if (since) qs.set('since', since);
  const suffix = qs.toString() ? `?${qs}` : '';
  return fetchJSON(`/internal-users/reconcile-all${suffix}`, { method: 'POST' });
}

// getInternalUserModelSpend returns per-model aggregations (cost / tokens /
// request count) computed on-the-fly from usage_events. Optional from/to
// bounds scope the window. Ordered by cost_usd descending.
export async function getInternalUserModelSpend(id, { from = '', to = '' } = {}) {
  const qs = new URLSearchParams();
  if (from) qs.set('from', from);
  if (to) qs.set('to', to);
  const suffix = qs.toString() ? `?${qs}` : '';
  return fetchJSON(`/internal-users/${encodeURIComponent(id)}/model-spend${suffix}`);
}

// getInternalUserModels returns the configured allowed_models grant list +
// optional per-model max-budget JSON (stored in metadata.model_max_budgets)
// + the realized per-model spend computed on-the-fly from usage_events.
export async function getInternalUserModels(id) {
  return fetchJSON(`/internal-users/${encodeURIComponent(id)}/models`);
}

export async function getInternalUser(id) {
  return fetchJSON(`/internal-users/${encodeURIComponent(id)}`);
}

export async function patchInternalUser(id, patch) {
  return fetchJSON(`/internal-users/${encodeURIComponent(id)}`, {
    method: 'PATCH',
    body: JSON.stringify(patch),
  });
}

export async function deleteInternalUser(id) {
  await fetchJSON(`/internal-users/${encodeURIComponent(id)}`, { method: 'DELETE' });
}

export async function resetInternalUserSpend(id) {
  return fetchJSON(`/internal-users/${encodeURIComponent(id)}/reset-spend`, {
    method: 'POST',
  });
}

// Per-user benchmark endpoints — mirror /usage-stats but scoped to a user.

export async function getInternalUserTotals(id, params = {}) {
  const qs = toUsageQS(params);
  return fetchJSON(`/internal-users/${encodeURIComponent(id)}/totals${qs}`);
}

export async function getInternalUserTimeSeries(id, params = {}, interval = 'hour') {
  const qs = toUsageQS({ ...params, interval });
  return fetchJSON(`/internal-users/${encodeURIComponent(id)}/timeseries${qs}`);
}

export async function getInternalUserTop(id, {
  dimension = 'model',
  metric = 'request_count',
  limit = 10,
  ...filter
} = {}) {
  const qs = toUsageQS({ ...filter, dimension, metric, limit });
  return fetchJSON(`/internal-users/${encodeURIComponent(id)}/top${qs}`);
}

export async function getInternalUserEvents(id, params = {}) {
  const qs = toUsageQS(params);
  return fetchJSON(`/internal-users/${encodeURIComponent(id)}/events${qs}`);
}

export async function getInternalUserWindows(id) {
  return fetchJSON(`/internal-users/${encodeURIComponent(id)}/windows`);
}

// List of an internal user's owned API keys (with key_alias + status).
export async function listInternalUserKeys(id, { page = 1, pageSize = 50, status = '' } = {}) {
  const qs = new URLSearchParams();
  qs.set('page', String(page));
  qs.set('page_size', String(pageSize));
  if (status) qs.set('status', status);
  return fetchJSON(`/internal-users/${encodeURIComponent(id)}/keys?${qs}`);
}

// Attach (or detach with empty keyId) an existing API key to this user.
export async function attachKeyToUser(userId, keyId) {
  return fetchJSON(
    `/internal-users/${encodeURIComponent(userId)}/keys/${encodeURIComponent(keyId)}/attach`,
    { method: 'POST' },
  );
}

export async function detachKeyFromUser(userId, keyId) {
  await fetchJSON(
    `/internal-users/${encodeURIComponent(userId)}/keys/${encodeURIComponent(keyId)}`,
    { method: 'DELETE' },
  );
}

// Leaderboard: top-N internal users by spend (default running spend) or, when
// from/to params are supplied, by the chosen metric over that window.
export async function getInternalUsersLeaderboard({
  limit = 10,
  metric = 'cost_usd',
  from = '',
  to = '',
} = {}) {
  const qs = new URLSearchParams();
  qs.set('limit', String(limit));
  qs.set('metric', metric);
  if (from) qs.set('from', from);
  if (to) qs.set('to', to);
  return fetchJSON(`/internal-users/leaderboard?${qs}`);
}

// --- Models Catalog + Pricing -----------------------------------------------

export async function listModelsCatalog({
  page = 1,
  pageSize = 25,
  provider = '',
  officialProvider = '',
  availableOnly = true,
} = {}) {
  const qs = new URLSearchParams();
  qs.set('page', String(page));
  qs.set('page_size', String(pageSize));
  if (provider) qs.set('provider', provider);
  if (officialProvider) qs.set('official_provider', officialProvider);
  // Default to filtering down to models the active registry reports as
  // available — the dashboard should show live models only. Pass
  // availableOnly:false to browse the full persisted catalog.
  qs.set('available_only', availableOnly ? 'true' : 'false');
  return fetchJSON(`/models-catalog?${qs}`);
}

export async function getModelsCatalogCount() {
  return fetchJSON('/models-catalog/count');
}

export async function getModelPricing(id) {
  return fetchJSON(`/models-catalog/${encodeURIComponent(id)}/pricing`);
}

export async function putModelPricing(id, pricing) {
  return fetchJSON(`/models-catalog/${encodeURIComponent(id)}/pricing`, {
    method: 'PUT',
    body: JSON.stringify(pricing),
  });
}

// syncModelsFromV1 triggers an in-process sync from the caller-facing
// /v1/models endpoint into the models_catalog PostgreSQL table. The server
// auto-picks the first caller API key from config_store (the `api-keys:` YAML
// list) — operators do not need to paste one. Pass a `callerKey` only if you
// want to override the auto-pick. Returns { synced, caller_key_prefix }.
//
// After a successful sync, re-fetch the catalog with listModelsCatalog() to
// pick up the new rows. The dashboard's "available only" toggle now means
// "I want the catalog that came from the latest /v1/models sync" — no extra
// client-side join is needed.
export async function syncModelsFromV1(callerKey = '') {
  const body = callerKey ? { caller_key: callerKey } : {};
  return fetchJSON('/models-catalog/sync-from-v1', {
    method: 'POST',
    body: JSON.stringify(body),
  });
}

// previewPricingSync fetches, for every model currently in models_catalog,
// the suggested prices from every source the bundled pricing catalog knows
// (openai-official, anthropic-official, openrouter, cloudflare, etc). The
// call is read-only — nothing is written to model_pricing until the operator
// POSTs their picks via applyPricingSync.
export async function previewPricingSync() {
  return fetchJSON('/models-catalog/sync-pricing-preview', {
    method: 'POST',
  });
}

// applyPricingSync commits the operator's picks. Each selection references
// one source's published prices for one model (or source="manual" with
// inline numeric overrides). Returns { applied, requested, results: [...] }.
export async function applyPricingSync(selections) {
  return fetchJSON('/models-catalog/sync-pricing-apply', {
    method: 'POST',
    body: JSON.stringify({ selections }),
  });
}

// --- Error message customization -------------------------------------------
//
// The dashboard lets operators override the JSON error body the proxy
// returns for each HTTP status code. Listing returns one entry per known
// code plus any operator-added custom codes, each tagged with is_default so
// the UI can show "Reset to default" buttons only for overridden rows.

export async function listErrorMessages() {
  return fetchJSON('/error-messages');
}

export async function getErrorMessage(code) {
  return fetchJSON(`/error-messages/${encodeURIComponent(code)}`);
}

export async function putErrorMessage(code, body) {
  return fetchJSON(`/error-messages/${encodeURIComponent(code)}`, {
    method: 'PUT',
    body: JSON.stringify(body),
  });
}

export async function deleteErrorMessage(code) {
  return fetchJSON(`/error-messages/${encodeURIComponent(code)}`, {
    method: 'DELETE',
  });
}

// previewErrorMessage POSTs a candidate (title, message, body_template,
// details) to the server and receives back the exact JSON body the proxy
// would emit for that status code. Used by the live preview pane so the
// operator sees the rendered response before committing.
export async function previewErrorMessage(code, body) {
  return fetchJSON(`/error-messages/preview`, {
    method: 'POST',
    body: JSON.stringify({ ...body, code }),
  });
}

// --- Model entry (per-model catalog edit) -----------------------------------
//
// The dashboard can edit individual catalog rows: rename display fields,
// adjust context_length / max_completion_tokens, add a new user-defined
// model not present in the upstream registry, or prune a stale entry. These
// endpoints key on (id, provider) — both segments must be URL-encoded when
// they contain slashes or special chars.

export async function getModelEntry(id, provider) {
  return fetchJSON(`/models-catalog/entry/${enc(id)}/${enc(provider)}`);
}

export async function putModelEntry(id, provider, entry) {
  return fetchJSON(`/models-catalog/entry/${enc(id)}/${enc(provider)}`, {
    method: 'PUT',
    body: JSON.stringify(entry),
  });
}

export async function deleteModelEntry(id, provider) {
  await fetchJSON(`/models-catalog/entry/${enc(id)}/${enc(provider)}`, {
    method: 'DELETE',
  });
}

function enc(s) {
  return encodeURIComponent(String(s ?? ''));
}

// --- Health -----------------------------------------------------------------

export async function getHealth() {
  // /healthz is unauthenticated; useful as a liveness check before showing login.
  const res = await fetch('/healthz');
  if (!res.ok) throw new ApiError('server unreachable', res.status);
  return true;
}

// --- Caller-side /v1/models (sync source for "available only") -------------

// The dashboard's "Show available only" filter on the Models Catalog page
// reflects what callers can actually invoke right now. The authoritative
// source for that is the caller-facing /v1/models endpoint — it returns the
// registry's live list filtered by the supplied caller API key's own policy
// (model allow/deny lists on the caller key, if any).
//
// Storage: the caller key is kept in localStorage under a separate key from
// the management token so rotating the dashboard login does not clear it.
const CALLER_KEY_STORAGE = 'nixllm.dashboard.callerKey';

export function getStoredCallerKey() {
  try {
    return localStorage.getItem(CALLER_KEY_STORAGE) || '';
  } catch {
    return '';
  }
}

export function setStoredCallerKey(key) {
  try {
    if (key) localStorage.setItem(CALLER_KEY_STORAGE, key);
    else localStorage.removeItem(CALLER_KEY_STORAGE);
  } catch {
    /* ignore */
  }
}

// fetchV1Models calls GET /v1/models with the supplied caller API key as
// Bearer auth. Returns the parsed `data[]` array of model entries. Throws
// ApiError on non-2xx (401/403 means the key is invalid or policy-rejected).
export async function fetchV1Models(callerKey) {
  if (!callerKey) {
    throw new ApiError('A caller API key is required to sync available models.', 401);
  }
  const res = await fetch('/v1/models', {
    headers: { Authorization: `Bearer ${callerKey}` },
  });
  if (!res.ok) {
    let message = `Request failed (${res.status})`;
    try {
      const payload = await res.json();
      if (payload?.error?.message) message = payload.error.message;
      else if (typeof payload?.error === 'string') message = payload.error;
    } catch {
      /* ignore body parse errors */
    }
    throw new ApiError(message, res.status);
  }
  const body = await res.json();
  return Array.isArray(body?.data) ? body.data : [];
}

// --- Manage CPA: full server Config + AI Provider management ---------------
//
// These helpers expose every Config and AI-Provider surface that the CPA
// (CLIProxyAPI) Go server manages through /v0/management. They are consumed
// by the dashboard's "Manage CPA" menu (Overview / AI Providers / Raw
// Config tabs). All endpoints already exist on the server — this file just
// gives the SPA a typed, ergonomic wrapper layer.
//
// Conventions:
//   - GET endpoints return parsed JSON via cpaFetch (default path).
//   - Raw-YAML round-trip uses cpaFetch(..., { raw: true, method: 'PUT' })
//     so the body is sent as text and the response is read as text.
//   - Provider-key PATCH helpers accept the exact {index|match, value} shape
//     documented in the Go handler so the dashboard can drive the same
//     field-level updates without re-encoding the full list.

export async function getCpaConfig() {
  return cpaFetch('/config');
}

export async function getCpaConfigYaml() {
  return cpaFetch('/config.yaml', { raw: true });
}

export async function putCpaConfigYaml(yamlText) {
  return cpaFetch('/config.yaml', {
    method: 'PUT',
    raw: true,
    body: yamlText,
  });
}

export async function getCpaLatestVersion() {
  return cpaFetch('/latest-version');
}

// --- Auth-files (provider accounts) ----------------------------------------

export async function listAuthFiles() {
  return cpaFetch('/auth-files');
}

export async function getAuthFileModels(name) {
  return cpaFetch(`/auth-files/models?name=${encodeURIComponent(name)}`);
}

export async function getAuthFileDownloadUrl(name) {
  // Auth download is a single GET against /v0/management/auth-files/download
  // with ?name=. We return the URL string so the dashboard can hand it to a
  // plain <a download> anchor; the bearer token in localStorage is not
  // auto-injected for anchor navigation, so the operator gets a 401 and
  // the form is intentionally surfaced for copy-paste. Use the fetch-based
  // helper below when blob download is required.
  return `${API_BASE}/auth-files/download?name=${encodeURIComponent(name)}`;
}

export async function downloadAuthFile(name) {
  // Fetch the file with auth, then trigger a client-side download via a
  // Blob URL. This keeps the bearer token out of the URL bar.
  const token = getStoredToken();
  const res = await fetch(
    `${API_BASE}/auth-files/download?name=${encodeURIComponent(name)}`,
    { headers: token ? { Authorization: `Bearer ${token}` } : {} },
  );
  if (!res.ok) {
    let message = `Download failed (${res.status})`;
    try {
      const payload = await res.json();
      message = extractErrorMessage(payload, message);
    } catch { /* ignore */ }
    throw new ApiError(message, res.status);
  }
  return res.blob();
}

export async function patchAuthFileStatus({ name, disabled }) {
  return cpaFetch('/auth-files/status', {
    method: 'PATCH',
    body: JSON.stringify({ name, disabled }),
  });
}

export async function deleteAuthFile(names) {
  const list = Array.isArray(names) ? names : [names];
  if (list.length === 0) {
    throw new ApiError('At least one auth-file name is required', 400);
  }
  const qs = list.map((n) => `name=${encodeURIComponent(n)}`).join('&');
  return cpaFetch(`/auth-files?${qs}`, { method: 'DELETE' });
}

// --- Provider-key config lists (Gemini, Claude, Codex, xAI, Vertex, OpenAI) --

function providerListEndpoint(provider) {
  // The server uses kebab-case path segments; expose a single mapper to keep
  // the per-provider helpers in this file declarative.
  return {
    gemini: 'gemini-api-key',
    interactions: 'interactions-api-key',
    claude: 'claude-api-key',
    codex: 'codex-api-key',
    xai: 'xai-api-key',
    vertex: 'vertex-api-key',
    openai: 'openai-compatibility',
  }[provider];
}

function providerDeleteQuery({ index, apiKey, baseUrl, name }) {
  const qs = new URLSearchParams();
  if (index !== undefined && index !== null) qs.set('index', String(index));
  if (apiKey) qs.set('api-key', apiKey);
  if (baseUrl) qs.set('base-url', baseUrl);
  if (name) qs.set('name', name);
  const s = qs.toString();
  return s ? `?${s}` : '';
}

export async function getProviderKeys(provider) {
  const path = providerListEndpoint(provider);
  if (!path) throw new ApiError(`Unknown provider: ${provider}`, 400);
  return cpaFetch(`/${path}`);
}

export async function putProviderKeys(provider, list) {
  const path = providerListEndpoint(provider);
  if (!path) throw new ApiError(`Unknown provider: ${provider}`, 400);
  return cpaFetch(`/${path}`, {
    method: 'PUT',
    body: JSON.stringify({ items: list }),
  });
}

export async function patchProviderKey(provider, { index, match, value }) {
  const path = providerListEndpoint(provider);
  if (!path) throw new ApiError(`Unknown provider: ${provider}`, 400);
  const body = {};
  if (index !== undefined && index !== null) body.index = index;
  if (match) body.match = match;
  body.value = value;
  return cpaFetch(`/${path}`, {
    method: 'PATCH',
    body: JSON.stringify(body),
  });
}

export async function deleteProviderKey(provider, criteria) {
  const path = providerListEndpoint(provider);
  if (!path) throw new ApiError(`Unknown provider: ${provider}`, 400);
  return cpaFetch(`/${path}${providerDeleteQuery(criteria)}`, { method: 'DELETE' });
}

// --- OAuth connect (per provider) ------------------------------------------
//
// Each "Connect" button in the providers tab calls one of these to fetch the
// authorize URL. The server returns {url, state, ...} immediately; the user
// completes auth in their own browser tab. After the flow, the operator
// clicks Refresh in the providers table to see the new auth file.

const OAUTH_PROVIDERS = ['anthropic', 'codex', 'antigravity', 'kimi', 'xai'];

export function listOAuthProviders() {
  return [...OAUTH_PROVIDERS];
}

export async function requestOAuthUrl(provider, { isWebUI = true } = {}) {
  if (!OAUTH_PROVIDERS.includes(provider)) {
    throw new ApiError(`Unsupported OAuth provider: ${provider}`, 400);
  }
  const qs = isWebUI ? '?is_webui=1' : '';
  return cpaFetch(`/${provider}-auth-url${qs}`);
}

// --- Fetch / Apply Models --------------------------------------------------
//
// "Fetch models" surfaces the set of models the registry (or a remote
// OpenAI-Compat upstream) currently exposes for a given provider.
// "Apply models" writes the operator's pick back into config.yaml as the
// provider's `models:` list.
//
// Two fetch modes:
//   1. Registry-backed: GET /v0/management/auth-files/models?name=<id>
//      — used for OAuth/static-key providers whose auth files the in-memory
//      registry already tracks (gemini, claude, codex, xai, vertex, ...).
//   2. Remote probe: GET <base_url>/v1/models with a caller-supplied
//      bearer token — used for OpenAI-Compat entries where the proxy itself
//      has no live auth for that endpoint.
//
// Apply always goes through the existing per-provider PATCH endpoints so
// the server's sanitize + persist + reload pipeline is the single source of
// truth (no PUT-the-whole-list, no race with concurrent edits).

// fetchProviderModelsFromAuth — wrapper around the auth-files/models
// endpoint that returns a normalized list of {id, display_name, type,
// owned_by, context_length, max_completion_tokens} records.
export async function fetchProviderModelsFromAuth(authName) {
  const res = await getAuthFileModels(authName);
  const list = Array.isArray(res?.models) ? res.models : [];
  return list.map((m) => ({
    id: m.id,
    display_name: m.display_name || m.displayName || '',
    type: m.type || '',
    owned_by: m.owned_by || m.ownedBy || '',
    context_length: m.context_length || m.contextLength || 0,
    max_completion_tokens: m.max_completion_tokens || m.maxCompletionTokens || 0,
    source: 'auth-registry',
  }));
}

// fetchOpenAICompatModels — server-side probe of <base_url>/v1/models.
//
// Routes through POST /v0/management/remote-probe/models so the browser
// is not blocked by CORS. The Go server does the upstream call on the
// operator's behalf, then returns a normalized list of model records.
//
// For safety the server enforces an http(s) scheme on base_url, caps
// the response body at 4 MiB, and uses an 8s timeout. The dashboard
// receives the same shape as before so the inline component does not
// need to change.
export async function fetchOpenAICompatModels({ baseUrl, apiKey, name }) {
  const res = await cpaFetch('/remote-probe/models', {
    method: 'POST',
    // The dashboard ALSO sends `name` so the server can resolve the
    // per-provider credential from cfg.OpenAICompatibility[*] when the
    // dashboard's locally-known apiKey is empty, stale, or read from a
    // JSON path that the dashboard could not normalize (kebab-case
    // `api-key-entries[*].api-key` vs. the JS underscore convention).
    // When `name` matches a configured entry the server overrides
    // body.api_key with the entry's first configured key, so the
    // upstream probe uses the correct per-provider credential.
    body: JSON.stringify({
      base_url: baseUrl,
      api_key: apiKey || '',
      ...(name ? { name } : {}),
    }),
  });
  const list = Array.isArray(res?.models) ? res.models : [];
  return list.map((m) => ({
    id: m.id,
    display_name: m.display_name || '',
    type: m.type || '',
    owned_by: m.owned_by || '',
    context_length: m.context_length || 0,
    max_completion_tokens: m.max_completion_tokens || 0,
    source: 'openai-compat',
  }));
}

// applyProviderModels — write the operator's selected models into a
// provider's config list.
//
// For OAuth/static-key providers the server's PATCH endpoint accepts a
// `value.models` field, so we issue a PATCH that ONLY touches the models
// list (other fields are preserved verbatim).
//
// For OpenAI-Compat we PUT the full entry back because the existing
// openai-compatibility endpoint doesn't expose an "append models to entry X"
// sub-route; the dashboard already does the same thing for the manual
// "+ Add" flow, so this stays consistent.
export async function applyProviderModels(provider, { index, name, models }) {
  if (provider === 'openai') {
    // Read-modify-write the entry: keep everything else, replace the models.
    const current = await getProviderKeys('openai');
    const list = extractOpenAIList(current);
    let target = -1;
    if (typeof index === 'number' && index >= 0 && index < list.length) {
      target = index;
    } else if (name) {
      target = list.findIndex((it) => it?.name === name);
    }
    if (target < 0) {
      throw new ApiError(`OpenAI-Compat entry not found (index=${index}, name=${name}).`, 404);
    }
    const next = list.slice();
    next[target] = { ...next[target], models: cleanOpenAIModels(models) };
    return putProviderKeys('openai', next);
  }
  // OAuth / static-key providers.
  const body = { value: { models: cleanCompatModels(models, provider) } };
  if (typeof index === 'number') body.index = index;
  else if (name) body.match = name;
  return patchProviderKey(provider, body);
}

function extractOpenAIList(payload) {
  if (!payload || typeof payload !== 'object') return [];
  const arr = payload['openai-compatibility'];
  return Array.isArray(arr) ? arr : [];
}

// cleanCompatModels — drop empty ids, keep only fields the per-provider
// PATCH endpoint understands. The Go handlers accept the full struct shape
// of each provider's Model type; we keep {name, alias, display-name,
// force-mapping} which the server normalizes for every provider.
function cleanCompatModels(models, _provider) {
  if (!Array.isArray(models)) return [];
  return models
    .filter((m) => m && m.id)
    .map((m) => ({
      name: m.id,
      ...(m.alias ? { alias: m.alias } : {}),
      ...(m.display_name ? { 'display-name': m.display_name } : {}),
      ...(m.force_mapping ? { 'force-mapping': true } : {}),
    }));
}

function cleanOpenAIModels(models) {
  if (!Array.isArray(models)) return [];
  return models
    .filter((m) => m && m.id)
    .map((m) => ({
      name: m.id,
      ...(m.alias ? { alias: m.alias } : {}),
      ...(m.display_name ? { 'display-name': m.display_name } : {}),
      ...(m.force_mapping ? { 'force-mapping': true } : {}),
    }));
}
