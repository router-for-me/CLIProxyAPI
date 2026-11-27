/**
 * Routing board API.
 *
 * Normalizes the stored v8 configuration and the credential list into routing members, and
 * serializes a routing plan back. There is no backend transaction across credentials and
 * configuration: credentials are written one by one first (each can be retried on its own), then
 * every config-side change goes out as a single `PATCH /config` built against a fresh document.
 */

import { apiClient } from './client';
import { authFilesApi } from './authFiles';
import { guardConfigConnection } from './configValue';
import type { AuthFileItem } from '@/types/authFile';
import {
  ROUTING_COMPAT_FAMILY,
  ROUTING_KEY_FAMILIES,
  type RoutingBoard,
  type RoutingKeyFamily,
  type RoutingMember,
  type RoutingPlan,
  type RoutingSettings,
  type RoutingStrategy,
} from '@/types/routing';
import { maskApiKey } from '@/utils/format';
import { isRecord } from '@/utils/helpers';

const MODEL_REQUEST_CONCURRENCY = 4;

/** CPA disables a config API key by excluding every model. */
const DISABLE_ALL_MODELS_RULE = '*';

/** Built-in model channel CPA registers for a config API key without its own model list. */
const KEY_FAMILY_CHANNELS: Record<RoutingKeyFamily, string> = {
  gemini: 'gemini',
  interactions: 'gemini-interactions',
  codex: 'codex',
  meta: 'meta',
  xai: 'xai',
  claude: 'claude',
  vertex: 'vertex',
};

export class RoutingConflictError extends Error {
  constructor() {
    super('The configuration changed since the board was loaded; refresh and try again.');
    this.name = 'RoutingConflictError';
  }
}

/* ---------------------------------------------------------------------------------------------
 * Reading
 * ------------------------------------------------------------------------------------------- */

const readInteger = (value: unknown): number | null => {
  if (typeof value === 'number' && Number.isInteger(value)) return value;
  if (typeof value === 'string' && /^[+-]?\d+$/.test(value.trim())) return Number(value.trim());
  return null;
};

const readText = (value: unknown): string => (typeof value === 'string' ? value.trim() : '');

const readStringList = (value: unknown): string[] =>
  Array.isArray(value) ? value.map(readText).filter(Boolean) : [];

const readPath = (root: unknown, path: readonly string[]): unknown =>
  path.reduce<unknown>((value, key) => (isRecord(value) ? value[key] : undefined), root);

/** Configured model entries are callable by their alias, or their name when no alias is set. */
const readModelIds = (value: unknown): string[] => {
  if (!Array.isArray(value)) return [];
  const seen = new Set<string>();
  const ids: string[] = [];
  for (const entry of value) {
    if (!isRecord(entry)) continue;
    const id = readText(entry.alias) || readText(entry.name);
    if (id && !seen.has(id.toLowerCase())) {
      seen.add(id.toLowerCase());
      ids.push(id);
    }
  }
  return ids;
};

const readHost = (value: unknown): string => {
  const text = readText(value);
  if (!text) return '';
  try {
    return new URL(text).host || text;
  } catch {
    return text;
  }
};

export function normalizeRoutingStrategy(value: unknown): RoutingStrategy {
  const text = readText(value)
    .toLowerCase()
    .replace(/[\s_]+/g, '-');
  if (['weighted-round-robin', 'weightedroundrobin', 'wrr'].includes(text)) {
    return 'weighted-round-robin';
  }
  if (['fill-first', 'fillfirst', 'ff'].includes(text)) return 'fill-first';
  // CPA runs an unrecognised strategy as round-robin.
  return 'round-robin';
}

export function readRoutingSettings(raw: unknown): RoutingSettings {
  const routing = readPath(raw, ['routing']);
  return {
    strategy: normalizeRoutingStrategy(readPath(routing, ['strategy'])),
    sessionAffinity: readPath(routing, ['session-affinity']) === true,
    requestRetry: Math.max(0, readInteger(readPath(routing, ['retry', 'request-retry'])) ?? 0),
    maxRetryCredentials: Math.max(
      0,
      readInteger(readPath(routing, ['retry', 'max-retry-credentials'])) ?? 0
    ),
  };
}

const readGroups = (raw: unknown, family: string): unknown[] => {
  const groups = readPath(raw, ['api-keys', family]);
  return Array.isArray(groups) ? groups : [];
};

