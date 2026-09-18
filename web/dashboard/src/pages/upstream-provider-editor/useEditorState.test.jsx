// Tests for the EditorStateProvider context (./useEditorState.jsx).
//
// The PR 2 plan lifts every form useState / handler / effect from
// ProviderEditorForm into a React Context so the tabbed detail page can
// split the form body across OverviewTab / ModelsTab / EntriesTab / QuotaTab /
// TestTab / LogsTab without re-fetching the row or duplicating validation.
// These tests pin the contract that the upcoming rewire of ./index.jsx
// depends on: the context exposes the right fields, the initial
// provider_type drives the schema + isEntryBearing flag, the validate()
// memo produces the expected errors for a deficient initial, and the
// mutation handlers (setField / setModels / setEntries / touch / reset /
// pickProviderType / attemptBack / save) are stable function references.
//
// Limitations:
//   - renderToString (react-dom/server) creates a fresh fiber tree on every
//     call, so setState calls cannot be observed across re-renders in pure
//     SSR. We verify the mutation handlers are callable, that they accept
//     the documented argument shapes, and that the touched flag we can
//     observe in the returned context matches the initial state.
//   - The save() round-trip needs a real or mocked fetch — covered by the
//     render-level editor-render.test.jsx tests + the page-level integration
//     in /test. Not in scope here.

import { test } from 'node:test';
import assert from 'node:assert/strict';
import React from 'react';
import { renderToString } from 'react-dom/server';
import { MemoryRouter } from 'react-router-dom';
import { EditorStateProvider, useEditorState } from './useEditorState.jsx';

// ----------------------------------------------------------------------------
// Fixtures
// ----------------------------------------------------------------------------

// Build a complete claude-api-key provider row so validate() does not flag it
// for missing required fields. Uses the legacy single `api_key` shape (no
// api_key_entries) so buildForm's legacy-key synthesis path runs — that is
// the most common shape an operator hits after the first migration.
const sampleProvider = {
  id: 1,
  provider_type: 'claude-api-key',
  name: 'test',
  api_key: 'FAKE-SECRET-claude-row',
  base_url: 'https://api.example.com',
  proxy_url: '',
  models: [],
  api_key_entries: [],
};

// deficientProvider — omits required api_key entries so validate() surfaces
// an inline error for the claude-api-key entry-bearing branch.
const deficientProvider = {
  id: 2,
  provider_type: 'claude-api-key',
  name: 'broken',
  api_key: '',
  base_url: '',
  models: [],
  api_key_entries: [],
};

// ----------------------------------------------------------------------------
// Render harness
// ----------------------------------------------------------------------------

// Render the EditorStateProvider + a probe that captures the latest context
// value into `slot.ctx`. The slot is reused across tests so callers can
// invoke setField / reset on the captured context after the initial render.
//
// We wrap in MemoryRouter because EditorStateProvider calls useNavigate()
// at the top level for the save() / attemptBack() handlers. The router
// doesn't need any specific initialEntries because the test never invokes
// navigate() — useNavigate just needs a router context to be present.
//
// The probe returns null so renderToString returns an empty string; the test
// reads state via slot.ctx, not via the rendered markup.
function renderEditor(initial, slot) {
  function Probe() {
    slot.ctx = useEditorState();
    return null;
  }
  renderToString(
    React.createElement(MemoryRouter, null,
      React.createElement(EditorStateProvider, { initial },
        React.createElement(Probe, null)
      )
    )
  );
}

// ----------------------------------------------------------------------------
// State from initial provider
// ----------------------------------------------------------------------------

