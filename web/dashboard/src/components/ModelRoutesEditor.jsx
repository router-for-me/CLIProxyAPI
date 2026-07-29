import React, { useEffect, useMemo, useRef, useState } from 'react';
import { getModelProviders, listUpstreamProviders } from '../api/client.js';
import ModelRouteConfigSection from './ModelRouteConfigSection.jsx';

// ModelRoutesEditor — reusable per-model upstream routing editor.
//
// Renders one card per concrete (non-wildcard) allowed model and lets the
// operator toggle which configured upstream providers serve it. Beyond the
// allowlist, each route optionally carries a strategy + per-provider priority
// so an operator can express "prefer provider A, fall back to B":
//
//   strategy=""         → inherit global routing.strategy (round-robin).
//   strategy="priority" → stay on the highest-priority provider until it is
//                          exhausted/cooldown, then descend. Premium-first.
//   strategy="failover" → start at the highest-priority provider; the conductor
//                          walks to the next provider on upstream errors
//                          (5xx, 429, cooldown). Zero downtime.
//
// Extracted from PolicyForm.jsx; reused by the ModelGroup editor. The actual
// card body (provider chips + strategy segmented control) lives in
// ModelRouteConfigSection so the ModelGroup edit modal renders the identical
// workflow for a single model.
//
// Props:
//   allowedModels (string[]) — grant list driving which rows show up.
//     Wildcards (e.g. "gpt-4*") are excluded from routing because they
//     cannot be pinned to a concrete provider set.
//   routes ({ model, providers, strategy?, priorities? }[]) — current routes.
//   onChange (routes) — called with the updated route list on every change.

