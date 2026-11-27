import type { ProviderKeyConfig } from '@/types';
import { normalizeProviderKeyConfig } from './transformers';

export const memberWireFields = {
  proxyUrl: 'proxy-url',
  prefix: 'prefix',
  priority: 'priority',
  headers: 'headers',
  models: 'models',
  excludedModelsText: 'excluded-models',
} as const;
export type MemberField = keyof typeof memberWireFields;
export type MemberMode = 'inherit' | 'empty' | 'override';
export type MemberModes = Record<MemberField, MemberMode>;

// Mirrors sharedKeyFields in config_v8.go. Weight and provider behavior flags are member-only.
const groupFields = [
  'name',
  'base-url',
  'priority',
  'prefix',
  'proxy-url',
  'headers',
  'models',
  'excluded-models',
  'disable-cooling',
  'request-retry',
  'request-scoped-errors',
  'connection-pool',
] as const;

/** The raw settings editor carries opaque nested metadata with each row. Only replace
 * editable fields; member credentials and unknown top-level data belong to the snapshot. */
export function replaceNativeGroupSettings(
  original: Record<string, unknown>,
  edited: Record<string, unknown>
) {
  const next = { ...original };
  for (const field of groupFields) {
    if (Object.prototype.hasOwnProperty.call(edited, field) && edited[field] !== undefined)
      next[field] = edited[field];
    else delete next[field];
  }
  return next;
}

export type NativeProviderGroupConfig = Pick<
  ProviderKeyConfig,
  | 'priority'
  | 'prefix'
  | 'proxyUrl'
  | 'headers'
  | 'models'
  | 'excludedModels'
  | 'disableCooling'
  | 'requestRetry'
  | 'requestScopedErrors'
  | 'inheritFields'
  | 'baseUrl'
  | 'connectionPool'
> & { name: string };

export function normalizeNativeProviderGroup(
  group: Record<string, unknown>
): NativeProviderGroupConfig {
  // Reuse the same normalization (including model source identities) for empty and populated groups.
  const policy = normalizeProviderKeyConfig({ ...group, 'api-key': 'group-defaults' })!;
  return {
    name: typeof group.name === 'string' ? group.name : '',
    baseUrl: policy.baseUrl,
    connectionPool: policy.connectionPool,
    priority: policy.priority,
    prefix: policy.prefix,
    proxyUrl: policy.proxyUrl,
    headers: policy.headers,
    models: policy.models,
    excludedModels: policy.excludedModels,
    disableCooling: policy.disableCooling,
    requestRetry: policy.requestRetry,
    requestScopedErrors: policy.requestScopedErrors,
  };
}

export function serializeNativeGroup(
  config: NativeProviderGroupConfig,
  serialize: (key: ProviderKeyConfig) => Record<string, unknown>
): Record<string, unknown> {
  const key = serialize({ ...config, apiKey: '' });
  return replaceNativeGroupSettings({}, { ...key, name: config.name.trim() });
}
