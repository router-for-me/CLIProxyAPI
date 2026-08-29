import test from 'node:test';
import assert from 'node:assert/strict';
import React from 'react';
import { renderToStaticMarkup } from 'react-dom/server';
import CooldownBanner from './CooldownBanner.jsx';

test('renders nothing when state is null', () => {
  const html = renderToStaticMarkup(<CooldownBanner state={null} onRefresh={() => {}} />);
  assert.equal(html, '');
});

test('renders the warning copy and a refresh link when state is present', () => {
  const html = renderToStaticMarkup(
    <CooldownBanner
      state={{ state: 'cooldown', cooldownUntil: '2030-01-01T00:00:00Z' }}
      onRefresh={() => {}}
    />,
  );
  assert.match(html, /playground-cooldown-banner/);
  assert.match(html, /cooldown/i);
  assert.match(html, /Refresh/);
});