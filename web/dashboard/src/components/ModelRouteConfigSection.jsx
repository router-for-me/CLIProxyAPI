import React, { useEffect, useMemo, useState } from 'react';
import { getModelProviders, listUpstreamProviders } from '../api/client.js';
import {
  providerKeyIsLive,
  expandAllProvidersToChoices,
  mergeLiveWithChoices,
} from './modelRouteProvider.js';
import { ROUTE_STRATEGY_OPTIONS, WEIGHTED_ROUTE_OPTION, activeStrategyOption } from './routingStrategies.js';
import StrategyPicker from './StrategyPicker.jsx';

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

// Rank marker glyphs (avoid emoji-width issues; using circled digits).
const RANK_GLYPHS = ['①', '②', '③', '④', '⑤', '⑥', '⑦', '⑧', '⑨', '⑩'];

function rankGlyph(i) {
  return i < RANK_GLYPHS.length ? RANK_GLYPHS[i] : `${i + 1}`;
}

// upstreamDisplayLabel picks the human-readable identifier for an upstream
// provider row. The list shows several candidates (name / label / email /
// file_name) so a row whose `name` was left blank still gets a usable label.
// Falls back to the routing key when nothing is set so the chip is never empty.
function upstreamDisplayLabel(row) {
  if (!row) return '';
  const candidates = [row.name, row.label, row.email, row.file_name];
  for (const c of candidates) {
    const s = typeof c === 'string' ? c.trim() : '';
    if (s) return s;
  }
  return '';
}

// upstreamTypeLabel turns the upstream's provider_type into the short channel
// label operators see in the picker ("Claude API Key", "Claude OAuth", …).
// Falls back to the raw provider_type when the catalog is unknown.
function upstreamTypeLabel(providerType) {
  if (!providerType) return '';
  if (providerType.startsWith('oauth:')) {
    const channel = providerType.slice('oauth:'.length);
    return `${channel} (OAuth)`;
  }
  if (providerType === 'openai-compatibility') return 'OpenAI-compat';
  // Built-in api-key types: strip the "-api-key" suffix and title-case the
  // channel so "claude-api-key" renders as "Claude API key".
  const m = /^(.+?)-api-key$/.exec(providerType);
  if (m) return `${capitalize(m[1])} API key`;
  return providerType;
}

function capitalize(s) {
  if (!s) return '';
  return s.charAt(0).toUpperCase() + s.slice(1);
}

