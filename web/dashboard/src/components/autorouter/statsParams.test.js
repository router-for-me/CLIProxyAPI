import { test } from 'node:test';
import assert from 'node:assert/strict';
import { buildStatsParams } from './statsParams.js';

// The stats endpoints (and usage_events.router_id) key on the router's PK id,
// not its requestable model_id. Sending model_id silently matches zero
// persisted events, which rendered the Jev AI tab as "opted in but no request
// in range" even while the classifier was running.
test('buildStatsParams keys router_id on the router PK id, not model_id', () => {
  const params = buildStatsParams(
    { id: '9f1c-...', model_id: 'router:smart' },
    '',
    { from: '2026-09-01T00:00:00Z', to: '2026-09-08T00:00:00Z' },
  );
  assert.equal(params.router_id, '9f1c-...');
});

test('buildStatsParams omits router_id when no router is selected', () => {
  const params = buildStatsParams(null, '', { from: 'a', to: 'b' });
  assert.equal(params.router_id, undefined);
});

test('buildStatsParams includes api_key_id only when set', () => {
  const range = { from: 'a', to: 'b' };
  assert.equal(buildStatsParams({ id: 'r' }, 'key-1', range).api_key_id, 'key-1');
  assert.equal(buildStatsParams({ id: 'r' }, '', range).api_key_id, undefined);
});

test('buildStatsParams forwards the range bounds', () => {
  const params = buildStatsParams({ id: 'r' }, '', { from: 'F', to: 'T' });
  assert.equal(params.from, 'F');
  assert.equal(params.to, 'T');
});
