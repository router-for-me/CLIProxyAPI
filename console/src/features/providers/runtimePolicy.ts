import type { GeminiKeyConfig, OpenAIProviderConfig, ProviderKeyConfig } from '@/types/provider';
import {
  buildErrorRules,
  readErrorRules,
  validateErrorRule,
  type ErrorRuleDraft,
} from './errorRules';

export interface RuntimePolicyDraft {
  cooling: 'inherit' | 'enabled' | 'disabled';
  retry: string;
  errorsMode: 'inherit' | 'override';
  errorRules: ErrorRuleDraft[];
}

type RuntimePolicy = Pick<
  ProviderKeyConfig,
  'disableCooling' | 'requestRetry' | 'requestScopedErrors' | 'inheritFields'
>;

const isRecord = (value: unknown): value is Record<string, unknown> =>
  typeof value === 'object' && value !== null && !Array.isArray(value);

export function readRuntimePolicy(
  config?: ProviderKeyConfig | GeminiKeyConfig | OpenAIProviderConfig
): RuntimePolicyDraft {
  const source = config?.source;
  // A key inherits missing/null fields from its group. OpenAI policies live on the group itself.
  const keys = source?.group.keys;
  const raw = source
    ? source.keyIndex === undefined
      ? source.group
      : Array.isArray(keys) && isRecord(keys[source.keyIndex])
        ? keys[source.keyIndex]
        : {}
    : undefined;
  const cooling = raw ? raw['disable-cooling'] : config?.disableCooling;
  const retry = raw ? raw['request-retry'] : config?.requestRetry;
  const errors = raw ? raw['request-scoped-errors'] : config?.requestScopedErrors;

  return {
    cooling: typeof cooling === 'boolean' ? (cooling ? 'disabled' : 'enabled') : 'inherit',
    retry: typeof retry === 'number' ? String(retry) : '',
    errorsMode: errors == null ? 'inherit' : 'override',
    errorRules: readErrorRules(config?.requestScopedErrors),
  };
}

export function validateRuntimePolicy(
  draft: RuntimePolicyDraft,
  supportsErrors = true
): string | null {
  const retry = draft.retry.trim();
  if (retry && (!/^[+-]?\d+$/.test(retry) || !Number.isSafeInteger(Number(retry)))) {
    return 'providersPage.runtimePolicy.invalidRetry';
  }
  if (!supportsErrors || draft.errorsMode === 'inherit') return null;

  for (const rule of draft.errorRules) {
    const error = validateErrorRule(rule);
    if (error) return error;
  }
  return null;
}

/** Call validateRuntimePolicy before saving. Invalid drafts throw rather than silently lose rules. */
export function buildRuntimePolicy(
  draft: RuntimePolicyDraft,
  supportsErrors = true
): RuntimePolicy {
  const error = validateRuntimePolicy(draft, supportsErrors);
  if (error) throw new Error(error);
  const result: RuntimePolicy = {
    inheritFields: [],
    disableCooling: undefined,
    requestRetry: undefined,
    ...(supportsErrors ? { requestScopedErrors: undefined } : {}),
  };
  if (draft.cooling === 'inherit') result.inheritFields!.push('disable-cooling');
  else result.disableCooling = draft.cooling === 'disabled';
  if (!draft.retry.trim()) result.inheritFields!.push('request-retry');
  else result.requestRetry = Number(draft.retry.trim());
  if (supportsErrors) {
    if (draft.errorsMode === 'inherit') result.inheritFields!.push('request-scoped-errors');
    else result.requestScopedErrors = buildErrorRules(draft.errorRules);
  }
  return result;
}
