import { normalizeExcludedRules } from '@/components/excludedModels/excludedModelRules';
import type { OAuthModelAliasEntry } from '@/types';
import { normalizeProviderKey } from '../constants';
import { getModelAliasDraftSignature, getStringSetSignature } from '../oauthEditorState';

/* ============================================================
 * 模型规则工作台的草稿模型（纯函数，无 React / 无网络）。
 *
 * 整页只有一份草稿：所有 provider 的改动先落在草稿里，由一条保存栏统一提交。
 * 清单与图谱两个视图读写的是同一份草稿，因此不存在「图谱即时写、清单要保存」
 * 这种两套语义。
 * ============================================================ */

export type AliasDraftEntry = OAuthModelAliasEntry & { id: string };
export type AliasDraft = Record<string, AliasDraftEntry[]>;
export type ExcludedDraft = Record<string, string[]>;

export type AliasRowIssue = 'incomplete' | 'same' | 'duplicate';

type MakeId = () => string;

const key = (value: unknown) =>
  String(value ?? '')
    .trim()
    .toLowerCase();

const isBlankRow = (entry: OAuthModelAliasEntry) => !key(entry.name) && !key(entry.alias);

/* ---------- 别名：构建 / 清洗 ---------- */

export const createAliasRow = (makeId: MakeId): AliasDraftEntry => ({
  id: makeId(),
  name: '',
  alias: '',
  fork: true,
});

export const buildAliasDraft = (
  source: Record<string, OAuthModelAliasEntry[]>,
  makeId: MakeId
): AliasDraft => {
  const draft: AliasDraft = {};
  Object.entries(source).forEach(([provider, entries]) => {
    const providerKey = normalizeProviderKey(provider);
    if (!providerKey || !Array.isArray(entries) || entries.length === 0) return;
    draft[providerKey] = [
      ...(draft[providerKey] ?? []),
      ...entries.map((entry) => ({
        id: makeId(),
        name: entry.name ?? '',
        alias: entry.alias ?? '',
        fork: entry.fork === true,
        forceMapping: entry.forceMapping,
      })),
    ];
  });
  return draft;
};

/** 可提交的条目：去空白行、trim，只保留后端认识的字段。 */
export const toAliasEntries = (entries: readonly OAuthModelAliasEntry[]): OAuthModelAliasEntry[] =>
  entries.flatMap((entry) => {
    const name = String(entry.name ?? '').trim();
    const alias = String(entry.alias ?? '').trim();
    if (!name || !alias) return [];
    const next: OAuthModelAliasEntry = { name, alias };
    if (entry.fork) next.fork = true;
    if (typeof entry.forceMapping === 'boolean') next.forceMapping = entry.forceMapping;
    return [next];
  });

export const toAliasMap = (draft: AliasDraft): Record<string, OAuthModelAliasEntry[]> => {
  const result: Record<string, OAuthModelAliasEntry[]> = {};
  Object.entries(draft).forEach(([provider, entries]) => {
    const clean = toAliasEntries(entries);
    if (clean.length) result[provider] = clean;
  });
  return result;
};

/**
 * 行级校验，与后端 SanitizeOAuthModelAlias 的丢弃规则一一对应——
 * 后端会**静默丢掉**这些行，所以前端必须在保存前说出来。
 */
export const getAliasRowIssues = (
  entries: readonly AliasDraftEntry[]
): Record<string, AliasRowIssue> => {
  const issues: Record<string, AliasRowIssue> = {};
  const seen = new Set<string>();
  entries.forEach((entry) => {
    if (isBlankRow(entry)) return;
    const name = key(entry.name);
    const alias = key(entry.alias);
    if (!name || !alias) {
      issues[entry.id] = 'incomplete';
      return;
    }
    if (name === alias) {
      issues[entry.id] = 'same';
      return;
    }
    if (seen.has(alias)) {
      issues[entry.id] = 'duplicate';
      return;
    }
    seen.add(alias);
  });
  return issues;
};

export const findAliasDraftIssue = (
  draft: AliasDraft,
  providers: readonly string[]
): { provider: string; issue: AliasRowIssue } | null => {
  for (const provider of providers) {
    const issue = Object.values(getAliasRowIssues(draft[provider] ?? []))[0];
    if (issue) return { provider, issue };
  }
  return null;
};

/* ---------- 别名：行编辑 ---------- */

