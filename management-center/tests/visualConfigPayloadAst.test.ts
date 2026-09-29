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

const ruleYaml = (section: string, raw: boolean) => `payload:
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
      const parsed = parseYaml(output) as Record<string, Record<string, unknown[]>>;
      const rule = parsed.payload[section][0] as Record<string, unknown>;
      const model = (rule.models as Array<Record<string, unknown>>)[0];

      expect(parsed.payload[section]).toHaveLength(1);
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
    const yaml = `payload:
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
    const rule = parsed.payload.filter[0];
    const model = (rule.models as Array<Record<string, unknown>>)[0];

    expect(parsed.payload.filter).toHaveLength(1);
    expect(model).toEqual({ name: 'kept-filter', 'future-model': 'preserve' });
    expect(rule.params).toEqual(['keep.path', 'new.path']);
    expect(rule['future-rule']).toBe('preserve');
    expect(output).toContain('# belongs-to-kept-filter');
    expect(output).toContain('# belongs-to-kept-param');
    expect(output).not.toContain('belongs-to-deleted-filter');
    expect(output).not.toContain('belongs-to-deleted-param');
  });
});
