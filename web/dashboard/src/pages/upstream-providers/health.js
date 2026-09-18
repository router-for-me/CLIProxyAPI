// Pure helpers shared by Table.jsx and HealthPage.jsx for partitioning
// upstream providers into LIVE / COOLDOWN / BREAKER / STALE / DISABLED
// buckets. No React imports here — kept pure for unit testing and so
// lint rules can flag accidental dependency creep.

export const HEALTH_STATES = ['live', 'cooldown', 'breaker_open', 'stale', 'disabled'];

// statusFromHealth returns the canonical StatusDot status for a row.
// liveEntry is the row from the live-status payload (may be undefined).
// disabled comes from the row's own field — orthogonal to runtime health.
export function statusFromHealth(row, liveEntry) {
  if (row?.disabled) return 'disabled';
  if (liveEntry?.breaker_open) return 'breaker_open';
  if (liveEntry?.cooldown_until) return 'cooldown';
  if (liveEntry?.is_live) return 'live';
  return 'stale';
}

// cooldownReason returns a short human description for the tooltip.
export function cooldownReason(cooldownUntil) {
  if (!cooldownUntil) return null;
  const t = new Date(cooldownUntil).getTime();
  if (!Number.isFinite(t)) return null;
  const ms = t - Date.now();
  if (ms <= 0) return null;
  const minutes = Math.round(ms / 60000);
  if (minutes < 1) return 'retrying in <1m';
  if (minutes < 60) return `retrying in ${minutes}m`;
  const hours = Math.round(minutes / 60);
  if (hours < 24) return `retrying in ${hours}h`;
  const days = Math.round(hours / 24);
  return `retrying in ${days}d`;
}

// getHealthSummary counts rows in each bucket.
export function getHealthSummary(providers, liveStatus = {}) {
  const summary = { live: 0, cooldown: 0, breaker_open: 0, stale: 0, disabled: 0, total: providers.length };
  for (const row of providers) {
    summary[statusFromHealth(row, liveStatus[String(row.id)])]++;
  }
  return summary;
}

// partitionByHealth returns four buckets matching the HealthPage quadrants:
//   { live, cooldown, breaker, staleOrDisabled }
// staleOrDisabled includes both "stale" (no live evidence) and "disabled"
// rows — the dashboard treats them as one quadrant.
export function partitionByHealth(providers, liveStatus = {}) {
  const out = { live: [], cooldown: [], breaker: [], staleOrDisabled: [] };
  for (const row of providers) {
    const status = statusFromHealth(row, liveStatus[String(row.id)]);
    if (status === 'live') out.live.push({ row, status });
    else if (status === 'cooldown') out.cooldown.push({ row, status });
    else if (status === 'breaker_open') out.breaker.push({ row, status });
    else out.staleOrDisabled.push({ row, status });
  }
  return out;
}

export default {
  HEALTH_STATES,
  statusFromHealth,
  cooldownReason,
  getHealthSummary,
  partitionByHealth,
};