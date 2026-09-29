/**
 * 排除模型规则 —— 唯一的纯逻辑源。
 *
 * 由两个已删除的模块合并而来：`excludedModelSelection.ts`（文本/数组式）与
 * `oauthExcludedRules.ts`（Set 式），二者曾是同一个领域模型写了两遍——
 * 其 normalize 实现逐字相同，只是一个吃换行文本、一个吃可迭代对象。
 *
 * 规则语义（与后端一致）：
 * - 大小写不敏感；
 * - `*` 匹配任意字符，其余字符按字面量处理（`gpt-4.1` 里的 `.` 不是正则通配符）；
 * - 去重按小写 key，但**保留首次出现的拼写**。
 */

/** 后端「停用整个 provider」的编码。只属于 provider 表单的 disabled 开关，排除面永不产出它。 */
export const DISABLE_ALL_RULE = '*';

const ruleKey = (value: string): string => value.trim().toLowerCase();

export const isWildcardRule = (rule: string): boolean => rule.includes('*');

export function normalizeExcludedRules(values: Iterable<string>): string[] {
  const seen = new Set<string>();
  const rules: string[] = [];

  for (const value of values) {
    const rule = value.trim();
    const key = ruleKey(rule);
    if (!key || seen.has(key)) continue;
    seen.add(key);
    rules.push(rule);
  }

  return rules;
}

export const parseExcludedRulesText = (text: string): string[] =>
  normalizeExcludedRules(text.split(/\r?\n/));

export const formatExcludedRulesText = (rules: readonly string[]): string => rules.join('\n');

export function matchesExcludedRule(rule: string, modelId: string): boolean {
  const normalizedRule = ruleKey(rule);
  const normalizedModel = ruleKey(modelId);
  if (!normalizedRule || !normalizedModel) return false;
  if (!isWildcardRule(normalizedRule)) return normalizedRule === normalizedModel;

  // 按 `*` 切开，逐段转义正则元字符，再用 `.*` 接回——只有 `*` 是通配符。
  const escaped = normalizedRule
    .split('*')
    .map((part) => part.replace(/[.*+?^${}()|[\]\\]/g, '\\$&'))
    .join('.*');
  return new RegExp(`^${escaped}$`, 'i').test(normalizedModel);
}

/** 该模型是否被某条**通配符**规则命中（精确规则不算）。 */
export const isMatchedByWildcardRule = (rules: Iterable<string>, modelId: string): boolean =>
  Array.from(rules).some((rule) => isWildcardRule(rule) && matchesExcludedRule(rule, modelId));

/** 规则列表里是否存在与 candidate 字面相等（忽略大小写）的一条。不做通配符展开。 */
export function hasExcludedRule(rules: Iterable<string>, candidate: string): boolean {
  const candidateKey = ruleKey(candidate);
  if (!candidateKey) return false;
  return Array.from(rules).some((rule) => ruleKey(rule) === candidateKey);
}

/**
 * 增删一条字面规则。
 *
 * 注意：这里按 key 过滤，**不**豁免含 `*` 的规则。旧的 `toggleExcludedModel` 曾拒绝删除
 * 通配符规则，那是因为调用点有两个互不知情的写入面（列表管精确、textarea 管通配符）需要
 * 互不践踏。统一组件里两个面同属一个组件，该守卫属于组件而非纯函数。
 */
export function toggleExcludedRule(
  rules: Iterable<string>,
  candidate: string,
  excluded: boolean
): string[] {
  const candidateRule = candidate.trim();
  const candidateKey = ruleKey(candidateRule);
  const next = normalizeExcludedRules(rules).filter((rule) => ruleKey(rule) !== candidateKey);

  if (excluded && candidateKey) next.push(candidateRule);
  return next;
}

export interface SplitExcludedRules {
  /** 精确命中目录的规则，**改写为目录的拼写**（勾选框驱动，id 应当规范化）。 */
  exactRules: string[];
  /** 含 `*` 的规则，保留配置里的拼写。 */
  wildcardRules: string[];
  /** 精确但目录里没有的规则（如已下线的模型 id），保留配置里的拼写。 */
  unknownRules: string[];
  /** `wildcardRules ∪ unknownRules`，但按**原始出现顺序**——textarea 的内容与顺序敏感的 diff 都依赖它。 */
  customRules: string[];
}

