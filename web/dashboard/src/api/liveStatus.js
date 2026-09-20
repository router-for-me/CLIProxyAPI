// liveStatus — single source of truth for "is this provider key currently
// serving the model?" across the picker, the provider list, and the editor.
//
// Mirrors the Go helper `internal/api/handlers/management/routing_models.go
// IsProviderRowLive` EXACTLY — keep both in sync. If you change the rule
// (e.g. add OpenAI-Compat provider-level fallback), update both sides
// and extend both test suites.
//
// The shared hook polls /v0/management/upstream-providers/live-status
// (added in a follow-up; today: returns 404 — the picker can still use
// IsProviderRowLive with whatever evidence the upstream registry exposes).

import { useEffect, useRef, useState, useCallback } from 'react';
import { providerKeyIsLive as liveMatch } from '../components/modelRouteProvider.js';

/**
 * isProviderRowLive is the client-side companion to
 * IsProviderRowLive (Go). row is the provider_key, liveEvidence is
 * the array returned by the runtime registry, cooldown is an optional
 * provider-key → true map (from the cooldown management endpoint).
 *
 * Rules (mirror Go):
 *  - row must not be in cooldown
 *  - row must match liveEvidence exactly, OR
 *  - row is an OpenAI-Compat provider-level route (prefix
 *    "openai-compatible-" with no ":") and some evidence is the same
 *    provider key OR an entry key under it.
 */
export function isProviderRowLive(row, liveEvidence, cooldown) {
  if (!row) return false;
  const key = String(row).trim().toLowerCase();
  if (!key) return false;
  if (cooldown && cooldown[key]) return false;
  if (!Array.isArray(liveEvidence)) return false;
  const evidence = liveEvidence
    .map((p) => (typeof p === 'string' ? p.trim().toLowerCase() : ''))
    .filter(Boolean);
  for (const k of evidence) {
    if (k === key) return true;
  }
  const openaiPrefix = 'openai-compatible-';
  if (key.startsWith(openaiPrefix) && !key.includes(':')) {
    const prefix = key + ':';
    for (const k of evidence) {
      if (k.startsWith(prefix)) return true;
    }
  }
  return false;
}

// fetchLiveStatus queries /v0/management/upstream-providers/live-status
// (added in the Phase F3 follow-up). Until that endpoint exists, this
// returns an empty status map so callers degrade gracefully.
export async function fetchLiveStatus() {
  try {
    const r = await fetch('/v0/management/upstream-providers/live-status', { credentials: 'include' });
    if (!r.ok) return {};
    return await r.json();
  } catch {
    return {};
  }
}

// coerceLiveStatusResponse returns the id → entry rows map from the
// /live-status payload ({ rows: {...}, as_of }) so callers can index
// liveStatus[String(providerId)] directly. Returns an empty map when the
// response is missing/null/malformed so the table + health page degrade
// identically. Used by both surfaces to handle the "endpoint not yet
// shipped" case.
export function coerceLiveStatusResponse(json) {
  if (!json || typeof json !== 'object') return {};
  if (!json.rows || typeof json.rows !== 'object') return {};
  return json.rows;
}

// fetchLiveProviderKeys queries the auth manager's runtime registry for
// the providers currently serving a model. Returns an array (possibly
// empty) of provider keys.
export async function fetchLiveProviderKeys(model) {
  if (!model) return [];
  try {
    const r = await fetch(`/v0/management/cooldown/providers?model=${encodeURIComponent(model)}`, { credentials: 'include' });
    if (!r.ok) return [];
    const data = await r.json();
    return Array.isArray(data?.live) ? data.live : [];
  } catch {
    return [];
  }
}

// fetchPicker queries GET /v0/management/model-routing/picker?model=...
// and returns the parsed response. Returns null when the server returns
// 503 (PG not configured) so the picker can render a "not available" state.
export async function fetchPicker(model) {
  if (!model) return null;
  try {
    const r = await fetch(`/v0/management/model-routing/picker?model=${encodeURIComponent(model)}`, { credentials: 'include' });
    if (r.status === 503) return null;
    if (!r.ok) return null;
    return await r.json();
  } catch {
    return null;
  }
}

// pinProvider calls POST /v0/management/model-routing/pin. body is
// { model, provider_key, force? }. Returns the parsed PinResponse on
// 200, throws ApiError-shape { status, type, message } on non-2xx so
// callers can distinguish 409 (not live) from 503 (no PG).
export async function pinProvider({ model, providerKey, force = false }) {
  if (!model || !providerKey) {
    const err = new Error('model and providerKey are required');
    err.status = 400;
    err.type = 'invalid_request';
    throw err;
  }
  let r;
  try {
    r = await fetch('/v0/management/model-routing/pin', {
      method: 'POST',
      credentials: 'include',
      headers: { 'Content-Type': 'application/json' },
      body: JSON.stringify({ model, provider_key: providerKey, force }),
    });
  } catch (e) {
    const err = new Error('Network error');
    err.status = 0;
    err.type = 'network_error';
    throw err;
  }
  let payload = null;
  try {
    payload = await r.json();
  } catch {
    payload = null;
  }
  if (!r.ok) {
    const err = new Error(payload?.error?.message || `pin failed (${r.status})`);
    err.status = r.status;
    err.type = payload?.error?.type || 'unknown';
    throw err;
  }
  return payload;
}

// unpinProvider calls POST /model-routing/unpin (added in the Phase F3
// follow-up alongside the picker API). For now it's a stub that returns
// false so callers can wire the UI before the server endpoint ships.
export async function unpinProvider({ model, providerKey }) {
  // The unpin endpoint is deferred to a follow-up PR. Until then, this
  // is a no-op stub that surfaces the missing endpoint to the operator.
  const err = new Error('unpin endpoint not yet available');
  err.status = 501;
  err.type = 'not_implemented';
  throw err;
}

/**
 * useLiveStatus polls fetchLiveStatus on a 15s cadence (60s when the tab
 * is hidden, paused entirely when unmounted). Returns the latest status
 * map: { [providerKey]: { live: boolean, reason: string } }.
 */
export function useLiveStatus() {
  const [status, setStatus] = useState({});
  const timerRef = useRef(null);

  const tick = useCallback(async () => {
    const next = await fetchLiveStatus();
    setStatus(next);
    const hidden = typeof document !== 'undefined' && document.visibilityState === 'hidden';
    timerRef.current = setTimeout(tick, hidden ? 60000 : 15000);
  }, []);

  useEffect(() => {
    tick();
    return () => {
      if (timerRef.current) clearTimeout(timerRef.current);
    };
  }, [tick]);

  return status;
}

// Re-export for callers that prefer importing from the modelRouteProvider
// namespace. The helper exists there already; we re-export so picker code
// can import everything from one module.
export { liveMatch as providerKeyIsLive };