function keyMembers(raw: unknown, family: RoutingKeyFamily): RoutingMember[] {
  const members: RoutingMember[] = [];
  readGroups(raw, family).forEach((group, groupIndex) => {
    if (!isRecord(group) || !Array.isArray(group.keys)) return;
    group.keys.forEach((key, keyIndex) => {
      if (!isRecord(key)) return;
      // Group fields are shared; a non-null key field overrides them (as CPA expands v8 groups).
      const effective: Record<string, unknown> = { ...group };
      for (const [field, value] of Object.entries(key)) {
        if (value !== null) effective[field] = value;
      }
      const apiKey = readText(key['api-key']);
      const excludedModels = readStringList(effective['excluded-models']);
      const models = readModelIds(effective.models);
      const groupName = readText(group.name);
      const host = readHost(group['base-url']);
      const masked = maskApiKey(apiKey);
      members.push({
        key: `key:${family}:${groupIndex}:${keyIndex}`,
        kind: 'apiKey',
        family,
        name: groupName || host || family,
        detail: [host && host !== groupName ? host : '', masked].filter(Boolean).join(' · '),
        priority: readInteger(effective.priority) ?? 0,
        weights: [{ index: 0, weight: readInteger(key.weight), label: masked }],
        keyCount: 1,
        disabled: excludedModels.includes(DISABLE_ALL_MODELS_RULE),
        locked: false,
        prefix: readText(effective.prefix),
        excludedModels,
        modelSource:
          models.length > 0
            ? { type: 'list', models }
            : { type: 'channel', channel: KEY_FAMILY_CHANNELS[family] },
        unavailable: false,
        cooldowns: null,
        location: { kind: 'apiKey', family, groupIndex, keyIndex, apiKey },
      });
    });
  });
  return members;
}

function compatMembers(raw: unknown): RoutingMember[] {
  const members: RoutingMember[] = [];
  readGroups(raw, ROUTING_COMPAT_FAMILY).forEach((group, groupIndex) => {
    if (!isRecord(group)) return;
    const keys = Array.isArray(group.keys) ? group.keys.filter(isRecord) : [];
    const name = readText(group.name);
    const models = readModelIds(group.models);
    members.push({
      key: `compat:${groupIndex}`,
      kind: 'compat',
      family: ROUTING_COMPAT_FAMILY,
      name: name || `#${groupIndex + 1}`,
      detail: readHost(group['base-url']),
      priority: readInteger(group.priority) ?? 0,
      weights: keys.map((key, index) => ({
        index,
        weight: readInteger(key.weight),
        label: maskApiKey(readText(key['api-key'])) || `#${index + 1}`,
      })),
      // A keyless provider still schedules once.
      keyCount: Math.max(1, keys.length),
      disabled: group.disabled === true,
      locked: false,
      prefix: readText(group.prefix),
      excludedModels: [],
      modelSource: models.length > 0 ? { type: 'list', models } : { type: 'none' },
      unavailable: false,
      cooldowns: null,
      location: { kind: 'compat', groupIndex, name },
    });
  });
  return members;
}

const isTruthyFlag = (value: unknown): boolean =>
  value === true || (typeof value === 'string' && value.trim().toLowerCase() === 'true');

const stripJsonExtension = (name: string): string =>
  name.length > 5 && name.toLowerCase().endsWith('.json') ? name.slice(0, -5) : name;

function credentialMember(file: AuthFileItem): RoutingMember | null {
  const name = readText(file.name);
  if (!name) return null;
  // Identity fallback matches the credential page: email, then project, then the file name.
  // Never show account fields: API-key credentials may expose the key through them.
  const base = stripJsonExtension(name);
  const primary = readText(file.email) || readText(file.projectId) || base;
  return {
    key: `cred:${name}`,
    kind: 'credential',
    family: readText(file.type) || readText(file.provider) || 'unknown',
    name: primary,
    detail: primary.toLowerCase() === base.toLowerCase() ? '' : base,
    priority: readInteger(file.priority) ?? 0,
    weights: [{ index: 0, weight: readInteger(file.weight), label: primary }],
    keyCount: 1,
    disabled: file.disabled === true,
    locked: isTruthyFlag(file.runtimeOnly),
    prefix: readText(file.prefix),
    excludedModels: [],
    modelSource: { type: 'credential', name },
    unavailable: file.unavailable === true,
    cooldowns: file.cooldownSnapshot ?? null,
    location: { kind: 'credential', name },
  };
}

