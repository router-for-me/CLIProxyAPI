import { useCallback, useMemo, useReducer } from 'react';
import { isMap, isScalar, isSeq, parse as parseYaml, parseDocument } from 'yaml';
import type { Node, Pair, YAMLMap, YAMLSeq } from 'yaml';
import type {
  DisableImageGenerationMode,
  PluginStoreAuthApplyTo,
  PluginStoreAuthRule,
  PluginStoreAuthType,
  PayloadFilterRule,
  PayloadHeaderEntry,
  PayloadParamEntry,
  PayloadParamValueType,
  PayloadRule,
  RoutingStrategy,
  VisualConfigValues,
  VisualConfigValidationErrors,
  PayloadParamValidationErrorCode,
} from '@/types/visualConfig';
import { DEFAULT_VISUAL_VALUES } from '@/types/visualConfig';
import { assertConfigListsUnchanged, ConfigDraftConflictError } from '@/services/api/configPatch';
import {
  ADDITION_FIELDS,
  ICE_KEY,
  readVisualAdditions,
  writeVisualAdditions,
  writeICEServers,
  validateVisualAdditions,
} from '@/features/config/visualConfigAdditions';

import {
  SERVER_FIELDS,
  readVisualServer,
  writeVisualServer,
  validateVisualServer,
} from '@/features/config/visualConfigServer';

function asRecord(value: unknown): Record<string, unknown> | null {
  if (value === null || typeof value !== 'object' || Array.isArray(value)) return null;
  return value as Record<string, unknown>;
}

function parseApiKeysText(raw: unknown): string {
  if (!Array.isArray(raw)) return '';

  const keys: string[] = [];
  for (const item of raw) {
    const key = typeof item === 'string' ? item.trim() : '';
    if (key) keys.push(key);
  }
  return keys.join('\n');
}

type YamlDocument = ReturnType<typeof parseDocument>;
type YamlPath = string[];

function docHas(doc: YamlDocument, path: YamlPath): boolean {
  return doc.hasIn(path);
}

function ensureMapInDoc(doc: YamlDocument, path: YamlPath): void {
  const existing = doc.getIn(path, true);
  if (isMap(existing)) return;
  // Use a YAML node here; plain objects are not treated as collections by subsequent `setIn`.
  doc.setIn(path, doc.createNode({}));
}

function deleteIfMapEmpty(doc: YamlDocument, path: YamlPath): void {
  const value = doc.getIn(path, true);
  if (!isMap(value)) return;
  if (value.items.length === 0) deletePathInDoc(doc, path);
}

/** Prune only ancestors made empty by this deletion; keep unknown sibling nodes. */
function deletePathInDoc(doc: YamlDocument, path: YamlPath): void {
  doc.deleteIn(path);
  if (path.length > 1) deleteIfMapEmpty(doc, path.slice(0, -1));
}

function setBooleanInDoc(doc: YamlDocument, path: YamlPath, value: boolean): void {
  // Callers only write dirty fields. Explicit false must override backend defaults
  // even when the original document omitted the key (for example, ws-auth).
  doc.setIn(path, value);
}

function setStringInDoc(doc: YamlDocument, path: YamlPath, value: unknown): void {
  const safe = typeof value === 'string' ? value : '';
  const trimmed = safe.trim();
  if (trimmed !== '') {
    doc.setIn(path, safe);
    return;
  }
  // Preserve existing empty-string keys to avoid dropping template blocks/comments.
  // Only keep the key when it already exists in the YAML.
  if (docHas(doc, path)) {
    doc.setIn(path, '');
  }
}

function setStringListInDoc(doc: YamlDocument, path: YamlPath, values: string[]): void {
  const nextValues = values.map((value) => value.trim()).filter(Boolean);
  if (nextValues.length > 0) {
    doc.setIn(path, nextValues);
    return;
  }
  if (docHas(doc, path)) deletePathInDoc(doc, path);
}

function setIntFromStringInDoc(doc: YamlDocument, path: YamlPath, value: unknown): void {
  const safe = typeof value === 'string' ? value : '';
  const trimmed = safe.trim();
  if (trimmed === '') {
    if (docHas(doc, path)) deletePathInDoc(doc, path);
    return;
  }

  if (!/^-?\d+$/.test(trimmed)) {
    return;
  }

  const parsed = Number(trimmed);
  if (Number.isSafeInteger(parsed)) {
    doc.setIn(path, parsed);
    return;
  }
}

function setDisableImageGenerationInDoc(
  doc: YamlDocument,
  path: YamlPath,
  value: DisableImageGenerationMode
): void {
  if (value === 'chat' || value === 'passthrough') {
    doc.setIn(path, value);
    return;
  }

  if (value === 'true') {
    doc.setIn(path, true);
    return;
  }

  if (docHas(doc, path)) doc.setIn(path, false);
}

const PAYLOAD_DIRTY_FIELDS = [
  'payloadDefaultRules',
  'payloadDefaultRawRules',
  'payloadOverrideRules',
  'payloadOverrideRawRules',
  'payloadFilterRules',
] as const;

const PAYLOAD_SECTIONS = ['default', 'default-raw', 'override', 'override-raw', 'filter'] as const;

function hasPayloadDirtyFields(dirtyFields: Set<string>): boolean {
  return PAYLOAD_DIRTY_FIELDS.some((field) => dirtyFields.has(field));
}

function getIntegerError(value: string): 'integer' | undefined {
  const trimmed = value.trim();
  if (!trimmed) return undefined;
  return /^-?\d+$/.test(trimmed) && Number.isSafeInteger(Number(trimmed)) ? undefined : 'integer';
}

function getNonNegativeIntegerError(value: string): 'non_negative_integer' | undefined {
  if (getIntegerError(value)) return 'non_negative_integer';
  return Number(value.trim()) >= 0 ? undefined : 'non_negative_integer';
}

function getPortError(value: string): 'port_range' | undefined {
  const trimmed = value.trim();
  if (!trimmed) return undefined;
  if (!/^\d+$/.test(trimmed)) return 'port_range';
  const parsed = Number(trimmed);
  return parsed >= 1 && parsed <= 65535 ? undefined : 'port_range';
}

function getRedisRetentionError(value: string): 'integer_range_1_3600' | undefined {
  const trimmed = value.trim();
  if (!trimmed) return undefined;
  if (!/^\d+$/.test(trimmed)) return 'integer_range_1_3600';
  const parsed = Number(trimmed);
  return parsed >= 1 && parsed <= 3600 ? undefined : 'integer_range_1_3600';
}

export function getVisualConfigValidationErrors(
  values: VisualConfigValues,
  dirtyFields?: ReadonlySet<string>
): VisualConfigValidationErrors {
  return {
    ...validateVisualAdditions(values, dirtyFields),
    ...validateVisualServer(values),
    port: getPortError(values.port),
    errorLogsMaxFiles: getNonNegativeIntegerError(values.errorLogsMaxFiles),
    logsMaxTotalSizeMb: getNonNegativeIntegerError(values.logsMaxTotalSizeMb),
    redisUsageQueueRetentionSeconds: getRedisRetentionError(values.redisUsageQueueRetentionSeconds),
    requestRetry: getNonNegativeIntegerError(values.requestRetry),
    maxRetryCredentials: getIntegerError(values.maxRetryCredentials),
    maxRetryInterval: getIntegerError(values.maxRetryInterval),
    authAutoRefreshWorkers: getIntegerError(values.authAutoRefreshWorkers),
    'streaming.keepaliveSeconds': getIntegerError(values.streaming.keepaliveSeconds),
    'streaming.bootstrapRetries': getIntegerError(values.streaming.bootstrapRetries),
    'streaming.nonstreamKeepaliveInterval': getIntegerError(
      values.streaming.nonstreamKeepaliveInterval
    ),
  };
}

export function getPayloadParamValidationError(
  param: PayloadParamEntry
): PayloadParamValidationErrorCode | undefined {
  const trimmedValue = param.value.trim();

  switch (param.valueType) {
    case 'number': {
      if (!trimmedValue) return 'payload_invalid_number';
      const parsed = Number(trimmedValue);
      return Number.isFinite(parsed) ? undefined : 'payload_invalid_number';
    }
    case 'boolean': {
      const normalized = trimmedValue.toLowerCase();
      return normalized === 'true' || normalized === 'false'
        ? undefined
        : 'payload_invalid_boolean';
    }
    case 'json': {
      if (!trimmedValue) return 'payload_invalid_json';
      try {
        JSON.parse(param.value);
        return undefined;
      } catch {
        return 'payload_invalid_json';
      }
    }
    default:
      return undefined;
  }
}

function hasPayloadParamValidationErrors(rules: PayloadRule[]): boolean {
  return rules.some(
    (rule) =>
      rule.params.some((param) => Boolean(getPayloadParamValidationError(param))) ||
      rule.models.some(
        (model) =>
          (model.match ?? []).some((param) => Boolean(getPayloadParamValidationError(param))) ||
          (model.notMatch ?? []).some((param) => Boolean(getPayloadParamValidationError(param)))
      )
  );
}

function deepClone<T>(value: T): T {
  if (typeof structuredClone === 'function') return structuredClone(value);
  return JSON.parse(JSON.stringify(value)) as T;
}

function arePayloadModelEntriesEqual(
  left: PayloadRule['models'],
  right: PayloadRule['models']
): boolean {
  if (left === right) return true;
  if (left.length !== right.length) return false;
  for (let i = 0; i < left.length; i += 1) {
    const a = left[i];
    const b = right[i];
    if (!a || !b) return false;
    if (
      a.id !== b.id ||
      a.name !== b.name ||
      a.protocol !== b.protocol ||
      a.fromProtocol !== b.fromProtocol
    ) {
      return false;
    }
    if (!arePayloadHeaderEntriesEqual(a.headers, b.headers)) return false;
    if (!arePayloadParamEntriesEqual(a.match ?? [], b.match ?? [])) return false;
    if (!arePayloadParamEntriesEqual(a.notMatch ?? [], b.notMatch ?? [])) return false;
    if (!areStringArraysEqual(a.exist, b.exist)) return false;
    if (!areStringArraysEqual(a.notExist, b.notExist)) return false;
  }
  return true;
}

function arePayloadParamEntriesEqual(
  left: PayloadRule['params'],
  right: PayloadRule['params']
): boolean {
  if (left === right) return true;
  if (left.length !== right.length) return false;
  for (let i = 0; i < left.length; i += 1) {
    const a = left[i];
    const b = right[i];
    if (!a || !b) return false;
    if (a.id !== b.id || a.path !== b.path || a.valueType !== b.valueType || a.value !== b.value) {
      return false;
    }
  }
  return true;
}

