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

export async function createAPIKey({ name, secret, expires_at, metadata, policy }) {
  return fetchJSON('/api-keys-pg', {
    method: 'POST',
    body: JSON.stringify({ name, secret, expires_at, metadata, policy }),
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

// Distinct { api_keys: [{id, alias}], providers, models } observed in the
// filter window. Used by the dashboard to populate dropdown filter menus so
// the operator never types a free-text value (which is impossible against
// the sealed api_key_principal column).
export async function getUsageFilterOptions(params = {}) {
  const qs = toUsageQS(params);
  return fetchJSON(`/usage-stats/filters${qs}`);
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
