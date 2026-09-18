import React, { useEffect, useState, useCallback } from 'react';
import { fetchPicker, pinProvider } from '../../api/liveStatus.js';
import { Spinner, ErrorBanner, EmptyState } from '../../components/Primitives.jsx';
import { mergePinnedAfterPin, clearPending, partitionPicker } from './pickerHelpers.js';

// Picker — model-routing picker with the four-quadrant layout promised by
// the zero-downtime design (Pinned / Live / Stale / Hidden).
//
// Behaviour:
//   - On mount (or when `model` changes), fetches GET
//     /v0/management/model-routing/picker?model=X.
//   - Renders Pinned / Live / Stale quadrants; Hidden is collapsed by
//     default and toggleable.
//   - Pinning a Live or Stale row calls POST /model-routing/pin; the
//     UI optimistically inserts the pin with the suggested priority.
//   - Live evidence updates (cooldown / status flip) come from a parent
//     poller — this component is pure / presentational w.r.t. that data.
//
// Props:
//   model:        string — the model id being routed (required)
//   onPinned?:    (pinned: PickerPinned[]) => void — invoked whenever
//                 the pinned list changes (after a pin or refresh). Use
//                 this to keep parent state in sync.
//   onError?:     (err: Error) => void — invoked on fetch/pin errors.

export function Picker({ model, onPinned, onError }) {
  const [loading, setLoading] = useState(true);
  const [error, setError] = useState(null);
  const [picker, setPicker] = useState(null);
  const [showStale, setShowStale] = useState(true);
  const [pending, setPending] = useState({}); // provider_key → true while pinning

  const load = useCallback(async () => {
    if (!model) return;
    setLoading(true);
    setError(null);
    try {
      const data = await fetchPicker(model);
      setPicker(data);
      if (data && typeof onPinned === 'function') onPinned(data.pinned || []);
    } catch (e) {
      setError(e);
      if (typeof onError === 'function') onError(e);
    } finally {
      setLoading(false);
    }
  }, [model, onPinned, onError]);

  useEffect(() => {
    load();
  }, [load]);

  const handlePin = useCallback(
    async (candidate, force = false) => {
      const key = candidate.provider_key;
      setPending((p) => ({ ...p, [key]: true }));
      try {
        const result = await pinProvider({ model, providerKey: key, force });
        // Optimistic update: append (or replace if existing) the pin.
        setPicker((prev) => {
          if (!prev) return prev;
          const nextPinned = mergePinnedAfterPin(prev.pinned, result, candidate);
          const updated = { ...prev, pinned: nextPinned };
          if (typeof onPinned === 'function') onPinned(nextPinned);
          return updated;
        });
      } catch (e) {
        setError(e);
        if (typeof onError === 'function') onError(e);
      } finally {
        setPending((p) => clearPending(p, key));
      }
    },
    [model, onPinned, onError],
  );

  if (!model) {
    return <EmptyState title="No model selected" hint="Pick a model from the catalog to see the routing picker." />;
  }
  if (loading && !picker) {
    return <Spinner label="Loading picker…" />;
  }
  if (error) {
    return <ErrorBanner error={error} onRetry={load} />;
  }
  if (!picker) {
    return (
      <EmptyState
        title="Picker unavailable"
        hint="The model-routing picker requires the PG store. Configure PGSTORE_DSN to enable it."
      />
    );
  }

  const { live, stale, pinned } = partitionPicker(picker);

  return (
    <div className="grid grid-cols-1 lg:grid-cols-2 gap-4">
      <Quadrant testId="picker-pinned" title="Pinned" subtitle="Currently routed">
        {pinned.length === 0 ? (
          <EmptyState title="No pins yet" hint="Pin a row below to start routing this model." />
        ) : (
          <ul className="space-y-2">
            {pinned.map((p) => (
              <PinnedRow key={p.provider_key} row={p} />
            ))}
          </ul>
        )}
      </Quadrant>

      <Quadrant testId="picker-live" title="Live" subtitle="Healthy candidates">
        {live.length === 0 ? (
          <EmptyState title="No live providers" hint="No upstream is currently serving this model." />
        ) : (
          <ul className="space-y-2">
            {live.map((c) => (
              <CandidateRow
                key={c.provider_key}
                candidate={c}
                pending={!!pending[c.provider_key]}
                onPin={() => handlePin(c, false)}
              />
            ))}
          </ul>
        )}
      </Quadrant>

      <Quadrant
        testId="picker-stale"
        title="Stale"
        subtitle={showStale ? 'Cooldown or no live evidence' : 'Hidden'}
      >
        <button
          type="button"
          onClick={() => setShowStale((v) => !v)}
          className="text-xs text-zinc-400 hover:text-zinc-200 mb-2"
        >
          {showStale ? 'Hide stale' : 'Show stale'}
        </button>
        {showStale &&
          (stale.length === 0 ? (
            <EmptyState title="No stale rows" />
          ) : (
            <ul className="space-y-2">
              {stale.map((c) => (
                <CandidateRow
                  key={c.provider_key}
                  candidate={c}
                  pending={!!pending[c.provider_key]}
                  onPin={() => handlePin(c, true)}
                  pinLabel="Pin anyway"
                />
              ))}
            </ul>
          ))}
      </Quadrant>

      <Quadrant testId="picker-hidden" title="Hidden" subtitle="Inactive rows">
        <p className="text-sm text-zinc-500">
          Disabled or filtered rows will appear here when added.
        </p>
      </Quadrant>
    </div>
  );
}