function arePayloadHeaderEntriesEqual(
  left: PayloadHeaderEntry[] | undefined,
  right: PayloadHeaderEntry[] | undefined
): boolean {
  const leftEntries = left ?? [];
  const rightEntries = right ?? [];
  if (leftEntries === rightEntries) return true;
  if (leftEntries.length !== rightEntries.length) return false;
  for (let i = 0; i < leftEntries.length; i += 1) {
    const a = leftEntries[i];
    const b = rightEntries[i];
    if (!a || !b) return false;
    if (a.id !== b.id || a.name !== b.name || a.value !== b.value) return false;
  }
  return true;
}

function areStringArraysEqual(left: string[] | undefined, right: string[] | undefined): boolean {
  const leftItems = left ?? [];
  const rightItems = right ?? [];
  if (leftItems === rightItems) return true;
  if (leftItems.length !== rightItems.length) return false;
  for (let i = 0; i < leftItems.length; i += 1) {
    if (leftItems[i] !== rightItems[i]) return false;
  }
  return true;
}

function arePluginStoreAuthRulesEqual(
  left: PluginStoreAuthRule[] | undefined,
  right: PluginStoreAuthRule[] | undefined
): boolean {
  const leftItems = left ?? [];
  const rightItems = right ?? [];
  if (leftItems === rightItems) return true;
  if (leftItems.length !== rightItems.length) return false;
  for (let i = 0; i < leftItems.length; i += 1) {
    const a = leftItems[i];
    const b = rightItems[i];
    if (!a || !b) return false;
    if (
      a.match !== b.match ||
      a.type !== b.type ||
      a.tokenEnv !== b.tokenEnv ||
      a.usernameEnv !== b.usernameEnv ||
      a.passwordEnv !== b.passwordEnv ||
      a.headerName !== b.headerName ||
      a.headerValueEnv !== b.headerValueEnv ||
      a.allowInsecure !== b.allowInsecure
    ) {
      return false;
    }
    if (!areStringArraysEqual(a.applyTo, b.applyTo)) return false;
  }
  return true;
}

function arePayloadRulesEqual(left: PayloadRule[], right: PayloadRule[]): boolean {
  if (left === right) return true;
  if (left.length !== right.length) return false;
  for (let i = 0; i < left.length; i += 1) {
    const a = left[i];
    const b = right[i];
    if (!a || !b) return false;
    if (a.id !== b.id) return false;
    if (!arePayloadModelEntriesEqual(a.models, b.models)) return false;
    if (!arePayloadParamEntriesEqual(a.params, b.params)) return false;
  }
  return true;
}

function arePayloadFilterRulesEqual(
  left: PayloadFilterRule[],
  right: PayloadFilterRule[]
): boolean {
  if (left === right) return true;
  if (left.length !== right.length) return false;
  for (let i = 0; i < left.length; i += 1) {
    const a = left[i];
    const b = right[i];
    if (!a || !b) return false;
    if (a.id !== b.id) return false;
    if (!arePayloadModelEntriesEqual(a.models, b.models)) return false;
    if (a.params.length !== b.params.length) return false;
    for (let j = 0; j < a.params.length; j += 1) {
      if (a.params[j] !== b.params[j]) return false;
    }
  }
  return true;
}

function parsePayloadParamValue(raw: unknown): { valueType: PayloadParamValueType; value: string } {
  if (typeof raw === 'number') {
    return { valueType: 'number', value: String(raw) };
  }

  if (typeof raw === 'boolean') {
    return { valueType: 'boolean', value: String(raw) };
  }

  if (raw === null || typeof raw === 'object') {
    try {
      const json = JSON.stringify(raw, null, 2);
      return { valueType: 'json', value: json ?? 'null' };
    } catch {
      return { valueType: 'json', value: String(raw) };
    }
  }

  return { valueType: 'string', value: String(raw ?? '') };
}

function parseRawPayloadParamValue(raw: unknown): string {
  if (typeof raw === 'string') return raw;

  try {
    const json = JSON.stringify(raw, null, 2);
    return json ?? '';
  } catch {
    return String(raw ?? '');
  }
}

function parsePayloadProtocol(raw: unknown): string | undefined {
  if (typeof raw !== 'string') return undefined;
  return raw.trim() ? raw : undefined;
}

export function parseRoutingStrategy(raw: unknown): RoutingStrategy {
  const normalized = String(raw ?? '')
    .trim()
    .toLowerCase();
  if (['weighted-round-robin', 'weightedroundrobin', 'wrr'].includes(normalized)) {
    return 'weighted-round-robin';
  }
  if (['fill-first', 'fillfirst', 'ff'].includes(normalized)) return 'fill-first';
  return 'round-robin';
}

export function parseDisableImageGenerationMode(raw: unknown): DisableImageGenerationMode {
  if (raw === true) return 'true';
  if (typeof raw === 'string') {
    const normalized = raw.trim().toLowerCase();
    if (normalized === 'true') return 'true';
    if (normalized === 'chat') return 'chat';
    if (normalized === 'passthrough') return 'passthrough';
  }
  return 'false';
}

function parsePayloadHeaders(raw: unknown, idPrefix: string): PayloadHeaderEntry[] {
  const record = asRecord(raw);
  if (!record) return [];

  return Object.entries(record).map(([name, value], index) => ({
    id: `${idPrefix}-header-${index}`,
    name,
    value: String(value ?? ''),
  }));
}

function parsePayloadConditions(raw: unknown, idPrefix: string): PayloadParamEntry[] {
  if (!Array.isArray(raw)) return [];

  const entries: PayloadParamEntry[] = [];
  raw.forEach((item, itemIndex) => {
    const record = asRecord(item);
    if (!record) {
      if (typeof item === 'string') {
        entries.push({
          id: `${idPrefix}-condition-${itemIndex}-0`,
          path: item,
          valueType: 'string',
          value: '',
        });
      }
      return;
    }

    Object.entries(record).forEach(([path, value], valueIndex) => {
      const parsedValue = parsePayloadParamValue(value);
      entries.push({
        id: `${idPrefix}-condition-${itemIndex}-${valueIndex}`,
        path,
        valueType: parsedValue.valueType,
        value: parsedValue.value,
      });
    });
  });

  return entries;
}

function parseStringList(raw: unknown): string[] {
  return Array.isArray(raw) ? raw.map((item) => String(item ?? '').trim()).filter(Boolean) : [];
}

const PLUGIN_STORE_AUTH_TYPES: PluginStoreAuthType[] = [
  'none',
  'bearer',
  'basic',
  'header',
  'github-token',
];
const PLUGIN_STORE_AUTH_APPLY_TO: PluginStoreAuthApplyTo[] = ['registry', 'metadata', 'artifact'];

function parsePluginStoreAuthType(raw: unknown): PluginStoreAuthType {
  const value = String(raw ?? '')
    .trim()
    .toLowerCase();
  return PLUGIN_STORE_AUTH_TYPES.includes(value as PluginStoreAuthType)
    ? (value as PluginStoreAuthType)
    : 'none';
}

function parsePluginStoreAuthApplyTo(raw: unknown): PluginStoreAuthApplyTo[] {
  return parseStringList(raw)
    .map((item) => item.toLowerCase())
    .filter((item): item is PluginStoreAuthApplyTo =>
      PLUGIN_STORE_AUTH_APPLY_TO.includes(item as PluginStoreAuthApplyTo)
    );
}

function parsePluginStoreAuthRules(raw: unknown): PluginStoreAuthRule[] {
  if (!Array.isArray(raw)) return [];
  return raw
    .map((item, index): PluginStoreAuthRule | null => {
      const record = asRecord(item);
      if (!record) return null;
      const match = typeof record.match === 'string' ? record.match : '';
      const rule: PluginStoreAuthRule = {
        id: `plugin-store-auth-${index}`,
        match,
        applyTo: parsePluginStoreAuthApplyTo(record['apply-to'] ?? record.apply_to),
        type: parsePluginStoreAuthType(record.type),
        tokenEnv: typeof record['token-env'] === 'string' ? record['token-env'] : '',
        usernameEnv: typeof record['username-env'] === 'string' ? record['username-env'] : '',
        passwordEnv: typeof record['password-env'] === 'string' ? record['password-env'] : '',
        headerName: typeof record['header-name'] === 'string' ? record['header-name'] : '',
        headerValueEnv:
          typeof record['header-value-env'] === 'string' ? record['header-value-env'] : '',
        allowInsecure: Boolean(record['allow-insecure'] ?? record.allow_insecure),
      };
      return rule.match.trim() ||
        rule.type !== 'none' ||
        rule.applyTo.length > 0 ||
        rule.tokenEnv.trim() ||
        rule.usernameEnv.trim() ||
        rule.passwordEnv.trim() ||
        rule.headerName.trim() ||
        rule.headerValueEnv.trim() ||
        rule.allowInsecure
        ? rule
        : null;
    })
    .filter((rule): rule is PluginStoreAuthRule => Boolean(rule));
}

function parsePayloadModelEntries(raw: unknown, idPrefix: string): PayloadRule['models'] {
  if (!Array.isArray(raw)) return [];

  return raw.map((model, modelIndex) => {
    const modelRecord = asRecord(model);
    const nameRaw =
      typeof model === 'string' ? model : (modelRecord?.name ?? modelRecord?.id ?? '');
    const name = typeof nameRaw === 'string' ? nameRaw : String(nameRaw ?? '');
    const modelId = `${idPrefix}-${modelIndex}`;

    return {
      id: modelId,
      name,
      protocol: parsePayloadProtocol(modelRecord?.protocol),
      fromProtocol: parsePayloadProtocol(modelRecord?.['from-protocol']),
      headers: parsePayloadHeaders(modelRecord?.headers, modelId),
      match: parsePayloadConditions(modelRecord?.match, `${modelId}-match`),
      notMatch: parsePayloadConditions(modelRecord?.['not-match'], `${modelId}-not-match`),
      exist: parseStringList(modelRecord?.exist),
      notExist: parseStringList(modelRecord?.['not-exist']),
    };
  });
}

