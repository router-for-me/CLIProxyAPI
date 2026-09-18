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