const withProvider = (
  draft: AliasDraft,
  provider: string,
  update: (entries: AliasDraftEntry[]) => AliasDraftEntry[]
): AliasDraft => {
  const providerKey = normalizeProviderKey(provider);
  if (!providerKey) return draft;
  const current = draft[providerKey] ?? [];
  const next = update(current);
  return next === current ? draft : { ...draft, [providerKey]: next };
};

export const addAliasRow = (draft: AliasDraft, provider: string, makeId: MakeId): AliasDraft =>
  withProvider(draft, provider, (entries) => [...entries, createAliasRow(makeId)]);

/** 进入一个尚无内容的 provider 时垫一行空白，省掉「先点添加」这一步。空白行不计入脏状态。 */
export const ensureAliasRow = (draft: AliasDraft, provider: string, makeId: MakeId): AliasDraft =>
  withProvider(draft, provider, (entries) => (entries.length ? entries : [createAliasRow(makeId)]));

export const updateAliasRow = (
  draft: AliasDraft,
  provider: string,
  id: string,
  patch: Partial<Pick<OAuthModelAliasEntry, 'name' | 'alias' | 'fork'>>
): AliasDraft =>
  withProvider(draft, provider, (entries) =>
    entries.map((entry) => (entry.id === id ? { ...entry, ...patch } : entry))
  );

export const removeAliasRow = (draft: AliasDraft, provider: string, id: string): AliasDraft =>
  withProvider(draft, provider, (entries) => entries.filter((entry) => entry.id !== id));

export const clearAliasProvider = (draft: AliasDraft, provider: string): AliasDraft =>
  withProvider(draft, provider, (entries) => (entries.length ? [] : entries));

/* ---------- 别名：图谱操作（按 provider + 源模型 + 别名寻址） ---------- */

const isLink = (entry: OAuthModelAliasEntry, name: string, alias: string) =>
  key(entry.name) === key(name) && key(entry.alias) === key(alias);

export const addAliasLink = (
  draft: AliasDraft,
  provider: string,
  name: string,
  alias: string,
  makeId: MakeId
): AliasDraft => {
  const nextName = name.trim();
  const nextAlias = alias.trim();
  if (!nextName || !nextAlias) return draft;
  return withProvider(draft, provider, (entries) =>
    entries.some((entry) => isLink(entry, nextName, nextAlias))
      ? entries
      : [
          ...entries.filter((entry) => !isBlankRow(entry)),
          { id: makeId(), name: nextName, alias: nextAlias, fork: true },
        ]
  );
};

export const removeAliasLink = (
  draft: AliasDraft,
  provider: string,
  name: string,
  alias: string
): AliasDraft =>
  withProvider(draft, provider, (entries) => {
    const next = entries.filter((entry) => !isLink(entry, name, alias));
    return next.length === entries.length ? entries : next;
  });

export const setAliasLinkFork = (
  draft: AliasDraft,
  provider: string,
  name: string,
  alias: string,
  fork: boolean
): AliasDraft =>
  withProvider(draft, provider, (entries) =>
    entries.some((entry) => isLink(entry, name, alias) && Boolean(entry.fork) !== fork)
      ? entries.map((entry) => (isLink(entry, name, alias) ? { ...entry, fork } : entry))
      : entries
  );

/** 别名是跨 provider 的概念：改名/删除作用于所有用到它的 provider。 */
export const renameAlias = (draft: AliasDraft, oldAlias: string, newAlias: string): AliasDraft => {
  const from = key(oldAlias);
  const to = newAlias.trim();
  if (!from || !to || oldAlias.trim() === to) return draft;
  let changed = false;
  const next: AliasDraft = {};
  Object.entries(draft).forEach(([provider, entries]) => {
    const hit = entries.some((entry) => key(entry.alias) === from);
    if (hit) changed = true;
    next[provider] = hit
      ? entries.map((entry) => (key(entry.alias) === from ? { ...entry, alias: to } : entry))
      : entries;
  });
  return changed ? next : draft;
};

export const deleteAlias = (draft: AliasDraft, alias: string): AliasDraft => {
  const target = key(alias);
  if (!target) return draft;
  let changed = false;
  const next: AliasDraft = {};
  Object.entries(draft).forEach(([provider, entries]) => {
    const kept = entries.filter((entry) => key(entry.alias) !== target);
    if (kept.length !== entries.length) changed = true;
    next[provider] = kept.length !== entries.length ? kept : entries;
  });
  return changed ? next : draft;
};

/* ---------- 别名：脏状态 / 保存计划 / 统计 ---------- */

