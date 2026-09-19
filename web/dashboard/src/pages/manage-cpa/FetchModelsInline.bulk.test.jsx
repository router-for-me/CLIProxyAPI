// Smoke + visual regression guard for FetchModelsInline's bulk-select
// additions (Task 1 of PR 3: "Select all N visible" header + N counter
// on the Add button + dedup-before-add).
//
// FetchModelsInline keeps `fetched` in internal state and renders the
// bulk-select UI only after the operator clicks Fetch. A static
// renderToStaticMarkup() snapshot therefore only reaches the collapsed
// header — the per-row picker is gated on stage==='ready', which is set
// by an async network call. Full click-through behavior for Select all
// + Add selected is covered manually per the PR 3 design doc; here we
// only need to (a) prove the component still mounts under the updated
// prop shape and (b) keep a regression guard for the section title and
// idle-state hint, both of which are part of the discover-models UX
// surface that PR 3 expanded.

import { test } from 'node:test';
import assert from 'node:assert/strict';
import React from 'react';
import { renderToStaticMarkup } from 'react-dom/server';
import FetchModelsInline from './FetchModelsInline.jsx';

test('FetchModelsInline: mounts and renders the discover-models section header', () => {
  const html = renderToStaticMarkup(
    React.createElement(FetchModelsInline, {
      form: { models: [] },
      onAddModels: () => {},
    }),
  );
  // The header carries the section title — if the bulk-select refactor
  // accidentally removes or rewrites it, this guard catches it.
  // react-dom/server escapes the apostrophe in "form's" as &#x27;.
  assert.match(html, /Discover models from this form(&#x27;|')s endpoint/);
});

test('FetchModelsInline: hint copy explains the probe path and registry fallback', () => {
  // The expanded-section hint names both the primary path (probe
  // <base_url>/v1/models) and the registry fallback ("only used as a
  // fallback when no Base URL is set"). The bulk-select refactor left
  // this copy alone but it's the kind of thing a careless rename could
  // quietly drop — guard it explicitly.
  const html = renderToStaticMarkup(
    React.createElement(FetchModelsInline, {
      form: { models: [] },
      onAddModels: () => {},
    }),
  );
  assert.match(html, /\/v1\/models/);
  assert.match(html, /fallback when no Base URL is set/);
});

test('FetchModelsInline: does not throw when form.models is missing', () => {
  // The dedup path in handleAdd reads `form.models` defensively (returns
  // [] when undefined). This guard ensures a parent that hasn't seeded
  // models yet still renders cleanly.
  assert.doesNotThrow(() => {
    renderToStaticMarkup(
      React.createElement(FetchModelsInline, {
        form: {},
        onAddModels: () => {},
      }),
    );
  });
});