function parsePayloadRules(rules: unknown): PayloadRule[] {
  if (!Array.isArray(rules)) return [];

  return rules.map((rule, index) => {
    const record = asRecord(rule) ?? {};

    const models = parsePayloadModelEntries(record.models, `model-${index}`);

    const paramsRecord = asRecord(record.params);
    const params = paramsRecord
      ? Object.entries(paramsRecord).map(([path, value], pIndex) => {
          const parsedValue = parsePayloadParamValue(value);
          return {
            id: `param-${index}-${pIndex}`,
            path,
            valueType: parsedValue.valueType,
            value: parsedValue.value,
          };
        })
      : [];

    return { id: `payload-rule-${index}`, models, params };
  });
}

function parsePayloadFilterRules(rules: unknown): PayloadFilterRule[] {
  if (!Array.isArray(rules)) return [];

  return rules.map((rule, index) => {
    const record = asRecord(rule) ?? {};

    const models = parsePayloadModelEntries(record.models, `filter-model-${index}`);

    const paramsRaw = record.params;
    const params = Array.isArray(paramsRaw) ? paramsRaw.map(String) : [];

    return { id: `payload-filter-rule-${index}`, models, params };
  });
}

function parseRawPayloadRules(rules: unknown): PayloadRule[] {
  if (!Array.isArray(rules)) return [];

  return rules.map((rule, index) => {
    const record = asRecord(rule) ?? {};

    const models = parsePayloadModelEntries(record.models, `raw-model-${index}`);

    const paramsRecord = asRecord(record.params);
    const params = paramsRecord
      ? Object.entries(paramsRecord).map(([path, value], pIndex) => ({
          id: `raw-param-${index}-${pIndex}`,
          path,
          valueType: 'json' as const,
          value: parseRawPayloadParamValue(value),
        }))
      : [];

    return { id: `payload-raw-rule-${index}`, models, params };
  });
}

function serializePayloadParamEntryValue(param: PayloadParamEntry): unknown {
  if (param.valueType === 'number') {
    const num = Number(param.value);
    return Number.isFinite(num) ? num : param.value;
  }
  if (param.valueType === 'boolean') {
    return param.value === 'true';
  }
  if (param.valueType === 'json') {
    try {
      return JSON.parse(param.value);
    } catch {
      return param.value;
    }
  }
  return param.value;
}

function serializeStringListForYaml(items?: string[]): string[] {
  return (items ?? []).map((item) => item.trim()).filter(Boolean);
}

function serializePluginStoreAuthForYaml(
  rules: PluginStoreAuthRule[]
): Array<Record<string, unknown>> {
  return rules
    .map((rule) => {
      const match = rule.match.trim();
      if (!match) return null;
      const item: Record<string, unknown> = {
        match,
        type: rule.type,
      };
      const applyTo = serializeStringListForYaml(rule.applyTo);
      if (applyTo.length > 0) item['apply-to'] = applyTo;
      if (rule.tokenEnv.trim()) item['token-env'] = rule.tokenEnv.trim();
      if (rule.usernameEnv.trim()) item['username-env'] = rule.usernameEnv.trim();
      if (rule.passwordEnv.trim()) item['password-env'] = rule.passwordEnv.trim();
      if (rule.headerName.trim()) item['header-name'] = rule.headerName.trim();
      if (rule.headerValueEnv.trim()) item['header-value-env'] = rule.headerValueEnv.trim();
      if (rule.allowInsecure) item['allow-insecure'] = true;
      return item;
    })
    .filter((rule): rule is Record<string, unknown> => Boolean(rule));
}

function mapPair(map: YAMLMap, key: string): Pair | undefined {
  return map.items.find((pair) => String(isScalar(pair.key) ? pair.key.value : pair.key) === key);
}

function preserveNodeComments(previous: Node | null | undefined, next: Node): Node {
  if (!previous) return next;
  next.comment = previous.comment;
  next.commentBefore = previous.commentBefore;
  next.spaceBefore = previous.spaceBefore;
  return next;
}

function updatePairValue(doc: YamlDocument, pair: Pair, value: unknown): void {
  if (isScalar(pair.value) && (value === null || typeof value !== 'object')) {
    pair.value.value = value;
    return;
  }
  const previous = pair.value && typeof pair.value === 'object' ? (pair.value as Node) : undefined;
  pair.value = preserveNodeComments(previous, doc.createNode(value));
}

function setMapValue(doc: YamlDocument, map: YAMLMap, key: string, value: unknown): void {
  const pair = mapPair(map, key);
  if (pair) {
    updatePairValue(doc, pair, value);
  } else {
    map.items.push(doc.createPair(key, value));
  }
}

function deleteMapValue(map: YAMLMap, key: string): void {
  const pair = mapPair(map, key);
  if (pair) map.items.splice(map.items.indexOf(pair), 1);
}

function ensureMapValue(doc: YamlDocument, map: YAMLMap, key: string): YAMLMap {
  const pair = mapPair(map, key);
  if (pair && isMap(pair.value)) return pair.value;
  const next = doc.createNode({});
  if (!isMap(next)) throw new Error('Expected YAML map');
  if (pair) {
    pair.value = preserveNodeComments(
      pair.value && typeof pair.value === 'object' ? (pair.value as Node) : undefined,
      next
    );
  } else {
    map.items.push(doc.createPair(key, next));
  }
  return next;
}

function ensureSeqValue(doc: YamlDocument, map: YAMLMap, key: string): YAMLSeq {
  const pair = mapPair(map, key);
  if (pair && isSeq(pair.value)) return pair.value;
  const next = doc.createNode([]);
  if (!isSeq(next)) throw new Error('Expected YAML sequence');
  if (pair) {
    pair.value = preserveNodeComments(
      pair.value && typeof pair.value === 'object' ? (pair.value as Node) : undefined,
      next
    );
  } else {
    map.items.push(doc.createPair(key, next));
  }
  return next;
}

function replaceSequenceItems(seq: YAMLSeq, items: Node[]): void {
  const originalItems = [...seq.items] as Node[];
  const comments = new Map<Node, string | null | undefined>();
  originalItems.forEach((item, index) => {
    comments.set(item, index === 0 ? seq.commentBefore : item.commentBefore);
  });

  items.forEach((item) => {
    item.commentBefore = comments.get(item);
  });
  seq.items = items;
  seq.commentBefore = items.length > 0 ? items[0].commentBefore : undefined;
  if (items[0]) items[0].commentBefore = undefined;
}

function syncStringSequence(
  doc: YamlDocument,
  map: YAMLMap,
  key: string,
  baseline: string[] | undefined,
  desired: string[] | undefined
): void {
  const values = serializeStringListForYaml(desired);
  if (values.length === 0) {
    deleteMapValue(map, key);
    return;
  }

  const seq = ensureSeqValue(doc, map, key);
  const available = (baseline ?? []).map((value, index) => ({ value: value.trim(), index }));
  const used = new Set<number>();
  const items = values.map((value) => {
    const match = available.find((item) => item.value === value && !used.has(item.index));
    const existing = match ? seq.items[match.index] : undefined;
    if (match) used.add(match.index);
    if (isScalar(existing)) {
      existing.value = value;
      return existing;
    }
    return doc.createNode(value);
  });
  replaceSequenceItems(seq, items);
}

function replaceMapItems(map: YAMLMap, items: Pair[]): void {
  const originalItems = [...map.items];
  const comments = new Map<Pair, string | null | undefined>();
  originalItems.forEach((pair, index) => {
    const key = pair.key && typeof pair.key === 'object' ? (pair.key as Node) : undefined;
    comments.set(pair, index === 0 ? map.commentBefore : key?.commentBefore);
  });

  items.forEach((pair) => {
    const key = pair.key && typeof pair.key === 'object' ? (pair.key as Node) : undefined;
    if (key) key.commentBefore = comments.get(pair);
  });
  map.items = items;
  const firstKey = items[0]?.key;
  const firstKeyNode = firstKey && typeof firstKey === 'object' ? (firstKey as Node) : undefined;
  map.commentBefore = firstKeyNode?.commentBefore;
  if (firstKeyNode) firstKeyNode.commentBefore = undefined;
}

function syncEntryMap<T extends { id: string }>(
  doc: YamlDocument,
  map: YAMLMap,
  baseline: T[],
  desired: T[],
  getKey: (entry: T) => string,
  getValue: (entry: T) => unknown
): void {
  const pairsById = new Map<string, Pair>();
  baseline.forEach((entry) => {
    const pair = mapPair(map, getKey(entry));
    if (pair) pairsById.set(entry.id, pair);
  });

  const managedKeys = new Set(baseline.map((entry) => getKey(entry)));
  const entries = desired.filter((entry) => getKey(entry).trim());
  const desiredKeys = new Set(entries.map((entry) => getKey(entry).trim()));
  const items = entries.map((entry) => {
    const key = getKey(entry).trim();
    const pair = pairsById.get(entry.id) ?? doc.createPair(key, getValue(entry));
    if (isScalar(pair.key)) pair.key.value = key;
    else pair.key = doc.createNode(key);
    updatePairValue(doc, pair, getValue(entry));
    return pair;
  });
  const unmanagedItems = map.items.filter((pair) => {
    const key = String(isScalar(pair.key) ? pair.key.value : pair.key);
    return !managedKeys.has(key) && !desiredKeys.has(key);
  });
  replaceMapItems(map, [...items, ...unmanagedItems]);
}

