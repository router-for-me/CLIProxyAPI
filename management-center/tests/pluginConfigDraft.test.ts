import { describe, expect, test } from 'bun:test';
import {
  buildPluginConfigDraft,
  buildPluginConfigPatch,
} from '../src/features/plugins/pluginConfigDraft';
import type { PluginConfigField } from '../src/types';

const fields: PluginConfigField[] = [
  { name: 'mixed', type: 'array', enumValues: [], description: '' },
  { name: 'optional', type: 'boolean', enumValues: [], description: '' },
  { name: 'label', type: 'string', enumValues: [], description: '' },
];

const t = (key: string) => key;

describe('plugin config draft', () => {
  test('represents arrays as JSON without marking missing booleans as touched', () => {
    const draft = buildPluginConfigDraft(
      { enabled: true, configFields: fields },
      { priority: 3, mixed: [1, true, { x: 1 }] }
    );

    expect(draft.values.mixed).toBe('[\n  1,\n  true,\n  {\n    "x": 1\n  }\n]');
    expect(draft.values.optional).toBe(false);
    expect(draft.touchedFields).toEqual({});
  });

  test('only patches touched base fields and preserves untouched plugin values', () => {
    const draft = buildPluginConfigDraft(
      { enabled: true, configFields: fields },
      { priority: 3, mixed: [1, true, { x: 1 }] }
    );
    draft.priority = '5';
    draft.priorityTouched = true;

    expect(buildPluginConfigPatch(draft, fields, t)).toEqual({
      patch: { priority: 5 },
      errors: {},
    });
  });

  test('parses touched array JSON without coercing item types', () => {
    const draft = buildPluginConfigDraft(
      { enabled: true, configFields: fields },
      { mixed: ['old'] }
    );
    draft.values.mixed = '[1, false, {"next": 2}]';
    draft.touchedFields.mixed = true;

    expect(buildPluginConfigPatch(draft, fields, t)).toEqual({
      patch: { mixed: [1, false, { next: 2 }] },
      errors: {},
    });
  });

  test('writes a missing boolean only after the user touches it', () => {
    const draft = buildPluginConfigDraft({ enabled: true, configFields: fields }, {});
    draft.touchedFields.optional = true;

    expect(buildPluginConfigPatch(draft, fields, t)).toEqual({
      patch: { optional: false },
      errors: {},
    });
  });

  test('deletes a cleared touched field with null', () => {
    const draft = buildPluginConfigDraft(
      { enabled: true, configFields: fields },
      { label: 'keep me' }
    );
    draft.values.label = '   ';
    draft.touchedFields.label = true;

    expect(buildPluginConfigPatch(draft, fields, t)).toEqual({
      patch: { label: null },
      errors: {},
    });
  });

  test('rejects non-array JSON for an array field', () => {
    const draft = buildPluginConfigDraft(
      { enabled: true, configFields: fields },
      { mixed: ['old'] }
    );
    draft.values.mixed = '{"not":"an array"}';
    draft.touchedFields.mixed = true;

    expect(buildPluginConfigPatch(draft, fields, t)).toEqual({
      patch: {},
      errors: { mixed: 'plugin_management.expected_array' },
    });
  });
});