// nextPriority returns a sensible default priority for a newly assigned
// provider: ten above the current maximum (floor 10), so pins stay ordered and
// never display 0. Mirrors the backend pin's MAX + 1 intent with a readable
// step.
function nextPriority(priorities) {
  const max = (priorities || []).reduce(
    (m, p) => Math.max(m, Number(p && p.priority) || 0),
    0,
  );
  return max + 10;
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

export default function ModelRouteConfigSection({ model, route, onChange, allowWeighted = false }) {
  const [liveProviders, setLiveProviders] = useState(null); // null = loading
  const [allProviderKeys, setAllProviderKeys] = useState(null); // null = loading
  // Full upstream rows keyed by their provider_key, so the picker can show
  // the operator's stored identity (name/label/email/file_name) alongside the
  // raw routing key. Populated from listUpstreamProviders().
  const [upstreamByKey, setUpstreamByKey] = useState(new Map());

  // Load the live providers serving this model + every configured upstream
  // row once on mount. We keep the full row (not just provider_key) so the
  // picker can show the operator's stored identifier instead of an opaque
  // executor key they may not recognise (e.g. a "Claude API Key" row whose
  // name field is "anthropic" still renders as "anthropic · Claude API key").
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
        const byKey = new Map();
        for (const r of rows) {
          if (!r || r.disabled) continue;
          const key = typeof r.provider_key === 'string' ? r.provider_key.trim() : '';
          if (!key) continue;
          // Each upstream row now maps to a unique routing key (e.g.
          // "claude:42"), so two rows of the same channel no longer collapse
          // onto one chip. The picker expands each row through the shared
          // helper to surface both provider-level and OpenAI entry routes.
          byKey.set(key, {
            key,
            row: r,
            label: upstreamDisplayLabel(r),
            providerType: r.provider_type || '',
            count: 1,
          });
        }
        const keys = [...byKey.keys()].sort((a, b) => a.localeCompare(b));
        if (!cancelled) {
          setUpstreamByKey(byKey);
          setAllProviderKeys(keys);
        }
      } catch {
        if (!cancelled) {
          setUpstreamByKey(new Map());
          setAllProviderKeys([]);
        }
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
    // Live keys first so the operator sees the actually-serving providers
    // up top; configured-only keys follow. Each choice carries its routing
    // key (the value that gets saved into route.providers) plus display
    // metadata from the upstream row when one exists. The shared helper
    // expands each management row into provider-level + entry-level choices,
    // so OpenAI Compatibility providers expose one chip per entry as well
    // as the pool-level route.
    const upstreamRows = [...(upstreamByKey.values() || [])].map((entry) => entry.row);
    const configured = expandAllProvidersToChoices(upstreamRows);
    const upstreamByKeyForMerge = new Map();
    for (const entry of upstreamByKey.values()) {
      upstreamByKeyForMerge.set(entry.key, entry);
    }
    const merged = mergeLiveWithChoices(liveProviders || [], configured, upstreamByKeyForMerge);
    // A pinned provider that is neither live nor configured — e.g. its row was
    // disabled or deleted — still needs a row so the operator can unpin it.
    const seen = new Set(merged.map((c) => c.key));
    for (const key of selected) {
      if (seen.has(key)) continue;
      seen.add(key);
      merged.push({ key, label: key, providerType: '', level: 'provider', identity: '', count: 0, orphan: true });
    }
    return merged;
  }, [liveProviders, upstreamByKey, selected]);

  const liveLoaded = liveProviders !== null;

  // Only providers that are live OR already pinned are shown. Non-live
  // configured providers are hidden to keep the panel focused; a pinned row
  // that is no longer live stays visible so it can be unpinned.
  const visibleChoices = useMemo(() => {
    if (!liveLoaded) return [];
    return choices.filter((c) => providerKeyIsLive(c.key, liveProviders) || selected.includes(c.key));
  }, [choices, liveProviders, liveLoaded, selected]);

  const hiddenCount = liveLoaded ? choices.length - visibleChoices.length : 0;

  // True while either provider list is still loading.
  const loading = liveProviders === null && allProviderKeys === null;
  const catalogEmpty = allProviderKeys !== null && allProviderKeys.length === 0;
  const liveLoadedAndEmpty = liveLoaded && liveProviders.length === 0;

  const strategyActive = strategy === 'priority' || strategy === 'failover' || strategy === 'weighted';

  function priorityFor(p) {
    if (!strategyActive) return 0;
    const pr = priorities.find((x) => x && x.provider === p);
    return pr ? Number(pr.priority) || 0 : 0;
  }

  const { ranks } = computeRanks(selected, priorityFor);

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

  const strategyOptions = allowWeighted
    ? [WEIGHTED_ROUTE_OPTION, ...ROUTE_STRATEGY_OPTIONS]
    : ROUTE_STRATEGY_OPTIONS;
  const activeOption = activeStrategyOption(strategyOptions, strategy);

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
        // Seed a priority so a freshly pinned provider is never stuck at 0 and
        // sits above existing pins (MAX + 10, matching the backend pin). Only
        // meaningful under an active strategy; priorityFor ignores it otherwise.
        if (strategyActive && !next.priorities.some((p) => p && p.provider === provider)) {
          next.priorities.push({ provider, priority: nextPriority(next.priorities) });
        }
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
      if (!value) {
        next.priorities = [];
        return;
      }
      // Switching into an active strategy: assign a default priority (10, 20,
      // …) to every pinned provider that lacks one, so the try-order is
      // explicit and never displays 0.
      for (const p of next.providers) {
        if (next.priorities.some((x) => x && x.provider === p)) continue;
        next.priorities.push({ provider: p, priority: nextPriority(next.priorities) });
      }
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

      {strategyActive && hasTieConflict && (
        <div className="model-routes__warning" role="status">
          <span className="model-routes__warningicon" aria-hidden="true">⚠</span>
          Two pinned providers share the same priority — their order is ambiguous. Assign distinct priorities to make the failover order explicit.
        </div>
      )}

      <div className="model-routes__tablewrap">
        <table className="table table--dense model-routes__table">
          <thead>
            <tr>
              <th scope="col">Provider</th>
              <th scope="col">Status</th>
              {strategyActive && <th scope="col" className="sgl-num">Priority</th>}
              {strategyActive && <th scope="col" className="sgl-num">Rank</th>}
              <th scope="col" className="model-routes__col-toggle">
                <span className="sr-only">Toggle pin</span>
              </th>
            </tr>
          </thead>
          <tbody>
            {loading && (
              <tr><td colSpan={strategyActive ? 5 : 3} className="muted">Loading providers…</td></tr>
            )}
            {!loading && visibleChoices.length === 0 && liveLoadedAndEmpty && catalogEmpty && (
              <tr><td colSpan={strategyActive ? 5 : 3} className="muted">
                No live providers serve this model, and no upstream providers are configured.
              </td></tr>
            )}
            {!loading && visibleChoices.length === 0 && liveLoadedAndEmpty && !catalogEmpty && (
              <tr><td colSpan={strategyActive ? 5 : 3} className="muted">
                No live providers serve this model right now. {hiddenCount} configured
                upstream{hiddenCount === 1 ? '' : 's'} hidden until {hiddenCount === 1 ? 'it' : 'they'} come online.
              </td></tr>
            )}
            {!loading && visibleChoices.length === 0 && !liveLoadedAndEmpty && (
              <tr><td colSpan={strategyActive ? 5 : 3} className="muted">
                No live providers serve this model.
              </td></tr>
            )}
            {visibleChoices.map((choice) => {
              const on = selected.includes(choice.key);
              const isLive = liveLoaded && providerKeyIsLive(choice.key, liveProviders);
              // removed = pinned key absent from the configured catalog, i.e.
              // its upstream row was deleted or disabled.
              const removed = !!choice.orphan;
              const priority = on ? priorityFor(choice.key) : 0;
              const rank = on ? ranks[choice.key] : -1;
              // The display label prefers the upstream row's stored identity
              // (name/label/email/file_name) over the raw routing key, so the
              // operator sees "anthropic · Claude API key" rather than an
              // opaque "claude" they may not recognise.
              const labelText = choice.label || choice.key;
              const typeText = upstreamTypeLabel(choice.providerType);
              const countSuffix = choice.count > 1 ? ` ×${choice.count}` : '';
              const levelTag = choice.level === 'entry' ? 'Entry pin' : 'Provider pool';
              const statusLabel = isLive ? 'live' : (removed ? 'removed' : 'config');
              const statusTitle = isLive
                ? 'Live provider (currently serving this model)'
                : (removed
                  ? 'Pinned provider is no longer live or configured (disabled/removed). Unpin to clean up.'
                  : 'Configured upstream (no live auth right now)');
              return (
                <tr
                  key={choice.key}
                  className={`row-link ${on ? 'row--selected' : ''}`}
                  onClick={() => toggleProvider(choice.key)}
                  title={`${levelTag}: ${choice.key}${typeText ? ` · ${typeText}` : ''}${countSuffix}\n${statusTitle}${on && strategyActive ? `\nRank ${rank + 1} · priority ${priority}` : ''}`}
                >
                  <td>
                    <div className="cell-stack">
                      <span className="cell-stack__main mono">{labelText}{isLive ? '' : ' *'}</span>
                      <span className="dim" style={{ fontSize: 11 }}>
                        <code>{choice.key}</code>{choice.level === 'entry' ? ' · entry' : ''}{typeText ? ` · ${typeText}` : ''}{countSuffix}
                      </span>
                    </div>
                  </td>
                  <td>
                    <span className={`live-dot ${isLive ? 'live-dot--on' : 'live-dot--off'}`} aria-hidden="true" />
                    <span className="muted">{statusLabel}</span>
                  </td>
                  {strategyActive && (
                    <td className="sgl-num">
                      {on ? (
                        <input
                          type="number"
                          className="prio-input"
                          value={priority}
                          min={0}
                          step={1}
                          aria-label={`Priority for ${labelText} (higher = primary)`}
                          onClick={(e) => e.stopPropagation()}
                          onChange={(e) => setPriority(choice.key, parseInt(e.target.value, 10) || 0)}
                        />
                      ) : (
                        <span className="dim">—</span>
                      )}
                    </td>
                  )}
                  {strategyActive && (
                    <td className="sgl-num mono">
                      {on && rank >= 0 ? rankGlyph(rank) : <span className="dim">—</span>}
                    </td>
                  )}
                  <td className="model-routes__col-toggle">
                    <button
                      type="button"
                      className="row-actions__btn"
                      aria-pressed={on}
                      onClick={(e) => { e.stopPropagation(); toggleProvider(choice.key); }}
                      aria-label={on ? `Unpin ${labelText}` : `Pin ${labelText}`}
                    >
                      {on ? 'Pinned' : 'Pin'}
                    </button>
                  </td>
                </tr>
              );
            })}
          </tbody>
        </table>
      </div>

      {hiddenCount > 0 && (
        <div className="model-routes__hidden muted">
          {hiddenCount} non-live provider{hiddenCount === 1 ? '' : 's'} hidden.
        </div>
      )}

      <div className="model-routes__strategy">
        <StrategyPicker
          options={strategyOptions}
          value={strategy}
          onChange={setStrategy}
          ariaLabel={`Routing strategy for ${model}`}
          showBlurb
        />
      </div>
    </div>
  );
}