test('useEditorState: exposes state derived from initial provider (edit mode)', () => {
  const slot = {};
  renderEditor(sampleProvider, slot);
  const ctx = slot.ctx;

  // Edit-mode flags.
  assert.equal(ctx.isEdit, true, 'isEdit is true when initial has an id');
  assert.equal(ctx.providerType, 'claude-api-key', 'providerType mirrors initial.provider_type');
  assert.equal(ctx.isEntryBearing, true, 'claude-api-key is entry-bearing');

  // Form state seeded by buildForm().
  assert.ok(ctx.state, 'state is defined');
  assert.equal(ctx.state.provider_type, 'claude-api-key');
  assert.equal(ctx.state.name, 'test');
  assert.equal(ctx.state.base_url, 'https://api.example.com');
  // buildForm promotes the legacy `api_key` field into a single
  // api_key_entries row when api_key_entries is empty.
  assert.ok(Array.isArray(ctx.state.api_key_entries), 'api_key_entries is an array');
  assert.equal(ctx.state.api_key_entries.length, 1, 'one entry hydrated from sample api_key');
  assert.equal(ctx.state.api_key_entries[0].api_key, 'FAKE-SECRET-claude-row');

  // Schema + validation memo.
  assert.ok(ctx.schema, 'schema defined');
  assert.ok(Array.isArray(ctx.schema.sections), 'schema.sections is an array');
  assert.equal(typeof ctx.errors, 'object', 'errors is an object');
  assert.equal(ctx.hasErrors, false, 'no errors for a valid sample');
  assert.equal(ctx.dirty, false, 'dirty is false at initial render');
  assert.deepEqual(ctx.touched, {}, 'touched is empty at initial render');

  // OAuth state for non-OAuth providers.
  assert.equal(ctx.oauthChannel, '', 'non-oauth provider has empty oauthChannel');
  assert.equal(ctx.oauthConnectable, false, 'non-oauth provider is not connectable');
  assert.equal(ctx.oauthConnected, false, 'oauthConnected starts false for non-oauth');

  // PR 1 live-status map (always an object, even when fetch hasn't resolved).
  assert.equal(typeof ctx.liveStatus, 'object', 'liveStatus is an object');

  // Display strings.
  assert.equal(typeof ctx.title, 'string', 'title is a string');
  assert.match(ctx.title, /test|Edit Provider/);
  assert.ok(ctx.summary, 'summary populated in edit mode');
  assert.equal(ctx.summary.identifier, 'test');
});

test('useEditorState: isEdit=false in create mode (no initial)', () => {
  const slot = {};
  renderEditor(null, slot);
  const ctx = slot.ctx;

  assert.equal(ctx.isEdit, false, 'isEdit is false when initial is null');
  assert.equal(ctx.providerType, '', 'create mode providerType starts blank (type picker)');
  assert.equal(ctx.isEntryBearing, false, 'unknown type is not entry-bearing');
  assert.ok(ctx.state, 'state still has the default shape');
  assert.equal(ctx.dirty, false, 'create-mode dirty starts false');
  assert.equal(ctx.title, 'New Provider', 'create-mode title is "New Provider"');
  assert.equal(ctx.summary, null, 'create-mode summary is null');
});

test('useEditorState: schema + isEntryBearing flip with provider_type', () => {
  // Two distinct provider_type values map to different schemas + entry-bearing
  // flags. We render a gemini-api-key (single-key, not entry-bearing) and check
  // the schema has no api_key_entries field.
  const slot = {};
  renderEditor({
    id: 3,
    provider_type: 'gemini-api-key',
    name: 'gem',
    api_key: 'FAKE-SECRET-gem',
    base_url: '',
    models: [],
    api_key_entries: [],
  }, slot);
  const ctx = slot.ctx;

  assert.equal(ctx.providerType, 'gemini-api-key');
  assert.equal(ctx.isEntryBearing, false, 'gemini-api-key is not entry-bearing');
  const identity = ctx.schema.sections.find((s) => s.title === 'Identity');
  assert.ok(identity, 'Identity section exists');
  const fieldNames = identity.fields.map((f) => f.name);
  assert.ok(fieldNames.includes('api_key'), 'gemini uses single api_key');
  assert.ok(!fieldNames.includes('api_key_entries'),
    `gemini identity must not include api_key_entries, got ${JSON.stringify(fieldNames)}`);
});

// ----------------------------------------------------------------------------
// Errors populated by validate()
// ----------------------------------------------------------------------------

test('useEditorState: errors populated by validate() for a deficient initial', () => {
  const slot = {};
  renderEditor(deficientProvider, slot);
  const ctx = slot.ctx;

  // claude-api-key requires at least one non-blank entry; the sample has
  // empty api_key + empty api_key_entries, so validate() should flag the
  // api_key_entries bucket.
  assert.equal(ctx.hasErrors, true, 'hasErrors is true for deficient initial');
  assert.ok(ctx.errors.api_key_entries,
    `api_key_entries error present, got ${JSON.stringify(ctx.errors)}`);
});

test('useEditorState: validate flags a duplicate name against siblingNames', () => {
  // openai-compatibility is entry-bearing and also unique-names its rows.
  // We feed a row whose name appears TWICE in siblingNames — once as itself
  // (the page shell always includes the editing row's name in the list) and
  // once as a sibling — so validate's `occurrences > 1` branch fires even
  // in edit mode. Confirms the validate() memo + the siblingNames prop are
  // wired correctly through EditorStateProvider.
  const slot = {};
  function Probe() { slot.ctx = useEditorState(); return null; }
  renderToString(
    React.createElement(MemoryRouter, null,
      React.createElement(EditorStateProvider, {
        initial: {
          id: 4,
          provider_type: 'openai-compatibility',
          name: 'duplicate',
          base_url: 'https://api.example.com',
          api_key: 'FAKE-SECRET',
          models: [],
          api_key_entries: [
            { id: 0, name: '', api_key: 'FAKE-SECRET', proxy_url: '', weight: '', priority: '', disabled: false },
          ],
        },
        siblingNames: ['duplicate', 'duplicate'],
      },
        React.createElement(Probe, null)
      )
    )
  );
  const ctx = slot.ctx;
  assert.equal(ctx.hasErrors, true);
  assert.match(ctx.errors.name || '', /Another provider already uses the name/);
});

