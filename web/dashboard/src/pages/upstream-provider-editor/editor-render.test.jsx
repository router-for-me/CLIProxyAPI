// Render-level regression tests for the upstream provider editor.
//
// editor.test.js covers the pure form layer only; these tests additionally
// render the editor component with react-dom/server so render-time crashes
// (e.g. a prop never passed into the component tree — the "proxyPools is
// not defined" regression) surface in CI instead of only in the browser.
// No DOM and no network: MemoryRouter satisfies useNavigate, and useToast
// degrades to a no-op outside its provider.

import test from 'node:test';
import assert from 'node:assert/strict';
import { renderToString } from 'react-dom/server';
import { MemoryRouter, Route, Routes } from 'react-router-dom';
import UpstreamProviderEditorPage, { ProviderEditorForm } from './index.jsx';

function renderEditor(provider, props = {}) {
  return renderToString(
    <MemoryRouter>
      <ProviderEditorForm provider={provider} siblingNames={[]} {...props} />
    </MemoryRouter>,
  );
}

test('ProviderEditorForm renders proxy_pool_id picker with active pools passed as prop', () => {
  const provider = {
    id: 3,
    provider_type: 'gemini-api-key',
    name: 'gemini-main',
    api_key: 'FAKE-SECRET-KEY',
    base_url: 'https://api.example.com',
  };
  const pools = [
    { id: 7, name: 'eu-pool', is_active: true },
    { id: 9, name: 'retired-pool', is_active: false },
  ];
  const html = renderEditor(provider, { proxyPools: pools });
  assert.ok(html.includes('up_proxy_pool_id'), 'renders the proxy pool select');
  assert.ok(html.includes('eu-pool'), 'active pool appears as an option');
  assert.ok(!html.includes('retired-pool'), 'inactive pool is filtered out');
});

test('ProviderEditorForm renders without pools (picker degrades to inherit/none)', () => {
  const provider = {
    id: 3,
    provider_type: 'gemini-api-key',
    name: 'gemini-main',
    api_key: 'FAKE-SECRET-KEY',
  };
  const html = renderEditor(provider);
  assert.ok(html.includes('up_proxy_pool_id'), 'renders the proxy pool select');
});

// Regression: the page previously referenced `proxyPools` inside
// ProviderEditorForm without ever passing it down, crashing the detail page
// on open with "ReferenceError: proxyPools is not defined". Create mode
// renders the type picker first (empty schema → no fields), so this asserts
// the page shell mounts the editor without crashing; the prop pass-through
// itself is covered by the component-level tests above. The page must be
// mounted inside a matching Route — resolveEditorMode depends on useParams.
test('UpstreamProviderEditorPage (/new) renders the editor without proxyPools ReferenceError', () => {
  const html = renderToString(
    <MemoryRouter initialEntries={['/upstream-providers/new']}>
      <Routes>
        <Route path="/upstream-providers/:id" element={<UpstreamProviderEditorPage />} />
      </Routes>
    </MemoryRouter>,
  );
  assert.ok(html.length > 0, 'page renders markup');
  assert.ok(html.includes('type-picker__card'), 'create mode renders the type picker');
});

test('ProviderEditorForm renders per-entry disabled toggle (checked for disabled entries)', () => {
  const provider = {
    id: 5,
    provider_type: 'openai-compatibility',
    name: 'compat',
    api_key_entries: [
      { id: 1, api_key: 'FAKE-SECRET-ONE', name: 'live' },
      { id: 2, api_key: 'FAKE-SECRET-TWO', name: 'off', disabled: true },
    ],
  };
  const html = renderEditor(provider);
  assert.ok(html.includes('api-key-entry-disabled-0'), 'toggle renders for entry 0');
  assert.ok(html.includes('api-key-entry-disabled-1'), 'toggle renders for entry 1');
  // SSR renders checked checkboxes with the checked attribute; entry 1's
  // toggle (the disabled one) must carry it while entry 0's must not.
  const toggles = html.match(/<label[^>]*api-key-entry-disabled-\d[\s\S]*?<\/label>/g) || [];
  assert.equal(toggles.length, 2, 'two entry toggles rendered');
  assert.ok(!/checked/.test(toggles[0]), 'active entry toggle is unchecked');
  assert.ok(/checked/.test(toggles[1]), 'disabled entry toggle is checked');
  // The disabled row group carries the dimmed styling hook.
  assert.ok(html.includes('list-editor__rowgroup--disabled'), 'disabled row group is dimmed');
});

test('ProviderEditorForm renders the TestPanel in edit mode (entry-bearing provider)', () => {
  const provider = {
    id: 5,
    provider_type: 'openai-compatibility',
    name: 'compat',
    base_url: 'https://api.example.com',
    models: [{ name: 'gpt-4o' }],
    api_key_entries: [
      { id: 1, api_key: 'FAKE-SECRET-ONE', name: 'live' },
      { id: 2, api_key: 'FAKE-SECRET-TWO', name: 'off', disabled: true },
    ],
  };
  const html = renderEditor(provider);
  assert.ok(html.includes('provider-test-panel'), 'test panel renders');
  assert.ok(html.includes('Test entry'), 'panel title renders');
  assert.ok(html.includes('test-panel-entry'), 'entry dropdown renders for entry-bearing provider');
  assert.ok(html.includes('(provider-level)'), 'provider-level option present');
  assert.ok(html.includes('live'), 'entry 1 label visible');
  assert.ok(html.includes('off (disabled)'), 'disabled entry labeled with suffix');
  assert.ok(html.includes('gpt-4o'), 'model dropdown populated from form.models');
  assert.ok(html.includes('test-panel-run'), 'run button renders');
});

test('ProviderEditorForm renders the TestPanel entry dropdown for opencode-go', () => {
  // Regression: opencode-go rows are entry-bearing (schema renders the
  // multi-row api_key_entries editor and the server resolves entry-level
  // probes via "<routing-key>:key-<entryID>"), but isEntryBearing previously
  // listed only openai-compatibility and claude-api-key — so the Test
  // panel's Entry dropdown never appeared for OpenCode Go rows.
  const provider = {
    id: 95,
    provider_type: 'opencode-go',
    name: 'ocg',
    base_url: 'https://opencode.ai/zen/go/v1',
    models: [{ name: 'glm-5.2' }],
    api_key_entries: [
      { id: 33, api_key: 'FAKE-SECRET-ONE', name: 'semutsshopus5' },
      { id: 34, api_key: 'FAKE-SECRET-TWO', name: '', disabled: true },
    ],
  };
  const html = renderEditor(provider);
  assert.ok(html.includes('provider-test-panel'), 'test panel renders');
  assert.ok(html.includes('test-panel-entry'), 'entry dropdown renders for opencode-go');
  assert.ok(html.includes('(provider-level)'), 'provider-level option present');
  assert.ok(html.includes('semutsshopus5'), 'named entry label visible');
  assert.ok(html.includes('key-34 (disabled)'), 'unnamed disabled entry falls back to key-<id> with suffix');
  assert.ok(html.includes('glm-5.2'), 'model dropdown populated from form.models');
});

test('ProviderEditorForm omits the TestPanel in create mode', () => {
  const html = renderToString(
    <MemoryRouter initialEntries={['/upstream-providers/new']}>
      <Routes>
        <Route path="/upstream-providers/:id" element={<UpstreamProviderEditorPage />} />
      </Routes>
    </MemoryRouter>,
  );
  assert.ok(html.length > 0, 'page renders');
  assert.ok(!html.includes('provider-test-panel'), 'no test panel in create mode');
});