const unionKeys = (...maps: Array<Record<string, unknown>>) =>
  Array.from(new Set(maps.flatMap((map) => Object.keys(map)))).sort((a, b) => a.localeCompare(b));

export const getDirtyAliasProviders = (
  baseline: Record<string, OAuthModelAliasEntry[]>,
  draft: AliasDraft
): string[] =>
  unionKeys(baseline, draft).filter(
    (provider) =>
      getModelAliasDraftSignature(baseline[provider] ?? []) !==
      getModelAliasDraftSignature(draft[provider] ?? [])
  );

export type ProviderSavePlan<T> = { provider: string; value: T | null }[];

/** value 为 null 表示删除该 provider 的整条记录。 */
export const buildAliasSavePlan = (
  baseline: Record<string, OAuthModelAliasEntry[]>,
  draft: AliasDraft
): ProviderSavePlan<OAuthModelAliasEntry[]> =>
  getDirtyAliasProviders(baseline, draft).flatMap<ProviderSavePlan<OAuthModelAliasEntry[]>[number]>(
    (provider) => {
      const entries = toAliasEntries(draft[provider] ?? []);
      if (entries.length) return [{ provider, value: entries }];
      return baseline[provider]?.length ? [{ provider, value: null }] : [];
    }
  );

export type AliasStats = { providers: number; mappings: number; aliases: number };

export const getAliasStats = (source: Record<string, OAuthModelAliasEntry[]>): AliasStats => {
  const aliases = new Set<string>();
  let providers = 0;
  let mappings = 0;
  Object.values(source).forEach((entries) => {
    const clean = toAliasEntries(entries ?? []);
    if (!clean.length) return;
    providers += 1;
    mappings += clean.length;
    clean.forEach((entry) => aliases.add(key(entry.alias)));
  });
  return { providers, mappings, aliases: aliases.size };
};

/* ---------- 禁用规则 ---------- */

export const buildExcludedDraft = (source: Record<string, string[]>): ExcludedDraft => {
  const draft: ExcludedDraft = {};
  Object.entries(source).forEach(([provider, rules]) => {
    const providerKey = normalizeProviderKey(provider);
    if (!providerKey) return;
    const merged = normalizeExcludedRules([...(draft[providerKey] ?? []), ...(rules ?? [])]);
    if (merged.length) draft[providerKey] = merged;
  });
  return draft;
};

export const setExcludedRules = (
  draft: ExcludedDraft,
  provider: string,
  rules: readonly string[]
): ExcludedDraft => {
  const providerKey = normalizeProviderKey(provider);
  if (!providerKey) return draft;
  return { ...draft, [providerKey]: normalizeExcludedRules(rules) };
};

const excludedSignature = (rules: readonly string[] | undefined) =>
  getStringSetSignature(normalizeExcludedRules(rules ?? []));

export const getDirtyExcludedProviders = (
  baseline: Record<string, string[]>,
  draft: ExcludedDraft
): string[] =>
  unionKeys(baseline, draft).filter(
    (provider) => excludedSignature(baseline[provider]) !== excludedSignature(draft[provider])
  );

export const buildExcludedSavePlan = (
  baseline: Record<string, string[]>,
  draft: ExcludedDraft
): ProviderSavePlan<string[]> =>
  getDirtyExcludedProviders(baseline, draft).flatMap<ProviderSavePlan<string[]>[number]>(
    (provider) => {
      const rules = normalizeExcludedRules(draft[provider] ?? []);
      if (rules.length) return [{ provider, value: rules }];
      return baseline[provider]?.length ? [{ provider, value: null }] : [];
    }
  );

export type ExcludedStats = { providers: number; rules: number };

export const getExcludedStats = (source: Record<string, string[]>): ExcludedStats => {
  let providers = 0;
  let rules = 0;
  Object.values(source).forEach((list) => {
    const count = normalizeExcludedRules(list ?? []).length;
    if (!count) return;
    providers += 1;
    rules += count;
  });
  return { providers, rules };
};

/* ---------- 保存后的草稿重定基 ---------- */

/**
 * 保存结束、基线重新拉取之后：成功的 provider 跟随新基线，
 * 失败的 provider 保留用户的草稿（不能因为一个失败就把别人的输入也丢掉）。
 */
export const rebaseDraft = <T>(
  fresh: Record<string, T>,
  previous: Record<string, T>,
  keep: readonly string[]
): Record<string, T> => {
  const next = { ...fresh };
  keep.forEach((provider) => {
    if (provider in previous) next[provider] = previous[provider];
    else delete next[provider];
  });
  return next;
};
