import React, { useEffect, useMemo, useState } from 'react';
import { getModelProviders, listUpstreamProviders } from '../api/client.js';

// ModelRouteConfigSection — shared per-model routing configuration UI.
//
// Extracted from ModelRoutesEditor so both the PolicyForm editor (embedded
// per-card) and the ModelGroup entry modal (single-model workflow) render
// the exact same controls: provider chip toggles, optional per-provider
// priority inputs, the strategy segmented control, tie-warning and
// try-order visualisation. The modal additionally layers RPM / Max Budget
// inputs on top (rendered by the caller below this section).
//
// Props:
//   model     — concrete model id this section configures.
//   route     — current route entry { providers, strategy, priorities }.
//   onChange  — called with a fresh partial route entry on every change:
//               { providers, strategy, priorities }.
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

export default function ModelRouteConfigSection({ model, route, onChange }) {
  const [liveProviders, setLiveProviders] = useState(null); // null = loading
  const [allProviderKeys, setAllProviderKeys] = useState(null); // null = loading

  // Load the live providers serving this model + every configured upstream
  // key once on mount.
  useEffect(() => {
    let cancelled = false;
    (async () => {
      try {
        const res = await getModelProviders(model);
        const providers = Array.isArray(res?.providers) ? res.providers : [];
        if (!cancelled) setLiveProviders(providers);
      } catch {
        if (!cancelled) setLiveProviders([]);
      }
    })();
    (async () => {
      try {
        const res = await listUpstreamProviders();
        const rows = Array.isArray(res?.providers) ? res.providers : [];
        const keys = rows
          .filter((r) => !r.disabled)
          .map((r) => (typeof r.provider_key === 'string' ? r.provider_key : ''))
          .filter((k) => k)
          .sort((a, b) => a.localeCompare(b));
        if (!cancelled) setAllProviderKeys(keys);
      } catch {
        if (!cancelled) setAllProviderKeys([]);
      }
    })();
    return () => { cancelled = true; };
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [model]);

  const selected = useMemo(
    () => (route && Array.isArray(route.providers) ? route.providers : []),
    [route],
  );
  const strategy = route && typeof route.strategy === 'string' ? route.strategy : '';
  const priorities = useMemo(
    () => (route && Array.isArray(route.priorities) ? route.priorities : []),
    [route],
  );

  const choices = useMemo(() => {
    const seen = new Set();
    const out = [];
    for (const p of liveProviders || []) {
      const key = String(p || '').trim();
      if (!key || seen.has(key)) continue;
      seen.add(key);
      out.push(key);
    }
    for (const key of allProviderKeys || []) {
      if (!key || seen.has(key)) continue;
      seen.add(key);
      out.push(key);
    }
    return out;
  }, [liveProviders, allProviderKeys]);

  // True while either provider list is still loading.
  const loading = liveProviders === null && allProviderKeys === null;
  const liveLoaded = liveProviders !== null;
  const catalogEmpty = allProviderKeys !== null && allProviderKeys.length === 0;
  const liveLoadedAndEmpty = liveLoaded && liveProviders.length === 0;

  const strategyActive = strategy === 'priority' || strategy === 'failover';

  function priorityFor(p) {
    if (!strategyActive) return 0;
    const pr = priorities.find((x) => x && x.provider === p);
    return pr ? Number(pr.priority) || 0 : 0;
  }

  const { ranks, ordered } = computeRanks(selected, priorityFor);

  const hasTieConflict =
    strategyActive &&
    selected.length > 1 &&
    (() => {
      const seenRanks = new Set();
      for (const p of selected) {
        const r = ranks[p];
        if (seenRanks.has(r)) return true;
        seenRanks.add(r);
      }
      return false;
    })();

  const activeOption = STRATEGY_OPTIONS.find((o) => o.value === strategy) || STRATEGY_OPTIONS[0];

  function emit(mutator) {
    const next = {
      providers: [...selected],
      strategy,
      priorities: priorities.map((p) => ({ ...p })),
    };
    mutator(next);
    onChange(next);
  }

  function toggleProvider(provider) {
    emit((next) => {
      const idx = next.providers.indexOf(provider);
      if (idx >= 0) {
        next.providers.splice(idx, 1);
        next.priorities = next.priorities.filter((p) => p.provider !== provider);
      } else {
        next.providers.push(provider);
      }
    });
  }

  function setPriority(provider, value) {
    emit((next) => {
      const num = Number.isFinite(value) ? Math.trunc(value) : 0;
      const existing = next.priorities.find((p) => p.provider === provider);
      if (existing) existing.priority = num;
      else next.priorities.push({ provider, priority: num });
    });
  }

  function setStrategy(value) {
    emit((next) => {
      next.strategy = value;
      if (!value) next.priorities = [];
    });
  }

  return (
    <div
      className={`model-routes__card ${strategyActive ? `model-routes__card--${strategy}` : ''} ${selected.length === 0 ? 'model-routes__card--empty' : ''}`}
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
          const isLive = liveLoaded && liveProviders.includes(p);
          const priority = on ? priorityFor(p) : 0;
          const rank = on ? ranks[p] : -1;
          return (
            <div className={`chip-pri ${on ? 'chip-pri--on' : ''}`} key={p}>
              <button
                type="button"
                className={`chip ${on ? 'chip--selected' : ''} mono`}
                onClick={() => toggleProvider(p)}
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
                    onChange={(e) => setPriority(p, parseInt(e.target.value, 10) || 0)}
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
                onClick={() => setStrategy(opt.value)}
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
}
