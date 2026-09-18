import { test } from 'node:test';
import assert from 'node:assert/strict';
import React from 'react';
import { renderToStaticMarkup } from 'react-dom/server';
import { MemoryRouter, Routes, Route } from 'react-router-dom';
import { TabBar } from './TabBar.jsx';

test('TabBar: shows all 6 tabs for opencode-go', () => {
  const html = renderToStaticMarkup(
    React.createElement(MemoryRouter, { initialEntries: ['/1/overview'] },
      React.createElement(Routes, null,
        React.createElement(Route, { path: '/:id/:tab', element: React.createElement(TabBar, { providerType: 'opencode-go', isEntryBearing: true }) })
      )
    )
  );
  assert.match(html, /Overview/);
  assert.match(html, /Models/);
  assert.match(html, /Entries/);
  assert.match(html, /Quota/);
  assert.match(html, /Test/);
  assert.match(html, /Logs/);
});

test('TabBar: hides Quota for non-opencode-go API-key types', () => {
  const html = renderToStaticMarkup(
    React.createElement(MemoryRouter, { initialEntries: ['/1/overview'] },
      React.createElement(Routes, null,
        React.createElement(Route, { path: '/:id/:tab', element: React.createElement(TabBar, { providerType: 'claude-api-key', isEntryBearing: true }) })
      )
    )
  );
  assert.doesNotMatch(html, />Quota</);
  assert.match(html, /Test/);
});

test('TabBar: hides Entries + Quota + Test for OAuth providers', () => {
  const html = renderToStaticMarkup(
    React.createElement(MemoryRouter, { initialEntries: ['/1/overview'] },
      React.createElement(Routes, null,
        React.createElement(Route, { path: '/:id/:tab', element: React.createElement(TabBar, { providerType: 'oauth:claude', isEntryBearing: false }) })
      )
    )
  );
  assert.match(html, /Overview/);
  assert.match(html, /Models/);
  assert.match(html, /Logs/);
  assert.doesNotMatch(html, />Entries</);
  assert.doesNotMatch(html, />Quota</);
  assert.doesNotMatch(html, />Test</);
});