function syncConditionSequence(
  doc: YamlDocument,
  modelMap: YAMLMap,
  key: string,
  baseline: PayloadParamEntry[] | undefined,
  desired: PayloadParamEntry[] | undefined
): void {
  // Unedited conditions retain their original grouping and nested value comments.
  if (JSON.stringify(baseline ?? []) === JSON.stringify(desired ?? [])) return;

  const entries = (desired ?? []).filter((entry) => entry.path.trim());
  if (entries.length === 0) {
    deleteMapValue(modelMap, key);
    return;
  }
  const seq = ensureSeqValue(doc, modelMap, key);
  const flattened: YAMLMap[] = [];
  seq.items.forEach((node, itemIndex) => {
    if (isMap(node)) {
      // Match parsePayloadConditions' Object.entries order, including numeric keys.
      // Every map field is a condition, not an unknown extension field. The backend
      // ANDs all fields across all maps, so singleton maps preserve its semantics.
      Object.keys(node.toJSON()).forEach((path) => {
        const pair = mapPair(node, path);
        if (!pair) return;
        const item = doc.createNode({}) as YAMLMap;
        item.flow = node.flow;
        item.items = [pair];
        if (pair === node.items[0]) {
          item.commentBefore = itemIndex === 0 ? seq.commentBefore : node.commentBefore;
          item.spaceBefore = node.spaceBefore;
        }
        if (pair === node.items[node.items.length - 1]) item.comment = node.comment;
        flattened.push(item);
      });
    } else if (isScalar(node) && typeof node.value === 'string') {
      // Keep the parser's compatibility with scalar entries without shifting IDs.
      const item = doc.createNode({ [node.value]: '' }) as YAMLMap;
      preserveNodeComments(node, item);
      if (itemIndex === 0) item.commentBefore = seq.commentBefore;
      flattened.push(item);
    }
  });
  const nodesById = new Map((baseline ?? []).map((entry, index) => [entry.id, flattened[index]]));
  const priorById = new Map((baseline ?? []).map((entry) => [entry.id, entry]));
  const items = entries.map((entry) => {
    const item = nodesById.get(entry.id) ?? (doc.createNode({}) as YAMLMap);
    const prior = priorById.get(entry.id);
    const pair = item.items[0];
    if (pair) {
      if (isScalar(pair.key)) pair.key.value = entry.path.trim();
      else pair.key = doc.createNode(entry.path.trim());
      if (prior?.value !== entry.value || prior?.valueType !== entry.valueType) {
        updatePairValue(doc, pair, serializePayloadParamEntryValue(entry));
      }
    } else {
      item.items.push(doc.createPair(entry.path.trim(), serializePayloadParamEntryValue(entry)));
    }
    return item;
  });
  // These are new singleton wrappers; replaceSequenceItems would discard their
  // comments because they are not members of the original sequence.
  seq.items = items;
  seq.commentBefore = items[0]?.commentBefore;
  if (items[0]) items[0].commentBefore = undefined;
}

function syncPayloadModels(
  doc: YamlDocument,
  ruleMap: YAMLMap,
  baseline: PayloadRule['models'],
  desired: PayloadRule['models']
): void {
  const models = desired.filter((model) => model.name.trim());
  const seq = ensureSeqValue(doc, ruleMap, 'models');
  const nodesById = new Map(baseline.map((model, index) => [model.id, seq.items[index]]));
  const items = models.map((model) => {
    const existing = nodesById.get(model.id);
    const modelMap = isMap(existing) ? existing : (doc.createNode({}) as YAMLMap);
    const prior = baseline.find((candidate) => candidate.id === model.id);
    setMapValue(doc, modelMap, 'name', model.name.trim());
    if (model.protocol) setMapValue(doc, modelMap, 'protocol', model.protocol);
    else deleteMapValue(modelMap, 'protocol');
    if (model.fromProtocol) setMapValue(doc, modelMap, 'from-protocol', model.fromProtocol);
    else deleteMapValue(modelMap, 'from-protocol');

    const headers = model.headers?.filter((header) => header.name.trim()) ?? [];
    if (headers.length) {
      const headersMap = ensureMapValue(doc, modelMap, 'headers');
      syncEntryMap(
        doc,
        headersMap,
        prior?.headers ?? [],
        headers,
        (header) => header.name,
        (header) => header.value
      );
    } else deleteMapValue(modelMap, 'headers');

    syncConditionSequence(doc, modelMap, 'match', prior?.match, model.match);
    syncConditionSequence(doc, modelMap, 'not-match', prior?.notMatch, model.notMatch);
    syncStringSequence(doc, modelMap, 'exist', prior?.exist, model.exist);
    syncStringSequence(doc, modelMap, 'not-exist', prior?.notExist, model.notExist);
    return modelMap;
  });
  replaceSequenceItems(seq, items);
}

function syncPayloadRuleSequence(
  doc: YamlDocument,
  section: string,
  baseline: PayloadRule[],
  desired: PayloadRule[],
  rawValues: boolean
): void {
  const payload = doc.getIn(['requests', 'payload'], true);
  if (!isMap(payload)) throw new Error('Expected payload map');
  const rules = desired.filter((rule) => rule.models.some((model) => model.name.trim()));
  if (rules.length === 0) {
    deleteMapValue(payload, section);
    return;
  }

  const seq = ensureSeqValue(doc, payload, section);
  const nodesById = new Map(baseline.map((rule, index) => [rule.id, seq.items[index]]));
  const items = rules.map((rule) => {
    const existing = nodesById.get(rule.id);
    const ruleMap = isMap(existing) ? existing : (doc.createNode({}) as YAMLMap);
    const prior = baseline.find((candidate) => candidate.id === rule.id);
    syncPayloadModels(doc, ruleMap, prior?.models ?? [], rule.models);
    const params = ensureMapValue(doc, ruleMap, 'params');
    syncEntryMap(
      doc,
      params,
      prior?.params ?? [],
      rule.params,
      (param) => param.path,
      (param) => (rawValues ? param.value : serializePayloadParamEntryValue(param))
    );
    return ruleMap;
  });
  replaceSequenceItems(seq, items);
}

function syncPayloadFilterSequence(
  doc: YamlDocument,
  baseline: PayloadFilterRule[],
  desired: PayloadFilterRule[]
): void {
  const payload = doc.getIn(['requests', 'payload'], true);
  if (!isMap(payload)) throw new Error('Expected payload map');
  const rules = desired.filter((rule) => rule.models.some((model) => model.name.trim()));
  if (rules.length === 0) {
    deleteMapValue(payload, 'filter');
    return;
  }

  const seq = ensureSeqValue(doc, payload, 'filter');
  const nodesById = new Map(baseline.map((rule, index) => [rule.id, seq.items[index]]));
  const items = rules.map((rule) => {
    const existing = nodesById.get(rule.id);
    const ruleMap = isMap(existing) ? existing : (doc.createNode({}) as YAMLMap);
    const prior = baseline.find((candidate) => candidate.id === rule.id);
    syncPayloadModels(doc, ruleMap, prior?.models ?? [], rule.models);
    syncStringSequence(doc, ruleMap, 'params', prior?.params, rule.params);
    return ruleMap;
  });
  replaceSequenceItems(seq, items);
}

/** IDs are editor identity, not YAML data. Reserve exact matches before matching edited
 * entries, so a deletion/reorder cannot steal another entry's AST node. New entries
 * use a separate namespace rather than their parsed positional IDs.
 */
function withoutEditorIds(value: unknown): string {
  return JSON.stringify(value, (key, item: unknown) => (key === 'id' ? undefined : item));
}

function alignRebasedEntries<T extends { id: string }>(
  baseline: T[],
  draft: T[],
  identity: (entry: T) => string,
  alignChildren: (entry: T, prior: T | undefined) => T = (entry) => entry
): T[] {
  const available = new Set(baseline.map((_, index) => index));
  const matches = new Map<number, number>();
  for (const key of [withoutEditorIds, identity]) {
    draft.forEach((entry, index) => {
      if (matches.has(index)) return;
      const signature = key(entry);
      const match = [...available].find((candidate) => key(baseline[candidate]) === signature);
      if (match !== undefined) {
        matches.set(index, match);
        available.delete(match);
      }
    });
  }
  return draft.map((entry, index) => {
    const match = matches.get(index);
    const prior = match === undefined ? undefined : baseline[match];
    return alignChildren({ ...entry, id: prior?.id ?? `rebase-new-${entry.id}` }, prior);
  });
}

function alignRebasedModels(
  draft: PayloadRule['models'],
  baseline: PayloadRule['models']
): PayloadRule['models'] {
  const alignParams = (entries: PayloadParamEntry[], prior: PayloadParamEntry[]) =>
    alignRebasedEntries(prior, entries, (entry) => entry.path);
  return alignRebasedEntries(
    baseline,
    draft,
    (model) => JSON.stringify([model.name, model.protocol, model.fromProtocol]),
    (model, prior) => ({
      ...model,
      headers: alignRebasedEntries(
        prior?.headers ?? [],
        model.headers ?? [],
        (entry) => entry.name
      ),
      match: alignParams(model.match ?? [], prior?.match ?? []),
      notMatch: alignParams(model.notMatch ?? [], prior?.notMatch ?? []),
    })
  );
}

function alignRebasedValues(
  baseline: VisualConfigValues,
  draft: VisualConfigValues
): VisualConfigValues {
  const ruleIdentity = (rule: PayloadRule | PayloadFilterRule) =>
    JSON.stringify(rule.models.map((model) => [model.name, model.protocol, model.fromProtocol]));
  const alignRules = (prior: PayloadRule[], rules: PayloadRule[]) =>
    alignRebasedEntries(prior, rules, ruleIdentity, (rule, original) => ({
      ...rule,
      models: alignRebasedModels(rule.models, original?.models ?? []),
      params: alignRebasedEntries(original?.params ?? [], rule.params, (param) => param.path),
    }));
  return {
    ...draft,
    [ICE_KEY]: alignRebasedEntries(baseline[ICE_KEY], draft[ICE_KEY], (row) => row.urlsText),
    payloadDefaultRules: alignRules(baseline.payloadDefaultRules, draft.payloadDefaultRules),
    payloadDefaultRawRules: alignRules(
      baseline.payloadDefaultRawRules,
      draft.payloadDefaultRawRules
    ),
    payloadOverrideRules: alignRules(baseline.payloadOverrideRules, draft.payloadOverrideRules),
    payloadOverrideRawRules: alignRules(
      baseline.payloadOverrideRawRules,
      draft.payloadOverrideRawRules
    ),
    payloadFilterRules: alignRebasedEntries(
      baseline.payloadFilterRules,
      draft.payloadFilterRules,
      ruleIdentity,
      (rule, original) => ({
        ...rule,
        models: alignRebasedModels(rule.models, original?.models ?? []),
      })
    ),
    pluginStoreAuth: alignRebasedEntries(
      baseline.pluginStoreAuth,
      draft.pluginStoreAuth,
      (rule) => rule.match
    ),
  };
}

type VisualConfigState = {
  baselineYaml: string;
  visualValues: VisualConfigValues;
  baselineValues: VisualConfigValues;
  dirtyFields: Set<string>;
  visualParseError: string | null;
  rebasedPayload: { yaml: string; serverYaml: string; values: VisualConfigValues } | null;
};

type VisualConfigAction =
  | {
      type: 'load_success';
      yaml: string;
      values: VisualConfigValues;
    }
  | {
      type: 'rebase_success';
      baseline: VisualConfigValues;
      draft: VisualConfigValues;
      draftYaml: string;
      serverYaml: string;
    }
  | {
      type: 'load_error';
      error: string;
    }
  | {
      type: 'set_values';
      values: Partial<VisualConfigValues>;
    };

