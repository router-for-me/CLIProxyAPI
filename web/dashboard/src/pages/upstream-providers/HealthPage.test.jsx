import { test } from 'node:test';
import assert from 'node:assert/strict';
import React from 'react';
import { renderToStaticMarkup } from 'react-dom/server';
import { partitionByHealth, getHealthSummary } from './health.js';

test('HealthPage quadrants: partitionByHealth returns 4 buckets', () => {
  const providers = [
    { id: 1, provider_type: 'a', name: 'live', disabled: false },
    { id: 2, provider_type: 'b', name: 'cooldown', disabled: false },
    { id: 3, provider_type: 'c', name: 'breaker', disabled: false },
    { id: 4, provider_type: 'd', name: 'disabled', disabled: true },
  ];
  const liveStatus = {
    1: { is_live: true },
    2: { cooldown_until: '2099-01-01T00:00:00Z' },
    3: { breaker_open: true },
  };
  const p = partitionByHealth(providers, liveStatus);
  assert.equal(p.live.length, 1);
  assert.equal(p.cooldown.length, 1);
  assert.equal(p.breaker.length, 1);
  assert.equal(p.staleOrDisabled.length, 1);
  assert.equal(p.staleOrDisabled[0].row.name, 'disabled');
});

test('HealthPage: summary counts match partition sizes', () => {
  const providers = [
    { id: 1, provider_type: 'a', name: 'live', disabled: false },
    { id: 2, provider_type: 'b', name: 'cooldown', disabled: false },
  ];
  const liveStatus = {
    1: { is_live: true },
    2: { cooldown_until: '2099-01-01T00:00:00Z' },
  };
  const s = getHealthSummary(providers, liveStatus);
  const p = partitionByHealth(providers, liveStatus);
  assert.equal(s.live + s.cooldown + s.breaker_open + s.stale + s.disabled, p.live.length + p.cooldown.length + p.breaker.length + p.staleOrDisabled.length);
});