import { describe, expect, test } from 'bun:test';
import {
  addAliasLink,
  addAliasRow,
  buildAliasDraft,
  buildAliasSavePlan,
  buildExcludedDraft,
  buildExcludedSavePlan,
  clearAliasProvider,
  deleteAlias,
  ensureAliasRow,
  findAliasDraftIssue,
  getAliasRowIssues,
  getAliasStats,
  getDirtyAliasProviders,
  getDirtyExcludedProviders,
  getExcludedStats,
  rebaseDraft,
  removeAliasLink,
  removeAliasRow,
  renameAlias,
  setAliasLinkFork,
  setExcludedRules,
  toAliasMap,
  updateAliasRow,
} from '../src/features/authFiles/modelRules/draft';
import { runSavePlan } from '../src/features/authFiles/modelRules/useModelRulesData';

const ids = () => {
  let next = 0;
  return () => `id-${++next}`;
};

const baseline = {
  codex: [
    { name: 'gpt-5-codex', alias: 'gpt-5' },
    { name: 'gpt-5-mini', alias: 'mini', fork: true, forceMapping: false },
  ],
  claude: [{ name: 'claude-opus-4', alias: 'opus' }],
};

describe('alias draft', () => {
  test('a freshly built draft is clean and round-trips backend fields', () => {
    const draft = buildAliasDraft(baseline, ids());
    expect(getDirtyAliasProviders(baseline, draft)).toEqual([]);
    expect(buildAliasSavePlan(baseline, draft)).toEqual([]);
    expect(toAliasMap(draft)).toEqual(baseline);
  });

  test('merges provider spellings into one normalized group', () => {
    const draft = buildAliasDraft(
      { Codex: [{ name: 'a', alias: 'x' }], ' codex ': [{ name: 'b', alias: 'y' }] },
      ids()
    );
    expect(Object.keys(draft)).toEqual(['codex']);
    expect(draft.codex.map((entry) => entry.alias)).toEqual(['x', 'y']);
  });

  test('placeholder rows never count as changes', () => {
    const makeId = ids();
    let draft = buildAliasDraft(baseline, makeId);
    draft = ensureAliasRow(draft, 'gemini', makeId);
    draft = addAliasRow(draft, 'codex', makeId);
    expect(draft.gemini).toHaveLength(1);
    expect(getDirtyAliasProviders(baseline, draft)).toEqual([]);
    // an existing group is left alone
    expect(ensureAliasRow(draft, 'codex', makeId)).toBe(draft);
  });

  test('row edits mark only the touched provider and build a per-provider plan', () => {
    const makeId = ids();
    let draft = buildAliasDraft(baseline, makeId);
    draft = updateAliasRow(draft, 'codex', draft.codex[0].id, { alias: ' gpt-five ' });
    expect(getDirtyAliasProviders(baseline, draft)).toEqual(['codex']);
    expect(buildAliasSavePlan(baseline, draft)).toEqual([
      {
        provider: 'codex',
        value: [
          { name: 'gpt-5-codex', alias: 'gpt-five' },
          { name: 'gpt-5-mini', alias: 'mini', fork: true, forceMapping: false },
        ],
      },
    ]);
  });

  test('emptying a saved provider deletes it; emptying an unsaved one is a no-op', () => {
    const makeId = ids();
    let draft = buildAliasDraft(baseline, makeId);
    draft = removeAliasRow(draft, 'claude', draft.claude[0].id);
    draft = clearAliasProvider(ensureAliasRow(draft, 'gemini', makeId), 'gemini');
    expect(buildAliasSavePlan(baseline, draft)).toEqual([{ provider: 'claude', value: null }]);
  });

  test('flags exactly the rows the backend would silently drop', () => {
    const makeId = ids();
    let draft = buildAliasDraft({ codex: [{ name: 'a', alias: 'x' }] }, makeId);
    draft = addAliasLink(draft, 'codex', 'b', 'X', makeId);
    draft = addAliasRow(draft, 'codex', makeId);
    draft = updateAliasRow(draft, 'codex', draft.codex[2].id, { name: 'half' });
    draft = addAliasRow(draft, 'codex', makeId);
    draft = updateAliasRow(draft, 'codex', draft.codex[3].id, { name: 'same', alias: 'SAME' });
    draft = addAliasRow(draft, 'codex', makeId);

    const [, duplicate, incomplete, same, blank] = draft.codex;
    expect(getAliasRowIssues(draft.codex)).toEqual({
      [duplicate.id]: 'duplicate',
      [incomplete.id]: 'incomplete',
      [same.id]: 'same',
    });
    expect(getAliasRowIssues(draft.codex)[blank.id]).toBeUndefined();
    expect(findAliasDraftIssue(draft, ['claude', 'codex'])).toEqual({
      provider: 'codex',
      issue: 'duplicate',
    });
    expect(findAliasDraftIssue(draft, ['claude'])).toBeNull();
  });

  test('diagram operations address links case-insensitively and stay idempotent', () => {
    const makeId = ids();
    let draft = buildAliasDraft(baseline, makeId);

    expect(addAliasLink(draft, 'codex', 'GPT-5-Codex', 'GPT-5', makeId)).toBe(draft);
    draft = addAliasLink(
      ensureAliasRow(draft, 'gemini', makeId),
      'Gemini',
      'gemini-pro',
      'pro',
      makeId
    );
    // linking replaces the placeholder row instead of leaving it behind
    expect(draft.gemini.map(({ name, alias, fork }) => ({ name, alias, fork }))).toEqual([
      { name: 'gemini-pro', alias: 'pro', fork: true },
    ]);

    draft = setAliasLinkFork(draft, 'codex', 'gpt-5-codex', 'gpt-5', true);
    expect(draft.codex[0].fork).toBe(true);
    expect(setAliasLinkFork(draft, 'codex', 'gpt-5-codex', 'gpt-5', true)).toBe(draft);

    draft = removeAliasLink(draft, 'codex', 'GPT-5-MINI', 'Mini');
    expect(draft.codex).toHaveLength(1);
    expect(removeAliasLink(draft, 'codex', 'missing', 'mini')).toBe(draft);
  });

  test('renaming and deleting an alias reach every provider that uses it', () => {
    const makeId = ids();
    const shared = {
      codex: [{ name: 'gpt-5', alias: 'best' }],
      claude: [
        { name: 'claude-opus-4', alias: 'Best' },
        { name: 'claude-haiku', alias: 'fast' },
      ],
    };
    const draft = buildAliasDraft(shared, makeId);

    const renamed = renameAlias(draft, 'BEST', 'top');
    expect(toAliasMap(renamed)).toEqual({
      codex: [{ name: 'gpt-5', alias: 'top' }],
      claude: [
        { name: 'claude-opus-4', alias: 'top' },
        { name: 'claude-haiku', alias: 'fast' },
      ],
    });
    expect(getDirtyAliasProviders(shared, renamed)).toEqual(['claude', 'codex']);
    expect(renameAlias(draft, 'missing', 'x')).toBe(draft);
    expect(renameAlias(draft, 'best', ' best ')).toBe(draft);

    const deleted = deleteAlias(draft, 'best');
    expect(buildAliasSavePlan(shared, deleted)).toEqual([
      { provider: 'claude', value: [{ name: 'claude-haiku', alias: 'fast' }] },
      { provider: 'codex', value: null },
    ]);
  });

  test('stats count complete mappings and distinct aliases only', () => {
    const makeId = ids();
    const draft = addAliasRow(
      buildAliasDraft(
        { codex: [{ name: 'a', alias: 'shared' }], claude: [{ name: 'b', alias: 'Shared' }] },
        makeId
      ),
      'gemini',
      makeId
    );
    expect(getAliasStats(draft)).toEqual({ providers: 2, mappings: 2, aliases: 1 });
  });
});