function createInitialVisualConfigState(): VisualConfigState {
  const initialValues = deepClone(DEFAULT_VISUAL_VALUES);
  return {
    baselineYaml: '{}',
    visualValues: initialValues,
    baselineValues: deepClone(initialValues),
    dirtyFields: new Set(),
    visualParseError: null,
    rebasedPayload: null,
  };
}

function mergeVisualConfigValues(
  currentValues: VisualConfigValues,
  patch: Partial<VisualConfigValues>
): VisualConfigValues {
  const nextValues: VisualConfigValues = { ...currentValues, ...patch } as VisualConfigValues;
  if (patch.streaming) {
    nextValues.streaming = { ...currentValues.streaming, ...patch.streaming };
  }
  return nextValues;
}

function getNextDirtyFields(
  currentDirtyFields: Set<string>,
  patch: Partial<VisualConfigValues>,
  nextValues: VisualConfigValues,
  baselineValues: VisualConfigValues
): Set<string> {
  const nextDirtyFields = new Set(currentDirtyFields);
  const updateDirty = (key: string, isEqual: boolean) => {
    if (isEqual) {
      nextDirtyFields.delete(key);
    } else {
      nextDirtyFields.add(key);
    }
  };
  const updateScalarDirty = (key: keyof VisualConfigValues) => {
    if (Object.prototype.hasOwnProperty.call(patch, key)) {
      updateDirty(key, nextValues[key] === baselineValues[key]);
    }
  };

  SERVER_FIELDS.forEach(({ key }) => {
    if (Object.prototype.hasOwnProperty.call(patch, key)) {
      updateDirty(key, JSON.stringify(nextValues[key]) === JSON.stringify(baselineValues[key]));
    }
  });
  ADDITION_FIELDS.forEach(({ key }) => updateScalarDirty(key));
  if (Object.prototype.hasOwnProperty.call(patch, ICE_KEY)) {
    updateDirty(
      ICE_KEY,
      withoutEditorIds(nextValues[ICE_KEY]) === withoutEditorIds(baselineValues[ICE_KEY])
    );
  }

  (
    [
      'rmDisableAutoUpdatePanel',
      'errorLogsMaxFiles',
      'usageStatisticsEnabled',
      'redisUsageQueueRetentionSeconds',
      'pluginsEnabled',
      'passthroughHeaders',
      'disableCooling',
      'disableImageGeneration',
      'gptImage2BaseModel',
      'authAutoRefreshWorkers',
      'antigravitySignatureCacheEnabled',
      'antigravitySignatureBypassStrict',
      'claudeHeaderUserAgent',
      'claudeHeaderPackageVersion',
      'claudeHeaderRuntimeVersion',
      'claudeHeaderOs',
      'claudeHeaderArch',
      'claudeHeaderTimeout',
      'claudeHeaderStabilizeDeviceProfile',
      'codexHeaderUserAgent',
      'codexHeaderBetaFeatures',
      'host',
      'port',
      'tlsEnable',
      'tlsCert',
      'tlsKey',
      'rmAllowRemote',
      'rmSecretKey',
      'rmDisableControlPanel',
      'rmPanelRepo',
      'authDir',
      'apiKeysText',
      'debug',
      'commercialMode',
      'loggingToFile',
      'logsMaxTotalSizeMb',
      'proxyUrl',
      'forceModelPrefix',
      'requestRetry',
      'maxRetryCredentials',
      'maxRetryInterval',
      'wsAuth',
      'quotaAntigravityCredits',
      'routingStrategy',
      'routingSessionAffinity',
      'routingSessionAffinityTTL',
    ] as Array<keyof VisualConfigValues>
  ).forEach(updateScalarDirty);

  if (Object.prototype.hasOwnProperty.call(patch, 'pluginStoreSources')) {
    updateDirty(
      'pluginStoreSources',
      areStringArraysEqual(nextValues.pluginStoreSources, baselineValues.pluginStoreSources)
    );
  }
  if (Object.prototype.hasOwnProperty.call(patch, 'antigravitySensitiveWords')) {
    updateDirty(
      'antigravitySensitiveWords',
      areStringArraysEqual(
        nextValues.antigravitySensitiveWords,
        baselineValues.antigravitySensitiveWords
      )
    );
  }
  if (Object.prototype.hasOwnProperty.call(patch, 'devinSensitiveWords')) {
    updateDirty(
      'devinSensitiveWords',
      areStringArraysEqual(nextValues.devinSensitiveWords, baselineValues.devinSensitiveWords)
    );
  }
  if (Object.prototype.hasOwnProperty.call(patch, 'pluginStoreAuth')) {
    updateDirty(
      'pluginStoreAuth',
      arePluginStoreAuthRulesEqual(nextValues.pluginStoreAuth, baselineValues.pluginStoreAuth)
    );
  }

  if (Object.prototype.hasOwnProperty.call(patch, 'payloadDefaultRules')) {
    updateDirty(
      'payloadDefaultRules',
      arePayloadRulesEqual(nextValues.payloadDefaultRules, baselineValues.payloadDefaultRules)
    );
  }
  if (Object.prototype.hasOwnProperty.call(patch, 'payloadDefaultRawRules')) {
    updateDirty(
      'payloadDefaultRawRules',
      arePayloadRulesEqual(nextValues.payloadDefaultRawRules, baselineValues.payloadDefaultRawRules)
    );
  }
  if (Object.prototype.hasOwnProperty.call(patch, 'payloadOverrideRules')) {
    updateDirty(
      'payloadOverrideRules',
      arePayloadRulesEqual(nextValues.payloadOverrideRules, baselineValues.payloadOverrideRules)
    );
  }
  if (Object.prototype.hasOwnProperty.call(patch, 'payloadOverrideRawRules')) {
    updateDirty(
      'payloadOverrideRawRules',
      arePayloadRulesEqual(
        nextValues.payloadOverrideRawRules,
        baselineValues.payloadOverrideRawRules
      )
    );
  }
  if (Object.prototype.hasOwnProperty.call(patch, 'payloadFilterRules')) {
    updateDirty(
      'payloadFilterRules',
      arePayloadFilterRulesEqual(nextValues.payloadFilterRules, baselineValues.payloadFilterRules)
    );
  }
  if (patch.streaming) {
    const streamingPatch = patch.streaming;
    if (Object.prototype.hasOwnProperty.call(streamingPatch, 'keepaliveSeconds')) {
      updateDirty(
        'streaming.keepaliveSeconds',
        nextValues.streaming.keepaliveSeconds === baselineValues.streaming.keepaliveSeconds
      );
    }
    if (Object.prototype.hasOwnProperty.call(streamingPatch, 'bootstrapRetries')) {
      updateDirty(
        'streaming.bootstrapRetries',
        nextValues.streaming.bootstrapRetries === baselineValues.streaming.bootstrapRetries
      );
    }
    if (Object.prototype.hasOwnProperty.call(streamingPatch, 'nonstreamKeepaliveInterval')) {
      updateDirty(
        'streaming.nonstreamKeepaliveInterval',
        nextValues.streaming.nonstreamKeepaliveInterval ===
          baselineValues.streaming.nonstreamKeepaliveInterval
      );
    }
  }

  return nextDirtyFields;
}

function visualConfigReducer(
  state: VisualConfigState,
  action: VisualConfigAction
): VisualConfigState {
  switch (action.type) {
    case 'load_success':
      return {
        baselineYaml: action.yaml,
        visualValues: action.values,
        baselineValues: deepClone(action.values),
        rebasedPayload: null,
        dirtyFields: new Set(),
        visualParseError: null,
      };
    case 'rebase_success': {
      const values = alignRebasedValues(action.baseline, action.draft);
      return {
        baselineYaml: action.serverYaml,
        visualValues: values,
        baselineValues: action.baseline,
        rebasedPayload: {
          yaml: action.draftYaml,
          serverYaml: action.serverYaml,
          values: deepClone(values),
        },
        dirtyFields: getNextDirtyFields(new Set(), values, values, action.baseline),
        visualParseError: null,
      };
    }
    case 'load_error':
      return {
        ...state,
        visualParseError: action.error,
      };
    case 'set_values': {
      const nextValues = mergeVisualConfigValues(state.visualValues, action.values);
      const nextDirtyFields = getNextDirtyFields(
        state.dirtyFields,
        action.values,
        nextValues,
        state.baselineValues
      );

      return {
        ...state,
        visualValues: nextValues,
        dirtyFields: nextDirtyFields,
      };
    }
    default:
      return state;
  }
}

