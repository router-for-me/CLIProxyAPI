import type { RequestScopedErrorRule } from '@/types/provider';

export interface ErrorMatchDraft {
  id: string;
  kind: 'text' | 'regex';
  value: string;
}

export interface ErrorRuleDraft {
  id: string;
  status: string;
  action: string;
  matches: ErrorMatchDraft[];
  /** Preserve untouched optional fields and backend-supported action spelling. */
  original?: RequestScopedErrorRule;
}

export const ERROR_RULE_ACTIONS = [
  'stop',
  'stop-and-cooldown',
  'continue',
  'continue-and-cooldown',
] as const;

const isAction = (value: string): value is NonNullable<RequestScopedErrorRule['action']> =>
  ERROR_RULE_ACTIONS.some((action) => action === value);
const normalizedAction = (value: string | undefined) => value?.trim().toLowerCase() ?? '';

export const createErrorMatch = (id: string): ErrorMatchDraft => ({ id, kind: 'text', value: '' });

export const createErrorRule = (id: string): ErrorRuleDraft => ({
  id,
  status: '',
  action: 'continue',
  matches: [createErrorMatch(`${id}-initial-match`)],
});

/** Stable IDs keep repeated form initialization and dirty-state comparison deterministic. */
export function readErrorRules(rules: RequestScopedErrorRule[] = []): ErrorRuleDraft[] {
  return rules.map((rule, index) => {
    const id = `rule-${index}`;
    return {
      id,
      status: rule.status === undefined ? '' : String(rule.status),
      action: normalizedAction(rule.action),
      matches: [
        ...(rule.match ?? []).map((value, i): ErrorMatchDraft => ({
          id: `${id}-text-${i}`,
          kind: 'text',
          value,
        })),
        ...(rule.matchRegex ?? []).map((value, i): ErrorMatchDraft => ({
          id: `${id}-regex-${i}`,
          kind: 'regex',
          value,
        })),
      ],
      original: {
        ...rule,
        ...(rule.match !== undefined ? { match: [...rule.match] } : {}),
        ...(rule.matchRegex !== undefined ? { matchRegex: [...rule.matchRegex] } : {}),
      },
    };
  });
}

const patterns = (rule: ErrorRuleDraft, kind: ErrorMatchDraft['kind']) =>
  rule.matches.filter((match) => match.kind === kind).map((match) => match.value);
const equalPatterns = (left: string[], right: string[]) =>
  left.length === right.length && left.every((value, index) => value === right[index]);

function isUnchangedRule(rule: ErrorRuleDraft): boolean {
  const original = rule.original;
  return (
    original !== undefined &&
    rule.status === (original.status === undefined ? '' : String(original.status)) &&
    rule.action === normalizedAction(original.action) &&
    equalPatterns(patterns(rule, 'text'), original.match ?? []) &&
    equalPatterns(patterns(rule, 'regex'), original.matchRegex ?? [])
  );
}

export function isInvalidErrorMatch(rule: ErrorRuleDraft, match: ErrorMatchDraft): boolean {
  if (!['text', 'regex'].includes(match.kind)) return true;
  if (match.value.length > 0) return false;
  const original = match.kind === 'text' ? rule.original?.match : rule.original?.matchRegex;
  // Go ignores empty patterns. Preserve an existing empty condition when another
  // field changes, but reject newly added or newly cleared conditions.
  return !original?.some(
    (value, index) => value === '' && match.id === `${rule.id}-${match.kind}-${index}`
  );
}

export function validateErrorRule(rule: ErrorRuleDraft): string | null {
  // Existing inert rules are accepted by the backend; unrelated edits must not discard them.
  if (isUnchangedRule(rule)) return null;
  const status = rule.status.trim();
  if (!/^\d+$/.test(status) || !Number.isSafeInteger(Number(status)) || Number(status) <= 0) {
    return 'providersPage.errorRules.invalidStatus';
  }
  if (!isAction(normalizedAction(rule.action))) return 'providersPage.errorRules.invalidAction';
  if (!rule.matches.length) return 'providersPage.errorRules.missingMatch';
  if (rule.matches.some((match) => isInvalidErrorMatch(rule, match))) {
    return 'providersPage.errorRules.invalidMatch';
  }
  // Match strings are literal (including whitespace/newlines). Go, not JS, interprets regexes.
  return null;
}

export function buildErrorRules(rules: ErrorRuleDraft[]): RequestScopedErrorRule[] {
  return rules.map((rule) => {
    const error = validateErrorRule(rule);
    if (error) throw new Error(error);
    if (isUnchangedRule(rule)) return { ...rule.original! };
    const action = normalizedAction(rule.action);
    if (!isAction(action)) throw new Error('providersPage.errorRules.invalidAction');
    const match = patterns(rule, 'text');
    const matchRegex = patterns(rule, 'regex');
    return {
      status: Number(rule.status.trim()),
      action:
        rule.original && rule.action === normalizedAction(rule.original.action)
          ? rule.original.action
          : action,
      ...(match.length || rule.original?.match !== undefined ? { match } : {}),
      ...(matchRegex.length || rule.original?.matchRegex !== undefined ? { matchRegex } : {}),
    };
  });
}

export function moveErrorRule(
  rules: ErrorRuleDraft[],
  id: string,
  direction: -1 | 1
): ErrorRuleDraft[] {
  const index = rules.findIndex((rule) => rule.id === id);
  const target = index + direction;
  if (index < 0 || target < 0 || target >= rules.length) return rules;
  const next = [...rules];
  [next[index], next[target]] = [next[target], next[index]];
  return next;
}
