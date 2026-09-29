import { describe, expect, test } from 'bun:test';
import {
  formatExcludedRulesText,
  hasExcludedRule,
  isMatchedByWildcardRule,
  isWildcardRule,
  matchesExcludedRule,
  normalizeExcludedRules,
  parseExcludedRulesText,
  replaceCustomExcludedRules,
  splitExcludedRules,
  toggleExcludedRule,
} from '../src/components/excludedModels/excludedModelRules';

describe('normalizeExcludedRules / parseExcludedRulesText', () => {
  test('trims, drops blanks, and removes case-insensitive duplicates', () => {
    expect(normalizeExcludedRules([' gpt-* ', 'GPT-*', '', 'claude-3'])).toEqual([
      'gpt-*',
      'claude-3',
    ]);
  });

  test('keeps the first spelling of a case-insensitive duplicate', () => {
    expect(normalizeExcludedRules(['GPT-4o', 'gpt-4O'])).toEqual(['GPT-4o']);
  });

  test('parsing text is the same operation as normalizing its lines', () => {
    const text = ' GPT-5-*\ngpt-5-*\nclaude-opus ';
    expect(parseExcludedRulesText(text)).toEqual(['GPT-5-*', 'claude-opus']);
    expect(parseExcludedRulesText(text)).toEqual(normalizeExcludedRules(text.split(/\r?\n/)));
  });

  test('handles CRLF line endings', () => {
    expect(parseExcludedRulesText('a-*\r\nb-model\r\n')).toEqual(['a-*', 'b-model']);
  });

  test('round-trips through formatExcludedRulesText', () => {
    const rules = ['GPT-5-*', 'claude-opus'];
    expect(parseExcludedRulesText(formatExcludedRulesText(rules))).toEqual(rules);
  });

  /**
   * 凭证编辑器把 excluded_models 存成换行文本，保存时用 `JSON.stringify` 做**顺序敏感**的
   * diff（useAuthFilesPrefixProxyEditor.ts:327）。picker 只要在读写之间保持顺序不变，
   * 「打开但不修改就保存」就永远不会写出与原文件不同的内容。
   */
  test('parse→format is a fixed point for already-normalized input (order preserved)', () => {
    const fromBackend = ['GPT-5-Codex', 'gpt-5-*', 'retired-model'];
    const text = fromBackend.join('\n');

    expect(formatExcludedRulesText(parseExcludedRulesText(text))).toBe(text);
    // 再跑一轮仍是同一个不动点。
    expect(parseExcludedRulesText(formatExcludedRulesText(parseExcludedRulesText(text)))).toEqual(
      fromBackend
    );
  });

  test('normalization never reorders surviving rules', () => {
    expect(normalizeExcludedRules(['z-model', 'a-model', 'm-*'])).toEqual([
      'z-model',
      'a-model',
      'm-*',
    ]);
  });
});

describe('matchesExcludedRule', () => {
  test('matches backend wildcard semantics case-insensitively', () => {
    expect(matchesExcludedRule('gpt-5-*', 'GPT-5-Codex')).toBe(true);
    expect(matchesExcludedRule('*-preview', 'gemini-3-pro-preview')).toBe(true);
    expect(matchesExcludedRule('gpt-5-*', 'gpt-4.1')).toBe(false);
  });

  test('treats regex metacharacters in the rule as literals', () => {
    // The `.` must be a literal dot, not "any character".
    expect(matchesExcludedRule('gpt-4.1', 'gpt-4.1')).toBe(true);
    expect(matchesExcludedRule('gpt-4.1', 'gpt-4x1')).toBe(false);
    expect(matchesExcludedRule('gpt-4.*', 'gpt-4.1-mini')).toBe(true);
    expect(matchesExcludedRule('gpt-4.*', 'gpt-4x1-mini')).toBe(false);
  });

  test('anchors at both ends', () => {
    expect(matchesExcludedRule('gpt-5', 'gpt-5-codex')).toBe(false);
    expect(matchesExcludedRule('gpt-5*', 'gpt-5-codex')).toBe(true);
  });

  test('a blank rule or model never matches', () => {
    expect(matchesExcludedRule('', 'gpt-5')).toBe(false);
    expect(matchesExcludedRule('gpt-5', '  ')).toBe(false);
  });

  test('isWildcardRule / isMatchedByWildcardRule ignore exact rules', () => {
    expect(isWildcardRule('gpt-5-*')).toBe(true);
    expect(isWildcardRule('gpt-5-codex')).toBe(false);
    expect(isMatchedByWildcardRule(['gpt-5-*'], 'gpt-5-mini')).toBe(true);
    // An exact rule matching the model is not a *wildcard* match.
    expect(isMatchedByWildcardRule(['gpt-5-mini'], 'gpt-5-mini')).toBe(false);
  });
});

