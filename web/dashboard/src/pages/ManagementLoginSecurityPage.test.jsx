// Smoke test for the Management Login Security page. Kept as a .jsx test so
// the repo's JSX ESM loader can process it. react-dom/server does not run
// effects, so the page renders its loading shell; this guards against
// import/render regressions in the page and its primitives without needing a
// live PG backend. Mirrors the renderToStaticMarkup pattern used by
// ModelPicker.component.test.jsx.
import test from 'node:test';
import assert from 'node:assert/strict';
import React from 'react';
import { renderToStaticMarkup } from 'react-dom/server';
import ManagementLoginSecurityPage from './ManagementLoginSecurityPage.jsx';
import { ToastProvider } from '../components/Toast.jsx';

test('ManagementLoginSecurityPage renders its primary chrome', () => {
  const html = renderToStaticMarkup(
    <ToastProvider>
      <ManagementLoginSecurityPage />
    </ToastProvider>,
  );
  assert.match(html, /Management Login Security/);
  assert.match(html, /Brute-force policy/);
  assert.match(html, /Active bans/);
  assert.match(html, /Login attempts/);
});
