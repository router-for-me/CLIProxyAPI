import React, { useEffect, useMemo, useRef, useState } from 'react';
import { getModelProviders, listUpstreamProviders } from '../api/client.js';

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
// Extracted from PolicyForm.jsx; reused by the ModelGroup editor.
//
// Props:
//   allowedModels (string[]) — grant list driving which rows show up.
//     Wildcards (e.g. "gpt-4*") are excluded from routing because they
//     cannot be pinned to a concrete provider set.
//   routes ({ model, providers, strategy?, priorities? }[]) — current routes.
//   onChange (routes) — called with the updated route list on every change.

const STRATEGY_OPTIONS = [
  {
    value: '',
    label: 'Default',
    short: 'round-robin',
    blurb: 'Rotate across all pinned providers. Inherits the global routing strategy.',
  },
  {
    value: 'priority',
    label: 'Priority',
    short: 'priority',
    blurb: 'Stay on the highest-priority provider until exhausted, then descend. Best for response quality.',
  },
  {
    value: 'failover',
    label: 'Failover',
    short: 'failover',
    blurb: "Start at the highest-priority provider; the conductor switches providers on upstream errors. Best for zero downtime.",
  },
];

// Rank marker glyphs (avoid emoji-width issues; using circled digits).
const RANK_GLYPHS = ['①', '②', '③', '④', '⑤', '⑥', '⑦', '⑧', '⑨', '⑩'];

function rankGlyph(i) {
  return i < RANK_GLYPHS.length ? RANK_GLYPHS[i] : `${i + 1}`;
}

