import { readFileSync } from 'node:fs';
import { describe, expect, test } from 'bun:test';
import {
  applyCredentialPolicyPatch,
  buildCredentialPolicyPatch,
  credentialPolicyError,
  readCredentialPolicy,
  type CredentialPolicyValues,
} from '../src/features/authFiles/credentialPolicy';
import { createErrorRule, moveErrorRule } from '../src/features/providers/errorRules';
import {
  buildAuthFileFieldsPatch,
  type PrefixProxyEditorState,
} from '../src/features/authFiles/hooks/useAuthFilesPrefixProxyEditor';

const changed = <K extends keyof CredentialPolicyValues>(
  json: Record<string, unknown>,
  field: K,
  value: CredentialPolicyValues[K]
) => ({ ...readCredentialPolicy(json), [field]: value, touched: { [field]: true } });
const aliases = [
  {
    name: 'upstream',
    alias: 'public',
    fork: true,
    'force-mapping': true,
    'display-name': 'Example',
  },
];
const rules = [
  {
    status: 429,
    match: [' quota '],
    'match-regexr': ['(?i)limit'],
    action: 'continue-and-cooldown',
  },
];

describe('auth-file structured credential policy contracts', () => {
  test('reads canonical keys before config-style aliases and preserves explicit zero/false', () => {
    const draft = readCredentialPolicy({
      request_retry: 0,
      'request-retry': 3,
      model_aliases: [],
      'model-aliases': aliases,
    });
    expect(draft.requestRetry).toEqual({ mode: 'custom', value: '0' });
    expect(draft.modelAliases).toEqual({ mode: 'inherit', rows: [] });
    const alternate = readCredentialPolicy({
      'request-retry': '2',
      'model-aliases': aliases,
      'request-scoped-errors': rules,
    });
    expect(alternate.requestRetry).toEqual({ mode: 'custom', value: '2' });
    expect(alternate.modelAliases.rows?.[0]).toMatchObject({
      name: 'upstream',
      alias: 'public',
      displayName: 'Example',
      fork: true,
      forceMapping: true,
    });
    expect(alternate.errorRules.rows?.[0]).toMatchObject({
      status: '429',
      action: 'continue-and-cooldown',
      matches: [
        { kind: 'text', value: ' quota ' },
        { kind: 'regex', value: '(?i)limit' },
      ],
    });
  });

  test('recognizes negative retry sentinels and null policy lists as inherited without rewriting them', () => {
    for (const request_retry of [-1, '-2', null, undefined]) {
      const original = { request_retry, model_aliases: null, request_scoped_errors: null };
      const draft = readCredentialPolicy(original);
      expect(draft.requestRetry.mode).toBe('inherit');
      expect(draft.modelAliases).toEqual({ mode: 'inherit', rows: [] });
      expect(draft.errorRules).toEqual({ mode: 'inherit', rows: [] });
      expect(buildCredentialPolicyPatch(original, draft)).toEqual({});
    }
  });

  test('inherit writes null, custom zero is not inheritance, and empty custom input is invalid', () => {
    expect(
      buildCredentialPolicyPatch({}, changed({}, 'requestRetry', { mode: 'custom', value: '0' }))
    ).toEqual({ request_retry: 0 });
    const original = { 'request-retry': 4 };
    expect(
      buildCredentialPolicyPatch(
        original,
        changed(original, 'requestRetry', { mode: 'inherit', value: '4' })
      )
    ).toEqual({ request_retry: null });
    expect(
      buildCredentialPolicyPatch({}, changed({}, 'requestRetry', { mode: 'inherit', value: '0' }))
    ).toEqual({});
    for (const value of ['', '-1', '1.2', '1e2', 'NaN', '9007199254740992']) {
      const draft = changed({}, 'requestRetry', { mode: 'custom', value });
      expect(credentialPolicyError(draft)).toBe('auth_files.policy_retry_invalid');
      expect(() => buildCredentialPolicyPatch({}, draft)).toThrow();
    }
  });

  test('new incomplete model rows block saving instead of silently disappearing', () => {
    for (const row of [
      { name: '', alias: '' },
      { name: 'model', alias: ' ' },
      { name: ' ', alias: 'public' },
    ]) {
      const draft = changed({}, 'modelAliases', { mode: 'custom', rows: [row] });
      expect(credentialPolicyError(draft)).toBe('auth_files.policy_aliases_invalid');
      expect(() => buildCredentialPolicyPatch({}, draft)).toThrow();
    }
  });

  test('new rules use the same Provider validation for status, action and conditions', () => {
    const valid = {
      ...createErrorRule('new'),
      status: '429',
      matches: [{ id: 'text', kind: 'text' as const, value: 'quota' }],
    };
    for (const row of [
      createErrorRule('empty'),
      { ...valid, status: '0' },
      { ...valid, status: '1e3' },
      { ...valid, action: '' },
      { ...valid, matches: [] },
      { ...valid, matches: [{ id: 'empty', kind: 'text' as const, value: '' }] },
    ]) {
      const draft = changed({}, 'errorRules', { mode: 'custom', rows: [row] });
      expect(credentialPolicyError(draft)).toBe('auth_files.policy_rules_invalid');
      expect(() => buildCredentialPolicyPatch({}, draft)).toThrow();
    }
  });

  test('serializes structured aliases and ordered literal Go-regex conditions without draft metadata', () => {
    const aliasDraft = readCredentialPolicy({ model_aliases: aliases }).modelAliases;
    expect(buildCredentialPolicyPatch({}, changed({}, 'modelAliases', aliasDraft))).toEqual({
      model_aliases: aliases,
    });
    const original = {
      request_scoped_errors: [...rules, { status: 400, match: ['bad\nrequest'], action: 'stop' }],
    };
    const rows = readCredentialPolicy(original).errorRules.rows!;
    const reordered = moveErrorRule(rows, rows[1].id, -1);
    const result = buildCredentialPolicyPatch(
      original,
      changed(original, 'errorRules', { mode: 'custom', rows: reordered })
    );
    expect(result).toEqual({
      request_scoped_errors: [original.request_scoped_errors[1], rules[0]],
    });
    expect(JSON.stringify(result)).not.toContain('original');
    expect(JSON.stringify(result)).not.toContain('matchRegex');
    expect(JSON.stringify(result)).not.toContain('rule-');
  });

  test('inherited modes omit retained drafts and returning to custom preserves them', () => {
    const original = { model_aliases: aliases, request_scoped_errors: rules };
    for (const field of ['modelAliases', 'errorRules'] as const) {
      const value = readCredentialPolicy(original)[field];
      const inherit = changed(original, field, { ...value, mode: 'inherit' });
      expect(buildCredentialPolicyPatch(original, inherit)).toEqual({
        [field === 'modelAliases' ? 'model_aliases' : 'request_scoped_errors']: [],
      });
      expect(
        buildCredentialPolicyPatch(original, { ...inherit, [field]: { ...value, mode: 'custom' } })
      ).toEqual({});
    }
  });

  test('deleting all rows restores fallback rather than disabling inherited policy', () => {
    const original = { model_aliases: aliases, request_scoped_errors: rules };
    expect(
      buildCredentialPolicyPatch(
        original,
        changed(original, 'modelAliases', { mode: 'custom', rows: [] })
      )
    ).toEqual({ model_aliases: [] });
    expect(
      buildCredentialPolicyPatch(
        original,
        changed(original, 'errorRules', { mode: 'custom', rows: [] })
      )
    ).toEqual({ request_scoped_errors: [] });
  });

  test('editing known options retains extension fields and explicit false on existing rows', () => {
    const original = {
      model_aliases: [
        { ...aliases[0], fork: false, 'force-mapping': false, future: { keep: true } },
      ],
      request_scoped_errors: [
        { ...rules[0], action: ' Continue ', match: ['', ' quota '], future: { keep: true } },
      ],
    };
    const initial = readCredentialPolicy(original);
    const aliasRows = initial.modelAliases.rows!.map((row) => ({ ...row, alias: 'renamed' }));
    expect(
      buildCredentialPolicyPatch(
        original,
        changed(original, 'modelAliases', { mode: 'custom', rows: aliasRows })
      )
    ).toEqual({ model_aliases: [{ ...original.model_aliases[0], alias: 'renamed' }] });
    const ruleRows = initial.errorRules.rows!.map((row) => ({ ...row, status: '503' }));
    expect(
      buildCredentialPolicyPatch(
        original,
        changed(original, 'errorRules', { mode: 'custom', rows: ruleRows })
      )
    ).toEqual({ request_scoped_errors: [{ ...original.request_scoped_errors[0], status: 503 }] });
  });

  test('opaque imported values are preserved until explicitly cleared or replaced', () => {
    const original = { model_aliases: { future: true }, request_scoped_errors: [null] };
    const initial = readCredentialPolicy(original);
    expect(initial.modelAliases.rows).toBeNull();
    expect(initial.errorRules.rows).toBeNull();
    expect(buildCredentialPolicyPatch(original, initial)).toEqual({});
    expect(
      buildCredentialPolicyPatch(
        original,
        changed(original, 'modelAliases', { ...initial.modelAliases, mode: 'inherit' })
      )
    ).toEqual({ model_aliases: [] });
    expect(
      buildCredentialPolicyPatch(
        original,
        changed(original, 'errorRules', { mode: 'custom', rows: [] })
      )
    ).toEqual({ request_scoped_errors: [] });
  });

  test('preview applies canonical values without stale aliases or touching unrelated data', () => {
    const json = {
      'request-retry': 3,
      'model-aliases': aliases,
      'request-scoped-errors': rules,
      custom: { retained: true },
    };
    applyCredentialPolicyPatch(json, {
      request_retry: null,
      model_aliases: [],
      request_scoped_errors: [],
    });
    expect(json).toEqual({
      model_aliases: [],
      request_scoped_errors: [],
      custom: { retained: true },
    });
  });

  test('integrates structured drafts into the existing fields patch without file rewrites', () => {
    const editor = {
      json: {},
      providerKey: 'codex',
      prefix: '',
      proxyUrl: '',
      priority: '',
      weight: '',
      policy: changed({}, 'requestRetry', { mode: 'custom', value: '2' }),
    } as PrefixProxyEditorState;
    expect(buildAuthFileFieldsPatch(editor, (key) => key)).toEqual({ request_retry: 2 });
  });

  test('all policy translation keys are present in every supported locale', () => {
    const messages = ['en', 'zh-CN', 'zh-TW', 'ru'].map(
      (locale) =>
        JSON.parse(
          readFileSync(new URL(`../src/i18n/locales/${locale}.json`, import.meta.url), 'utf8')
        ).auth_files as Record<string, string>
    );
    const keys = Object.keys(messages[0])
      .filter((key) => key.startsWith('policy_'))
      .sort();
    for (const section of messages) {
      expect(
        Object.keys(section)
          .filter((key) => key.startsWith('policy_'))
          .sort()
      ).toEqual(keys);
      for (const key of keys) expect(section[key].trim().length).toBeGreaterThan(0);
    }
  });
});