function Quadrant({ testId, title, subtitle, children }) {
  return (
    <section
      data-testid={testId}
      className="rounded-lg border border-zinc-700 bg-zinc-900/50 p-4"
    >
      <header className="mb-3">
        <h3 className="text-sm font-semibold text-zinc-100">{title}</h3>
        {subtitle && <p className="text-xs text-zinc-400">{subtitle}</p>}
      </header>
      {children}
    </section>
  );
}

function PinnedRow({ row }) {
  return (
    <li className="flex items-center justify-between rounded-md border border-zinc-700 bg-zinc-800/60 px-3 py-2">
      <div className="min-w-0">
        <div className="text-sm text-zinc-100 truncate">{row.name || row.provider_key}</div>
        <div className="text-xs text-zinc-500 truncate">{row.provider_key}</div>
      </div>
      <div className="flex items-center gap-2">
        <span
          className={`inline-block w-2 h-2 rounded-full ${row.is_live ? 'bg-emerald-500' : 'bg-zinc-500'}`}
          title={row.is_live ? 'Live' : 'Not live'}
        />
        <span className="text-xs font-mono text-zinc-300">p={row.priority}</span>
      </div>
    </li>
  );
}

function CandidateRow({ candidate, pending, onPin, pinLabel = 'Pin' }) {
  const label = pinLabel
    ? `${pinLabel} → priority ${candidate.suggested_priority}`
    : `Pin → priority ${candidate.suggested_priority}`;
  return (
    <li className="flex items-center justify-between rounded-md border border-zinc-700 bg-zinc-800/60 px-3 py-2">
      <div className="min-w-0">
        <div className="text-sm text-zinc-100 truncate">
          {candidate.name || candidate.provider_key}
        </div>
        <div className="text-xs text-zinc-500 truncate">
          {candidate.provider_key}
          {candidate.level === 'entry' && candidate.identity && (
            <span className="ml-2 text-zinc-600">· entry {candidate.identity}</span>
          )}
        </div>
      </div>
      <button
        type="button"
        onClick={onPin}
        disabled={pending}
        className="ml-3 inline-flex items-center rounded-md bg-emerald-600 hover:bg-emerald-500 disabled:opacity-50 px-3 py-1 text-xs font-medium text-white"
      >
        {pending ? 'Pinning…' : label}
      </button>
    </li>
  );
}

export default Picker;