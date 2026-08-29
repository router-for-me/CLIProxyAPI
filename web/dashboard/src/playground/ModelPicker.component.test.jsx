// Component-level smoke test for the unified ModelPicker. Kept in a
// separate file so the JSX ESM loader (which only matches .jsx) can
// process it without disturbing the existing modelPickerFilters unit
// tests in ModelPicker.test.js.
//
// Smoke test for the ModelPicker component shell: the Catalog/Upstream
// tabs were collapsed into a single text search input. We render via
// react-dom/server so the data-loading useEffect never fires; the input
// must be present in the initial markup either way.

import test from 'node:test';
import assert from 'node:assert/strict';
import React from 'react';
import { renderToStaticMarkup } from 'react-dom/server';
import ModelPicker from './ModelPicker.jsx';

test('ModelPicker renders a single search input (no tab strip)', () => {
  const html = renderToStaticMarkup(<ModelPicker value="" onChange={() => {}} />);
  assert.match(html, /<input[^>]*type="text"/);
  assert.doesNotMatch(html, /role="tab"/);
});
