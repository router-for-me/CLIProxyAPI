import { describe, expect, test } from 'bun:test';
import { createElement, useState } from 'react';
import { renderToStaticMarkup } from 'react-dom/server';
import { parse as parseYaml } from 'yaml';
import { useVisualConfig } from '../src/hooks/useVisualConfig';
import type { PayloadFilterRule, PayloadRule, VisualConfigValues } from '../src/types/visualConfig';

function unwrapPre(markup: string): string {
  return markup
    .slice('<pre>'.length, -'</pre>'.length)
    .replaceAll('&quot;', '"')
    .replaceAll('&#x27;', "'")
    .replaceAll('&lt;', '<')
    .replaceAll('&gt;', '>')
    .replaceAll('&amp;', '&');
}

function applyPayloadEdit(
  yaml: string,
  edit: (values: VisualConfigValues) => Partial<VisualConfigValues>
): string {
  function Harness() {
    const visualConfig = useVisualConfig();
    const [phase, setPhase] = useState(0);

    if (phase === 0) {
      visualConfig.loadVisualValuesFromYaml(yaml);
      setPhase(1);
    } else if (phase === 1) {
      visualConfig.setVisualValues(edit(visualConfig.visualValues));
      setPhase(2);
    } else {
      return createElement('pre', null, visualConfig.applyVisualChangesToYaml(yaml));
    }
    return null;
  }

  return unwrapPre(renderToStaticMarkup(createElement(Harness)));
}

const ruleYaml = (section: string, raw: boolean) => `requests:
  payload:
    ${section}:
      # belongs-to-deleted-rule
      - models:
          - name: delete-me
        params:
          obsolete: ${raw ? "'false'" : 'false'}
        future-rule: deleted
      # belongs-to-kept-rule
      - models:
          # belongs-to-kept-model
          - name: keep-me
            future-model: preserve
        params:
          # belongs-to-deleted-param
          remove-me: ${raw ? "'0'" : '0'}
          # belongs-to-kept-param
          temperature: ${raw ? "'1'" : '1'}
        future-rule: preserve
`;

function retainAndEditRule(rules: PayloadRule[], raw: boolean): PayloadRule[] {
  const retained = rules[1];
  return [
    {
      ...retained,
      models: [{ ...retained.models[0], name: 'kept-and-edited' }],
      params: [
        {
          ...retained.params[1],
          value: raw ? '2' : '2',
          valueType: raw ? 'json' : 'number',
        },
      ],
    },
  ];
}

describe('visual config payload YAML AST updates', () => {
  const ruleSections = [
    ['default', 'payloadDefaultRules', false],
    ['override', 'payloadOverrideRules', false],
    ['default-raw', 'payloadDefaultRawRules', true],
    ['override-raw', 'payloadOverrideRawRules', true],
  ] as const;

  for (const [section, field, raw] of ruleSections) {
    test(`preserves comments and unknown keys while editing and deleting ${section} rules`, () => {
      const output = applyPayloadEdit(ruleYaml(section, raw), (values) => ({
        [field]: retainAndEditRule(values[field], raw),
      }));
      const parsed = parseYaml(output) as { requests: { payload: Record<string, unknown[]> } };
      const rule = parsed.requests.payload[section][0] as Record<string, unknown>;
      const model = (rule.models as Array<Record<string, unknown>>)[0];

      expect(parsed.requests.payload[section]).toHaveLength(1);
      expect(model.name).toBe('kept-and-edited');
      expect(model['future-model']).toBe('preserve');
      expect(rule['future-rule']).toBe('preserve');
      expect(rule.params).toEqual({ temperature: raw ? '2' : 2 });
      expect(output).toContain('# belongs-to-kept-rule');
      expect(output).toContain('# belongs-to-kept-model');
      expect(output).toContain('# belongs-to-kept-param');
      expect(output).not.toContain('belongs-to-deleted-rule');
      expect(output).not.toContain('belongs-to-deleted-param');
    });
  }

  test('preserves filter rule nodes and sequence-item comments through deletion and reordering', () => {
    const yaml = `requests:
  payload:
    filter:
      # belongs-to-deleted-filter
      - models:
          - name: delete-me
        params:
          - deleted.path
        future-rule: deleted
      # belongs-to-kept-filter
      - models:
          - name: keep-me
            future-model: preserve
        params:
          # belongs-to-deleted-param
          - first.path
          # belongs-to-kept-param
          - keep.path
        future-rule: preserve
`;
    const output = applyPayloadEdit(yaml, (values) => {
      const retained = values.payloadFilterRules[1];
      const edited: PayloadFilterRule = {
        ...retained,
        models: [{ ...retained.models[0], name: 'kept-filter' }],
        params: ['keep.path', 'new.path'],
      };
      return { payloadFilterRules: [edited] };
    });
    const parsed = parseYaml(output) as {
      payload: { filter: Array<Record<string, unknown>> };
    };
    const rule = parsed.requests.payload.filter[0];
    const model = (rule.models as Array<Record<string, unknown>>)[0];

    expect(parsed.requests.payload.filter).toHaveLength(1);
    expect(model).toEqual({ name: 'kept-filter', 'future-model': 'preserve' });
    expect(rule.params).toEqual(['keep.path', 'new.path']);
    expect(rule['future-rule']).toBe('preserve');
    expect(output).toContain('# belongs-to-kept-filter');
    expect(output).toContain('# belongs-to-kept-param');
    expect(output).not.toContain('belongs-to-deleted-filter');
    expect(output).not.toContain('belongs-to-deleted-param');
  });
});