// computeRanks returns, for each selected provider, its rank (0-indexed)
// within the descending-priority ordering. Providers sharing a priority
// share a rank (tie) and are surfaced as a conflict for the operator.
function computeRanks(selected, priorityFor) {
  const withPriority = selected.map((p) => ({ p, priority: priorityFor(p) }));
  withPriority.sort((a, b) => b.priority - a.priority);
  const ranks = {};
  let lastPriority = null;
  let lastRank = -1;
  withPriority.forEach((entry, idx) => {
    if (lastPriority === null || entry.priority !== lastPriority) {
      lastRank = idx;
      lastPriority = entry.priority;
    }
    ranks[entry.p] = lastRank;
  });
  return { ranks, ordered: withPriority.map((e) => e.p) };
}

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

  function routeStrategyFor(model) {
    const r = (routes || []).find((x) => x.model === model);
    return r && typeof r.strategy === 'string' ? r.strategy : '';
  }

  function routePriorityFor(model, provider) {
    const r = (routes || []).find((x) => x.model === model);
    if (!r || !Array.isArray(r.priorities)) return 0;
    const pr = r.priorities.find((p) => p && p.provider === provider);
    return pr ? Number(pr.priority) || 0 : 0;
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

  function toggleRouteProvider(model, provider) {
    mutateRoute(model, (entry) => {
      const idx = entry.providers.indexOf(provider);
      if (idx >= 0) {
        entry.providers.splice(idx, 1);
        if (Array.isArray(entry.priorities)) {
          entry.priorities = entry.priorities.filter((p) => p.provider !== provider);
        }
      } else {
        entry.providers.push(provider);
      }
    });
  }

  function setRouteStrategy(model, strategy) {
    mutateRoute(model, (entry) => {
      entry.strategy = strategy;
      if (!strategy) entry.priorities = [];
    });
  }

  function setRoutePriority(model, provider, value) {
    mutateRoute(model, (entry) => {
      if (!Array.isArray(entry.priorities)) entry.priorities = [];
      const num = Number.isFinite(value) ? Math.trunc(value) : 0;
      const existing = entry.priorities.find((p) => p.provider === provider);
      if (existing) existing.priority = num;
      else entry.priorities.push({ provider, priority: num });
    });
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
            const selected = routeProvidersFor(model);
            const strategy = routeStrategyFor(model);
            const strategyActive = strategy === 'priority' || strategy === 'failover';
            const liveLoaded = model in providersByModel;
            const choices = routeProviderChoices(model);
            const loading = !liveLoaded && allUpstreamProviderKeys === null;
            const liveLoadedAndEmpty = liveLoaded && (providersByModel[model] || []).length === 0;
            const catalogEmpty =
              allUpstreamProviderKeys !== null && allUpstreamProviderKeys.length === 0;
            const priorityFor = (p) => (strategyActive ? routePriorityFor(model, p) : 0);
            const { ranks, ordered } = computeRanks(selected, priorityFor);
            // A tie exists when two selected providers share the same rank.
            const hasTieConflict = strategyActive && selected.length > 1 && (() => {
              const seenRanks = new Set();
              for (const p of selected) {
                const r = ranks[p];
                if (seenRanks.has(r)) return true;
                seenRanks.add(r);
              }
              return false;
            })();
            const activeOption = STRATEGY_OPTIONS.find((o) => o.value === strategy) || STRATEGY_OPTIONS[0];
            return (
              <div
                className={`model-routes__card ${strategyActive ? `model-routes__card--${strategy}` : ''} ${selected.length === 0 ? 'model-routes__card--empty' : ''}`}
                key={model}
              >
                <div className="model-routes__cardhead">
                  <div className="model-routes__model mono" title={model}>{model}</div>
                  <div className="model-routes__summary">
                    {selected.length === 0
                      ? <span className="model-routes__pill model-routes__pill--muted">no pin · default</span>
                      : <span className={`model-routes__pill ${strategyActive ? `model-routes__pill--${strategy}` : 'model-routes__pill--muted'}`}>
                          {activeOption.short} · {selected.length} provider{selected.length === 1 ? '' : 's'}
                        </span>}
                  </div>
                </div>

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
                    const priority = on ? priorityFor(p) : 0;
                    const rank = on ? ranks[p] : -1;
                    return (
                      <div className={`chip-pri ${on ? 'chip-pri--on' : ''}`} key={p}>
                        <button
                          type="button"
                          className={`chip ${on ? 'chip--selected' : ''} mono`}
                          onClick={() => toggleRouteProvider(model, p)}
                          title={`${p}\n${isLive ? 'Live provider (currently serving this model)' : 'Configured upstream (no live auth right now)'}${on && strategyActive ? `\nRank ${rank + 1} · priority ${priority}` : ''}`}
                        >
                          {on && strategyActive && rank >= 0 && (
                            <span className="chip__rank" aria-hidden="true">{rankGlyph(rank)}</span>
                          )}
                          <span className="chip__label">{p}{isLive ? '' : ' *'}</span>
                        </button>
                        {on && strategyActive && (
                          <label className="chip-pri__field" title={`Priority for ${p} (higher = primary)`}>
                            <span className="chip-pri__hash" aria-hidden="true">P</span>
                            <input
                              type="number"
                              className="chip-pri__input"
                              value={priority}
                              min={0}
                              step={1}
                              onChange={(e) => setRoutePriority(model, p, parseInt(e.target.value, 10) || 0)}
                            />
                          </label>
                        )}
                      </div>
                    );
                  })}
                </div>

                {strategyActive && hasTieConflict && (
                  <div className="model-routes__warning" role="status">
                    <span className="model-routes__warningicon" aria-hidden="true">⚠</span>
                    Two pinned providers share the same priority — their order is ambiguous. Assign distinct priorities to make the failover order explicit.
                  </div>
                )}

                {strategyActive && ordered.length > 1 && !hasTieConflict && (
                  <div className="model-routes__order mono" title="Providers will be tried in this order">
                    <span className="model-routes__orderlabel">try order</span>
                    {ordered.map((p, i) => (
                      <React.Fragment key={p}>
                        {i > 0 && <span className="model-routes__arrow">→</span>}
                        <span className="model-routes__orderitem">
                          <span className="model-routes__orderrank">{rankGlyph(i)}</span>
                          {p}
                          {priorityFor(p) !== 0 ? <span className="model-routes__ordernum">·p{priorityFor(p)}</span> : null}
                        </span>
                      </React.Fragment>
                    ))}
                  </div>
                )}

                <div className="model-routes__strategy">
                  <div className="seg" role="group" aria-label={`Routing strategy for ${model}`}>
                    {STRATEGY_OPTIONS.map((opt) => {
                      const active = (strategy || '') === opt.value;
                      return (
                        <button
                          key={opt.value || 'default'}
                          type="button"
                          className={`seg__btn ${active ? 'seg__btn--active' : ''}`}
                          onClick={() => setRouteStrategy(model, opt.value)}
                          title={opt.blurb}
                          aria-pressed={active}
                        >
                          {opt.label}
                        </button>
                      );
                    })}
                  </div>
                  <div className="model-routes__strategyblurb muted">{activeOption.blurb}</div>
                </div>
              </div>
            );
          })}
        </div>
      )}
    </div>
  );
}
