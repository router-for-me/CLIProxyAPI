// Regression test for the "id must be a positive integer" bug.
//
// Root cause: the /upstream-providers/:id redirect used a static
// <Navigate to="/upstream-providers/:id/overview" /> — React Router 6
// does NOT interpolate route params inside a static `to`, so the browser
// landed on the literal path /upstream-providers/:id/overview, the tabbed
// route matched with id=":id", and the editor fetched a non-numeric id
// (server 400: "id must be a positive integer").
//
// detailRedirectTarget pins the interpolation contract (the actual bug:
// the old code had no interpolation at all). The SSR smoke asserts the
// redirect mounts as a route element without throwing. Note that <Navigate>
// performs navigation in an effect, which renderToStaticMarkup does not
// run — so the final-location assertion is not SSR-verifiable; the
// interpolation helper is the deterministic contract surface.

import { test } from 'node:test';
import assert from 'node:assert/strict';
import React from 'react';
import { renderToStaticMarkup } from 'react-dom/server';
import { MemoryRouter, Routes, Route } from 'react-router-dom';
import { UpstreamDetailRedirect, detailRedirectTarget } from './index.jsx';

test('detailRedirectTarget interpolates the provider id', () => {
  assert.equal(detailRedirectTarget('42'), '/upstream-providers/42/overview');
  assert.equal(detailRedirectTarget('7'), '/upstream-providers/7/overview');
});

test('detailRedirectTarget never emits a literal :id segment', () => {
  const target = detailRedirectTarget('42');
  assert.ok(!target.includes(':id'), `target must not contain literal ":id": ${target}`);
});

test('UpstreamDetailRedirect renders null without throwing under SSR', () => {
  const html = renderToStaticMarkup(
    React.createElement(
      MemoryRouter,
      { initialEntries: ['/upstream-providers/42'] },
      React.createElement(
        Routes,
        null,
        React.createElement(Route, {
          path: '/upstream-providers/:id',
          element: React.createElement(UpstreamDetailRedirect),
        }),
      ),
    ),
  );
  assert.equal(html, '');
});