export default function ModelRoutesEditor({ allowedModels, routes, onChange }) {
  const [providersByModel, setProvidersByModel] = useState({});
  const [allUpstreamProviderKeys, setAllUpstreamProviderKeys] = useState(null);
  const [query, setQuery] = useState('');
  const [bulkOpen, setBulkOpen] = useState(false);
  const bulkRef = useRef(null);

  useEffect(() => {
    if (!bulkOpen) return undefined;
    function onDocClick(e) {
      if (bulkRef.current && !bulkRef.current.contains(e.target)) {
        setBulkOpen(false);
      }
    }
    function onKey(e) {
      if (e.key === 'Escape') setBulkOpen(false);
    }
    document.addEventListener('mousedown', onDocClick);
    document.addEventListener('keydown', onKey);
    return () => {
      document.removeEventListener('mousedown', onDocClick);
      document.removeEventListener('keydown', onKey);
    };
  }, [bulkOpen]);

  const routableModels = useMemo(
    () => (allowedModels || []).filter((m) => m && !m.endsWith('*')),
    [allowedModels],
  );

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

  function routeProvidersFor(model) {
    const r = (routes || []).find((x) => x.model === model);
    return r && Array.isArray(r.providers) ? r.providers : [];
  }

  function routeStrategyFor(model) {
    const r = (routes || []).find((x) => x.model === model);
    return r && typeof r.strategy === 'string' ? r.strategy : '';
  }

  function mutateRoute(model, fn) {
    const next = (routes || []).map((x) => ({
      ...x,
      providers: [...x.providers],
      priorities: Array.isArray(x.priorities) ? x.priorities.map((p) => ({ ...p })) : [],
    }));
    let entry = next.find((x) => x.model === model);
    if (!entry) {
      entry = { model, providers: [] };
      next.push(entry);
    }
    fn(entry);
    onChange(next.filter((x) => x.providers.length > 0 || x.strategy));
  }

  function mutateAll(fn) {
    const seen = new Set();
    const next = [];
    for (const model of routableModels) {
      const existing = (routes || []).find((x) => x.model === model);
      const entry = {
        model,
        providers: Array.isArray(existing?.providers) ? [...existing.providers] : [],
        strategy: typeof existing?.strategy === 'string' ? existing.strategy : '',
        priorities: Array.isArray(existing?.priorities) ? existing.priorities.map((p) => ({ ...p })) : [],
      };
      fn(entry);
      if (entry.providers.length > 0 || entry.strategy) {
        next.push(entry);
        seen.add(model);
      }
    }
    // Preserve routes for models no longer in allowedModels (harmless; filtered
    // at formToGroup time anyway).
    for (const r of routes || []) {
      if (r && r.model && !seen.has(r.model)) next.push({ ...r, providers: [...r.providers], priorities: Array.isArray(r.priorities) ? r.priorities.map((p) => ({ ...p })) : [] });
    }
    onChange(next);
  }

  function setStrategyForAll(strategy) {
    mutateAll((entry) => {
      if (entry.providers.length === 0) return;
      entry.strategy = strategy;
      if (!strategy) entry.priorities = [];
    });
  }

  function clearAllRoutes() {
    mutateAll((entry) => {
      entry.providers = [];
      entry.priorities = [];
      entry.strategy = '';
    });
  }

  if (routableModels.length === 0) {
    return (
      <div className="form__row">
        <label className="form__label">Per-model routing</label>
        <div className="model-routes__emptystate">
          <div className="model-routes__emptytitle">No concrete models to pin</div>
          <div className="model-routes__emptybody muted">
            Routing applies to concrete model ids, but every model in the
            allowed list is a wildcard (e.g. <code className="mono">gpt-4*</code>).
            Add at least one concrete model to allowed models to enable
            per-model provider pinning.
          </div>
        </div>
      </div>
    );
  }

  const filteredModels = query.trim()
    ? routableModels.filter((m) => m.toLowerCase().includes(query.trim().toLowerCase()))
    : routableModels;

  // Summary counters across all routes (for the toolbar).
  const stats = (() => {
    let pinned = 0, priority = 0, failover = 0;
    for (const m of routableModels) {
      const sel = routeProvidersFor(m);
      const st = routeStrategyFor(m);
      if (sel.length > 0) pinned++;
      if (st === 'priority') priority++;
      else if (st === 'failover') failover++;
    }
    return { pinned, priority, failover, total: routableModels.length };
  })();

  return (
    <div className="form__row">
      <label className="form__label">Per-model routing</label>
      <div className="form__hint" style={{ marginBottom: 10 }}>
        Pin each model to specific upstream providers. A model with no pinned
        providers uses the default round-robin. Add a strategy to prefer
        premium providers (priority) or stay available when one fails (failover).
      </div>

      <div className="model-routes__toolbar">
        <div className="model-routes__search">
          <span className="model-routes__searchicon" aria-hidden="true">⌕</span>
          <input
            type="search"
            value={query}
            onChange={(e) => setQuery(e.target.value)}
            placeholder={`Filter ${routableModels.length} model${routableModels.length === 1 ? '' : 's'}…`}
            aria-label="Filter models"
          />
          {query && (
            <button
              type="button"
              className="model-routes__searchclear"
              onClick={() => setQuery('')}
              aria-label="Clear filter"
              title="Clear filter"
            >×</button>
          )}
        </div>
        <div className="model-routes__stats muted mono">
          <span title="Models with a pinned provider set">{stats.pinned}/{stats.total} pinned</span>
          {stats.priority > 0 && <span className="model-routes__stat--priority" title="Models using the priority strategy">priority {stats.priority}</span>}
          {stats.failover > 0 && <span className="model-routes__stat--failover" title="Models using the failover strategy">failover {stats.failover}</span>}
        </div>
        <div className={`model-routes__bulk ${bulkOpen ? 'model-routes__bulk--open' : ''}`} ref={bulkRef}>
          <button
            type="button"
            className="model-routes__bulkbtn"
            onClick={() => setBulkOpen((v) => !v)}
            aria-expanded={bulkOpen}
          >
            Bulk actions ▾
          </button>
          {bulkOpen && (
            <div className="model-routes__bulkmenu" role="menu">
              <button type="button" role="menuitem" onClick={() => { setStrategyForAll('priority'); setBulkOpen(false); }}>
                Set strategy → Priority for all
              </button>
              <button type="button" role="menuitem" onClick={() => { setStrategyForAll('failover'); setBulkOpen(false); }}>
                Set strategy → Failover for all
              </button>
              <button type="button" role="menuitem" onClick={() => { setStrategyForAll(''); setBulkOpen(false); }}>
                Reset strategy → Default for all
              </button>
              <div className="model-routes__bulksep" />
              <button type="button" role="menuitem" className="model-routes__bulkdanger" onClick={() => { clearAllRoutes(); setBulkOpen(false); }}>
                Clear all routes
              </button>
            </div>
          )}
        </div>
      </div>

      {filteredModels.length === 0 ? (
        <div className="model-routes__nomatch muted">
          No models match “{query}”. <button type="button" className="linkish" onClick={() => setQuery('')}>Clear filter</button>.
        </div>
      ) : (
        <div className="model-routes">
          {filteredModels.map((model) => {
            const route = {
              providers: routeProvidersFor(model),
              strategy: routeStrategyFor(model),
              priorities: (routes || []).find((x) => x.model === model)?.priorities || [],
            };
            return (
              <ModelRouteConfigSection
                key={model}
                model={model}
                route={route}
                onChange={(next) => mutateRoute(model, (entry) => {
                  entry.providers = next.providers;
                  entry.strategy = next.strategy;
                  entry.priorities = next.priorities;
                })}
              />
            );
          })}
        </div>
      )}
    </div>
  );
}
