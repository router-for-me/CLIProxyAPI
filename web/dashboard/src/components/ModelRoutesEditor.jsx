import React, { useEffect, useState } from 'react';
import { getModelProviders, listUpstreamProviders } from '../api/client.js';

// ModelRoutesEditor — reusable per-model upstream routing editor.
//
// Renders one row per concrete (non-wildcard) allowed model and lets the
// operator toggle which configured upstream providers serve it. A route
// entry pins the model to a subset of providers (no failover outside the
// pinned set); an empty selection means "use the registry default
// round-robin".
//
// Extracted from PolicyForm.jsx so the ModelGroup editor can reuse the exact
// same UX (groups also carry model_routes that are honored when the group is
// attached to an API-key policy — per-user routing is intentionally ignored).
//
// Props:
//   allowedModels (string[]) — the model grant list driving which rows show
//     up. Wildcards (e.g. "gpt-4*") are excluded from routing because they
//     cannot be pinned to a concrete provider set.
//   routes ({ model, providers }[]) — the current routes.
//   onChange (routes) — called with the updated route list on every toggle.
export default function ModelRoutesEditor({ allowedModels, routes, onChange }) {
  // Map of model id -> upstream providers that actually serve it (from the
  // in-memory registry). Fetched lazily per routable allowed model.
  const [providersByModel, setProvidersByModel] = useState({});
  // All known upstream providers' executor keys (provider_key) sourced from
  // the upstream_providers table. Merged with each model's live providers so
  // the picker offers every upstream that could serve the model, not just
  // the ones with live auths right now.
  const [allUpstreamProviderKeys, setAllUpstreamProviderKeys] = useState(null);

  const routableModels = (allowedModels || []).filter((m) => m && !m.endsWith('*'));

  useEffect(() => {
    let cancelled = false;
    (async () => {
      try {
        const res = await listUpstreamProviders();
        const rows = Array.isArray(res?.providers) ? res.providers : [];
        const keys = rows
          .filter((r) => !r.disabled)
          .map((r) => (typeof r.provider_key === 'string' ? r.provider_key : ''))
          .filter((k) => k)
          .sort((a, b) => a.localeCompare(b));
        if (!cancelled) setAllUpstreamProviderKeys(keys);
      } catch {
        if (!cancelled) setAllUpstreamProviderKeys([]);
      }
    })();
    return () => { cancelled = true; };
  }, []);

  useEffect(() => {
    let cancelled = false;
    (async () => {
      const next = { ...providersByModel };
      await Promise.all(
        routableModels.map(async (model) => {
          if (next[model]) return;
          try {
            const res = await getModelProviders(model);
            next[model] = Array.isArray(res?.providers) ? res.providers : [];
          } catch {
            next[model] = [];
          }
        }),
      );
      if (!cancelled) setProvidersByModel(next);
    })();
    return () => { cancelled = true; };
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [(allowedModels || []).join('|')]);

  function routeProviderChoices(model) {
    const live = providersByModel[model] || [];
    const seen = new Set();
    const out = [];
    for (const p of live) {
      const key = String(p || '').trim();
      if (!key || seen.has(key)) continue;
      seen.add(key);
      out.push(key);
    }
    for (const key of allUpstreamProviderKeys || []) {
      if (!key || seen.has(key)) continue;
      seen.add(key);
      out.push(key);
    }
    return out;
  }

  function routeProvidersFor(model) {
    const r = (routes || []).find((x) => x.model === model);
    return r && Array.isArray(r.providers) ? r.providers : [];
  }

  function toggleRouteProvider(model, provider) {
    const next = (routes || []).map((x) => ({ ...x, providers: [...x.providers] }));
    let entry = next.find((x) => x.model === model);
    if (!entry) {
      entry = { model, providers: [] };
      next.push(entry);
    }
    const idx = entry.providers.indexOf(provider);
    if (idx >= 0) entry.providers.splice(idx, 1);
    else entry.providers.push(provider);
    onChange(next.filter((x) => x.providers.length > 0));
  }

  if (routableModels.length === 0) return null;

  return (
    <div className="form__row">
      <label className="form__label">Per-model routing</label>
      <div className="muted" style={{ marginBottom: 8 }}>
        Pin each model to specific upstream providers. Empty = use the default
        round-robin across all serving providers (no failover outside a pinned
        single-provider set).
      </div>
      <div className="model-routes">
        {routableModels.map((model) => {
          const selected = routeProvidersFor(model);
          const liveLoaded = model in providersByModel;
          const choices = routeProviderChoices(model);
          const loading = !liveLoaded && allUpstreamProviderKeys === null;
          const liveLoadedAndEmpty = liveLoaded && (providersByModel[model] || []).length === 0;
          const catalogEmpty =
            allUpstreamProviderKeys !== null && allUpstreamProviderKeys.length === 0;
          return (
            <div className="model-routes__row" key={model}>
              <div className="model-routes__model mono">{model}</div>
              <div className="model-routes__providers">
                {loading && <span className="muted">Loading providers…</span>}
                {!loading && choices.length === 0 && liveLoadedAndEmpty && catalogEmpty && (
                  <span className="muted">No live providers serve this model, and no upstream providers are configured.</span>
                )}
                {!loading && choices.length === 0 && liveLoadedAndEmpty && !catalogEmpty && (
                  <span className="muted">No live providers serve this model; pin to a configured upstream below.</span>
                )}
                {choices.map((p) => {
                  const on = selected.includes(p);
                  const isLive = liveLoaded && (providersByModel[model] || []).includes(p);
                  return (
                    <button
                      type="button"
                      key={p}
                      className={`chip ${on ? 'chip--selected' : ''} mono`}
                      onClick={() => toggleRouteProvider(model, p)}
                      title={isLive ? 'Live provider (currently serving this model)' : 'Configured upstream (no live auth right now)'}
                    >
                      {p}{isLive ? '' : ' *'}
                    </button>
                  );
                })}
              </div>
              <div className="muted model-routes__hint">
                {selected.length === 0 ? 'default' : selected.join(', ')}
              </div>
            </div>
          );
        })}
      </div>
    </div>
  );
}
