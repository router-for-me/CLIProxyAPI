import { describe, expect, test } from 'bun:test';
import {
  buildErrorRules,
  createErrorMatch,
  createErrorRule,
  moveErrorRule,
  readErrorRules,
  isInvalidErrorMatch,
  validateErrorRule,
  type ErrorRuleDraft,
} from '@/features/providers/errorRules';
import { buildRuntimePolicy, readRuntimePolicy } from '@/features/providers/runtimePolicy';
import type { ProviderKeyConfig, RequestScopedErrorRule } from '@/types/provider';

const validRule = (id = 'new'): ErrorRuleDraft => ({
  ...createErrorRule(id),
  status: '429',
  matches: [{ ...createErrorMatch(`${id}-text`), value: 'quota' }],
});

describe('provider error-rule rows', () => {
  test('deterministic read IDs do not dirty an untouched form', () => {
    const config: ProviderKeyConfig = {
      apiKey: 'fixture-key',
      requestScopedErrors: [{ status: 429, match: ['quota'], action: 'stop' }],
    };
    expect(JSON.stringify(readRuntimePolicy(config))).toBe(
      JSON.stringify(readRuntimePolicy(config))
    );
    const draft = readRuntimePolicy(config);
    expect(buildRuntimePolicy(draft).requestScopedErrors).toEqual(config.requestScopedErrors);
  });

  test('literal whitespace, commas, backslashes and embedded newlines are not split or trimmed', () => {
    const rules: RequestScopedErrorRule[] = [
      {
        status: 429,
        match: [' quota ', 'one,two', 'line one\nline two'],
        matchRegex: ['(?i)quota', 'a\\s+b', 'line one\nline two'],
        action: 'continue-and-cooldown',
      },
    ];
    const draft = readErrorRules(rules);
    expect(buildErrorRules(draft)).toEqual(rules);
    expect(buildErrorRules([{ ...draft[0], status: '500' }])).toEqual([
      { ...rules[0], status: 500 },
    ]);
    expect(rules[0].status).toBe(429);
  });

  test('untouched optional fields, empty arrays and inert imported rules survive', () => {
    const rules: RequestScopedErrorRule[] = [
      {},
      { status: 0, match: [], matchRegex: [] },
      { status: -1, action: 'stop' },
    ];
    expect(buildErrorRules(readErrorRules(rules))).toEqual(rules);
    expect(buildErrorRules([])).toEqual([]);
  });

  test('editing an imported rule preserves its existing empty conditions, but not new empty conditions', () => {
    const original: RequestScopedErrorRule = {
      status: 429,
      match: ['', 'quota'],
      matchRegex: ['', '(?i)limit'],
      action: 'stop',
    };
    const [row] = readErrorRules([original]);
    const changed = { ...row, action: 'continue' };
    expect(validateErrorRule(changed)).toBeNull();
    expect(buildErrorRules([changed])).toEqual([{ ...original, action: 'continue' }]);
    expect(isInvalidErrorMatch(changed, changed.matches[0])).toBe(false);
    expect(isInvalidErrorMatch(changed, changed.matches[2])).toBe(false);
    expect(isInvalidErrorMatch(changed, { ...changed.matches[1], value: '' })).toBe(true);
    expect(isInvalidErrorMatch(changed, { ...changed.matches[0], kind: 'regex' })).toBe(true);
    expect(
      validateErrorRule({ ...changed, matches: [...changed.matches, createErrorMatch('new')] })
    ).toBe('providersPage.errorRules.invalidMatch');
  });
  test('new rows require a positive safe-integer status, action and nonempty conditions', () => {
    expect(validateErrorRule(createErrorRule('blank'))).toBe(
      'providersPage.errorRules.invalidStatus'
    );
    expect(validateErrorRule({ ...validRule(), action: '' })).toBe(
      'providersPage.errorRules.invalidAction'
    );
    expect(validateErrorRule({ ...validRule(), action: 'retry' })).toBe(
      'providersPage.errorRules.invalidAction'
    );
    expect(validateErrorRule({ ...validRule(), matches: [] })).toBe(
      'providersPage.errorRules.missingMatch'
    );
    expect(validateErrorRule({ ...validRule(), matches: [createErrorMatch('empty')] })).toBe(
      'providersPage.errorRules.invalidMatch'
    );
    expect(validateErrorRule({ ...validRule(), status: '999' })).toBeNull();
    expect(
      validateErrorRule({ ...validRule(), matches: [{ id: 'spaces', kind: 'text', value: ' ' }] })
    ).toBeNull();
    expect(() => buildErrorRules([createErrorRule('blank')])).toThrow();
  });

  test('builds mixed substring and Go regex conditions without editor metadata', () => {
    const rule = {
      ...validRule(),
      matches: [
        { id: 'text-1', kind: 'text' as const, value: 'quota' },
        { id: 'regex', kind: 'regex' as const, value: '(?i)rate.limit' },
        { id: 'text-2', kind: 'text' as const, value: 'capacity' },
      ],
    };
    expect(buildErrorRules([rule])).toEqual([
      {
        status: 429,
        action: 'continue',
        match: ['quota', 'capacity'],
        matchRegex: ['(?i)rate.limit'],
      },
    ]);
    expect(JSON.stringify(buildErrorRules([rule]))).not.toContain('"id"');
    expect(JSON.stringify(buildErrorRules([rule]))).not.toContain('"original"');
  });

  test('reordering preserves first-match precedence and stable row identities', () => {
    const rows = readErrorRules([
      { status: 429, match: ['quota'], action: 'stop' },
      { status: 429, match: ['quota'], action: 'continue' },
    ]);
    const moved = moveErrorRule(rows, rows[1].id, -1);
    expect(moved[0]).toBe(rows[1]);
    expect(moved[1]).toBe(rows[0]);
    expect(buildErrorRules(moved).map((rule) => rule.action)).toEqual(['continue', 'stop']);
    expect(moveErrorRule(rows, rows[0].id, -1)).toBe(rows);
    expect(moveErrorRule(rows, rows[1].id, 1)).toBe(rows);
    expect(moveErrorRule(rows, 'missing', 1)).toBe(rows);
    expect(buildErrorRules(moved.filter((rule) => rule.id !== rows[0].id))).toEqual([
      { status: 429, match: ['quota'], action: 'continue' },
    ]);
  });

  test('switching inheritance keeps drafts but does not serialize them', () => {
    const policy = {
      ...readRuntimePolicy(),
      errorsMode: 'override' as const,
      errorRules: [validRule()],
    };
    expect(buildRuntimePolicy(policy).requestScopedErrors).toHaveLength(1);
    const inherited = { ...policy, errorsMode: 'inherit' as const };
    expect(buildRuntimePolicy(inherited).requestScopedErrors).toBeUndefined();
    expect(inherited.errorRules).toBe(policy.errorRules);
    expect(
      buildRuntimePolicy({ ...inherited, errorsMode: 'override' }).requestScopedErrors
    ).toEqual(buildRuntimePolicy(policy).requestScopedErrors);
  });
});
