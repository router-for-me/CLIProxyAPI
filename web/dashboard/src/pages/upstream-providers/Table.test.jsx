import { test } from 'node:test';
import assert from 'node:assert/strict';
import React from 'react';
import { renderToStaticMarkup } from 'react-dom/server';
import { MemoryRouter } from 'react-router-dom';
import Table from './Table.jsx';

// MemoryRouter wrapper is required because Table calls useNavigate()
// (clickable rows navigate to /upstream-providers/<id>). React Router's
// invariant throws when useNavigate is invoked outside a <Router> context,
// so SSR tests must render inside one. MemoryRouter is the standard SSR
// fixture for this case — it does not change Table's render output.
function render(element) {
  return renderToStaticMarkup(
    React.createElement(MemoryRouter, null, element),
  );
}

const providers = [
  { id: 1, provider_type: 'openai-compatibility', name: 'live-row', base_url: 'https://a', priority: 0, disabled: false, models: [], updated_at: '2026-09-19T00:00:00Z' },
  { id: 2, provider_type: 'claude-api-key', name: 'cooldown-row', base_url: 'https://b', priority: 0, disabled: false, models: [], updated_at: '2026-09-19T00:00:00Z' },
  { id: 3, provider_type: 'gemini-api-key', name: 'disabled-row', base_url: 'https://c', priority: 0, disabled: true, models: [], updated_at: '2026-09-19T00:00:00Z' },
];

const liveStatus = {
  1: { is_live: true },
  2: { cooldown_until: '2099-01-01T00:00:00Z', last_error: '429' },
};

test('Table: renders one row per provider', () => {
  const html = render(
    React.createElement(Table, {
      providers, liveStatus, refreshing: false,
      onRefreshHealth: () => {}, onEdit: () => {}, onDelete: () => {}, onBulkAction: () => {},
    }),
  );
  assert.match(html, /live-row/);
  assert.match(html, /cooldown-row/);
  assert.match(html, /disabled-row/);
});

test('Table: refresh health button is present', () => {
  const html = render(
    React.createElement(Table, {
      providers, liveStatus, refreshing: false,
      onRefreshHealth: () => {}, onEdit: () => {}, onDelete: () => {}, onBulkAction: () => {},
    }),
  );
  assert.match(html, /Refresh health/i);
});

test('Table: StatusDot rendered with correct status per row', () => {
  const html = render(
    React.createElement(Table, {
      providers, liveStatus, refreshing: false,
      onRefreshHealth: () => {}, onEdit: () => {}, onDelete: () => {}, onBulkAction: () => {},
    }),
  );
  assert.match(html, /Live/);
  assert.match(html, /Cooldown/);
  assert.match(html, /Disabled/);
});
