import test from 'node:test';
import assert from 'node:assert/strict';
import { filterLiveModels, modelHealthIsLive } from './modelPickerFilters.js';

test('modelHealthIsLive returns true for status healthy', () => {
  assert.equal(modelHealthIsLive({ status: 'healthy', consecutive_failures: 0 }), true);
});

test('modelHealthIsLive returns false for status down or consecutive_failures >= 3', () => {
  assert.equal(modelHealthIsLive({ status: 'healthy', consecutive_failures: 5 }), false);
  assert.equal(modelHealthIsLive({ status: 'down', consecutive_failures: 0 }), false);
});

test('filterLiveModels keeps only models with at least one healthy auth', () => {
  const upstreams = [
    { id: 'u1', model: 'gpt-4o', provider_type: 'openai' },
    { id: 'u2', model: 'gpt-4o', provider_type: 'openai' },
    { id: 'u3', model: 'claude-3-5-sonnet', provider_type: 'claude' },
  ];
  const health = [
    { model: 'gpt-4o', status: 'healthy', consecutive_failures: 0 },
    { model: 'gpt-4o', status: 'down', consecutive_failures: 4 },
    { model: 'claude-3-5-sonnet', status: 'down', consecutive_failures: 9 },
  ];
  const live = filterLiveModels(upstreams, health);
  // gpt-4o has at least one healthy auth; claude-3-5-sonnet has none.
  assert.deepEqual(live.map((u) => u.model).sort(), ['gpt-4o', 'gpt-4o']);
});