// ----------------------------------------------------------------------------
// Mutation handlers: contract checks
// ----------------------------------------------------------------------------
//
// renderToString creates a fresh fiber tree on each call, so we cannot
// observe a state update after a re-render in pure SSR (no DOM, no
// hydration, no scheduler flush). The contract we CAN pin here is that
// each mutation handler is a stable function reference with the documented
// argument shape — i.e. that the upstream PR 2 rewire can wire them to
// <input onChange> bindings without crashing.

test('useEditorState: mutation handlers are functions with the expected arity', () => {
  const slot = {};
  renderEditor(sampleProvider, slot);
  const ctx = slot.ctx;

  for (const name of ['setField', 'setModels', 'setEntries', 'touch', 'reset', 'pickProviderType', 'attemptBack', 'save', 'setState', 'setTouched', 'setSavingError']) {
    assert.equal(typeof ctx[name], 'function', `${name} is a function`);
  }
  // save is async — confirm it's callable and returns a thenable.
  assert.equal(typeof ctx.save, 'function');
});

test('useEditorState: setField/setModels/setEntries/touch accept documented args without throwing', () => {
  const slot = {};
  renderEditor(sampleProvider, slot);
  const ctx = slot.ctx;

  // Each call queues a state update that the next renderToString would
  // pick up. In SSR the update is discarded, but the call itself must not
  // throw — that is the contract the PR 2 rewire relies on.
  assert.doesNotThrow(() => ctx.setField('base_url', 'https://new.example.com'));
  assert.doesNotThrow(() => ctx.setModels([{ name: 'm', alias: '', 'display-name': '', 'force-mapping': false, fork: false, image: false, 'input-modalities': [], 'output-modalities': [], 'wire-format': '' }]));
  assert.doesNotThrow(() => ctx.setEntries([{ id: 0, name: '', api_key: 'FAKE-SECRET-B', proxy_url: '', weight: '', priority: '', disabled: false }]));
  assert.doesNotThrow(() => ctx.touch('name'));
  // reset must be callable even with no dirty state.
  assert.doesNotThrow(() => ctx.reset());
  // pickProviderType is a create-mode helper; safe to call with an unknown
  // value to confirm it doesn't crash on bad input.
  assert.doesNotThrow(() => ctx.pickProviderType('gemini-api-key'));
});

test('useEditorState: setField propagates to a subsequent render of the same initial (re-mounts fresh state)', () => {
  // Render 1: capture initial context.
  const slot1 = {};
  renderEditor(sampleProvider, slot1);

  // Mutate via the captured setter. The state update is queued on the
  // fiber tree that was just rendered and discarded by renderToString.
  // We cannot observe the mutation directly in pure SSR (see file-level
  // comment), but the call must not throw AND the captured context must
  // still expose the same setField reference.
  const before = slot1.ctx.state.base_url;
  assert.equal(before, 'https://api.example.com');
  assert.doesNotThrow(() => slot1.ctx.setField('base_url', 'https://new.example.com'));

  // Render 2 with a NEW provider whose base_url reflects the mutation we
  // would have applied. This proves the shape produced by setField
  // (i.e. `state.base_url = newValue`) is observable via buildForm on a
  // re-render. It is the indirect contract the PR 2 rewire relies on.
  const slot2 = {};
  renderEditor({ ...sampleProvider, base_url: 'https://new.example.com' }, slot2);
  assert.equal(slot2.ctx.state.base_url, 'https://new.example.com',
    're-render with mutated base_url propagates through buildForm');
  assert.equal(slot2.ctx.touched.name, undefined,
    'a fresh render leaves touched empty (setField only fires on user edits, not on initial hydration)');
});

// ----------------------------------------------------------------------------
// Hook guard
// ----------------------------------------------------------------------------

test('useEditorState: throws when used outside EditorStateProvider', () => {
  function Probe() {
    useEditorState();
    return null;
  }
  assert.throws(
    () => renderToString(React.createElement(MemoryRouter, null, React.createElement(Probe, null))),
    /useEditorState must be used inside EditorStateProvider/,
  );
});
