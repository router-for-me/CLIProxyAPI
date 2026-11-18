import { describe, expect, test } from 'bun:test';
import { createElement, useState } from 'react';
import { renderToStaticMarkup } from 'react-dom/server';
import { parse, stringify } from 'yaml';
import { useVisualConfig } from '../src/hooks/useVisualConfig';
import type { VisualConfigValues } from '../src/types/visualConfig';

function run(yaml: string, patches: Partial<VisualConfigValues>[] = []) {
  let result!: { values: VisualConfigValues; dirty: string[]; output: string };
  function Harness() {
    const config = useVisualConfig();
    const [phase, setPhase] = useState(0);
    if (phase === 0) {
      config.loadVisualValuesFromYaml(yaml);
      setPhase(1);
    } else if (phase <= patches.length) {
      config.setVisualValues(patches[phase - 1]);
      setPhase(phase + 1);
    } else {
      result = {
        values: config.visualValues,
        dirty: [...config.visualDirtyFields],
        output: config.applyVisualChangesToYaml(yaml),
      };
    }
    return null;
  }
  renderToStaticMarkup(createElement(Harness));
  return result;
}

const words = { remove: '', padded: '  keep spaces  ' };
const patterns = [
  { pattern: ' (?i)\\bsecret\\b ', replacement: '' },
  { pattern: '(?P<name>text)', replacement: ' ${name} ' },
];

describe('visual config sensitive replacements', () => {
  for (const provider of ['antigravity', 'devin'] as const) {
    const wordsField = `${provider}SensitiveWordsReplace` as const;
    const patternsField = `${provider}SensitivePatternsReplace` as const;
    const yaml = stringify({
      oauth: {
        providers: {
          [provider]: {
            'sensitive-words-replace': words,
            'sensitive-patterns-replace': patterns,
            'future-option': true,
          },
        },
      },
    });

    test(`${provider}: defaults and lossless loading, including RE2-only syntax`, () => {
      const defaults = run('{}').values;
      expect(defaults[wordsField]).toEqual({});
      expect(defaults[patternsField]).toEqual([]);
      const loaded = run(yaml);
      expect(loaded.values[wordsField]).toEqual(words);
      expect(loaded.values[patternsField]).toEqual(patterns);
      expect(loaded.dirty).toEqual([]);
    });

    test(`${provider}: saves empty replacements, raw whitespace and ordered rules`, () => {
      const nextPatterns = [...patterns].reverse();
      const nextWords = { ...words, added: '' };
      const saved = run('# root comment\n' + yaml, [
        { [wordsField]: nextWords, [patternsField]: nextPatterns },
      ]);
      expect(saved.dirty.sort()).toEqual([wordsField, patternsField].sort());
      expect(parse(saved.output).oauth.providers[provider]).toEqual({
        'sensitive-words-replace': nextWords,
        'sensitive-patterns-replace': nextPatterns,
        'future-option': true,
      });
      expect(saved.output).toContain('# root comment');
      expect(run(saved.output).values[patternsField]).toEqual(nextPatterns);
    });

    test(`${provider}: clears keys without removing unknown siblings`, () => {
      const cleared = run(yaml, [{ [wordsField]: {}, [patternsField]: [] }]);
      expect(parse(cleared.output).oauth.providers[provider]).toEqual({ 'future-option': true });
      const noSibling = yaml.replace('      future-option: true\n', '');
      expect(parse(run(noSibling, [{ [wordsField]: {}, [patternsField]: [] }]).output)).toEqual({});
    });

    test(`${provider}: restores dirty state structurally; map order is immaterial`, () => {
      const result = run(yaml, [
        { [wordsField]: { changed: '' }, [patternsField]: [...patterns].reverse() },
        {
          [wordsField]: Object.fromEntries(Object.entries(words).reverse()),
          [patternsField]: patterns.map((rule) => ({ ...rule })),
        },
      ]);
      expect(result.dirty).toEqual([]);
      expect(parse(result.output)).toEqual(parse(yaml));
    });

    test(`${provider}: creates missing ancestors and keeps unrelated provider data`, () => {
      const saved = run('oauth:\n  providers:\n    other:\n      untouched: true\n', [
        { [wordsField]: { drop: '' }, [patternsField]: patterns },
      ]);
      expect(parse(saved.output).oauth.providers.other).toEqual({ untouched: true });
      expect(run(saved.output).values[wordsField]).toEqual({ drop: '' });
    });

    test(`${provider}: retains AST comments and unknown rule properties on reorder`, () => {
      const source = `oauth:
  providers:
    ${provider}:
      sensitive-words-replace:
        # literal comment
        drop: old # inline
      sensitive-patterns-replace:
        - pattern: first
          replacement: old
          future: retained
        - pattern: second # regex comment
          replacement: ''
`;
      const output = run(source, [
        {
          [wordsField]: { drop: '' },
          [patternsField]: [
            { pattern: 'second', replacement: '' },
            { pattern: 'first', replacement: ' new ' },
          ],
        },
      ]).output;
      expect(output).toContain('# literal comment');
      expect(output).toContain('# inline');
      expect(output).toContain('# regex comment');
      expect(parse(output).oauth.providers[provider]['sensitive-patterns-replace'][1].future).toBe(
        'retained'
      );
    });
  }

  test('loads only string values and supports prototype-named literal targets', () => {
    const yaml = `oauth:
  providers:
    devin:
      sensitive-words-replace:
        __proto__: ''
        invalid: 42
      sensitive-patterns-replace:
        - pattern: '(?i)test'
          replacement: ''
        - pattern: 42
          replacement: invalid
`;
    const loaded = run(yaml);
    expect(Object.entries(loaded.values.devinSensitiveWordsReplace)).toEqual([['__proto__', '']]);
    expect(loaded.values.devinSensitivePatternsReplace).toEqual([
      { pattern: '(?i)test', replacement: '' },
    ]);
  });
});
