import { describe, expect, test } from 'bun:test';
import { createElement } from 'react';
import { renderToStaticMarkup } from 'react-dom/server';
import { createInstance } from 'i18next';
import { I18nextProvider } from 'react-i18next';
import {
  discoveryCatalog,
  discoverySelectionDiff,
  resolveDiscoverySelection,
  setDiscoveryChoices,
  syncDiscoveredModels,
} from '@/features/providers/discoverySelection';
import { ModelDiscoveryPanel } from '@/features/providers/sheets/forms/ModelDiscoveryPanel';
import type { ModelEntryInput } from '@/features/providers/types';
import { normalizeModelList } from '@/utils/models';

const catalog = [{ name: 'a' }, { name: 'b' }, { name: 'c' }];

describe('discovery synchronization', () => {
  test('preselects existing names and counts distinct additions/removals', () => {
    const existing = new Set([' a ', 'outside']);
    expect([...resolveDiscoverySelection(catalog, existing, new Map())]).toEqual(['a']);
    expect(discoverySelectionDiff(catalog, existing, new Set(['b', 'c']))).toEqual({
      added: 2,
      removed: 1,
    });
  });

  test('preserves retained duplicate aliases and all metadata and outside-catalog rows', () => {
    const first: ModelEntryInput = { name: ' a ', alias: 'one', priority: 8, image: true };
    const second: ModelEntryInput = {
      name: 'a',
      alias: 'two',
      thinkingJson: '{"custom":true}',
      sourceIndex: 3,
    };
    const outside = { name: 'outside', alias: 'custom' };
    const current = [first, second, outside, { name: 'b' }, { name: 'b', alias: 'other' }];
    const result = syncDiscoveredModels(current, [{ name: ' a ' }, { name: ' c ' }], catalog);
    expect(result).toEqual([first, second, outside, { name: 'c', alias: '' }]);
    expect(result[0]).toBe(first);
    expect(result[1]).toBe(second);
    expect(current).toHaveLength(5);
  });

  test('zero picks removes catalog names only and leaves a clean placeholder if empty', () => {
    expect(syncDiscoveredModels([{ name: 'a', image: true }], [], catalog)).toEqual([
      { name: '', alias: '' },
    ]);
    expect(syncDiscoveredModels([{ name: 'a' }, { name: 'outside' }], [], catalog)).toEqual([
      { name: 'outside' },
    ]);
    expect(discoverySelectionDiff(catalog, new Set(['a']), new Set())).toEqual({
      added: 0,
      removed: 1,
    });
  });

  test('normalizes and deduplicates catalog and new picks, ignores out-of-catalog picks', () => {
    expect(discoveryCatalog([{ name: ' a ' }, { name: 'a' }, { name: ' ' }])).toEqual([
      { name: 'a' },
    ]);
    expect(
      syncDiscoveredModels(
        [{ name: '', alias: '' }],
        [{ name: ' b ', alias: ' Bee ' }, { name: 'b' }, { name: 'outside' }],
        catalog
      )
    ).toEqual([{ name: 'b', alias: 'Bee' }]);
  });

  test('imports only explicit aliases, not upstream display names or descriptions', () => {
    const models = normalizeModelList({
      data: [
        { id: 'snake', display_name: 'Snake label', description: 'Description' },
        { id: 'camel', displayName: 'Camel label' },
        { id: 'explicit', alias: 'my-alias', displayName: 'Explicit label' },
        { id: 'described', description: 'Description only' },
      ],
    });

    expect(models).toEqual([
      { name: 'snake', displayName: 'Snake label', description: 'Description' },
      { name: 'camel', displayName: 'Camel label' },
      { name: 'explicit', alias: 'my-alias', displayName: 'Explicit label' },
      { name: 'described', description: 'Description only' },
    ]);
    expect(syncDiscoveredModels([], models, models)).toEqual([
      { name: 'snake', alias: '' },
      { name: 'camel', alias: '' },
      { name: 'explicit', alias: 'my-alias' },
      { name: 'described', alias: '' },
    ]);
  });

  test('filtered select-all and clear-all preserve hidden staged choices', () => {
    let choices = setDiscoveryChoices(new Map(), ['b'], true);
    choices = setDiscoveryChoices(choices, ['a', 'c'], true);
    choices = setDiscoveryChoices(choices, ['a', 'c'], false);
    expect([...resolveDiscoverySelection(catalog, new Set(['a']), choices)]).toEqual(['b']);
    expect(choices.get('b')).toBe(true);
  });

  test('reload and parent updates preserve explicit choices while untouched names follow parent', () => {
    const choices = setDiscoveryChoices(setDiscoveryChoices(new Map(), ['a'], false), ['b'], true);
    expect([...resolveDiscoverySelection([], new Set(['a']), choices)]).toEqual([]);
    expect([...resolveDiscoverySelection(catalog, new Set(['a', 'c']), choices)]).toEqual([
      'b',
      'c',
    ]);
    expect([...resolveDiscoverySelection(catalog, new Set(), choices)]).toEqual(['b']);
  });
});

const i18n = createInstance();
await i18n.init({
  lng: 'en',
  resources: {
    en: {
      translation: {
        providersPage: {
          discovery: {
            applyChanges: 'Apply +{{added}} / -{{removed}}',
            syncHint: 'Selection sync hint',
          },
        },
      },
    },
  },
});

test('panel still shows upstream display names', () => {
  const html = renderToStaticMarkup(
    createElement(
      I18nextProvider,
      { i18n },
      createElement(ModelDiscoveryPanel, {
        loading: false,
        error: null,
        models: [{ name: 'model-id', displayName: 'Friendly label' }],
        hasFetched: true,
        existingNames: new Set(),
        onApply: () => {},
        onReload: () => {},
        onClose: () => {},
      })
    )
  );
  expect(html).toContain('model-id');
  expect(html).toContain('Friendly label');
});

test.each([
  { loading: true, error: null, mutating: false },
  { loading: false, error: 'Fetch failed', mutating: false },
  { loading: false, error: null, mutating: true },
])('panel blocks apply while loading, failed, or saving (%j)', (state) => {
  const html = renderToStaticMarkup(
    createElement(
      I18nextProvider,
      { i18n },
      createElement(ModelDiscoveryPanel, {
        ...state,
        models: catalog,
        hasFetched: true,
        existingNames: new Set(['a']),
        onApply: () => {},
        onReload: () => {},
        onClose: () => {},
      })
    )
  );
  expect(html).toMatch(/<button[^>]*disabled=""[^>]*>Apply \+0 \/ -0<\/button>/);
  const checkboxes = [...html.matchAll(/<input[^>]*type="checkbox"[^>]*>/g)];
  for (const [checkbox] of checkboxes) expect(checkbox).toContain('disabled');
});

test('panel renders existing names as selected editable checkboxes and disables no-op apply', () => {
  const html = renderToStaticMarkup(
    createElement(
      I18nextProvider,
      { i18n },
      createElement(ModelDiscoveryPanel, {
        loading: false,
        error: null,
        models: catalog,
        hasFetched: true,
        existingNames: new Set(['a']),
        onApply: () => {},
        onReload: () => {},
        onClose: () => {},
      })
    )
  );
  const checkboxes = [...html.matchAll(/<input[^>]*type="checkbox"[^>]*>/g)];
  expect(checkboxes).toHaveLength(4);
  expect(checkboxes[1][0]).toContain('checked=""');
  expect(checkboxes[1][0]).not.toContain('disabled');
  expect(html).toContain('Selection sync hint');
  expect(html).toMatch(/<button[^>]*disabled=""[^>]*>Apply \+0 \/ -0<\/button>/);
});