describe('excluded draft', () => {
  const excluded = { codex: ['gpt-5', 'o3-*'], claude: ['claude-opus-4'] };

  test('rule order and duplicates do not make a provider dirty', () => {
    let draft = buildExcludedDraft(excluded);
    expect(getDirtyExcludedProviders(excluded, draft)).toEqual([]);
    draft = setExcludedRules(draft, 'Codex', ['o3-*', 'gpt-5', 'gpt-5']);
    expect(getDirtyExcludedProviders(excluded, draft)).toEqual([]);
  });

  test('plans saves, deletions and nothing for untouched or never-saved providers', () => {
    let draft = buildExcludedDraft(excluded);
    draft = setExcludedRules(draft, 'codex', ['gpt-5']);
    draft = setExcludedRules(draft, 'claude', []);
    draft = setExcludedRules(draft, 'gemini', []);
    expect(buildExcludedSavePlan(excluded, draft)).toEqual([
      { provider: 'claude', value: null },
      { provider: 'codex', value: ['gpt-5'] },
    ]);
    expect(getExcludedStats(draft)).toEqual({ providers: 1, rules: 1 });
  });
});

describe('saving', () => {
  test('a failed provider does not stop the others and keeps its draft on rebase', async () => {
    const calls: string[] = [];
    const outcome = await runSavePlan(
      [
        { provider: 'claude', value: null },
        { provider: 'codex', value: ['a'] },
        { provider: 'gemini', value: ['b'] },
      ],
      async (provider) => {
        calls.push(`save:${provider}`);
        if (provider === 'codex') throw new Error('rejected');
      },
      async (provider) => {
        calls.push(`delete:${provider}`);
      }
    );
    expect(calls).toEqual(['delete:claude', 'save:codex', 'save:gemini']);
    expect(outcome).toEqual({ failed: ['codex'], message: 'rejected' });

    if (!outcome) throw new Error('The unchanged connection must finish saving');
    const fresh = { codex: ['server'], gemini: ['b'] };
    const previous = { codex: ['a'], gemini: ['stale'], claude: [] as string[] };
    expect(rebaseDraft(fresh, previous, outcome.failed)).toEqual({ codex: ['a'], gemini: ['b'] });
    // a failed deletion keeps the (empty) draft so the provider stays marked as changed
    expect(rebaseDraft({ claude: ['x'] }, previous, ['claude'])).toEqual({ claude: [] });
  });
});