function parseVisualValuesFromYaml(yamlContent: string): VisualConfigValues {
  const document = parseDocument(yamlContent);
  if (document.errors.length > 0) {
    throw new Error(document.errors[0]?.message ?? 'Invalid YAML');
  }

  const parsedRaw: unknown = parseYaml(yamlContent) || {};
  const parsed = asRecord(parsedRaw) ?? {};
  const v8Server = asRecord(parsed?.['server']);
  const v8Routing = asRecord(parsed?.['routing']);
  const v8RoutingRetry = asRecord(v8Routing?.['retry']);
  const v8RoutingCooldown = asRecord(v8Routing?.['cooldown']);
  const v8Requests = asRecord(parsed?.['requests']);
  const v8Oauth = asRecord(parsed?.['oauth']);
  const v8OauthProviders = asRecord(v8Oauth?.['providers']);
  const v8OauthProvidersAistudio = asRecord(v8OauthProviders?.['aistudio']);
  const v8OauthProvidersCodex = asRecord(v8OauthProviders?.['codex']);
  const v8Upstream = asRecord(parsed?.['upstream']);
  const v8UpstreamClaude = asRecord(v8Upstream?.['claude']);
  const v8OauthProvidersAntigravity = asRecord(v8OauthProviders?.['antigravity']);
  const v8Multimedia = asRecord(parsed?.['multimedia']);
  const v8Observability = asRecord(parsed?.['observability']);
  const v8ObservabilityLogs = asRecord(v8Observability?.['logs']);
  const v8ObservabilityUsage = asRecord(v8Observability?.['usage']);
  const tls = asRecord(v8Server?.['tls']);
  const remoteManagement = asRecord(parsed['management']);
  const routing = asRecord(parsed.routing);
  const payload = asRecord(v8Requests?.['payload']);
  const streaming = asRecord(v8Requests?.['streaming']);
  const plugins = asRecord(parsed.plugins);
  const antigravity = asRecord(v8OauthProviders?.['antigravity']);
  const devin = asRecord(v8OauthProviders?.['devin']);
  const claudeHeaderDefaults = asRecord(v8UpstreamClaude?.['header-defaults']);
  const codexHeaderDefaults = asRecord(v8OauthProvidersCodex?.['header-defaults']);

  const newValues: VisualConfigValues = {
    ...readVisualAdditions(document),
    ...readVisualServer(document),
    host: typeof v8Server?.['host'] === 'string' ? v8Server?.['host'] : '',
    port: String(v8Server?.['port'] ?? ''),

    tlsEnable: Boolean(tls?.enable),
    tlsCert: typeof tls?.cert === 'string' ? tls.cert : '',
    tlsKey: typeof tls?.key === 'string' ? tls.key : '',

    rmAllowRemote: Boolean(remoteManagement?.['allow-remote']),
    rmSecretKey:
      typeof remoteManagement?.['secret-key'] === 'string' ? remoteManagement['secret-key'] : '',
    rmDisableControlPanel: Boolean(remoteManagement?.['disable-control-panel']),
    rmDisableAutoUpdatePanel: Boolean(remoteManagement?.['disable-auto-update-panel']),
    rmPanelRepo:
      typeof remoteManagement?.['panel-github-repository'] === 'string'
        ? remoteManagement['panel-github-repository']
        : '',

    authDir: typeof v8Oauth?.['auth-dir'] === 'string' ? v8Oauth?.['auth-dir'] : '',
    apiKeysText: parseApiKeysText(asRecord(parsed.access)?.['api-keys']),
    pluginsEnabled: Boolean(plugins?.enabled),
    pluginStoreSources: parseStringList(plugins?.['store-sources']),
    pluginStoreAuth: parsePluginStoreAuthRules(plugins?.['store-auth']),

    debug: Boolean(v8ObservabilityLogs?.['debug']),
    commercialMode: Boolean(v8Server?.['commercial-mode']),
    loggingToFile: Boolean(v8ObservabilityLogs?.['logging-to-file']),
    logsMaxTotalSizeMb: String(v8ObservabilityLogs?.['logs-max-total-size-mb'] ?? ''),
    errorLogsMaxFiles: String(v8ObservabilityLogs?.['error-logs-max-files'] ?? ''),
    usageStatisticsEnabled: Boolean(v8ObservabilityUsage?.['usage-statistics-enabled']),
    redisUsageQueueRetentionSeconds: String(
      v8ObservabilityUsage?.['redis-usage-queue-retention-seconds'] ?? ''
    ),

    proxyUrl: typeof v8Requests?.['proxy-url'] === 'string' ? v8Requests?.['proxy-url'] : '',
    forceModelPrefix: Boolean(v8Routing?.['force-model-prefix']),
    passthroughHeaders: Boolean(v8Requests?.['passthrough-headers']),
    requestRetry: String(v8RoutingRetry?.['request-retry'] ?? ''),
    maxRetryCredentials: String(v8RoutingRetry?.['max-retry-credentials'] ?? ''),
    maxRetryInterval: String(v8RoutingRetry?.['max-retry-interval'] ?? ''),
    disableCooling: Boolean(v8RoutingCooldown?.['disable-cooling']),
    disableImageGeneration: parseDisableImageGenerationMode(
      v8Multimedia?.['disable-image-generation']
    ),
    gptImage2BaseModel:
      typeof v8Multimedia?.['gpt-image-2-base-model'] === 'string'
        ? v8Multimedia?.['gpt-image-2-base-model']
        : '',
    authAutoRefreshWorkers: String(v8Oauth?.['auth-auto-refresh-workers'] ?? ''),
    wsAuth: Boolean(v8OauthProvidersAistudio?.['ws-auth'] ?? DEFAULT_VISUAL_VALUES.wsAuth),
    antigravitySensitiveWords: parseStringList(antigravity?.['sensitive-words']),
    devinSensitiveWords: parseStringList(devin?.['sensitive-words']),
    antigravitySignatureCacheEnabled: Boolean(
      v8OauthProvidersAntigravity?.['signature-cache-enabled'] ?? true
    ),
    antigravitySignatureBypassStrict: Boolean(
      v8OauthProvidersAntigravity?.['signature-bypass-strict']
    ),

    claudeHeaderUserAgent:
      typeof claudeHeaderDefaults?.['user-agent'] === 'string'
        ? claudeHeaderDefaults['user-agent']
        : '',
    claudeHeaderPackageVersion:
      typeof claudeHeaderDefaults?.['package-version'] === 'string'
        ? claudeHeaderDefaults['package-version']
        : '',
    claudeHeaderRuntimeVersion:
      typeof claudeHeaderDefaults?.['runtime-version'] === 'string'
        ? claudeHeaderDefaults['runtime-version']
        : '',
    claudeHeaderOs: typeof claudeHeaderDefaults?.os === 'string' ? claudeHeaderDefaults.os : '',
    claudeHeaderArch:
      typeof claudeHeaderDefaults?.arch === 'string' ? claudeHeaderDefaults.arch : '',
    claudeHeaderTimeout:
      typeof claudeHeaderDefaults?.timeout === 'string' ? claudeHeaderDefaults.timeout : '',
    claudeHeaderStabilizeDeviceProfile: Boolean(claudeHeaderDefaults?.['stabilize-device-profile']),
    codexHeaderUserAgent:
      typeof codexHeaderDefaults?.['user-agent'] === 'string'
        ? codexHeaderDefaults['user-agent']
        : '',
    codexHeaderBetaFeatures:
      typeof codexHeaderDefaults?.['beta-features'] === 'string'
        ? codexHeaderDefaults['beta-features']
        : '',

    quotaAntigravityCredits: Boolean(antigravity?.['antigravity-credits'] ?? false),

    routingStrategy: parseRoutingStrategy(routing?.strategy),
    routingSessionAffinity: Boolean(routing?.['session-affinity']),
    routingSessionAffinityTTL:
      typeof routing?.['session-affinity-ttl'] === 'string' ? routing['session-affinity-ttl'] : '',

    payloadDefaultRules: parsePayloadRules(payload?.default),
    payloadDefaultRawRules: parseRawPayloadRules(payload?.['default-raw']),
    payloadOverrideRules: parsePayloadRules(payload?.override),
    payloadOverrideRawRules: parseRawPayloadRules(payload?.['override-raw']),
    payloadFilterRules: parsePayloadFilterRules(payload?.filter),

    streaming: {
      keepaliveSeconds: String(streaming?.['keepalive-seconds'] ?? ''),
      bootstrapRetries: String(streaming?.['bootstrap-retries'] ?? ''),
      nonstreamKeepaliveInterval: String(v8Requests?.['nonstream-keepalive-interval'] ?? ''),
    },
  };

  return newValues;
}

