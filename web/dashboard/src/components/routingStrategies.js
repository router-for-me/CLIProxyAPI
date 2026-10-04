// routingStrategies — single source of truth for every "routing strategy"
// value set the dashboard exposes.
//
// "Routing strategy" is overloaded across four distinct namespaces. Keeping
// their option lists in one module makes the differences explicit instead of
// leaving each surface to invent (and drift from) its own values.
//
//   1. Global  — `routing.strategy`, the top-level credential selector.
//                Accepted set mirrors the management API's
//                normalizeRoutingStrategy (config_basic.go): round-robin,
//                weighted-round-robin, fill-first, power-of-two-choices,
//                least-used. Note this differs from the config package's
//                GlobalStrategy* constants (weighted/headroom), which the
//                /routing/strategy endpoint rejects.
//   2. Pool    — an entry-bearing upstream provider's `routing_strategy`
//                (claude-api-key / openai-compatibility). Same value set as
//                global today, different scope: it selects within one
//                provider's api_key_entries pool.
//   3. Route   — a per-model ModelRoute strategy: "" (inherit), priority,
//                failover; plus weighted for the vision-bridge route only.
//   4. Target  — an auto-router tier's multi-target selection strategy:
//                "" (weighted) and priority.

// GLOBAL_STRATEGY_OPTIONS is the top-level `routing.strategy` selector.
export const GLOBAL_STRATEGY_OPTIONS = [
  {
    value: 'round-robin',
    label: 'Round-robin',
    short: 'round-robin',
    blurb: 'Rotate evenly across every eligible credential. The default.',
  },
  {
    value: 'weighted-round-robin',
    label: 'Weighted round-robin',
    short: 'weighted-rr',
    blurb: 'Rotate proportionally to each credential\u2019s weight (default 1, max 1,000,000). Non-positive weights are excluded.',
  },
  {
    value: 'fill-first',
    label: 'Fill-first',
    short: 'fill-first',
    blurb: 'Keep using the preferred credential until it is exhausted or at capacity, then move on.',
  },
  {
    value: 'power-of-two-choices',
    label: 'Power of two choices',
    short: 'p2c',
    blurb: 'Sample two candidates and pick the less loaded one. Falls back to fill-first until the in-flight scheduler lands.',
  },
  {
    value: 'least-used',
    label: 'Least used',
    short: 'least-used',
    blurb: 'Pick the credential currently handling the fewest requests. Falls back to fill-first until the in-flight scheduler lands.',
  },
];

// POOL_STRATEGY_OPTIONS is the per-upstream `routing_strategy` select. It
// shares the global value set; empty means "follow the global strategy".
export const POOL_STRATEGY_OPTIONS = [
  { value: '', label: 'Default (global)' },
  { value: 'round-robin', label: 'Round-robin' },
  { value: 'weighted-round-robin', label: 'Weighted round-robin' },
  { value: 'fill-first', label: 'Fill-first (priority)' },
  { value: 'power-of-two-choices', label: 'Power of two choices' },
  { value: 'least-used', label: 'Least used' },
  { value: 'failover', label: 'Failover' },
];

export const POOL_STRATEGY_HINT =
  'Empty = follow the global routing strategy. Any value enables aggressive in-pool failover: on any entry error the next entry is tried first; errors surface only after the whole pool is exhausted. Fill-first \u2248 priority, failover \u2248 round-robin within a priority tier. Power of two choices and least used currently fall back to the deterministic fill-first behavior until the in-flight-aware scheduler lands.';

// ROUTE_STRATEGY_OPTIONS is the per-model ModelRoute strategy. "" inherits the
// global strategy.
export const ROUTE_STRATEGY_OPTIONS = [
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
    blurb: 'Start at the highest-priority provider; the conductor switches providers on upstream errors. Best for zero downtime.',
  },
];

// WEIGHTED_ROUTE_OPTION is appended to the route picker only when the caller
// passes allowWeighted. The backend's per-target tier routing rejects
// "weighted", so regular route editors must not surface it; the vision bridge
// route (the only consumer today) accepts it as a per-request load spread.
export const WEIGHTED_ROUTE_OPTION = {
  value: 'weighted',
  label: 'Weighted',
  short: 'weighted',
  blurb: "Pick one provider per request, weighted by each provider's priority. Spreads load across providers.",
};

// routeStrategyOptions returns the route strategy option list, including the
// weighted option only when allowWeighted is true.
export function routeStrategyOptions(allowWeighted) {
  return allowWeighted
    ? [WEIGHTED_ROUTE_OPTION, ...ROUTE_STRATEGY_OPTIONS]
    : ROUTE_STRATEGY_OPTIONS;
}

// TARGET_STRATEGY_OPTIONS is an auto-router tier's multi-target selection
// strategy. "" is weighted-random failover; a single target is unaffected.
export const TARGET_STRATEGY_OPTIONS = [
  {
    value: '',
    label: 'Weighted failover',
    short: 'weighted-failover',
    blurb: 'Pick a target model at random weighted by weight, then try the next target if the first fails (failover chain).',
  },
  {
    value: 'priority',
    label: 'Priority',
    short: 'priority',
    blurb: 'Always pick the highest-weight target model; ties resolve to the first listed. No randomness.',
  },
];

// activeOption returns the option matching value, falling back to the first
// entry (the default) so callers always render a blurb.
export function activeStrategyOption(options, value) {
  const v = typeof value === 'string' ? value : '';
  return options.find((o) => o.value === v) || options[0];
}
