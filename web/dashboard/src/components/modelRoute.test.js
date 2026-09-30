import { test } from 'node:test';
import assert from 'node:assert/strict';
import {
  routeToWire,
  routeHasConfig,
  isPersistedRouteStrategy,
  dedupeStrings,
} from './modelRoute.js';

// --- isPersistedRouteStrategy ---------------------------------------------

test('isPersistedRouteStrategy accepts priority and failover only', () => {
  assert.equal(isPersistedRouteStrategy('priority'), true);
  assert.equal(isPersistedRouteStrategy('failover'), true);
  assert.equal(isPersistedRouteStrategy('  priority  '), true);
  assert.equal(isPersistedRouteStrategy('weighted'), false);
  assert.equal(isPersistedRouteStrategy(''), false);
  assert.equal(isPersistedRouteStrategy(undefined), false);
});

// --- dedupeStrings --------------------------------------------------------

test('dedupeStrings trims, drops empties, and preserves first-seen order', () => {
  assert.deepEqual(dedupeStrings([' a ', 'b', 'a', '', '  ', 'b']), ['a', 'b']);
});

test('dedupeStrings returns [] for non-arrays', () => {
  assert.deepEqual(dedupeStrings(null), []);
  assert.deepEqual(dedupeStrings(undefined), []);
  assert.deepEqual(dedupeStrings('a,b'), []);
});

// --- routeToWire ----------------------------------------------------------

test('routeToWire keeps providers and a valid strategy with priorities', () => {
  const out = routeToWire({
    model: 'gpt-4o',
    providers: ['claude:1', 'claude:1', 'openai-compatible-x'],
    strategy: 'failover',
    priorities: [
      { provider: 'claude:1', priority: 10 },
      { provider: 'openai-compatible-x', priority: 2 },
      { provider: 'not-pinned', priority: 99 },
    ],
  });
  assert.deepEqual(out.providers, ['claude:1', 'openai-compatible-x']);
  assert.equal(out.strategy, 'failover');
  assert.deepEqual(out.priorities, [
    { provider: 'claude:1', priority: 10 },
    { provider: 'openai-compatible-x', priority: 2 },
  ]);
});

test('routeToWire drops the weighted strategy and its priorities', () => {
  const out = routeToWire({
    model: 'gpt-4o',
    providers: ['claude:1'],
    strategy: 'weighted',
    priorities: [{ provider: 'claude:1', priority: 5 }],
  });
  assert.equal(out.strategy, undefined);
  assert.equal(out.priorities, undefined);
  assert.deepEqual(out.providers, ['claude:1']);
});

test('routeToWire omits priorities when none match a pinned provider', () => {
  const out = routeToWire({
    model: 'gpt-4o',
    providers: ['claude:1'],
    strategy: 'priority',
    priorities: [{ provider: 'other', priority: 1 }],
  });
  assert.equal(out.strategy, 'priority');
  assert.equal(out.priorities, undefined);
});

test('routeToWire handles a route with no pins', () => {
  const out = routeToWire({ model: 'gpt-4o', providers: [], strategy: 'priority', priorities: [] });
  assert.deepEqual(out.providers, []);
  assert.equal(out.strategy, 'priority');
  assert.equal(out.priorities, undefined);
});

test('routeToWire tolerates null', () => {
  assert.deepEqual(routeToWire(null), { model: '', providers: [] });
});

// --- routeHasConfig -------------------------------------------------------

test('routeHasConfig is true when providers are pinned', () => {
  assert.equal(routeHasConfig({ providers: ['claude:1'] }), true);
});

test('routeHasConfig is true for a persisted strategy even with no pins', () => {
  assert.equal(routeHasConfig({ providers: [], strategy: 'priority' }), true);
});

test('routeHasConfig is false for weighted with no pins', () => {
  assert.equal(routeHasConfig({ providers: [], strategy: 'weighted' }), false);
});

test('routeHasConfig honors hasExtras for cap-only rows', () => {
  assert.equal(routeHasConfig({ providers: [], strategy: '' }), false);
  assert.equal(routeHasConfig({ providers: [], strategy: '' }, { hasExtras: true }), true);
});

test('routeHasConfig is false for null', () => {
  assert.equal(routeHasConfig(null), false);
});