/** Every schedulable member of the stored configuration and the credential list. */
export function buildRoutingBoard(raw: unknown, files: readonly AuthFileItem[]): RoutingBoard {
  return {
    members: [
      ...ROUTING_KEY_FAMILIES.flatMap((family) => keyMembers(raw, family)),
      ...compatMembers(raw),
      ...files.flatMap((file) => credentialMember(file) ?? []),
    ],
    settings: readRoutingSettings(raw),
    forceModelPrefix: readPath(raw, ['routing', 'force-model-prefix']) === true,
  };
}

/* ---------------------------------------------------------------------------------------------
 * Writing
 * ------------------------------------------------------------------------------------------- */

export interface CredentialWrite {
  key: string;
  name: string;
  fields: { priority?: number; weight?: number | null };
}

export function credentialWrites(board: RoutingBoard, plan: RoutingPlan): CredentialWrite[] {
  return board.members.flatMap((member) => {
    if (member.location.kind !== 'credential') return [];
    const fields: CredentialWrite['fields'] = {};
    if (plan.priorities[member.key] !== undefined) fields.priority = plan.priorities[member.key];
    if (plan.weights[member.key]) fields.weight = plan.weights[member.key][0] ?? null;
    return Object.keys(fields).length
      ? [{ key: member.key, name: member.location.name, fields }]
      : [];
  });
}

const cloneJson = <T>(value: T): T => JSON.parse(JSON.stringify(value)) as T;

/** Response metadata the backend injects on read and ignores on write. */
const stripAuthIndex = (value: Record<string, unknown>) => {
  delete value.auth_index;
  delete value['auth-index'];
};

const setWeight = (key: Record<string, unknown>, weight: number | null) => {
  if (weight === null) delete key.weight;
  else key.weight = weight;
};

/**
 * The single `PATCH /config` body for every config-side change, built against the latest
 * document. The backend replaces lists whole, so each touched family is sent complete; a
 * position whose key or provider name no longer matches is a conflict, not a guess.
 * Returns null when nothing config-side changed.
 */
export function buildRoutingConfigPatch(
  latestRaw: unknown,
  board: RoutingBoard,
  plan: RoutingPlan
): Record<string, unknown> | null {
  const patch: Record<string, unknown> = {};

  const routing: Record<string, unknown> = {};
  const retry: Record<string, unknown> = {};
  if (plan.settings.strategy !== undefined) routing.strategy = plan.settings.strategy;
  if (plan.settings.sessionAffinity !== undefined) {
    routing['session-affinity'] = plan.settings.sessionAffinity;
  }
  if (plan.settings.requestRetry !== undefined) retry['request-retry'] = plan.settings.requestRetry;
  if (plan.settings.maxRetryCredentials !== undefined) {
    retry['max-retry-credentials'] = plan.settings.maxRetryCredentials;
  }
  if (Object.keys(retry).length) routing.retry = retry;
  if (Object.keys(routing).length) patch.routing = routing;

  const families = new Map<string, Record<string, unknown>[]>();
  const familyGroups = (family: string): Record<string, unknown>[] => {
    let groups = families.get(family);
    if (!groups) {
      const latest = readGroups(latestRaw, family);
      if (!latest.every(isRecord)) throw new RoutingConflictError();
      groups = cloneJson(latest as Record<string, unknown>[]);
      families.set(family, groups);
    }
    return groups;
  };

  for (const member of board.members) {
    const priority = plan.priorities[member.key];
    const weights = plan.weights[member.key];
    if (priority === undefined && !weights) continue;
    const location = member.location;
    if (location.kind === 'apiKey') {
      const group = familyGroups(location.family)[location.groupIndex];
      const keys = isRecord(group) && Array.isArray(group.keys) ? group.keys : null;
      const key = keys?.[location.keyIndex];
      if (!isRecord(key) || readText(key['api-key']) !== location.apiKey) {
        throw new RoutingConflictError();
      }
      // A key-level priority overrides the group's shared one for this key only.
      if (priority !== undefined) key.priority = priority;
      if (weights) setWeight(key, weights[0] ?? null);
    } else if (location.kind === 'compat') {
      const group = familyGroups(ROUTING_COMPAT_FAMILY)[location.groupIndex];
      if (!isRecord(group) || readText(group.name) !== location.name) {
        throw new RoutingConflictError();
      }
      if (priority !== undefined) group.priority = priority;
      if (weights) {
        const keys = (Array.isArray(group.keys) ? group.keys : []).filter(isRecord);
        if (keys.length !== member.weights.length) throw new RoutingConflictError();
        keys.forEach((key, index) => setWeight(key, weights[index] ?? null));
      }
    }
  }

  if (families.size) {
    const apiKeys: Record<string, unknown> = {};
    for (const [family, groups] of families) {
      for (const group of groups) {
        stripAuthIndex(group);
        if (Array.isArray(group.keys)) group.keys.filter(isRecord).forEach(stripAuthIndex);
      }
      apiKeys[family] = groups;
    }
    patch['api-keys'] = apiKeys;
  }

  return Object.keys(patch).length ? patch : null;
}