export function splitExcludedRules(
  rules: Iterable<string>,
  candidateIds: readonly string[]
): SplitExcludedRules {
  const candidateByKey = new Map(candidateIds.map((id) => [ruleKey(id), id]));
  const exactRules: string[] = [];
  const wildcardRules: string[] = [];
  const unknownRules: string[] = [];
  const customRules: string[] = [];

  normalizeExcludedRules(rules).forEach((rule) => {
    if (isWildcardRule(rule)) {
      wildcardRules.push(rule);
      customRules.push(rule);
      return;
    }
    const candidate = candidateByKey.get(ruleKey(rule));
    if (candidate) {
      exactRules.push(candidate);
      return;
    }
    unknownRules.push(rule);
    customRules.push(rule);
  });

  return { exactRules, wildcardRules, unknownRules, customRules };
}

/** 用一段文本整体替换「自定义」半边（通配符 + 目录外精确规则），保留精确勾选的那一半。 */
export function replaceCustomExcludedRules(
  rules: Iterable<string>,
  candidateIds: readonly string[],
  text: string
): string[] {
  const { exactRules } = splitExcludedRules(rules, candidateIds);
  return normalizeExcludedRules([...exactRules, ...parseExcludedRulesText(text)]);
}

/* -------------------------------------------------------------------------- */
/* 展示用派生量                                                                */
/* -------------------------------------------------------------------------- */

/**
 * 单个模型的排除态。
 *
 * `both` 是最微妙的一档：模型既被显式勾选、又被某条通配符规则命中。取消勾选后它**依然
 * 被排除**，所以那一行不能在视觉上「取消打勾」，否则用户会以为点击失败。旧 UI 把这一档
 * 完全藏了起来。
 */
export type ModelExclusionState =
  | { state: 'included' }
  | { state: 'excluded'; by: 'exact' }
  | { state: 'excluded'; by: 'wildcard'; rule: string }
  | { state: 'excluded'; by: 'both'; rule: string };

export function getModelExclusionState(
  rules: readonly string[],
  modelId: string
): ModelExclusionState {
  const modelKey = ruleKey(modelId);
  if (!modelKey) return { state: 'included' };

  let hasExact = false;
  let wildcard: string | undefined;

  for (const rule of rules) {
    if (isWildcardRule(rule)) {
      if (wildcard === undefined && matchesExcludedRule(rule, modelId)) wildcard = rule;
    } else if (!hasExact && ruleKey(rule) === modelKey) {
      hasExact = true;
    }
  }

  if (hasExact && wildcard !== undefined) return { state: 'excluded', by: 'both', rule: wildcard };
  if (hasExact) return { state: 'excluded', by: 'exact' };
  if (wildcard !== undefined) return { state: 'excluded', by: 'wildcard', rule: wildcard };
  return { state: 'included' };
}

export const isModelExcluded = (rules: readonly string[], modelId: string): boolean =>
  getModelExclusionState(rules, modelId).state === 'excluded';

export interface RuleMatchSummary {
  rule: string;
  /** 该规则命中的目录模型，按目录顺序。 */
  matched: string[];
  matchCount: number;
}

/** 每条规则各命中了目录里的哪些模型——通配符编辑器的实时反馈就靠它。 */
export const matchedModelsByRule = (
  rules: readonly string[],
  candidateIds: readonly string[]
): RuleMatchSummary[] =>
  rules.map((rule) => {
    const matched = candidateIds.filter((id) => matchesExcludedRule(rule, id));
    return { rule, matched, matchCount: matched.length };
  });

export interface ExclusionStats {
  total: number;
  excluded: number;
  available: number;
}

/**
 * 摘要行与计量条的数据源。
 *
 * `excluded` 数的是**目录内被任意规则命中的模型数**，不是 `rules.length`——一条
 * `gpt-5-*` 可能命中 6 个模型，也可能一个都不命中。用规则数当分子会在新地方复刻
 * 旧 UI 那个谎言：分子分母必须同源，计量条才是诚实的。
 */
export function summarizeExclusion(
  rules: readonly string[],
  candidateIds: readonly string[]
): ExclusionStats {
  const total = candidateIds.length;
  const excluded = candidateIds.reduce(
    (count, id) => (isModelExcluded(rules, id) ? count + 1 : count),
    0
  );
  return { total, excluded, available: total - excluded };
}
