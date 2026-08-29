// Subscribes to the management alerts runner's CooldownStateSnapshot() and
// exposes a per-provider view + a manual refresh hook.
//
// We do not poll on our own timer: the alerts runner already polls on a
// fixed cadence (see internal/api/handlers/management/alerts_runner.go).
// This hook just reads whatever the runner has most recently published.
//
// In tests we exercise the pure selector; the React wrapper is a thin
// pass-through that reads from a context the alerts runner populates.

import { useCallback, useEffect, useState } from 'react';

// Pure selector. Exported for testing.
export function selectProviderCooldown(snapshot, providerKey) {
  if (!snapshot || !snapshot.providers || typeof snapshot.providers !== 'object') return null;
  const entry = snapshot.providers[providerKey];
  if (!entry || entry.state !== 'cooldown') return null;
  return entry;
}

// React hook. The alerts runner is expected to expose its latest snapshot
// via window.__nixllmCooldownSnapshot (set by the dashboard topbar in a
// follow-up task). Until that's wired, the hook returns null and the
// banner simply doesn't render — graceful degradation.
//
// providerKey is provider-scoped, e.g. "openai", "gemini", "claude", "codex".
export function useCooldownWatch(providerKey) {
  const [snapshot, setSnapshot] = useState(() =>
    typeof window !== 'undefined' ? window.__nixllmCooldownSnapshot || null : null,
  );

  useEffect(() => {
    if (typeof window === 'undefined') return undefined;
    const onUpdate = (e) => setSnapshot(e?.detail || window.__nixllmCooldownSnapshot || null);
    window.addEventListener('nixllm:cooldown-snapshot', onUpdate);
    return () => window.removeEventListener('nixllm:cooldown-snapshot', onUpdate);
  }, []);

  const refresh = useCallback(() => {
    if (typeof window === 'undefined') return;
    setSnapshot(window.__nixllmCooldownSnapshot || null);
  }, []);

  return { state: selectProviderCooldown(snapshot, providerKey), refresh };
}
