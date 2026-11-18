import { describe, expect, test } from 'bun:test';
import { createElement, useState } from 'react';
import { renderToStaticMarkup } from 'react-dom/server';
import { parse as parseYaml } from 'yaml';
import { useVisualConfig, getPayloadReplaceValidationError } from '../src/hooks/useVisualConfig';
import type { VisualConfigValues } from '../src/types/visualConfig';

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

function parseVisualValuesFromYaml(yaml: string): VisualConfigValues {
  let result!: VisualConfigValues;
  function Harness() {
    const config = useVisualConfig();
    const [loaded, setLoaded] = useState(false);
    if (!loaded) {
      config.loadVisualValuesFromYaml(yaml);
      setLoaded(true);
    } else result = config.visualValues;
    return null;
  }
  renderToStaticMarkup(createElement(Harness));
  return result;
}

const yaml = `requests:
  payload:
    replace:
      # rule-comment
      - models:
          - name: '*'
            future-model: keep
        future-rule: keep
        target:
          role: user
          positions: [1, 2]
          future-target: keep
        replacements:
          # first-entry
          - ' a ': ' b '
          # second-entry
          - __proto__: ''
`;

describe('payload replace pipeline', () => {
  test('preserves literal operands, order, comments and unknown fields', () => {
    const output = applyPayloadEdit(yaml, (values) => {
      const rule = values.payloadReplaceRules[0];
      return {
        payloadReplaceRules: [
          {
            ...rule,
            target: { ...rule.target, all: true, positions: '' },
            replacements: [rule.replacements[1], { ...rule.replacements[0], to: '  edited  ' }],
          },
        ],
      };
    });
    const rule = parseYaml(output).requests.payload.replace[0];
    expect(rule.replacements).toEqual([JSON.parse('{"__proto__":""}'), { ' a ': '  edited  ' }]);
    expect(rule.target).toEqual({ role: 'user', all: true, 'future-target': 'keep' });
    expect(rule['future-rule']).toBe('keep');
    expect(rule.models[0]['future-model']).toBe('keep');
    expect(output).toContain('# first-entry');
    expect(output).toContain('# second-entry');
    expect(output.indexOf('# second-entry')).toBeLessThan(output.indexOf('# first-entry'));
    expect(
      getPayloadReplaceValidationError(parseVisualValuesFromYaml(output).payloadReplaceRules[0])
    ).toBeUndefined();
  });

  test('removes section when cleared and leaves unrelated changes untouched', () => {
    expect(
      parseYaml(applyPayloadEdit(yaml, () => ({ payloadReplaceRules: [] }))).requests?.payload
        ?.replace
    ).toBeUndefined();
    const output = applyPayloadEdit(yaml, () => ({ debug: true }));
    expect(parseYaml(output).requests.payload.replace).toEqual(
      parseYaml(yaml).requests.payload.replace
    );
  });

  test.each([
    yaml.replace('positions: [1, 2]', 'positions: ["1", 2]'),
    yaml.replace('positions: [1, 2]', 'positions: [1.5]'),
    yaml.replace('positions: [1, 2]', 'positions: [1.0]'),
    yaml.replace('positions: [1, 2]', 'positions: [1e0]'),
    yaml.replace('positions: [1, 2]', 'positions: [!!float 1]'),
    ...['1', 'true', 'false', 'null', '[a, b]', '!!int 1'].map((key) =>
      yaml.replace("- ' a ': ' b '", `- ${key}: ' b '`)
    ),
    `operand: &operand text\n${yaml.replace("- ' a ': ' b '", "- *operand : ' b '")}`,
    `operand: &operand text\n${yaml.replace("- ' a ': ' b '", "- ' a ': *operand")}`,
    `position: &position 1.0\n${yaml.replace('positions: [1, 2]', 'positions: [*position]')}`,
    yaml.replace("- ' a ': ' b '", "- ' a ': 12"),
    yaml.replace("- ' a ': ' b '", "- ' a ': ' b '\n            extra: entry"),
    yaml.replace("name: '*'", 'name: 123'),
    'requests: {payload: {replace: wrong}}',
  ])('keeps malformed input invalid until explicitly repaired', (source) => {
    const rule = parseVisualValuesFromYaml(source).payloadReplaceRules[0];
    expect(rule.invalid).toBe(true);
    expect(getPayloadReplaceValidationError(rule)).toBeDefined();
    expect(() => applyPayloadEdit(source, () => ({ debug: true }))).toThrow();
  });

  test('quoted sources, duplicate sources and positions survive serialize/reparse in order', () => {
    const source = yaml
      .replace('positions: [1, 2]', 'positions: [2, 1, 2]')
      .replace("- ' a ': ' b '", "- '1': 'true'\n          - '1': ''\n          - 'true': 'false'");
    const output = applyPayloadEdit(source, (values) => ({
      payloadReplaceRules: values.payloadReplaceRules.map((rule) => ({
        ...rule,
        replacements: [...rule.replacements].reverse(),
      })),
    }));
    const reparsed = parseVisualValuesFromYaml(output).payloadReplaceRules[0];
    expect(getPayloadReplaceValidationError(reparsed)).toBeUndefined();
    expect(reparsed.target.positions).toBe('2, 1, 2');
    expect(reparsed.replacements.map(({ from, to }) => [from, to])).toEqual([
      ['__proto__', ''],
      ['true', 'false'],
      ['1', ''],
      ['1', 'true'],
    ]);
    expect(parseYaml(applyPayloadEdit(output, () => ({ debug: true }))).requests).toEqual(
      parseYaml(output).requests
    );
  });

  test('accepts explicit string operands and integer position aliases', () => {
    const source = `position: &position 1\n${yaml
      .replace('positions: [1, 2]', 'positions: [*position, !!int 2]')
      .replace("- ' a ': ' b '", '- !!str true: !!str 1')}`;
    const rule = parseVisualValuesFromYaml(source).payloadReplaceRules[0];
    expect(getPayloadReplaceValidationError(rule)).toBeUndefined();
    expect(rule.replacements[0].from).toBe('true');
  });

  test('rebase retains replacement order and concurrent unrelated changes on retry', () => {
    const draft = applyPayloadEdit(yaml, (values) => ({
      payloadReplaceRules: values.payloadReplaceRules.map((rule) => ({
        ...rule,
        replacements: [...rule.replacements].reverse(),
      })),
    }));
    const latest = `server: {port: 9000}\n${yaml}`;
    let output = '';
    function Harness() {
      const config = useVisualConfig();
      const [phase, setPhase] = useState(0);
      if (phase === 0) {
        config.loadVisualValuesFromYaml(yaml);
        setPhase(1);
      } else if (phase === 1) {
        expect(config.rebaseVisualValuesFromYaml(latest, `server: {port: 9000}\n${draft}`)).toEqual(
          { ok: true }
        );
        setPhase(2);
      } else {
        output = config.applyVisualChangesToYaml(latest);
        expect(config.applyVisualChangesToYaml(latest)).toBe(output);
      }
      return null;
    }
    renderToStaticMarkup(createElement(Harness));
    expect(parseYaml(output).server.port).toBe(9000);
    expect(parseYaml(output).requests).toEqual(parseYaml(draft).requests);
    expect(output.indexOf('# second-entry')).toBeLessThan(output.indexOf('# first-entry'));
  });

  test.each(['system', 'developer'])('loads and saves %s role targets', (role) => {
    const source = yaml.replace('role: user', `role: ${role}`);
    const output = applyPayloadEdit(source, (values) => {
      const rule = values.payloadReplaceRules[0];
      expect(getPayloadReplaceValidationError(rule)).toBeUndefined();
      return { payloadReplaceRules: [{ ...rule, target: { role, all: true, positions: '' } }] };
    });
    expect(parseYaml(output).requests.payload.replace[0].target).toEqual({
      role,
      all: true,
      'future-target': 'keep',
    });
  });

  test('switches target modes without leaking inactive fields and preserves exact paths', () => {
    const paths = ['tools.#(type=="function")#.description', 'metadata. description ', '1'];
    const output = applyPayloadEdit(yaml, ({ payloadReplaceRules: [rule] }) => ({
      payloadReplaceRules: [{ ...rule, target: { role: '', positions: '', all: false, paths } }],
    }));
    expect(parseYaml(output).requests.payload.replace[0].target).toEqual({
      paths,
      'future-target': 'keep',
    });
    const back = applyPayloadEdit(output, ({ payloadReplaceRules: [rule] }) => {
      expect(rule.target.paths).toEqual(paths);
      expect(getPayloadReplaceValidationError(rule)).toBeUndefined();
      return {
        payloadReplaceRules: [{ ...rule, target: { role: 'system', positions: '2', all: false } }],
      };
    });
    expect(parseYaml(back).requests.payload.replace[0].target).toEqual({
      role: 'system',
      positions: [2],
      all: false,
      'future-target': 'keep',
    });
  });

  test('preserves path comments, aliases, and Go RE2 entries while editing', () => {
    const source = `path: &path metadata.description
${yaml
  .replace(
    'role: user\n          positions: [1, 2]',
    `paths:
            # path-comment
            - *path`
  )
  .replace(
    "- ' a ': ' b '",
    `- pattern: '(?i)(foo)'
            replacement: '$1'`
  )}`;
    const output = applyPayloadEdit(source, ({ payloadReplaceRules: [rule] }) => {
      expect(rule.replacements[0]).toMatchObject({ regex: true, from: '(?i)(foo)', to: '$1' });
      return {
        payloadReplaceRules: [
          {
            ...rule,
            target: { ...rule.target, paths: [...rule.target.paths!, 'metadata.title'] },
            replacements: [rule.replacements[1], { ...rule.replacements[0], to: '' }],
          },
        ],
      };
    });
    const rule = parseYaml(output).requests.payload.replace[0];
    expect(rule.target.paths).toEqual(['metadata.description', 'metadata.title']);
    expect(rule.replacements).toEqual([
      JSON.parse('{"__proto__":""}'),
      { pattern: '(?i)(foo)', replacement: '' },
    ]);
    expect(output).toContain('# path-comment');
    expect(output).toContain('- *path');
  });

  test.each(['[]', '[" "]', '[1]', 'metadata.description', '[true]'])(
    'rejects invalid paths: %s',
    (paths) => {
      const source = yaml.replace('role: user\n          positions: [1, 2]', `paths: ${paths}`);
      const rule = parseVisualValuesFromYaml(source).payloadReplaceRules[0];
      expect(getPayloadReplaceValidationError(rule)).toBe('payload_replace_invalid_paths');
      expect(() => applyPayloadEdit(source, () => ({ debug: true }))).toThrow();
    }
  );

  test.each(['role: system', 'all: true', 'positions: [1]'])(
    'rejects mixed path and role targets: %s',
    (selector) => {
      const source = yaml.replace(
        'role: user\n          positions: [1, 2]',
        `paths: [metadata.description]
          ${selector}`
      );
      const rule = parseVisualValuesFromYaml(source).payloadReplaceRules[0];
      expect(getPayloadReplaceValidationError(rule)).toBe('payload_replace_invalid_paths');
      expect(rule.invalid).toBe(true);
    }
  );

  test('validates drafts and model conditions', () => {
    const rule = parseVisualValuesFromYaml(yaml).payloadReplaceRules[0];
    expect(getPayloadReplaceValidationError(rule)).toBeUndefined();
    expect(
      getPayloadReplaceValidationError({ ...rule, target: { ...rule.target, positions: '1,,2' } })
    ).toBe('payload_replace_invalid_positions');
    expect(
      getPayloadReplaceValidationError({ ...rule, target: { ...rule.target, role: 'assistant' } })
    ).toBe('payload_replace_invalid_target');
    expect(getPayloadReplaceValidationError({ ...rule, replacements: [] })).toBe(
      'payload_replace_invalid_replacements'
    );
    expect(
      getPayloadReplaceValidationError({
        ...rule,
        models: [
          { ...rule.models[0], match: [{ id: 'c', path: 'x', valueType: 'json', value: '{' }] },
        ],
      })
    ).toBe('payload_replace_invalid_models');
  });
});
