// Cooldown watch — reads the live in-memory cooldown snapshot from
// /v0/management/cooldown-providers and projects the entries relevant to the
// model the user has selected in the playground.
//
// The backend already polls its own state; we simply re-read the endpoint on
// a slow cadence and on demand. The pure selector is exported for tests.

import { useMemo } from 'react';
import { useAsync } from '../hooks/useAsync.js';
import { useAutoRefresh } from '../hooks/useAutoRefresh.js';
import { getCooldownProviders } from '../api/client.js';

const REFRESH_INTERVAL_MS = 10 * 1000;

function recordMatches(record, provider, model) {
  if (!record) return false;
  if (provider && String(record.provider || '').toLowerCase() !== String(provider).toLowerCase()) {
    return false;
  }
  // Auth-level rows (no model) apply to every model on that provider;
  // model-level rows must match the selected model exactly.
  if (record.model && model && record.model !== model) return false;
  return true;
}

// selectProviderCooldown picks the first cooldown record relevant to the
// selected provider/model. `records` is the array from
// getCooldownProviders().records (or null). Returns a normalized view or
// null when nothing is cooling.
export function selectProviderCooldown(records, { provider, model } = {}) {
  if (!Array.isArray(records)) return null;
  for (const record of records) {
    if (!recordMatches(record, provider, model)) continue;
    return {
      provider: record.provider,
      model: record.model || '',
      status: record.status || 'cooling',
      cooldownUntil: record.next_retry_after || '',
      reason: record.reason || '',
    };
  }
  return null;
}

// cooldownProviderSet collapses the snapshot into a lowercased Set of
// provider keys currently cooling, for badge rendering in the model picker.
export function cooldownProviderSet(records) {
  const out = new Set();
  if (!Array.isArray(records)) return out;
  for (const r of records) {
    if (r && r.provider) out.add(String(r.provider).toLowerCase());
  }
  return out;
}

export function useCooldownWatch(providerKey, model) {
  const snapshot = useAsync(() => getCooldownProviders(), []);
  useAutoRefresh(() => snapshot.reload(), REFRESH_INTERVAL_MS);
  const records = snapshot.data?.records || null;
  // Stable identities so downstream memoization (the model picker) does not
  // recompute on every streamed token.
  const state = useMemo(
    () => selectProviderCooldown(records, { provider: providerKey, model }),
    [records, providerKey, model],
  );
  const providers = useMemo(() => cooldownProviderSet(records), [records]);
  return { state, records, providers, loading: snapshot.loading, refresh: snapshot.reload };
}
