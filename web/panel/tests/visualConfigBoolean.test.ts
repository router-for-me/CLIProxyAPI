import { describe, expect, test } from 'bun:test';
import { parseDocument } from 'yaml';
import { ADDITION_FIELDS } from '@/features/config/visualConfigAdditions';
import { SERVER_FIELDS } from '@/features/config/visualConfigServer';
import { readConfigBoolean } from '@/features/config/visualConfigBoolean';
import { DEFAULT_VISUAL_VALUES } from '@/types/visualConfig';
import { runVisualConfig } from './helpers/visualConfig';

const trueSpellings = ['y', 'Y', 'yes', 'Yes', 'YES', 'on', 'On', 'ON'];
const falseSpellings = ['n', 'N', 'no', 'No', 'NO', 'off', 'Off', 'OFF'];
const booleanFields = [...ADDITION_FIELDS, ...SERVER_FIELDS].filter(
  (field) => field.kind === 'boolean'
);

describe('backend-compatible visual booleans', () => {
  test('matches yaml.v3 bool spellings without interpreting arbitrary strings or numbers', () => {
    for (const fallback of [false, true]) {
      expect(readConfigBoolean(true, fallback)).toBe(true);
      expect(readConfigBoolean(false, fallback)).toBe(false);
      for (const value of trueSpellings) expect(readConfigBoolean(value, fallback)).toBe(true);
      for (const value of falseSpellings) expect(readConfigBoolean(value, fallback)).toBe(false);
      // Quoted true/false and unconventional casing/whitespace are not bool strings in yaml.v3.
      for (const value of [null, undefined, '', 'true', 'false', 'yEs', 'oFF', ' yes ', 0, 1, {}]) {
        expect(readConfigBoolean(value, fallback)).toBe(fallback);
      }
    }
  });

  for (const { key, path } of booleanFields) {
    test(`${key}: loads backend booleans, preserves untouched YAML, and saves the opposite value`, () => {
      for (const [spellings, enabled] of [
        [trueSpellings, true],
        [falseSpellings, false],
      ] as const) {
        for (const spelling of spellings) {
          for (const quoted of [false, true]) {
            const doc = parseDocument('future: retained # keep\n');
            doc.setIn(path, 'BOOL_FIXTURE');
            const yaml = doc
              .toString()
              .replace('BOOL_FIXTURE', quoted ? `"${spelling}"` : spelling);
            const loaded = runVisualConfig(yaml);
            expect(loaded.visualValues[key]).toBe(enabled);
            expect(loaded.visualDirty).toBe(false);
            expect(loaded.applyVisualChangesToYaml(yaml)).toBe(yaml);

            const changed = runVisualConfig(yaml, [{ [key]: !enabled }]);
            expect([...changed.visualDirtyFields]).toEqual([key]);
            const output = changed.applyVisualChangesToYaml(yaml);
            expect(parseDocument(output).getIn(path)).toBe(!enabled);
            expect(runVisualConfig(output).visualValues[key]).toBe(!enabled);
            expect(output).toContain('future: retained # keep');

            const same = runVisualConfig(yaml, [{ [key]: enabled }]);
            expect(same.visualDirty).toBe(false);
          }
        }
      }
    });

    test(`${key}: absent and null still use the declared default`, () => {
      expect(runVisualConfig('{}').visualValues[key]).toBe(DEFAULT_VISUAL_VALUES[key]);
      const doc = parseDocument('{}');
      doc.setIn(path, null);
      expect(runVisualConfig(doc.toString()).visualValues[key]).toBe(DEFAULT_VISUAL_VALUES[key]);
    });
  }
});
