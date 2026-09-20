// Route-shape + tab-link regression tests for the tabbed provider editor.
//
// Root cause A (route table): the tabbed route was declared with the tab
// segment in the PARENT path —
//   <Route path="/upstream-providers/:id/:tab"> + static children
// React Router 6 child paths are RELATIVE to the parent path, so the static
// children (overview/models/...) could only match the unreachable URL
// /upstream-providers/:id/:tab/models. Real tab URLs
// /upstream-providers/:id/:tab exhausted the parent path with zero
// remaining segments, the index route matched, and its
// <Navigate to="overview" replace /> bounced every tab click back to
// /overview. (Overview looked fine because that redirect resolved to the
// URL already shown, making it a no-op.)
//
// Root cause B (TabBar): `to="../models" relative="path"` resolved against
// the matched route (/upstream-providers/:id), so ".." consumed the :id
// segment and emitted /upstream-providers/models — a link with no provider.
//
// The route assertions run against App.jsx's REAL UPSTREAM_ROUTES export via
// React Router's own conversion + matcher, so the copy cannot drift from
// what ships. SSR alone cannot catch either bug: <Navigate> only navigates
// in an effect, which renderToStaticMarkup/renderToString never run.

import { test } from 'node:test';
import assert from 'node:assert/strict';
import React from 'react';
import {
  createRoutesFromChildren, matchRoutes, MemoryRouter, Routes, Route,
} from 'react-router-dom';
import { renderToStaticMarkup } from 'react-dom/server';
import { UPSTREAM_ROUTES } from '../../App.jsx';
import { TabBar } from './TabBar.jsx';

const routes = createRoutesFromChildren(UPSTREAM_ROUTES);

const leafOf = (url) => {
  const matches = matchRoutes(routes, url);
  assert.ok(matches, `${url} must match a route`);
  return matches[matches.length - 1];
};

test('each tab URL matches its own child route, not the index redirect', () => {
  for (const tab of ['overview', 'models', 'entries', 'quota', 'test', 'logs']) {
    const leaf = leafOf(`/upstream-providers/42/${tab}`);
    assert.equal(
      leaf.route.path,
      tab,
      `/upstream-providers/42/${tab} must match the "${tab}" child, got ` +
        (leaf.route.index ? 'INDEX (bounces to overview)' : leaf.route.path),
    );
  }
});

test('tab URLs keep the provider id param', () => {
  assert.deepEqual(leafOf('/upstream-providers/42/models').params, { id: '42' });
});

test('bare /:id matches the index route (the redirect to overview)', () => {
  const leaf = leafOf('/upstream-providers/42');
  assert.ok(leaf.route.index, 'bare /:id must match the index route');
  assert.deepEqual(leaf.params, { id: '42' });
});

test('unknown tab segment matches the * fallback instead of 404ing', () => {
  assert.equal(leafOf('/upstream-providers/42/bogus').route.path, '*');
});

test('static /new and /health win over the :id parent', () => {
  assert.equal(leafOf('/upstream-providers/new').route.path, '/upstream-providers/new');
  assert.equal(leafOf('/upstream-providers/health').route.path, '/upstream-providers/health');
});

test('TabBar links embed the provider id (not a stripped /upstream-providers/<tab>)', () => {
  const html = renderToStaticMarkup(
    <MemoryRouter initialEntries={['/upstream-providers/42/overview']}>
      <Routes>
        <Route path="/upstream-providers/:id" element={<TabBar providerType="opencode-go" isEntryBearing />}>
          <Route path="overview" element={<span />} />
        </Route>
      </Routes>
    </MemoryRouter>,
  );
  const hrefs = [...html.matchAll(/href="([^"]+)"/g)].map((m) => m[1]);
  assert.ok(hrefs.length > 0, 'TabBar must render links');
  for (const href of hrefs) {
    assert.match(
      href,
      /^\/upstream-providers\/42\//,
      `tab link must keep the provider id, got ${href}`,
    );
  }
  assert.ok(hrefs.includes('/upstream-providers/42/models'), `expected a models link, got ${hrefs.join(', ')}`);
});
