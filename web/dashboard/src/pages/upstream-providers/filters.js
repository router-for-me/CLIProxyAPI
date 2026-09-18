// Pure filter + sort helpers for the upstream-providers list. Mirrors the
// current UpstreamProvidersPage.jsx behavior + adds the new health filter.
// No React imports.

import { statusFromHealth } from './health.js';

const SORT_KEYS = ['provider_type', 'name', 'priority', 'base_url', 'disabled', 'model_count', 'updated_at', 'health'];

function matchesSearch(row, search) {
  if (!search) return true;
  const q = search.toLowerCase();
  const haystack = [row.provider_type, row.name, row.label, row.email, row.file_name, row.base_url]
    .filter(Boolean)
    .join(' ')
    .toLowerCase();
  return haystack.includes(q);
}

function matchesType(row, typeFilter) {
  if (!typeFilter) return true;
  // 'api' / 'oauth' are category shortcuts (used by the upstream-providers
  // Stat tiles and any future "filter to API-key only" affordance). They
  // match by prefix rather than exact provider_type because the actual
  // provider_type list is owned by upstream-provider-editor/schemas.js —
  // importing those lists here would couple two unrelated modules.
  if (typeFilter === 'api') {
    return row.provider_type && !row.provider_type.startsWith('oauth:');
  }
  if (typeFilter === 'oauth') {
    return row.provider_type && row.provider_type.startsWith('oauth:');
  }
  return row.provider_type === typeFilter;
}

function matchesHealth(row, liveStatus, healthFilters) {
  if (!healthFilters || healthFilters.size === 0) return true;
  return healthFilters.has(statusFromHealth(row, liveStatus[String(row.id)]));
}

export function applyFilters(providers, { search, typeFilter, healthFilters, liveStatus }) {
  return providers.filter(
    (row) =>
      matchesSearch(row, search) &&
      matchesType(row, typeFilter) &&
      matchesHealth(row, liveStatus, healthFilters),
  );
}

function compare(a, b, key, dir) {
  const av = a[key];
  const bv = b[key];
  if (av == null && bv == null) return 0;
  if (av == null) return 1;
  if (bv == null) return -1;
  if (typeof av === 'number' && typeof bv === 'number') {
    return dir === 'asc' ? av - bv : bv - av;
  }
  const as = String(av).toLowerCase();
  const bs = String(bv).toLowerCase();
  if (as < bs) return dir === 'asc' ? -1 : 1;
  if (as > bs) return dir === 'asc' ? 1 : -1;
  return 0;
}

const HEALTH_RANK = { breaker_open: 0, cooldown: 1, stale: 2, disabled: 3, live: 4 };

export function applySort(rows, sortKey, sortDir, liveStatus) {
  if (sortKey === 'off') return rows;
  const dir = sortDir === 'desc' ? 'desc' : 'asc';
  const sorted = [...rows];
  if (sortKey === 'health') {
    // Worst-first ascending puts Breaker → Cooldown → Stale → Disabled → Live.
    sorted.sort((a, b) => {
      const sa = HEALTH_RANK[statusFromHealth(a, liveStatus[String(a.id)])];
      const sb = HEALTH_RANK[statusFromHealth(b, liveStatus[String(b.id)])];
      return dir === 'asc' ? sa - sb : sb - sa;
    });
    return sorted;
  }
  sorted.sort((a, b) => compare(a, b, sortKey, dir));
  return sorted;
}

export { SORT_KEYS };
export default { applyFilters, applySort, SORT_KEYS };
