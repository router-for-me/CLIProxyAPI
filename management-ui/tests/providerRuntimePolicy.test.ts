import { afterEach, describe, expect, test } from 'bun:test';
import { createElement } from 'react';
import { renderToStaticMarkup } from 'react-dom/server';
import { apiClient } from '@/services/api/client';
import { providersApi, type ProviderFamily } from '@/services/api/providers';
import { normalizeProviderGroups } from '@/services/api/transformers';
import {
  buildRuntimePolicy,
  readRuntimePolicy,
  validateRuntimePolicy,
} from '@/features/providers/runtimePolicy';
import { RuntimePolicyEditor } from '@/features/providers/sheets/forms/RuntimePolicyEditor';
import { createErrorRule, moveErrorRule, readErrorRules } from '@/features/providers/errorRules';
import type { OpenAIProviderConfig, ProviderKeyConfig } from '@/types';

const originalGet = apiClient.get;
const originalPut = apiClient.put;
afterEach(() => {
  apiClient.get = originalGet;
  apiClient.put = originalPut;
});

function backend(family: ProviderFamily, group: Record<string, unknown>) {
  let stored = structuredClone(group);
  apiClient.get = (async () => ({ 'api-keys': { [family]: [stored] } })) as typeof apiClient.get;
  apiClient.put = (async (path: string, value: Record<string, unknown>[]) => {
    expect(path).toBe(`/config/api-keys/${family}`);
    stored = structuredClone(value[0]);
  }) as typeof apiClient.put;
  return () => stored;
}
const keyOf = (group: Record<string, unknown>) => (group.keys as Record<string, unknown>[])[0];
const row = (group: Record<string, unknown>) =>
  normalizeProviderGroups([group])[0] as ProviderKeyConfig;
const apis = [
  ['gemini', providersApi.updateGeminiKey],
  ['interactions', providersApi.updateInteractionsKey],
  ['claude', providersApi.updateClaudeConfig],
  ['codex', providersApi.updateCodexConfig],
  ['xai', providersApi.updateXAIConfig],
  ['meta', providersApi.updateMetaConfig],
  ['vertex', providersApi.updateVertexConfig],
] as const;

