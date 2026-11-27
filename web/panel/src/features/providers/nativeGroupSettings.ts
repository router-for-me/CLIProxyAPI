import type { NativeProviderFamily } from '@/services/api/providers';
import { normalizeModelAliases } from '@/services/api/transformers';
import { serializeModelOptions } from '@/services/api/providerModels';
import { normalizeErrorRule, serializeErrorRule } from '@/services/api/errorRules';
import { isRecord } from '@/utils/helpers';
import { goDurationSeconds } from '@/features/config/visualConfigAdditions';
import { readModelOptions, buildModelOptions, validateModelOptions } from './modelOptions';
import { readRuntimePolicy, buildRuntimePolicy } from './runtimePolicy';
import { PROVIDER_DESCRIPTORS } from './descriptors';
import type { ModelEntryInput } from './types';

type Group = Readonly<Record<string, unknown>>;
const text = (v: unknown) => (typeof v === 'string' || typeof v === 'number' ? String(v) : '');
const same = (a: unknown, b: unknown) => JSON.stringify(a) === JSON.stringify(b);
const fail = (key: string): never => {
  throw new Error(key);
};

export function readNativeGroupSettings(group: Group) {
  const pool = group['connection-pool'];
  const block = isRecord(pool) ? pool : {};
  const errors = group['request-scoped-errors'];
  const models: ModelEntryInput[] = normalizeModelAliases(group.models).map((model) => ({
    sourceIndex: model.sourceIndex,
    name: model.name,
    alias: model.alias ?? '',
    thinkingJson: JSON.stringify(model.thinking),
    ...readModelOptions(model),
  }));
  return {
    name: text(group.name),
    baseUrl: text(group['base-url']),
    prefix: text(group.prefix),
    proxyUrl: text(group['proxy-url']),
    priority: text(group.priority),
    headers: group.headers == null ? '' : JSON.stringify(group.headers, null, 2),
    excludedModels: Array.isArray(group['excluded-models'])
      ? group['excluded-models'].join('\n')
      : '',
    models,
    policy: readRuntimePolicy({
      apiKey: '',
      source: { groupIndex: 0, group },
      requestScopedErrors: Array.isArray(errors)
        ? errors.filter(isRecord).map(normalizeErrorRule)
        : undefined,
    }),
    poolPresent: isRecord(pool),
    poolEnabled: typeof block.enabled === 'boolean' ? String(block.enabled) : '',
    poolTimeout: text(block['idle-conn-timeout']),
    poolLimit: text(block['max-idle-conns-per-host']),
  };
}

export type NativeGroupDraft = ReturnType<typeof readNativeGroupSettings>;
const integer = (value: string, key: string) => {
  if (!value.trim()) return undefined;
  if (!/^[+-]?\d+$/.test(value.trim()) || !Number.isSafeInteger(Number(value))) fail(key);
  return Number(value);
};
const set = (obj: Record<string, unknown>, key: string, value: unknown) => {
  if (value === undefined) delete obj[key];
  else obj[key] = value;
};

const modelFields = (
  entry: ModelEntryInput,
  family: NativeProviderFamily
): Record<string, unknown> => ({
  name: entry.name.trim(),
  alias: entry.alias?.trim() || undefined,
  ...serializeModelOptions(
    { name: entry.name, ...buildModelOptions(entry) },
    false,
    family === 'vertex'
  ),
});

/** Patch only changed defaults; never flatten member overrides into the group or vice versa. */
export function buildNativeGroupSettings(
  group: Group,
  draft: NativeGroupDraft,
  family: NativeProviderFamily = 'gemini'
) {
  const initial = readNativeGroupSettings(group);
  const out = { ...group };
  for (const [field, wire] of [
    ['name', 'name'],
    ['baseUrl', 'base-url'],
    ['prefix', 'prefix'],
    ['proxyUrl', 'proxy-url'],
  ] as const) {
    if (draft[field] !== initial[field]) set(out, wire, draft[field].trim() || undefined);
  }
  if (draft.priority !== initial.priority)
    set(out, 'priority', integer(draft.priority, 'plugin_management.invalid_priority'));
  if (draft.headers !== initial.headers) {
    let value: unknown;
    if (draft.headers.trim()) {
      try {
        value = JSON.parse(draft.headers);
      } catch {
        fail('auth_files.headers_invalid_json');
      }
      if (!isRecord(value)) return fail('auth_files.headers_invalid_object');
      if (Object.values(value).some((v) => typeof v !== 'string'))
        fail('auth_files.headers_invalid_value');
    }
    set(out, 'headers', value);
  }
  if (draft.excludedModels !== initial.excludedModels) {
    set(
      out,
      'excluded-models',
      draft.excludedModels
        .split('\n')
        .map((v) => v.trim())
        .filter(Boolean)
    );
  }
  if (!same(draft.models, initial.models)) {
    const error = validateModelOptions(draft.models);
    if (error) fail(error);
    const originals = new Map(initial.models.map((m) => [m.sourceIndex, m]));
    out.models = draft.models.map((entry) => {
      if (!entry.name.trim()) return fail('providersPage.nativeGroup.modelNameRequired');
      const previous = entry.sourceIndex == null ? undefined : originals.get(entry.sourceIndex);
      const raw =
        entry.sourceIndex != null && Array.isArray(group.models)
          ? group.models[entry.sourceIndex]
          : undefined;
      const result = isRecord(raw) ? { ...raw } : {};
      const before = previous ? modelFields(previous, family) : {};
      const after = modelFields(entry, family);
      for (const k of new Set([...Object.keys(before), ...Object.keys(after)])) {
        if (!same(after[k], before[k]) || !isRecord(raw)) set(result, k, after[k]);
      }
      return result;
    });
  }
  const policy = draft.policy;
  if (!same(policy, initial.policy)) {
    const supported = PROVIDER_DESCRIPTORS[family].supportsRequestScopedErrors;
    const next = buildRuntimePolicy(policy, supported);
    if (policy.cooling !== initial.policy.cooling) set(out, 'disable-cooling', next.disableCooling);
    if (policy.retry !== initial.policy.retry) set(out, 'request-retry', next.requestRetry);
    if (
      supported &&
      (policy.errorsMode !== initial.policy.errorsMode ||
        !same(policy.errorRules, initial.policy.errorRules))
    ) {
      set(out, 'request-scoped-errors', next.requestScopedErrors?.map(serializeErrorRule));
    }
  }
  if (draft.poolPresent) {
    const original = group['connection-pool'];
    const pool = isRecord(original) ? { ...original } : {};
    if (draft.poolEnabled !== initial.poolEnabled)
      set(pool, 'enabled', draft.poolEnabled ? draft.poolEnabled === 'true' : undefined);
    if (draft.poolTimeout !== initial.poolTimeout) {
      const timeout = draft.poolTimeout.trim();
      if (timeout && goDurationSeconds(timeout) === undefined)
        fail('config_management.visual.validation.invalid_duration');
      set(pool, 'idle-conn-timeout', timeout || undefined);
    }
    if (draft.poolLimit !== initial.poolLimit)
      set(
        pool,
        'max-idle-conns-per-host',
        integer(draft.poolLimit, 'providersPage.nativeGroup.invalidLimit')
      );
    out['connection-pool'] = pool;
  } else if (initial.poolPresent) delete out['connection-pool'];
  return out;
}