export function useVisualConfig() {
  const [state, dispatch] = useReducer(
    visualConfigReducer,
    undefined,
    createInitialVisualConfigState
  );
  const {
    visualValues,
    baselineValues,
    baselineYaml,
    visualParseError,
    dirtyFields,
    rebasedPayload,
  } = state;
  const visualDirty = dirtyFields.size > 0;
  const visualValidationErrors = useMemo(
    () => getVisualConfigValidationErrors(visualValues, dirtyFields),
    [visualValues, dirtyFields]
  );
  const visualHasPayloadValidationErrors = useMemo(
    () =>
      hasPayloadParamValidationErrors(visualValues.payloadDefaultRules) ||
      hasPayloadParamValidationErrors(visualValues.payloadDefaultRawRules) ||
      hasPayloadParamValidationErrors(visualValues.payloadOverrideRules) ||
      hasPayloadParamValidationErrors(visualValues.payloadOverrideRawRules),
    [
      visualValues.payloadDefaultRules,
      visualValues.payloadDefaultRawRules,
      visualValues.payloadOverrideRules,
      visualValues.payloadOverrideRawRules,
    ]
  );

  const loadVisualValuesFromYaml = useCallback((yamlContent: string) => {
    try {
      const newValues = parseVisualValuesFromYaml(yamlContent);
      dispatch({ type: 'load_success', values: newValues, yaml: yamlContent });
      return { ok: true as const };
    } catch (error: unknown) {
      const message = error instanceof Error ? error.message : 'Invalid YAML';
      dispatch({ type: 'load_error', error: message });
      return { ok: false as const, error: message };
    }
  }, []);

  // Both documents are parsed before dispatch so a malformed draft cannot advance the baseline.
  const rebaseVisualValuesFromYaml = useCallback((serverYaml: string, draftYaml: string) => {
    try {
      const baseline = parseVisualValuesFromYaml(serverYaml);
      const draft = parseVisualValuesFromYaml(draftYaml);
      dispatch({ type: 'rebase_success', baseline, draft, draftYaml, serverYaml });
      return { ok: true as const };
    } catch (error: unknown) {
      // A failed readback is not a parse failure of the user's retained draft. Keep
      // the whole editor state intact; the document hook blocks editing until recovery.
      const message = error instanceof Error ? error.message : 'Invalid YAML';
      return { ok: false as const, error: message };
    }
  }, []);

  const applyVisualChangesToYaml = useCallback(
    (currentYaml: string, target: 'server' | 'draft' = 'server'): string => {
      try {
        const doc = parseDocument(currentYaml);
        if (doc.errors.length > 0) return currentYaml;
        if (!isMap(doc.contents)) {
          doc.contents = doc.createNode({}) as unknown as typeof doc.contents;
        }
        // YAML has no stable rule IDs. For rebased payload edits, retain the already
        // merged draft's AST as well as its ID snapshot. Inferring lineage solely
        // from server/draft contents is ambiguous (e.g. delete + rename + append),
        // and could otherwise attach unknown fields to the wrong rule/model.
        const payloadBaseline = rebasedPayload?.values ?? baselineValues;
        if (rebasedPayload && hasPayloadDirtyFields(dirtyFields)) {
          const draftDoc = parseDocument(rebasedPayload.yaml);
          PAYLOAD_DIRTY_FIELDS.forEach((field, index) => {
            if (!dirtyFields.has(field)) return;
            const path = ['requests', 'payload', PAYLOAD_SECTIONS[index]];
            const node = draftDoc.getIn(path, true);
            if (node) {
              ensureMapInDoc(doc, ['requests']);
              ensureMapInDoc(doc, ['requests', 'payload']);
              doc.setIn(path, node);
            } else if (doc.hasIn(path)) {
              deletePathInDoc(doc, path);
            }
          });
        }
        const values = visualValues;
        writeVisualAdditions(doc, values, dirtyFields);
        writeVisualServer(
          doc,
          values,
          dirtyFields,
          rebasedPayload?.yaml ?? baselineYaml,
          rebasedPayload?.serverYaml ?? baselineYaml,
          target === 'server'
        );
        if (dirtyFields.has(ICE_KEY)) {
          writeICEServers(
            doc,
            rebasedPayload?.yaml ?? baselineYaml,
            rebasedPayload?.serverYaml ?? baselineYaml,
            payloadBaseline[ICE_KEY],
            values[ICE_KEY],
            target === 'server'
          );
        }
        const shouldWritePluginStoreAuth = dirtyFields.has('pluginStoreAuth');

        // The backend accepts null routing as defaults, but YAML setIn cannot traverse it.
        // Normalize only this legal null section, and only when routing is being edited.
        const routingNode = doc.getIn(['routing'], true);
        if (
          isScalar(routingNode) &&
          routingNode.value === null &&
          [
            'forceModelPrefix',
            'requestRetry',
            'maxRetryCredentials',
            'maxRetryInterval',
            'disableCooling',
            'routingStrategy',
            'routingSessionAffinity',
            'routingSessionAffinityTTL',
          ].some((field) => dirtyFields.has(field as keyof VisualConfigValues))
        ) {
          ensureMapInDoc(doc, ['routing']);
        }

        if (dirtyFields.has('host')) setStringInDoc(doc, ['server', 'host'], values.host);
        if (dirtyFields.has('port')) setIntFromStringInDoc(doc, ['server', 'port'], values.port);

        const tlsDirty =
          dirtyFields.has('tlsEnable') || dirtyFields.has('tlsCert') || dirtyFields.has('tlsKey');
        if (tlsDirty) {
          ensureMapInDoc(doc, ['server', 'tls']);
          if (dirtyFields.has('tlsEnable')) {
            setBooleanInDoc(doc, ['server', 'tls', 'enable'], values.tlsEnable);
          }
          if (dirtyFields.has('tlsCert'))
            setStringInDoc(doc, ['server', 'tls', 'cert'], values.tlsCert);
          if (dirtyFields.has('tlsKey'))
            setStringInDoc(doc, ['server', 'tls', 'key'], values.tlsKey);
          deleteIfMapEmpty(doc, ['server', 'tls']);
        }

        const remoteManagementDirty =
          dirtyFields.has('rmAllowRemote') ||
          dirtyFields.has('rmSecretKey') ||
          dirtyFields.has('rmDisableControlPanel') ||
          dirtyFields.has('rmDisableAutoUpdatePanel') ||
          dirtyFields.has('rmPanelRepo');
        if (remoteManagementDirty) {
          ensureMapInDoc(doc, ['management']);
          if (dirtyFields.has('rmAllowRemote')) {
            setBooleanInDoc(doc, ['management', 'allow-remote'], values.rmAllowRemote);
          }
          if (dirtyFields.has('rmSecretKey')) {
            setStringInDoc(doc, ['management', 'secret-key'], values.rmSecretKey);
          }
          if (dirtyFields.has('rmDisableControlPanel')) {
            setBooleanInDoc(
              doc,
              ['management', 'disable-control-panel'],
              values.rmDisableControlPanel
            );
          }
          if (dirtyFields.has('rmDisableAutoUpdatePanel')) {
            setBooleanInDoc(
              doc,
              ['management', 'disable-auto-update-panel'],
              values.rmDisableAutoUpdatePanel
            );
          }
          if (dirtyFields.has('rmPanelRepo')) {
            setStringInDoc(doc, ['management', 'panel-github-repository'], values.rmPanelRepo);
          }
          deleteIfMapEmpty(doc, ['management']);
        }

        if (dirtyFields.has('authDir')) setStringInDoc(doc, ['oauth', 'auth-dir'], values.authDir);
        if (dirtyFields.has('apiKeysText')) {
          const apiKeys = values.apiKeysText
            .split('\n')
            .map((key) => key.trim())
            .filter(Boolean);
          if (apiKeys.length > 0) {
            doc.setIn(['access', 'api-keys'], apiKeys);
          } else if (docHas(doc, ['access', 'api-keys'])) {
            doc.deleteIn(['access', 'api-keys']);
          }
        }

        const pluginsDirty =
          dirtyFields.has('pluginsEnabled') ||
          dirtyFields.has('pluginStoreSources') ||
          shouldWritePluginStoreAuth;
        if (pluginsDirty) {
          ensureMapInDoc(doc, ['plugins']);
          if (dirtyFields.has('pluginsEnabled')) {
            setBooleanInDoc(doc, ['plugins', 'enabled'], values.pluginsEnabled);
          }
          if (dirtyFields.has('pluginStoreSources')) {
            setStringListInDoc(doc, ['plugins', 'store-sources'], values.pluginStoreSources);
          }
          if (shouldWritePluginStoreAuth) {
            const storeAuth = serializePluginStoreAuthForYaml(values.pluginStoreAuth);
            if (storeAuth.length > 0) {
              doc.setIn(['plugins', 'store-auth'], storeAuth);
            } else if (docHas(doc, ['plugins', 'store-auth'])) {
              doc.deleteIn(['plugins', 'store-auth']);
            }
          }
          deleteIfMapEmpty(doc, ['plugins']);
        }

        if (dirtyFields.has('debug'))
          setBooleanInDoc(doc, ['observability', 'logs', 'debug'], values.debug);
        if (dirtyFields.has('commercialMode')) {
          setBooleanInDoc(doc, ['server', 'commercial-mode'], values.commercialMode);
        }
        if (dirtyFields.has('loggingToFile')) {
          setBooleanInDoc(doc, ['observability', 'logs', 'logging-to-file'], values.loggingToFile);
        }
        if (dirtyFields.has('logsMaxTotalSizeMb')) {
          setIntFromStringInDoc(
            doc,
            ['observability', 'logs', 'logs-max-total-size-mb'],
            values.logsMaxTotalSizeMb
          );
        }
        if (dirtyFields.has('errorLogsMaxFiles')) {
          setIntFromStringInDoc(
            doc,
            ['observability', 'logs', 'error-logs-max-files'],
            values.errorLogsMaxFiles
          );
        }
        if (dirtyFields.has('usageStatisticsEnabled')) {
          setBooleanInDoc(
            doc,
            ['observability', 'usage', 'usage-statistics-enabled'],
            values.usageStatisticsEnabled
          );
        }
        if (dirtyFields.has('redisUsageQueueRetentionSeconds')) {
          setIntFromStringInDoc(
            doc,
            ['observability', 'usage', 'redis-usage-queue-retention-seconds'],
            values.redisUsageQueueRetentionSeconds
          );
        }

        if (dirtyFields.has('proxyUrl'))
          setStringInDoc(doc, ['requests', 'proxy-url'], values.proxyUrl);
        if (dirtyFields.has('forceModelPrefix')) {
          setBooleanInDoc(doc, ['routing', 'force-model-prefix'], values.forceModelPrefix);
        }
        if (dirtyFields.has('passthroughHeaders')) {
          setBooleanInDoc(doc, ['requests', 'passthrough-headers'], values.passthroughHeaders);
        }
        if (dirtyFields.has('requestRetry')) {
          setIntFromStringInDoc(doc, ['routing', 'retry', 'request-retry'], values.requestRetry);
        }
        if (dirtyFields.has('maxRetryCredentials')) {
          setIntFromStringInDoc(
            doc,
            ['routing', 'retry', 'max-retry-credentials'],
            values.maxRetryCredentials
          );
        }
        if (dirtyFields.has('maxRetryInterval')) {
          setIntFromStringInDoc(
            doc,
            ['routing', 'retry', 'max-retry-interval'],
            values.maxRetryInterval
          );
        }
        if (dirtyFields.has('disableCooling')) {
          setBooleanInDoc(doc, ['routing', 'cooldown', 'disable-cooling'], values.disableCooling);
        }
        if (dirtyFields.has('disableImageGeneration')) {
          setDisableImageGenerationInDoc(
            doc,
            ['multimedia', 'disable-image-generation'],
            values.disableImageGeneration
          );
        }
        if (dirtyFields.has('gptImage2BaseModel')) {
          setStringInDoc(doc, ['multimedia', 'gpt-image-2-base-model'], values.gptImage2BaseModel);
        }
        if (dirtyFields.has('authAutoRefreshWorkers')) {
          setIntFromStringInDoc(
            doc,
            ['oauth', 'auth-auto-refresh-workers'],
            values.authAutoRefreshWorkers
          );
        }
        if (dirtyFields.has('wsAuth'))
          setBooleanInDoc(doc, ['oauth', 'providers', 'aistudio', 'ws-auth'], values.wsAuth);
        if (dirtyFields.has('antigravitySensitiveWords')) {
          ensureMapInDoc(doc, ['oauth', 'providers', 'antigravity']);
          setStringListInDoc(
            doc,
            ['oauth', 'providers', 'antigravity', 'sensitive-words'],
            values.antigravitySensitiveWords
          );
          deleteIfMapEmpty(doc, ['oauth', 'providers', 'antigravity']);
        }
        if (dirtyFields.has('devinSensitiveWords')) {
          ensureMapInDoc(doc, ['oauth', 'providers', 'devin']);
          const devin = doc.getIn(['oauth', 'providers', 'devin'], true);
          if (isMap(devin)) {
            syncStringSequence(
              doc,
              devin,
              'sensitive-words',
              baselineValues.devinSensitiveWords,
              values.devinSensitiveWords
            );
          }
          deleteIfMapEmpty(doc, ['oauth', 'providers', 'devin']);
        }
        if (dirtyFields.has('antigravitySignatureCacheEnabled')) {
          if (
            docHas(doc, ['oauth', 'providers', 'antigravity', 'signature-cache-enabled']) ||
            !values.antigravitySignatureCacheEnabled
          ) {
            doc.setIn(
              ['oauth', 'providers', 'antigravity', 'signature-cache-enabled'],
              values.antigravitySignatureCacheEnabled
            );
          }
        }
        if (dirtyFields.has('antigravitySignatureBypassStrict')) {
          setBooleanInDoc(
            doc,
            ['oauth', 'providers', 'antigravity', 'signature-bypass-strict'],
            values.antigravitySignatureBypassStrict
          );
        }

        const claudeHeadersDirty =
          dirtyFields.has('claudeHeaderUserAgent') ||
          dirtyFields.has('claudeHeaderPackageVersion') ||
          dirtyFields.has('claudeHeaderRuntimeVersion') ||
          dirtyFields.has('claudeHeaderOs') ||
          dirtyFields.has('claudeHeaderArch') ||
          dirtyFields.has('claudeHeaderTimeout') ||
          dirtyFields.has('claudeHeaderStabilizeDeviceProfile');
        if (claudeHeadersDirty) {
          ensureMapInDoc(doc, ['upstream', 'claude', 'header-defaults']);
          if (dirtyFields.has('claudeHeaderUserAgent')) {
            setStringInDoc(
              doc,
              ['upstream', 'claude', 'header-defaults', 'user-agent'],
              values.claudeHeaderUserAgent
            );
          }
          if (dirtyFields.has('claudeHeaderPackageVersion')) {
            setStringInDoc(
              doc,
              ['upstream', 'claude', 'header-defaults', 'package-version'],
              values.claudeHeaderPackageVersion
            );
          }
          if (dirtyFields.has('claudeHeaderRuntimeVersion')) {
            setStringInDoc(
              doc,
              ['upstream', 'claude', 'header-defaults', 'runtime-version'],
              values.claudeHeaderRuntimeVersion
            );
          }
          if (dirtyFields.has('claudeHeaderOs')) {
            setStringInDoc(
              doc,
              ['upstream', 'claude', 'header-defaults', 'os'],
              values.claudeHeaderOs
            );
          }
          if (dirtyFields.has('claudeHeaderArch')) {
            setStringInDoc(
              doc,
              ['upstream', 'claude', 'header-defaults', 'arch'],
              values.claudeHeaderArch
            );
          }
          if (dirtyFields.has('claudeHeaderTimeout')) {
            setStringInDoc(
              doc,
              ['upstream', 'claude', 'header-defaults', 'timeout'],
              values.claudeHeaderTimeout
            );
          }
          if (dirtyFields.has('claudeHeaderStabilizeDeviceProfile')) {
            setBooleanInDoc(
              doc,
              ['upstream', 'claude', 'header-defaults', 'stabilize-device-profile'],
              values.claudeHeaderStabilizeDeviceProfile
            );
          }
          deleteIfMapEmpty(doc, ['upstream', 'claude', 'header-defaults']);
        }

        const codexHeadersDirty =
          dirtyFields.has('codexHeaderUserAgent') || dirtyFields.has('codexHeaderBetaFeatures');
        if (codexHeadersDirty) {
          ensureMapInDoc(doc, ['oauth', 'providers', 'codex', 'header-defaults']);
          if (dirtyFields.has('codexHeaderUserAgent')) {
            setStringInDoc(
              doc,
              ['oauth', 'providers', 'codex', 'header-defaults', 'user-agent'],
              values.codexHeaderUserAgent
            );
          }
          if (dirtyFields.has('codexHeaderBetaFeatures')) {
            setStringInDoc(
              doc,
              ['oauth', 'providers', 'codex', 'header-defaults', 'beta-features'],
              values.codexHeaderBetaFeatures
            );
          }
          deleteIfMapEmpty(doc, ['oauth', 'providers', 'codex', 'header-defaults']);
        }

        if (dirtyFields.has('quotaAntigravityCredits')) {
          ensureMapInDoc(doc, ['oauth', 'providers', 'antigravity']);
          doc.setIn(
            ['oauth', 'providers', 'antigravity', 'antigravity-credits'],
            values.quotaAntigravityCredits
          );
        }

        const routingDirty =
          dirtyFields.has('routingStrategy') ||
          dirtyFields.has('routingSessionAffinity') ||
          dirtyFields.has('routingSessionAffinityTTL');
        if (routingDirty) {
          ensureMapInDoc(doc, ['routing']);
          if (dirtyFields.has('routingStrategy')) {
            doc.setIn(['routing', 'strategy'], values.routingStrategy);
          }
          if (dirtyFields.has('routingSessionAffinity')) {
            setBooleanInDoc(doc, ['routing', 'session-affinity'], values.routingSessionAffinity);
          }
          if (dirtyFields.has('routingSessionAffinityTTL')) {
            setStringInDoc(
              doc,
              ['routing', 'session-affinity-ttl'],
              values.routingSessionAffinityTTL
            );
          }
          deleteIfMapEmpty(doc, ['routing']);
        }

        const keepaliveSeconds =
          typeof values.streaming?.keepaliveSeconds === 'string'
            ? values.streaming.keepaliveSeconds
            : '';
        const bootstrapRetries =
          typeof values.streaming?.bootstrapRetries === 'string'
            ? values.streaming.bootstrapRetries
            : '';
        const nonstreamKeepaliveInterval =
          typeof values.streaming?.nonstreamKeepaliveInterval === 'string'
            ? values.streaming.nonstreamKeepaliveInterval
            : '';

        const streamingDirty =
          dirtyFields.has('streaming.keepaliveSeconds') ||
          dirtyFields.has('streaming.bootstrapRetries');
        if (streamingDirty) {
          ensureMapInDoc(doc, ['requests', 'streaming']);
          if (dirtyFields.has('streaming.keepaliveSeconds')) {
            setIntFromStringInDoc(
              doc,
              ['requests', 'streaming', 'keepalive-seconds'],
              keepaliveSeconds
            );
          }
          if (dirtyFields.has('streaming.bootstrapRetries')) {
            setIntFromStringInDoc(
              doc,
              ['requests', 'streaming', 'bootstrap-retries'],
              bootstrapRetries
            );
          }
          deleteIfMapEmpty(doc, ['requests', 'streaming']);
        }

        if (dirtyFields.has('streaming.nonstreamKeepaliveInterval')) {
          setIntFromStringInDoc(
            doc,
            ['requests', 'nonstream-keepalive-interval'],
            nonstreamKeepaliveInterval
          );
        }

        if (hasPayloadDirtyFields(dirtyFields)) {
          ensureMapInDoc(doc, ['requests', 'payload']);
          if (dirtyFields.has('payloadDefaultRules')) {
            syncPayloadRuleSequence(
              doc,
              'default',
              payloadBaseline.payloadDefaultRules,
              values.payloadDefaultRules,
              false
            );
          }
          if (dirtyFields.has('payloadDefaultRawRules')) {
            syncPayloadRuleSequence(
              doc,
              'default-raw',
              payloadBaseline.payloadDefaultRawRules,
              values.payloadDefaultRawRules,
              true
            );
          }
          if (dirtyFields.has('payloadOverrideRules')) {
            syncPayloadRuleSequence(
              doc,
              'override',
              payloadBaseline.payloadOverrideRules,
              values.payloadOverrideRules,
              false
            );
          }
          if (dirtyFields.has('payloadOverrideRawRules')) {
            syncPayloadRuleSequence(
              doc,
              'override-raw',
              payloadBaseline.payloadOverrideRawRules,
              values.payloadOverrideRawRules,
              true
            );
          }
          if (dirtyFields.has('payloadFilterRules')) {
            syncPayloadFilterSequence(
              doc,
              payloadBaseline.payloadFilterRules,
              values.payloadFilterRules
            );
          }
          deleteIfMapEmpty(doc, ['requests', 'payload']);
        }

        const draftYaml = doc.toString({ indent: 2, lineWidth: 120, minContentWidth: 0 });
        if (target === 'server' && rebasedPayload && hasPayloadDirtyFields(dirtyFields)) {
          // The retained AST preserves local lineage, but must never silently replace a
          // list another client changed after recovery (including unknown model fields).
          const paths = PAYLOAD_DIRTY_FIELDS.flatMap((field, index) =>
            dirtyFields.has(field) ? [['requests', 'payload', PAYLOAD_SECTIONS[index]]] : []
          );
          assertConfigListsUnchanged(rebasedPayload.serverYaml, draftYaml, currentYaml, paths);
        }
        return draftYaml;
      } catch (error) {
        if (error instanceof ConfigDraftConflictError) throw error;
        return currentYaml;
      }
    },
    [baselineValues, baselineYaml, dirtyFields, visualValues, rebasedPayload]
  );

  const setVisualValues = useCallback((newValues: Partial<VisualConfigValues>) => {
    dispatch({ type: 'set_values', values: newValues });
  }, []);

  return {
    visualValues,
    visualDirty,
    /** 脏字段的叶值键集合（streaming 为点号叶），供 tab 脏点 / 头部计数消费。 */
    visualDirtyFields: dirtyFields as ReadonlySet<string>,
    visualParseError,
    visualValidationErrors,
    visualHasPayloadValidationErrors,
    loadVisualValuesFromYaml,
    rebaseVisualValuesFromYaml,
    applyVisualChangesToYaml,
    setVisualValues,
  };
}

