import React from 'react';

// StatusDot — single source of truth for the LIVE / COOLDOWN /
// BREAKER_OPEN / STALE heartbeat indicator used across the picker,
// the provider list, the editor entries, and the alerts page.
//
// Colors reuse the existing Tailwind tokens (no new design system):
//   - LIVE          → emerald-500
//   - COOLDOWN      → amber-500
//   - BREAKER_OPEN  → rose-500
//   - STALE         → zinc-400
//   - UNKNOWN       → zinc-600
//
// Props:
//   status: 'live' | 'cooldown' | 'breaker_open' | 'stale' | 'unknown'
//   label?: optional short text shown next to the dot (e.g. "Live")
//   reason?: optional longer reason string for the tooltip
//   size?: 'xs' | 'sm' | 'md' (default 'sm')

const COLOR_MAP = {
  live: 'bg-emerald-500',
  cooldown: 'bg-amber-500',
  breaker_open: 'bg-rose-500',
  stale: 'bg-zinc-400',
  unknown: 'bg-zinc-600',
};

const LABEL_MAP = {
  live: 'Live',
  cooldown: 'Cooldown',
  breaker_open: 'Breaker open',
  stale: 'Stale',
  unknown: 'Unknown',
};

const SIZE_MAP = {
  xs: 'w-1.5 h-1.5',
  sm: 'w-2 h-2',
  md: 'w-2.5 h-2.5',
};

export function StatusDot({ status = 'unknown', label, reason, size = 'sm', className = '' }) {
  const normalized = COLOR_MAP[status] ? status : 'unknown';
  const dotClass = `${SIZE_MAP[size]} ${COLOR_MAP[normalized]} rounded-full inline-block`;
  const tooltip = reason
    ? `${LABEL_MAP[normalized]}: ${reason}`
    : LABEL_MAP[normalized];
  const showLabel = label !== false;

  return (
    <span
      className={`inline-flex items-center gap-1.5 align-middle ${className}`}
      title={tooltip}
      aria-label={tooltip}
    >
      <span className={dotClass} aria-hidden="true" />
      {showLabel && (
        <span className="text-xs text-zinc-400">{label || LABEL_MAP[normalized]}</span>
      )}
    </span>
  );
}

// statusFromLiveEvidence + cooldown returns a normalized StatusDot status
// from the bits an upstream row carries. Keeps the LIVE rule in one place
// so callers don't have to recompute.
export function statusFromRow({ isLive, cooldownUntil, breakerOpen }) {
  if (breakerOpen) return 'breaker_open';
  if (cooldownUntil) return 'cooldown';
  if (isLive) return 'live';
  return 'stale';
}

export default StatusDot;