const conditionYaml = (section: string, key: string) => `requests:
  payload:
    ${section}:
      - models:
          - name: example
            future-model: preserve
            ${key}:
              # first-condition
              - first: 1 # first-inline
                # second-condition
                second: false # second-inline
              # third-condition
              - first: 3 # third-inline
                # fourth-condition
                fourth:
                  # nested-condition-value
                  future-value: [one, two]
        future-rule: preserve
        params: ${section === 'filter' ? '[remove.path]' : '{}'}
`;

describe('multi-field payload conditions', () => {
  const sections = [
    ['default', 'payloadDefaultRules'],
    ['override', 'payloadOverrideRules'],
    ['default-raw', 'payloadDefaultRawRules'],
    ['override-raw', 'payloadOverrideRawRules'],
    ['filter', 'payloadFilterRules'],
  ] as const;
  const conditions = [
    ['match', 'match'],
    ['not-match', 'notMatch'],
  ] as const;

  for (const [section, field] of sections) {
    for (const [key, property] of conditions) {
      const yaml = conditionYaml(section, key);
      const getModel = (output: string) => parseYaml(output).requests.payload[section][0].models[0];

      test(`${section}/${key}: edits, deletes, reorders and adds without stale siblings`, () => {
        const output = applyPayloadEdit(yaml, (values) => {
          const rule = values[field][0];
          const model = rule.models[0];
          const entries = model[property]!;
          expect(entries.map((entry) => entry.path)).toEqual([
            'first',
            'second',
            'first',
            'fourth',
          ]);
          return {
            [field]: [
              {
                ...rule,
                models: [
                  {
                    ...model,
                    [property]: [
                      entries[3],
                      { ...entries[1], path: 'renamed', value: 'true' },
                      { ...entries[2], value: '9' },
                      { id: 'new-condition', path: 'added', valueType: 'string', value: 'new' },
                    ],
                  },
                ],
              },
            ],
          };
        });
        expect(getModel(output)[key]).toEqual([
          { fourth: { 'future-value': ['one', 'two'] } },
          { renamed: true },
          { first: 9 },
          { added: 'new' },
        ]);
        expect(getModel(output)['future-model']).toBe('preserve');
        expect(parseYaml(output).requests.payload[section][0]['future-rule']).toBe('preserve');
        for (const comment of [
          'second-condition',
          'second-inline',
          'third-condition',
          'third-inline',
          'fourth-condition',
          'nested-condition-value',
        ]) {
          expect(output.split(`# ${comment}`)).toHaveLength(2);
        }
        expect(output).not.toContain('first-condition');
        expect(output).not.toContain('first-inline');
        // Reload the emitted singleton maps, then remove the first two conditions.
        const reloaded = applyPayloadEdit(output, (values) => {
          const rule = values[field][0];
          const model = rule.models[0];
          return {
            [field]: [{ ...rule, models: [{ ...model, [property]: model[property]!.slice(2) }] }],
          };
        });
        expect(getModel(reloaded)[key]).toEqual([{ first: 9 }, { added: 'new' }]);
        expect(reloaded).toContain('third-condition');
        expect(reloaded).not.toContain('second-condition');
        expect(reloaded).not.toContain('fourth-condition');
      });

      test(`${section}/${key}: editing one field keeps every sibling exactly once`, () => {
        const output = applyPayloadEdit(yaml, (values) => {
          const rule = values[field][0];
          const model = rule.models[0];
          return {
            [field]: [
              {
                ...rule,
                models: [
                  {
                    ...model,
                    [property]: model[property]!.map((entry, index) =>
                      index === 0 ? { ...entry, value: '7' } : entry
                    ),
                  },
                ],
              },
            ],
          };
        });
        expect(getModel(output)[key]).toEqual([
          { first: 7 },
          { second: false },
          { first: 3 },
          { fourth: { 'future-value': ['one', 'two'] } },
        ]);
        expect(output).toContain('nested-condition-value');
      });

      test(`${section}/${key}: reverse order keeps duplicate paths and their comments`, () => {
        const output = applyPayloadEdit(yaml, (values) => {
          const rule = values[field][0];
          const model = rule.models[0];
          return {
            [field]: [
              { ...rule, models: [{ ...model, [property]: [...model[property]!].reverse() }] },
            ],
          };
        });
        expect(getModel(output)[key]).toEqual([
          { fourth: { 'future-value': ['one', 'two'] } },
          { first: 3 },
          { second: false },
          { first: 1 },
        ]);
        expect(output.indexOf('# third-condition')).toBeLessThan(
          output.indexOf('# first-condition')
        );
        expect(output.indexOf('# second-condition')).toBeLessThan(
          output.indexOf('# first-condition')
        );
      });

      test(`${section}/${key}: deleting a later field does not resurrect it`, () => {
        const output = applyPayloadEdit(yaml, (values) => {
          const rule = values[field][0];
          const model = rule.models[0];
          return {
            [field]: [{ ...rule, models: [{ ...model, [property]: [model[property]![0]] }] }],
          };
        });
        expect(getModel(output)[key]).toEqual([{ first: 1 }]);
        expect(output).toContain('first-condition');
        expect(output).toContain('first-inline');
        expect(output).not.toContain('second-condition');
        expect(output).not.toContain('third-condition');
      });

      test(`${section}/${key}: clearing removes the sequence`, () => {
        const output = applyPayloadEdit(yaml, (values) => {
          const rule = values[field][0];
          return { [field]: [{ ...rule, models: [{ ...rule.models[0], [property]: [] }] }] };
        });
        expect(getModel(output)[key]).toBeUndefined();
        expect(output).not.toContain('first-condition');
      });

      test(`${section}/${key}: unrelated model edits preserve grouping and comments`, () => {
        const output = applyPayloadEdit(yaml, (values) => {
          const rule = values[field][0];
          return { [field]: [{ ...rule, models: [{ ...rule.models[0], name: 'renamed' }] }] };
        });
        expect(getModel(output)[key]).toEqual(getModel(yaml)[key]);
        expect(output).toContain('nested-condition-value');
        expect(output).toContain('second-condition');
      });
    }
  }

  test('numeric path keys follow parser order rather than YAML pair order', () => {
    const yaml = `requests:
  payload:
    default:
      - models:
          - name: example
            match:
              # ten-condition
              - "10": ten
                # two-condition
                "2": two
              - tail: true
        params: {}
`;
    const output = applyPayloadEdit(yaml, (values) => {
      const rule = values.payloadDefaultRules[0];
      const model = rule.models[0];
      expect(model.match!.map((entry) => entry.path)).toEqual(['2', '10', 'tail']);
      return {
        payloadDefaultRules: [
          {
            ...rule,
            models: [
              {
                ...model,
                match: [{ ...model.match![0], path: 'renamed', value: 'edited' }, model.match![2]],
              },
            ],
          },
        ],
      };
    });
    expect(parseYaml(output).requests.payload.default[0].models[0].match).toEqual([
      { renamed: 'edited' },
      { tail: true },
    ]);
    expect(output).toContain('two-condition');
    expect(output).not.toContain('ten-condition');
  });
});