/* ---------------------------------------------------------------------------------------------
 * Requests
 * ------------------------------------------------------------------------------------------- */

export interface RoutingApplyResult {
  /** Member keys (and 'settings') whose changes were written. */
  applied: string[];
  /** The first write that failed: a member key, or 'config' for the configuration patch. */
  failed: { key: string; error: unknown } | null;
  /** Writes skipped because an earlier one failed. */
  notAttempted: string[];
}

const modelIds = (models: { id?: unknown }[]): string[] =>
  models.flatMap((model) =>
    typeof model.id === 'string' && model.id.trim() ? [model.id.trim()] : []
  );

async function forEachWithConcurrency<T>(
  items: readonly string[],
  task: (item: string) => Promise<T>,
  onResult: (item: string, result: T | null) => void
): Promise<void> {
  let next = 0;
  const worker = async () => {
    while (next < items.length) {
      const item = items[next];
      next += 1;
      let result: T | null;
      try {
        result = await task(item);
      } catch {
        result = null;
      }
      onResult(item, result);
    }
  };
  await Promise.all(
    Array.from({ length: Math.min(MODEL_REQUEST_CONCURRENCY, items.length) }, worker)
  );
}

export const routingApi = {
  /** The stored v8 configuration document, unnormalized. */
  getRawConfig: () => apiClient.get<unknown>('/config'),

  /** The credentials the board shows next to the stored configuration. */
  async listCredentials(): Promise<AuthFileItem[]> {
    const response = await authFilesApi.list();
    return Array.isArray(response.files) ? response.files : [];
  },

  /** Built-in model ids per channel; a channel that cannot be read reports null. */
  loadChannelModels: (
    channels: readonly string[],
    onResult: (channel: string, models: string[] | null) => void
  ) =>
    forEachWithConcurrency(
      channels,
      async (channel) => modelIds(await authFilesApi.getModelDefinitions(channel)),
      onResult
    ),

  /** Live registry model ids per credential; a credential that cannot be read reports null. */
  loadCredentialModels: (
    names: readonly string[],
    onResult: (name: string, models: string[] | null) => void
  ) =>
    forEachWithConcurrency(
      names,
      async (name) => modelIds(await authFilesApi.getModelsForAuthFile(name)),
      onResult
    ),

  async applyPlan(board: RoutingBoard, plan: RoutingPlan): Promise<RoutingApplyResult> {
    const assertConnection = guardConfigConnection();
    const writes = credentialWrites(board, plan);
    const credentialKeys = new Set(writes.map((write) => write.key));
    const configKeys = Object.keys({ ...plan.priorities, ...plan.weights }).filter(
      (key) => !credentialKeys.has(key)
    );
    const configTargets = [
      ...configKeys,
      ...(Object.keys(plan.settings).length ? ['settings'] : []),
    ];
    const result: RoutingApplyResult = { applied: [], failed: null, notAttempted: [] };

    for (let index = 0; index < writes.length; index += 1) {
      const write = writes[index];
      assertConnection();
      try {
        await authFilesApi.patchFields(write.name, write.fields);
        result.applied.push(write.key);
      } catch (error) {
        result.failed = { key: write.key, error };
        result.notAttempted = [
          ...writes.slice(index + 1).map((item) => item.key),
          ...configTargets,
        ];
        return result;
      }
    }

    if (configTargets.length === 0) return result;
    try {
      assertConnection();
      const latest = await routingApi.getRawConfig();
      assertConnection();
      const patch = buildRoutingConfigPatch(latest, board, plan);
      if (patch) await apiClient.patch('/config', patch);
      result.applied.push(...configTargets);
    } catch (error) {
      result.failed = { key: 'config', error };
      result.notAttempted = configTargets;
    }
    return result;
  },
};
