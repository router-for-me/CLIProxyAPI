import { describe, expect, test } from 'bun:test';
import {
  getModelExclusionState,
  isModelExcluded,
  matchedModelsByRule,
  summarizeExclusion,
} from '../src/components/excludedModels/excludedModelRules';

const CATALOG = ['gpt-5-codex', 'gpt-5-mini', 'gpt-5-pro', 'claude-opus', 'gemini-3-pro'];

describe('getModelExclusionState', () => {
  test('included when no rule touches the model', () => {
    expect(getModelExclusionState(['claude-opus'], 'gpt-5-mini')).toEqual({ state: 'included' });
  });

  test('exact when only a literal rule matches', () => {
    expect(getModelExclusionState(['gpt-5-mini'], 'gpt-5-mini')).toEqual({
      state: 'excluded',
      by: 'exact',
    });
  });

  test('wildcard carries the responsible rule so the row can explain itself', () => {
    expect(getModelExclusionState(['gpt-5-*'], 'gpt-5-mini')).toEqual({
      state: 'excluded',
      by: 'wildcard',
      rule: 'gpt-5-*',
    });
  });

  test('both when a model is explicitly picked AND caught by a wildcard', () => {
    expect(getModelExclusionState(['gpt-5-mini', 'gpt-5-*'], 'gpt-5-mini')).toEqual({
      state: 'excluded',
      by: 'both',
      rule: 'gpt-5-*',
    });
  });

  test('order of the rules does not change the resolved state', () => {
    expect(getModelExclusionState(['gpt-5-*', 'gpt-5-mini'], 'gpt-5-mini')).toEqual({
      state: 'excluded',
      by: 'both',
      rule: 'gpt-5-*',
    });
  });

  test('reports the first matching wildcard when several apply', () => {
    expect(getModelExclusionState(['gpt-*', 'gpt-5-*'], 'gpt-5-mini')).toEqual({
      state: 'excluded',
      by: 'wildcard',
      rule: 'gpt-*',
    });
  });

  test('matching is case-insensitive in both directions', () => {
    expect(getModelExclusionState(['GPT-5-MINI'], 'gpt-5-mini')).toEqual({
      state: 'excluded',
      by: 'exact',
    });
    expect(getModelExclusionState(['gpt-5-mini'], 'GPT-5-MINI')).toEqual({
      state: 'excluded',
      by: 'exact',
    });
  });

  test('a blank model id is never excluded', () => {
    expect(getModelExclusionState(['*-mini'], '   ')).toEqual({ state: 'included' });
  });

  test('isModelExcluded collapses all three excluded variants', () => {
    expect(isModelExcluded(['gpt-5-mini'], 'gpt-5-mini')).toBe(true);
    expect(isModelExcluded(['gpt-5-*'], 'gpt-5-mini')).toBe(true);
    expect(isModelExcluded(['gpt-5-mini', 'gpt-5-*'], 'gpt-5-mini')).toBe(true);
    expect(isModelExcluded(['claude-opus'], 'gpt-5-mini')).toBe(false);
  });
});

describe('matchedModelsByRule', () => {
  test('reports what each rule actually catches, in catalog order', () => {
    expect(matchedModelsByRule(['gpt-5-*'], CATALOG)).toEqual([
      { rule: 'gpt-5-*', matched: ['gpt-5-codex', 'gpt-5-mini', 'gpt-5-pro'], matchCount: 3 },
    ]);
  });

  test('a rule matching nothing is reported with a zero count, not omitted', () => {
    expect(matchedModelsByRule(['retired-*'], CATALOG)).toEqual([
      { rule: 'retired-*', matched: [], matchCount: 0 },
    ]);
  });

  test('an exact rule matches exactly its own model', () => {
    expect(matchedModelsByRule(['claude-opus'], CATALOG)).toEqual([
      { rule: 'claude-opus', matched: ['claude-opus'], matchCount: 1 },
    ]);
  });

  test('overlapping rules each report the full set they catch', () => {
    expect(matchedModelsByRule(['gpt-*', 'gpt-5-pro'], CATALOG)).toEqual([
      { rule: 'gpt-*', matched: ['gpt-5-codex', 'gpt-5-mini', 'gpt-5-pro'], matchCount: 3 },
      { rule: 'gpt-5-pro', matched: ['gpt-5-pro'], matchCount: 1 },
    ]);
  });

  test('preserves the given rule order and length', () => {
    expect(matchedModelsByRule(['b-*', 'a-*'], CATALOG).map((s) => s.rule)).toEqual(['b-*', 'a-*']);
  });
});

describe('summarizeExclusion', () => {
  test('counts catalog models hit by any rule, not the rules themselves', () => {
    // One rule, three models — a rule count would say 1 and the meter would lie.
    expect(summarizeExclusion(['gpt-5-*'], CATALOG)).toEqual({
      total: 5,
      excluded: 3,
      available: 2,
    });
  });

  test('a model caught by both an exact and a wildcard rule counts once', () => {
    expect(summarizeExclusion(['gpt-5-mini', 'gpt-5-*'], CATALOG)).toEqual({
      total: 5,
      excluded: 3,
      available: 2,
    });
  });

  test('rules that match nothing in the catalog do not inflate the count', () => {
    expect(summarizeExclusion(['retired-model', 'gone-*'], CATALOG)).toEqual({
      total: 5,
      excluded: 0,
      available: 5,
    });
  });

  test('available is always total minus excluded', () => {
    const stats = summarizeExclusion(['gpt-*', 'claude-opus'], CATALOG);
    expect(stats.available).toBe(stats.total - stats.excluded);
    expect(stats).toEqual({ total: 5, excluded: 4, available: 1 });
  });

  test('an empty catalog yields all zeroes rather than NaN', () => {
    expect(summarizeExclusion(['gpt-5-*'], [])).toEqual({ total: 0, excluded: 0, available: 0 });
  });

  test('no rules means nothing excluded', () => {
    expect(summarizeExclusion([], CATALOG)).toEqual({ total: 5, excluded: 0, available: 5 });
  });
});
