import { isAlias, isMap, isNode, isScalar, isSeq, parseDocument, visit } from 'yaml';
import {
  buildErrorRules,
  readErrorRules,
  validateErrorRule,
} from '@/features/providers/errorRules';
import { assertConfigListsUnchanged, ConfigDraftConflictError } from '@/services/api/configPatch';
import { normalizeErrorRule, serializeErrorRule } from '@/services/api/errorRules';
import type { OAuthChannelRules } from '@/types/visualConfig';
import { isRecord } from '@/utils/helpers';

import { oauthMatchTerms, oauthRulesPath as root } from '@/services/api/configYaml';

// Copying anchored descendants would shadow their definitions. Moving an alias
// could also bind it to a different anchor at its destination.
function assertPortable(node: unknown, path: string[]) {
  if (!isNode(node)) return;
  visit(node, {
    Node: (_, child) => {
      if (isAlias(child) || child.anchor)
        throw new ConfigDraftConflictError(path, 'unsafe_yaml_structure');
    },
  });
}

function prepareParents(doc: ReturnType<typeof parseDocument>, create: boolean) {
  for (let i = 1; i <= root.length; i++) {
    const path = root.slice(0, i);
    const node = doc.getIn(path, true);
    if (isAlias(node)) {
      const target = node.resolve(doc);
      if (!isMap(target)) throw new ConfigDraftConflictError(path, 'unsafe_yaml_structure');
      const copy = target.clone();
      delete copy.anchor;
      assertPortable(copy, path);
      doc.setIn(path, copy);
    } else if (isMap(node)) {
      if (node.anchor) throw new ConfigDraftConflictError(path, 'unsafe_yaml_structure');
    } else if (node === undefined || (isScalar(node) && node.value === null && !node.anchor)) {
      if (!create) return;
      doc.setIn(path, doc.createNode({}));
    } else {
      throw new ConfigDraftConflictError(path, 'unsafe_yaml_structure');
    }
  }
}

export function readOAuthErrorRules(
  raw: unknown,
  doc: ReturnType<typeof parseDocument>
): OAuthChannelRules[] {
  if (!isRecord(raw)) return [];
  return Object.entries(raw).flatMap(([channel, list]) =>
    Array.isArray(list)
      ? [
          {
            channel,
            rules: readErrorRules(
              list.map((rule, index) => {
                const fields = isRecord(rule) ? { ...rule } : {};
                for (const key of ['match', 'match-regexr']) {
                  if (Array.isArray(fields[key]))
                    fields[key] = oauthMatchTerms(doc, [...root, channel, index, key], fields[key]);
                }
                return normalizeErrorRule(fields);
              })
            ),
          },
        ]
      : []
  );
}

export class InvalidOAuthRuleError extends Error {
  constructor(
    readonly channel: string,
    readonly ruleNumber: number
  ) {
    super(`Invalid OAuth rule ${ruleNumber} for ${channel}`);
    this.name = 'InvalidOAuthRuleError';
  }
}

export function invalidOAuthRule(groups: OAuthChannelRules[]) {
  for (const { channel, rules } of groups) {
    const index = rules.findIndex((rule) => validateErrorRule(rule, true));
    if (index !== -1) return new InvalidOAuthRuleError(channel, index + 1);
  }
}

export function writeOAuthErrorRules(
  doc: ReturnType<typeof parseDocument>,
  baseline: OAuthChannelRules[],
  desired: OAuthChannelRules[],
  sourceYaml: string,
  serverYaml: string,
  guard: boolean
) {
  const source = parseDocument(sourceYaml);
  const channels = new Set([...baseline, ...desired].map((group) => group.channel));
  const paths: string[][] = [];
  const latestYaml = doc.toString();
  for (const channel of channels) {
    const before = baseline.find((group) => group.channel === channel);
    const after = desired.find((group) => group.channel === channel);
    if (JSON.stringify(before) === JSON.stringify(after)) continue;
    const path = [...root, channel];
    paths.push(path);
    prepareParents(doc, Boolean(after));
    assertPortable(doc.getIn(path, true), path);
    if (!after) {
      if (doc.hasIn(path)) doc.deleteIn(path);
      continue;
    }
    // IDs refer to the retained source (the merged draft after recovery), not the latest list.
    // Move original nodes with their rules so reordering preserves extra fields and comments.
    prepareParents(source, false);
    const old = source.getIn(path, true);
    assertPortable(old, path);
    if (old !== undefined && !isSeq(old) && !(isScalar(old) && old.value === null))
      throw new ConfigDraftConflictError(path, 'unsafe_yaml_structure');
    const seq = isSeq(old) ? old : source.createNode([]);
    const originals = [...seq.items];
    const built = buildErrorRules(after.rules);
    seq.items = after.rules.map((rule, i) => {
      const node = rule.sourceIndex === undefined ? undefined : originals[rule.sourceIndex];
      const next = serializeErrorRule(built[i]);
      if (!rule.original) return source.createNode(next);
      if (!isMap(node)) {
        if (
          node !== undefined &&
          JSON.stringify(next) === JSON.stringify(serializeErrorRule(rule.original))
        )
          return node;
        throw new ConfigDraftConflictError(path, 'unsafe_yaml_structure');
      }
      const previous = serializeErrorRule(rule.original);
      for (const key of Object.keys(next) as (keyof typeof next)[]) {
        if (JSON.stringify(previous[key]) !== JSON.stringify(next[key])) {
          const prior = node.get(key, true);
          if (
            (key === 'match' || key === 'match-regexr') &&
            prior !== undefined &&
            (!isSeq(prior) || prior.items.some((item) => !isScalar(item) || item.value === null))
          )
            throw new ConfigDraftConflictError([...path, key], 'unsafe_yaml_structure');
          node.set(key, source.createNode(next[key]));
        }
      }
      return node;
    });
    doc.setIn(path, seq);
  }
  if (guard && paths.length)
    assertConfigListsUnchanged(serverYaml, doc.toString(), latestYaml, paths);
}
