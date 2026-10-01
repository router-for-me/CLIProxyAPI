// Pure helpers for the Manage LiteLLM → On-the-fly Log tab. Kept in a plain
// .js module (no JSX) so they can be unit-tested with the node:test runner,
// mirroring the LogsPage.test.js / ProxyPoolsPage.test.js convention.

export const ONTHEFLY_LIMIT_OPTIONS = [50, 100, 200, 500];

export const ONTHEFLY_AUTOREFRESH_STORAGE = 'nixllm.dashboard.litellmOnTheFlyAutorefresh';

export const ONTHEFLY_OUTCOME_OPTIONS = [
  { value: '', label: 'All outcomes' },
  { value: 'synced', label: 'Synced' },
  { value: 'unmatched', label: 'Unmatched' },
  { value: 'invalid', label: 'Invalid' },
  { value: 'error', label: 'Error' },
];

const OUTCOME_LABELS = {
  synced: 'Synced',
  unmatched: 'Unmatched',
  invalid: 'Invalid',
  error: 'Error',
};

// Badge classes here are limited to variants actually defined in global.css
// (badge--ok / badge--err are used elsewhere but are not defined).
const OUTCOME_BADGES = {
  synced: 'badge--active',
  unmatched: 'badge--disabled',
  invalid: 'badge--warn',
  error: 'badge--revoked',
};

export function formatOnTheFlyOutcome(outcome) {
  return OUTCOME_LABELS[outcome] || outcome || '—';
}

export function onTheFlyOutcomeBadge(outcome) {
  return OUTCOME_BADGES[outcome] || 'badge--muted';
}

// summarizeOnTheFlyOutcomes counts rows per known outcome for the KPI strip.
// Unknown outcome values are ignored.
export function summarizeOnTheFlyOutcomes(entries) {
  const counts = { synced: 0, unmatched: 0, invalid: 0, error: 0 };
  for (const e of entries || []) {
    if (Object.prototype.hasOwnProperty.call(counts, e?.outcome)) {
      counts[e.outcome] += 1;
    }
  }
  return counts;
}

// readOnTheFlyAutoRefresh reads the persisted auto-refresh preference
// (enabled by default). Falls back to enabled when storage is unavailable.
export function readOnTheFlyAutoRefresh() {
  try {
    return localStorage.getItem(ONTHEFLY_AUTOREFRESH_STORAGE) !== '0';
  } catch {
    return true;
  }
}