export const VISUAL_CONFIG_PROTOCOL_OPTIONS = [
  {
    value: '',
    labelKey: 'config_management.visual.payload_rules.provider_default',
    defaultLabel: 'Default',
  },
  {
    value: 'openai',
    labelKey: 'config_management.visual.payload_rules.provider_openai',
    defaultLabel: 'OpenAI',
  },
  {
    value: 'openai-response',
    labelKey: 'config_management.visual.payload_rules.provider_openai_response',
    defaultLabel: 'OpenAI Response',
  },
  {
    value: 'gemini',
    labelKey: 'config_management.visual.payload_rules.provider_gemini',
    defaultLabel: 'Gemini',
  },
  {
    value: 'claude',
    labelKey: 'config_management.visual.payload_rules.provider_claude',
    defaultLabel: 'Claude',
  },
  {
    value: 'codex',
    labelKey: 'config_management.visual.payload_rules.provider_codex',
    defaultLabel: 'Codex',
  },
  {
    value: 'antigravity',
    labelKey: 'config_management.visual.payload_rules.provider_antigravity',
    defaultLabel: 'Antigravity',
  },
] as const;

export const VISUAL_CONFIG_PAYLOAD_VALUE_TYPE_OPTIONS = [
  {
    value: 'string',
    labelKey: 'config_management.visual.payload_rules.value_type_string',
    defaultLabel: 'String',
  },
  {
    value: 'number',
    labelKey: 'config_management.visual.payload_rules.value_type_number',
    defaultLabel: 'Number',
  },
  {
    value: 'boolean',
    labelKey: 'config_management.visual.payload_rules.value_type_boolean',
    defaultLabel: 'Boolean',
  },
  {
    value: 'json',
    labelKey: 'config_management.visual.payload_rules.value_type_json',
    defaultLabel: 'JSON',
  },
] as const satisfies ReadonlyArray<{
  value: PayloadParamValueType;
  labelKey: string;
  defaultLabel: string;
}>;