describe('hasExcludedRule', () => {
  test('compares literally and case-insensitively, without wildcard expansion', () => {
    expect(hasExcludedRule(['GPT-4o'], 'gpt-4O')).toBe(true);
    expect(hasExcludedRule(['gpt-5-*'], 'gpt-5-codex')).toBe(false);
    expect(hasExcludedRule(['gpt-5-*'], 'GPT-5-*')).toBe(true);
    expect(hasExcludedRule(['gpt-4o'], '  ')).toBe(false);
  });
});

describe('toggleExcludedRule', () => {
  test('adds and removes exact rules without touching wildcard rules', () => {
    const added = toggleExcludedRule(['gpt-5-*'], 'claude-opus', true);
    expect(added).toEqual(['gpt-5-*', 'claude-opus']);
    expect(toggleExcludedRule(added, 'CLAUDE-OPUS', false)).toEqual(['gpt-5-*']);
  });

  test('removes a wildcard rule by name (the old auth-file helper refused to)', () => {
    expect(toggleExcludedRule(['gpt-5-*', 'claude-opus'], 'GPT-5-*', false)).toEqual([
      'claude-opus',
    ]);
  });

  test('adding an existing rule moves it to the end rather than duplicating', () => {
    expect(toggleExcludedRule(['a', 'b'], 'A', true)).toEqual(['b', 'A']);
  });

  test('a blank candidate is a no-op add', () => {
    expect(toggleExcludedRule(['a'], '   ', true)).toEqual(['a']);
  });

  test('trims the candidate when removing, down to an empty list', () => {
    expect(toggleExcludedRule(['GPT-4o'], ' gpt-4O ', false)).toEqual([]);
  });

  test('trims the candidate when adding', () => {
    expect(toggleExcludedRule(['gpt-4o'], ' gpt-* ', true)).toEqual(['gpt-4o', 'gpt-*']);
  });
});

describe('splitExcludedRules', () => {
  test('partitions into exact / wildcard / unknown', () => {
    expect(
      splitExcludedRules(
        ['GPT-5-Codex', 'gpt-5-*', 'unlisted-model'],
        ['gpt-5-codex', 'claude-opus']
      )
    ).toEqual({
      exactRules: ['gpt-5-codex'],
      wildcardRules: ['gpt-5-*'],
      unknownRules: ['unlisted-model'],
      customRules: ['gpt-5-*', 'unlisted-model'],
    });
  });

  test('rewrites exact rules to the catalog spelling', () => {
    expect(splitExcludedRules(['GPT-5-CODEX'], ['gpt-5-codex']).exactRules).toEqual([
      'gpt-5-codex',
    ]);
  });

  test('preserves the configured spelling for wildcard and unknown rules', () => {
    const { wildcardRules, unknownRules } = splitExcludedRules(
      ['GPT-5-*', 'Retired-Model'],
      ['gpt-5-codex']
    );
    expect(wildcardRules).toEqual(['GPT-5-*']);
    expect(unknownRules).toEqual(['Retired-Model']);
  });

  test('customRules keeps the original interleaved order, not bucket order', () => {
    // `unlisted` appears before `a-*`; concatenating the buckets would reverse them.
    expect(splitExcludedRules(['unlisted', 'a-*'], ['gpt-5-codex']).customRules).toEqual([
      'unlisted',
      'a-*',
    ]);
  });

  test('an empty catalog makes every exact rule unknown', () => {
    expect(splitExcludedRules(['a', 'b-*'], [])).toEqual({
      exactRules: [],
      wildcardRules: ['b-*'],
      unknownRules: ['a'],
      customRules: ['a', 'b-*'],
    });
  });
});

describe('replaceCustomExcludedRules', () => {
  test('swaps the custom half while retaining exact selections', () => {
    expect(
      replaceCustomExcludedRules(
        ['gpt-5-codex', 'old-*'],
        ['gpt-5-codex', 'claude-opus'],
        'new-*\nlegacy-model'
      )
    ).toEqual(['gpt-5-codex', 'new-*', 'legacy-model']);
  });

  test('clearing the text leaves only the exact selections', () => {
    expect(replaceCustomExcludedRules(['gpt-5-codex', 'old-*'], ['gpt-5-codex'], '')).toEqual([
      'gpt-5-codex',
    ]);
  });

  test('a custom rule duplicating an exact selection does not double it', () => {
    expect(replaceCustomExcludedRules(['gpt-5-codex'], ['gpt-5-codex'], 'GPT-5-CODEX')).toEqual([
      'gpt-5-codex',
    ]);
  });
});
