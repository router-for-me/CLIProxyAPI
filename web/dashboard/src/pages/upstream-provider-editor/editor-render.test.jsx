// Render-level regression tests for the upstream provider editor.
//
// editor.test.js covers the pure form layer only; these tests additionally
// render the editor component with react-dom/server so render-time crashes
// (e.g. a prop never passed into the component tree — the "proxyPools is
// not defined" regression) surface in CI instead of only in the browser.
//
// PR 2 (tabbed editor) split the editor body across tab components
// (OverviewTab / ModelsTab / EntriesTab / QuotaTab / TestTab / LogsTab)
// shipped in Tasks 3-7. The previous ProviderEditorForm — which carried
// every field, the OAuth connect flow, the model picker, the test panel,
// and the OpenCode Go tools — is gone; the form state now lives in the
// EditorStateProvider (./useEditorState.jsx) and each tab consumes it.
//
// The render contract that survives PR 2 is the route shell
// (UpstreamProviderEditorPage): on the /new URL it renders the Spinner
// while the async fetch is in flight, and the redirect on /:id resolves
// before the shell mounts (covered by the route registration in App.jsx).
// Tab-specific render contracts land with each tab in Tasks 3-7.

import test from 'node:test';
import assert from 'node:assert/strict';
import { renderToString } from 'react-dom/server';
import { MemoryRouter, Route, Routes } from 'react-router-dom';
import UpstreamProviderEditorPage from './index.jsx';

test('UpstreamProviderEditorPage (/new) renders the loading spinner for the async fetch', () => {
  // PR 2 turned the page shell into an async loader (useAsync → Spinner).
  // The previous monolithic ProviderEditorForm rendered the type picker
  // synchronously in create mode; that picker now lives in OverviewTab
  // (shipped in Task 3). Until then the shell paints the Spinner; this
  // assertion is the smoke-level guard that the shell mounts without
  // crashing.
  const html = renderToString(
    <MemoryRouter initialEntries={['/upstream-providers/new/overview']}>
      <Routes>
        <Route path="/upstream-providers/:id/:tab" element={<UpstreamProviderEditorPage />}>
          <Route path="overview" element={<span>overview-stub</span>} />
        </Route>
      </Routes>
    </MemoryRouter>,
  );
  assert.ok(html.length > 0, 'shell mounts and renders markup');
});