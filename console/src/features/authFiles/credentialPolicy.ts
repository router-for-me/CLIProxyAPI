import type { AuthFileFieldsPatch } from '@/services/api/authFiles';
import {
  applyCredentialPolicyValues,
  buildCredentialAliases,
  buildCredentialRules,
  readCredentialAliases,
  readCredentialPolicyValue,
  readCredentialRules,
  type CredentialAliasDraft,
} from '@/services/api/authFilePolicyAdapter';
import {
  buildErrorRules,
  readErrorRules,
  validateErrorRule,
  type ErrorRuleDraft,
} from '@/features/providers/errorRules';

export type PolicyMode = 'inherit' | 'custom';
export interface CredentialPolicyValues {
  requestRetry: { mode: PolicyMode; value: string };
  modelAliases: { mode: PolicyMode; rows: CredentialAliasDraft[] | null };
  errorRules: { mode: PolicyMode; rows: ErrorRuleDraft[] | null };
}
export type CredentialPolicyField = keyof CredentialPolicyValues;
export type CredentialPolicyValue = CredentialPolicyValues[CredentialPolicyField];
export type CredentialPolicyDraft = CredentialPolicyValues & {
  touched: Partial<Record<CredentialPolicyField, boolean>>;
};
export type CredentialPolicyError =
  | 'auth_files.policy_retry_invalid'
  | 'auth_files.policy_aliases_invalid'
  | 'auth_files.policy_rules_invalid';
const keys = {
  requestRetry: 'request_retry',
  modelAliases: 'model_aliases',
  errorRules: 'request_scoped_errors',
} as const;
const equal = (a: unknown, b: unknown) => JSON.stringify(a) === JSON.stringify(b);

export function readCredentialPolicy(json: Record<string, unknown>): CredentialPolicyDraft {
  const retry = readCredentialPolicyValue(json, keys.requestRetry);
  const retryNumber =
    typeof retry === 'number' || (typeof retry === 'string' && /^[+-]?\d+$/.test(retry.trim()))
      ? Number(retry)
      : null;
  const inheritsRetry = retry == null || (retryNumber !== null && retryNumber < 0);
  const aliases = readCredentialAliases(readCredentialPolicyValue(json, keys.modelAliases));
  const rules = readCredentialRules(readCredentialPolicyValue(json, keys.errorRules));
  return {
    requestRetry: {
      mode: inheritsRetry ? 'inherit' : 'custom',
      value: inheritsRetry ? '0' : String(retryNumber ?? retry),
    },
    modelAliases: { mode: aliases?.length === 0 ? 'inherit' : 'custom', rows: aliases },
    errorRules: {
      mode: rules?.length === 0 ? 'inherit' : 'custom',
      rows: rules === null ? null : readErrorRules(rules),
    },
    touched: {},
  };
}
export function credentialPolicyError(draft?: CredentialPolicyDraft): CredentialPolicyError | null {
  if (!draft) return null;
  const retry = draft.requestRetry;
  if (
    draft.touched.requestRetry &&
    retry.mode === 'custom' &&
    (!/^\d+$/.test(retry.value.trim()) || !Number.isSafeInteger(Number(retry.value)))
  )
    return 'auth_files.policy_retry_invalid';
  if (
    draft.touched.modelAliases &&
    draft.modelAliases.mode === 'custom' &&
    draft.modelAliases.rows?.some((row) => {
      if (row.original && equal(buildCredentialAliases([row])[0], row.original)) return false;
      return !row.name.trim() || !row.alias?.trim();
    })
  )
    return 'auth_files.policy_aliases_invalid';
  if (
    draft.touched.errorRules &&
    draft.errorRules.mode === 'custom' &&
    draft.errorRules.rows?.some(validateErrorRule)
  )
    return 'auth_files.policy_rules_invalid';
  return null;
}
export function buildCredentialPolicyPatch(
  original: Record<string, unknown>,
  draft?: CredentialPolicyDraft
): AuthFileFieldsPatch {
  const patch: Record<string, unknown> = {};
  if (!draft) return patch;
  const initial = readCredentialPolicy(original);
  const error = credentialPolicyError(draft);
  if (error) throw new Error(error);
  for (const field of Object.keys(keys) as CredentialPolicyField[]) {
    if (!draft.touched[field] || equal(draft[field], initial[field])) continue;
    let value: unknown;
    if (field === 'requestRetry')
      value = draft.requestRetry.mode === 'inherit' ? null : Number(draft.requestRetry.value);
    else if (draft[field].mode === 'inherit') value = [];
    else if (field === 'modelAliases') {
      if (draft.modelAliases.rows === null) continue;
      value = buildCredentialAliases(draft.modelAliases.rows);
    } else {
      if (draft.errorRules.rows === null) continue;
      value = buildCredentialRules(
        buildErrorRules(draft.errorRules.rows),
        draft.errorRules.rows.map((row) => row.original)
      );
    }
    if (
      !equal(
        value,
        readCredentialPolicyValue(original, keys[field]) ?? (field === 'requestRetry' ? null : [])
      )
    )
      patch[keys[field]] = value;
  }
  return patch as AuthFileFieldsPatch;
}
export function applyCredentialPolicyPatch(
  json: Record<string, unknown>,
  patch: AuthFileFieldsPatch
) {
  applyCredentialPolicyValues(json, patch);
}
