// modelRoute — shared conversion for per-model ModelRoute entries.
//
// A ModelRoute is the `{ model, providers, strategy, priorities }` shape used
// by API-key policies, Model Groups, and Global Model routes. Each surface
// previously re-implemented the same "drop empties, keep priority/failover,
// filter priorities to pinned providers" logic in formToPolicy /
// formToGroup. Centralising it here keeps the wire shape identical everywhere.

// STRATEGY_PRIORITY and STRATEGY_FAILOVER are the only strategies persisted
// on an entity route. "weighted" is accepted by the vision-bridge route only
// (see routingStrategies.ROUTE_STRATEGY_OPTIONS).
export const STRATEGY_PRIORITY = 'priority';
export const STRATEGY_FAILOVER = 'failover';

// isPersistedRouteStrategy reports whether a strategy string is one that an
// entity ModelRoute may persist (priority or failover). "" and "weighted"
// return false.
export function isPersistedRouteStrategy(strategy) {
  const s = String(strategy || '').trim();
  return s === STRATEGY_PRIORITY || s === STRATEGY_FAILOVER;
}

// dedupeStrings returns the unique, trimmed, non-empty members of an array in
// first-seen order. Non-array input yields [].
export function dedupeStrings(arr) {
  if (!Array.isArray(arr)) return [];
  const out = [];
  const seen = new Set();
  for (const raw of arr) {
    const s = String(raw == null ? '' : raw).trim();
    if (!s || seen.has(s)) continue;
    seen.add(s);
    out.push(s);
  }
  return out;
}

// routeToWire converts one editable route entry into the persisted wire
// shape. It dedupes providers, keeps the strategy only when it is a valid
// persisted value, and keeps priorities only for pinned providers.
//
// The caller decides whether the route is worth persisting (routeHasConfig);
// this function always returns an object so callers can inspect it.
export function routeToWire(route) {
  const providers = dedupeStrings(route && route.providers);
  const out = {
    model: route && typeof route.model === 'string' ? route.model : '',
    providers,
  };
  const strategy = String((route && route.strategy) || '').trim();
  if (isPersistedRouteStrategy(strategy)) {
    out.strategy = strategy;
    const priorities = (Array.isArray(route && route.priorities) ? route.priorities : [])
      .filter((pr) => pr && providers.includes(String(pr.provider || '').trim()))
      .map((pr) => ({ provider: String(pr.provider || '').trim(), priority: Number(pr.priority) || 0 }));
    if (priorities.length > 0) out.priorities = priorities;
  }
  return out;
}

// routeHasConfig reports whether a route entry carries anything worth saving:
// pinned providers, a persisted strategy, or extra caps/discounts. Callers
// that layer caps on top (Model Groups) pass extra truthy checks through
// `hasExtras`; entity policies leave it false.
export function routeHasConfig(route, { hasExtras = false } = {}) {
  if (!route || typeof route !== 'object') return false;
  const providers = Array.isArray(route.providers) ? route.providers : [];
  return providers.length > 0 || isPersistedRouteStrategy(route.strategy) || hasExtras;
}