describe('provider runtime policies', () => {
  for (const [family, update] of apis) {
    test(`${family}: writes zero retry, false cooling, and restores parent inheritance`, async () => {
      const group = {
        name: 'fixture',
        'request-retry': 5,
        'disable-cooling': true,
        keys: [{ 'api-key': 'fixture-key', 'opaque-option': 'keep' }, { 'api-key': 'sibling' }],
      };
      const stored = backend(family, group);
      let config = row(stored());
      expect(readRuntimePolicy(config)).toMatchObject({ cooling: 'inherit', retry: '' });
      const policy = buildRuntimePolicy(
        { ...readRuntimePolicy(config), cooling: 'enabled', retry: '0' },
        family !== 'vertex'
      );
      await update(config.apiKey, config.baseUrl, { ...config, ...policy });
      expect(keyOf(stored())).toMatchObject({
        'request-retry': 0,
        'disable-cooling': false,
        'opaque-option': 'keep',
      });
      config = row(stored());
      await update(config.apiKey, config.baseUrl, {
        ...config,
        ...buildRuntimePolicy(
          { ...readRuntimePolicy(config), cooling: 'inherit', retry: '' },
          family !== 'vertex'
        ),
      });
      expect(stored()).toEqual(group);
    });
  }
  test.each([undefined, null])(
    'does not materialize an inherited value (%s) on unrelated edits',
    async (value) => {
      const raw = {
        'api-key': 'fixture-key',
        ...(value === null
          ? { 'request-retry': null, 'disable-cooling': null, 'request-scoped-errors': null }
          : {}),
      };
      const group = {
        name: 'fixture',
        'request-retry': 8,
        'disable-cooling': true,
        'request-scoped-errors': [{ status: 429, match: ['quota'], action: 'continue' }],
        keys: [raw],
      };
      const stored = backend('codex', group);
      const config = row(stored());
      await providersApi.updateCodexConfig(config.apiKey, undefined, {
        ...config,
        ...buildRuntimePolicy(readRuntimePolicy(config)),
        weight: 3,
      });
      expect(keyOf(stored())).toEqual({ ...raw, weight: 3 });
    }
  );
  test('rules round trip with the v8 regex spelling, including explicit empty and inherit', async () => {
    const stored = backend('claude', {
      name: 'fixture',
      'request-scoped-errors': [{ status: 500, match: ['busy'], action: 'continue' }],
      keys: [{ 'api-key': 'fixture-key' }],
    });
    let config = row(stored());
    let draft = {
      ...readRuntimePolicy(config),
      errorsMode: 'override' as const,
      errorRules: readErrorRules([
        { status: 429, matchRegex: ['(?i)quota'], action: 'continue-and-cooldown' },
      ]),
    };
    await providersApi.updateClaudeConfig(config.apiKey, undefined, {
      ...config,
      ...buildRuntimePolicy(draft),
    });
    expect(keyOf(stored())['request-scoped-errors']).toEqual([
      { status: 429, 'match-regexr': ['(?i)quota'], action: 'continue-and-cooldown' },
    ]);
    config = row(stored());
    expect(readRuntimePolicy(config).errorRules[0].matches).toMatchObject([
      { kind: 'regex', value: '(?i)quota' },
    ]);
    draft = { ...readRuntimePolicy(config), errorsMode: 'override', errorRules: [] };
    await providersApi.updateClaudeConfig(config.apiKey, undefined, {
      ...config,
      ...buildRuntimePolicy(draft),
    });
    expect(keyOf(stored())['request-scoped-errors']).toEqual([]);
    config = row(stored());
    await providersApi.updateClaudeConfig(config.apiKey, undefined, {
      ...config,
      ...buildRuntimePolicy({ ...readRuntimePolicy(config), errorsMode: 'inherit' }),
    });
    expect(keyOf(stored())).not.toHaveProperty('request-scoped-errors');
    expect(stored()['request-scoped-errors']).toHaveLength(1);
  });
  test('row reordering and deletion persist exact rule precedence and patterns', async () => {
    const first = {
      status: 429,
      match: [' quota ', 'line one\nline two'],
      action: ' STOP ',
      'match-regexr': [],
    };
    const second = { status: 429, 'match-regexr': ['(?i)quota'], action: 'continue' };
    const stored = backend('codex', {
      name: 'fixture',
      keys: [{ 'api-key': 'fixture-key', 'request-scoped-errors': [first, second] }],
    });
    let config = row(stored());
    const draft = readRuntimePolicy(config);
    const errorRules = moveErrorRule(draft.errorRules, draft.errorRules[1].id, -1);
    await providersApi.updateCodexConfig(config.apiKey, undefined, {
      ...config,
      ...buildRuntimePolicy({ ...draft, errorRules }),
    });
    expect(keyOf(stored())['request-scoped-errors']).toEqual([second, first]);
    config = row(stored());
    const current = readRuntimePolicy(config);
    await providersApi.updateCodexConfig(config.apiKey, undefined, {
      ...config,
      ...buildRuntimePolicy({ ...current, errorRules: current.errorRules.slice(1) }),
    });
    expect(keyOf(stored())['request-scoped-errors']).toEqual([first]);
  });
  test('OpenAI edits and removes group policy without losing key metadata', async () => {
    const stored = backend('openai-compatibility', {
      name: 'fixture',
      'base-url': 'https://example.invalid',
      keys: [{ 'api-key': 'fixture-key', weight: 4 }],
      'request-retry': 2,
      'disable-cooling': false,
    });
    const config = normalizeProviderGroups([stored()], true)[0] as OpenAIProviderConfig;
    await providersApi.updateOpenAIProvider(config.name, 0, {
      ...config,
      ...buildRuntimePolicy({ ...readRuntimePolicy(config), retry: '', cooling: 'inherit' }),
    });
    expect(stored()).not.toHaveProperty('request-retry');
    expect(stored()).not.toHaveProperty('disable-cooling');
    expect(keyOf(stored()).weight).toBe(4);
  });
  test.each([undefined, null])(
    'explicit policies equal to inherited values still create overrides (%s)',
    async (value) => {
      const rules = [{ status: 429, match: ['quota'], action: 'stop' }];
      const raw = {
        'api-key': 'fixture-key',
        ...(value === null
          ? { 'request-retry': null, 'disable-cooling': null, 'request-scoped-errors': null }
          : {}),
      };
      const stored = backend('codex', {
        name: 'fixture',
        'request-retry': 0,
        'disable-cooling': false,
        'request-scoped-errors': rules,
        keys: [raw],
      });
      const config = row(stored());
      await providersApi.updateCodexConfig(config.apiKey, undefined, {
        ...config,
        ...buildRuntimePolicy({
          ...readRuntimePolicy(config),
          cooling: 'enabled',
          retry: '0',
          errorsMode: 'override',
        }),
      });
      expect(keyOf(stored())).toEqual({
        'api-key': 'fixture-key',
        'request-retry': 0,
        'disable-cooling': false,
        'request-scoped-errors': rules,
      });
    }
  );
  test('existing action casing and whitespace remain editable and preserved', async () => {
    const rules = [{ status: 429, match: ['quota'], action: ' STOP ' }];
    const stored = backend('claude', {
      name: 'fixture',
      keys: [{ 'api-key': 'fixture-key', 'request-scoped-errors': rules }],
    });
    const config = row(stored());
    const draft = readRuntimePolicy(config);
    expect(validateRuntimePolicy(draft)).toBeNull();
    await providersApi.updateClaudeConfig(config.apiKey, undefined, {
      ...config,
      ...buildRuntimePolicy(draft),
      weight: 5,
    });
    expect(keyOf(stored())['request-scoped-errors']).toEqual(rules);
    const current = row(stored());
    const edited = readRuntimePolicy(current);
    edited.errorRules[0] = {
      ...edited.errorRules[0],
      matches: edited.errorRules[0].matches.map((match) => ({ ...match, value: 'changed quota' })),
    };
    await providersApi.updateClaudeConfig(current.apiKey, undefined, {
      ...current,
      ...buildRuntimePolicy(edited),
    });
    expect(keyOf(stored())['request-scoped-errors']).toEqual([
      { ...rules[0], match: ['changed quota'] },
    ]);
  });
  test('Vertex excludes error rules while allowing cooling and negative global retry', () => {
    const policy = buildRuntimePolicy(
      {
        ...readRuntimePolicy(),
        cooling: 'disabled',
        retry: '-1',
        errorsMode: 'override',
        errorRules: [createErrorRule('invalid')],
      },
      false
    );
    expect(policy).toMatchObject({ disableCooling: true, requestRetry: -1 });
    expect(policy).not.toHaveProperty('requestScopedErrors');
    expect(policy.inheritFields).not.toContain('request-scoped-errors');
  });
  test.each(['1.5', 'NaN', '9007199254740992', '1e3'])('rejects invalid retry %s', (retry) => {
    expect(validateRuntimePolicy({ ...readRuntimePolicy(), retry })).toBeTruthy();
  });
  test.each(['', '0', '-1', '1.5', '9007199254740992', '1e3'])(
    'rejects invalid rule status %s',
    (status) => {
      const draft = {
        ...readRuntimePolicy(),
        errorsMode: 'override' as const,
        errorRules: [{ ...createErrorRule('new'), status }],
      };
      expect(validateRuntimePolicy(draft)).toBe('providersPage.errorRules.invalidStatus');
      expect(() => buildRuntimePolicy(draft)).toThrow();
      expect(validateRuntimePolicy({ ...draft, errorsMode: 'inherit' })).toBeNull();
    }
  );
  test('does not validate Go regex with JavaScript syntax', () => {
    expect(
      validateRuntimePolicy({
        ...readRuntimePolicy(),
        errorsMode: 'override',
        errorRules: [
          {
            ...createErrorRule('new'),
            status: '429',
            action: 'stop',
            matches: [{ id: 'regex', kind: 'regex', value: '(?i)quota' }],
          },
        ],
      })
    ).toBeNull();
  });
  test('renders associated labels and disabled controls', () => {
    const markup = renderToStaticMarkup(
      createElement(RuntimePolicyEditor, {
        value: { ...readRuntimePolicy(), errorsMode: 'override' },
        onChange: () => {},
        disabled: true,
      })
    );
    expect(markup).toContain('aria-describedby');
    expect(markup).toContain('for=');
    expect(markup).toContain('disabled=""');
  });
});
