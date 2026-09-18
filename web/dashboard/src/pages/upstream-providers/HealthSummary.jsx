import React from 'react';

const TILES = [
  { key: 'live', label: 'Live', tone: 'emerald' },
  { key: 'cooldown', label: 'Cooldown', tone: 'amber' },
  { key: 'breaker_open', label: 'Breaker open', tone: 'rose' },
  { key: 'stale', label: 'Stale', tone: 'zinc' },
  { key: 'disabled', label: 'Disabled', tone: 'zinc' },
];

const TONE_CLASSES = {
  emerald: 'border-emerald-500/40 bg-emerald-500/10 text-emerald-700 dark:text-emerald-300',
  amber: 'border-amber-500/40 bg-amber-500/10 text-amber-700 dark:text-amber-300',
  rose: 'border-rose-500/40 bg-rose-500/10 text-rose-700 dark:text-rose-300',
  zinc: 'border-zinc-400/40 bg-zinc-400/10 text-zinc-700 dark:text-zinc-300',
};

// HealthSummary renders five click-to-filter tiles. Clicking a tile
// invokes onNavigate(healthKey | null) — the parent decides what URL or
// filter state to apply. The "All" tile passes null (clear filter).
export function HealthSummary({ summary, onNavigate, activeKey = null }) {
  return (
    <div className="flex flex-wrap gap-2" role="group" aria-label="Provider health summary">
      <button
        type="button"
        onClick={() => onNavigate(null)}
        className={`px-3 py-2 rounded-md border text-sm ${activeKey === null ? 'border-zinc-500 bg-zinc-100 dark:bg-zinc-800' : 'border-zinc-300 dark:border-zinc-700'}`}
        aria-pressed={activeKey === null}
      >
        All ({summary.total})
      </button>
      {TILES.map((tile) => {
        const count = summary[tile.key] ?? 0;
        const isActive = activeKey === tile.key;
        return (
          <button
            key={tile.key}
            type="button"
            disabled={count === 0}
            onClick={() => onNavigate(tile.key)}
            className={`px-3 py-2 rounded-md border text-sm ${TONE_CLASSES[tile.tone]} ${isActive ? 'ring-2 ring-offset-1 ring-current' : ''} disabled:opacity-40 disabled:cursor-not-allowed`}
            aria-pressed={isActive}
            aria-label={`Filter: ${tile.label} (${count})`}
          >
            {tile.label} ({count})
          </button>
        );
      })}
    </div>
  );
}

export default HealthSummary;
