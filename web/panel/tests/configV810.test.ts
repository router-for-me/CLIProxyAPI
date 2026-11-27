import { describe, expect, test } from 'bun:test';
import { parseDocument } from 'yaml';
import { createErrorRule } from '@/features/providers/errorRules';
import { normalizeConfigResponse } from '@/services/api/transformers';
import { buildConfigPatch, ConfigDraftConflictError } from '@/services/api/configPatch';
import { InvalidOAuthRuleError } from '@/features/config/visualOAuthErrorRules';
import { runVisualConfig } from './helpers/visualConfig';

const yaml = `oauth:
  request-scoped-errors:
    copilot:
      - status: 400
        match-regexr: ['^context_limit']
        action: stop
        future: retained
      - status: 429
        match: [rate_limit]
        action: continue
    acme-plugin:
      - {status: 503, match: [unavailable], action: stop}
`;

describe('v8.1.0 configuration', () => {
  test('deployment UUID validation allows blank regeneration and rejects malformed or nil IDs', () => {
    const id = '172AAC37-C4AC-4488-998E-652D91001340';
    const source = `server: {server-id: ${id}}`;
    expect(normalizeConfigResponse(parseDocument(source).toJS()).serverId).toBe(id);
    expect(normalizeConfigResponse({ server: {} }).serverId).toBeUndefined();
    for (const invalid of ['123', id.replaceAll('-', ''), '00000000-0000-0000-0000-000000000000']) {
      expect(runVisualConfig(source, [{ serverId: invalid }]).visualValidationErrors.serverId).toBe(
        'invalid_server_id'
      );
    }
    for (const value of [id.toLowerCase(), '']) {
      const editor = runVisualConfig(source, [{ serverId: value }]);
      expect(editor.visualValidationErrors.serverId).toBeUndefined();
      expect(
        parseDocument(editor.applyVisualChangesToYaml(source)).getIn(['server', 'server-id'])
      ).toBe(value);
    }
  });

  test('edits and reorders Copilot rules while retaining metadata, regex spelling and concurrent plugin edits', () => {
    const groups = runVisualConfig(yaml).visualValues.oauthErrorRules;
    const [first, second] = groups[0].rules;
    const oauthErrorRules = [
      { channel: 'copilot', rules: [second, { ...first, status: '422' }] },
      groups[1],
    ];
    const editor = runVisualConfig(yaml, [{ oauthErrorRules }]);
    const latest = yaml.replace('unavailable', 'maintenance');
    const result = parseDocument(editor.applyVisualChangesToYaml(latest)).toJS().oauth[
      'request-scoped-errors'
    ];
    expect(result.copilot).toEqual([
      { status: 429, match: ['rate_limit'], action: 'continue' },
      { status: 422, 'match-regexr': ['^context_limit'], action: 'stop', future: 'retained' },
    ]);
    expect(result['acme-plugin'][0].match).toEqual(['maintenance']);
    expect(() => editor.applyVisualChangesToYaml(yaml.replace('rate_limit', 'concurrent'))).toThrow(
      ConfigDraftConflictError
    );
    const cleared = runVisualConfig(yaml, [{ oauthErrorRules: [groups[1]] }]);
    expect(
      parseDocument(cleared.applyVisualChangesToYaml(yaml)).toJS().oauth['request-scoped-errors']
    ).toEqual({
      'acme-plugin': [{ status: 503, match: ['unavailable'], action: 'stop' }],
    });
  });

  for (const parent of ['oauth', 'request-scoped-errors']) {
    test(`updates and deletes channels through a ${parent} alias without changing its source`, () => {
      const source =
        parent === 'oauth'
          ? yaml.replace('oauth:', 'defaults: &defaults\n  sibling: keep') + 'oauth: *defaults\n'
          : yaml.replace('oauth:\n  request-scoped-errors:', 'defaults: &defaults') +
            'oauth: {sibling: keep, request-scoped-errors: *defaults}\n';
      const groups = runVisualConfig(source).visualValues.oauthErrorRules;
      const latest = source.replace('unavailable', 'maintenance');
      const original = parseDocument(latest).toJS();
      for (const remove of [false, true]) {
        const oauthErrorRules = remove
          ? [groups[1]]
          : [{ ...groups[0], rules: [{ ...groups[0].rules[0], status: '422' }] }, groups[1]];
        const editor = runVisualConfig(source, [{ oauthErrorRules }]);
        const output = parseDocument(editor.applyVisualChangesToYaml(latest)).toJS();
        expect(output.defaults).toEqual(original.defaults);
        expect(output.oauth.sibling).toBe('keep');
        const channels = output.oauth['request-scoped-errors'];
        expect(channels['acme-plugin']).toEqual(
          original.oauth['request-scoped-errors']['acme-plugin']
        );
        if (remove) expect(channels).not.toHaveProperty('copilot');
        else
          expect(channels.copilot).toEqual([
            { ...original.oauth['request-scoped-errors'].copilot[0], status: 422 },
          ]);
        expect(() =>
          editor.applyVisualChangesToYaml(latest.replace('rate_limit', 'other'))
        ).toThrow(ConfigDraftConflictError);
      }
    });
  }

  test('refuses to shadow nested anchors when detaching an OAuth alias', () => {
    const source =
      yaml.replace('oauth:', 'defaults: &defaults').replace('copilot:', 'copilot: &rules') +
      'oauth: *defaults\nother: *rules\n';
    const groups = runVisualConfig(source).visualValues.oauthErrorRules;
    const editor = runVisualConfig(source, [{ oauthErrorRules: [groups[1]] }]);
    for (const target of ['draft', 'server'] as const) {
      expect(() => editor.applyVisualChangesToYaml(source, target)).toThrow(
        new ConfigDraftConflictError(['oauth'], 'unsafe_yaml_structure')
      );
    }
  });

  test('an unfinished rule blocks preview and save without replacing unrelated draft fields', () => {
    const source = `server: {port: 8317}\n${yaml}`;
    const groups = runVisualConfig(source).visualValues.oauthErrorRules;
    const blank = createErrorRule('unfinished');
    const edit = (rule: typeof blank) => ({
      port: '9000',
      oauthErrorRules: [{ ...groups[0], rules: [...groups[0].rules, rule] }, groups[1]],
    });
    const editor = runVisualConfig(source, [edit(blank)]);
    for (const target of ['draft', 'server'] as const) {
      expect(() => editor.applyVisualChangesToYaml(source, target)).toThrow(
        new InvalidOAuthRuleError('copilot', 3)
      );
    }
    expect(editor.visualValues.port).toBe('9000');
    const completed = edit({
      ...blank,
      status: '429',
      matches: [{ ...blank.matches[0], value: 'throttled' }],
    });
    const next = runVisualConfig(source, [edit(blank), completed]).applyVisualChangesToYaml(source);
    const patch = buildConfigPatch(source, next).patch;
    expect(patch.server).toEqual({ port: 9000 });
    expect(
      parseDocument(next).getIn(['oauth', 'request-scoped-errors', 'copilot', 2, 'status'])
    ).toBe(429);
  });

  test('OAuth scalar spellings survive source preview and the JSON Management API patch', () => {
    const terms = [
      '429',
      '001',
      '+400',
      '1e3',
      'TRUE',
      '.Inf',
      '+.INF',
      '-.Inf',
      '.NaN',
      'rate_limit',
    ];
    const source = yaml.replace('[rate_limit]', `[${terms.join(', ')}]`);
    const groups = runVisualConfig(source).visualValues.oauthErrorRules;
    const rule = groups[0].rules[1];
    expect(rule.matches.map((term) => term.value)).toEqual(terms);
    const update = {
      ...rule,
      matches: rule.matches.map((term) =>
        term.value === 'rate_limit' ? { ...term, value: 'quota' } : term
      ),
    };
    const editor = runVisualConfig(source, [
      { oauthErrorRules: [{ ...groups[0], rules: [groups[0].rules[0], update] }, groups[1]] },
    ]);
    const next = editor.applyVisualChangesToYaml(source);
    const match = [...terms.slice(0, -1), 'quota'];
    expect(parseDocument(next).toJS().oauth['request-scoped-errors'].copilot[1].match).toEqual(
      match
    );
    expect(buildConfigPatch(source, next).patch.oauth).toMatchObject({
      'request-scoped-errors': { copilot: [expect.anything(), expect.objectContaining({ match })] },
    });
    const portOnly = runVisualConfig(source, [{ port: '9000' }]).applyVisualChangesToYaml(source);
    expect(buildConfigPatch(source, portOnly).patch).toEqual({ server: { port: 9000 } });
    expect(
      runVisualConfig(portOnly).visualValues.oauthErrorRules[0].rules[1].matches.map(
        (term) => term.value
      )
    ).toEqual(terms);
  });

  test('creates plugin channel below a null parent and rejects empty OAuth match conditions', () => {
    const source = 'oauth: null';
    const rule = { ...createErrorRule('new'), status: '429' };
    const edit = (value: string) =>
      runVisualConfig(source, [
        {
          oauthErrorRules: [
            {
              channel: 'test-plugin',
              rules: [{ ...rule, matches: [{ id: 'm', kind: 'regex', value }] }],
            },
          ],
        },
      ]);
    expect(edit('   ').visualValidationErrors.oauthErrorRules).toBe('invalid_oauth_error_rules');
    const valid = edit('^quota');
    expect(valid.visualValidationErrors.oauthErrorRules).toBeUndefined();
    expect(
      parseDocument(valid.applyVisualChangesToYaml(source)).toJS().oauth['request-scoped-errors'][
        'test-plugin'
      ]
    ).toEqual([{ status: 429, 'match-regexr': ['^quota'], action: 'continue' }]);
  });
});
