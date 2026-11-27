import type { RequestScopedErrorRule } from '@/types/provider';

export const normalizeErrorRule = (rule: Record<string, unknown>): RequestScopedErrorRule => ({
  ...(typeof rule.status === 'number' ? { status: rule.status } : {}),
  ...(Array.isArray(rule.match)
    ? { match: rule.match.filter((v): v is string => typeof v === 'string') }
    : {}),
  ...(Array.isArray(rule['match-regexr'])
    ? { matchRegex: rule['match-regexr'].filter((v): v is string => typeof v === 'string') }
    : {}),
  ...(typeof rule.action === 'string'
    ? { action: rule.action as RequestScopedErrorRule['action'] }
    : {}),
});

export const serializeErrorRule = (rule: RequestScopedErrorRule) => ({
  ...(rule.status !== undefined ? { status: rule.status } : {}),
  ...(rule.match !== undefined ? { match: rule.match } : {}),
  ...(rule.matchRegex !== undefined ? { 'match-regexr': rule.matchRegex } : {}),
  ...(rule.action !== undefined ? { action: rule.action } : {}),
});
