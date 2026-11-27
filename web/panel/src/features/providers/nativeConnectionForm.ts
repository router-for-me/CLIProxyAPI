import type { ProviderKeyConfig } from '@/types';
import {
  memberWireFields,
  type MemberField,
  type MemberMode,
  type MemberModes,
} from '@/services/api/nativeProviderGroups';
import { isRecord } from '@/utils/helpers';
import type { NativeProviderFamily } from '@/services/api/providers';
import { normalizeConnectionPool } from '@/services/api/connectionPool';
import { buildNativeGroupSettings, readNativeGroupSettings } from './nativeGroupSettings';
import type { ProviderEntryFormInput, ProviderResource } from './types';

export interface NativeConnectionFormOutput {
  group: Record<string, unknown>;
  /** Group-wide on/off switch; omitted when the user did not touch it. */
  allDisabled?: boolean;
  keys: Array<{
    resource: ProviderResource | null;
    input: ProviderEntryFormInput;
    unchanged?: boolean;
    initialInput?: ProviderEntryFormInput;
    /** Desired state for a new key, or an explicit switch change for an existing key. */
    disabled?: boolean;
  }>;
}

type MemberRowDraft = {
  resource: ProviderResource | null;
  initial: ProviderEntryFormInput;
  input: ProviderEntryFormInput;
  disabled?: boolean;
};

export function memberDisabled(row: MemberRowDraft, groupDisabled: boolean, allDisabled?: boolean) {
  const inherits = row.input.memberModes?.excludedModelsText === 'inherit';
  return (
    row.disabled ??
    allDisabled ??
    (row.resource && !inherits ? row.initial.disabled : groupDisabled)
  );
}

export function nativeSwitchesDirty(
  rows: MemberRowDraft[],
  groupDisabled: boolean,
  allDisabled?: boolean
) {
  return (
    (allDisabled !== undefined && allDisabled !== groupDisabled) ||
    rows.some(
      (row) =>
        memberDisabled(row, groupDisabled, allDisabled) !==
        (row.resource ? row.initial.disabled : groupDisabled)
    )
  );
}

export function buildSubmittedMember(
  row: MemberRowDraft,
  unchanged: boolean,
  groupDisabled: boolean,
  allDisabled?: boolean
): NativeConnectionFormOutput['keys'][number] {
  return {
    resource: row.resource,
    input: row.input,
    initialInput: row.initial,
    unchanged: !!row.resource && unchanged,
    // A new key's own exclusions replace the group's list, including its disable rule.
    disabled: row.resource ? row.disabled : memberDisabled(row, groupDisabled, allDisabled),
  };
}

const emptyMemberValues = {
  proxyUrl: '',
  prefix: '',
  priority: undefined,
  headers: [],
  models: [],
  excludedModelsText: '',
};
export const memberFields = Object.keys(memberWireFields) as MemberField[];

export function readMemberForm(form: ProviderEntryFormInput, resource: ProviderResource | null) {
  const source = (resource?.raw as ProviderKeyConfig | undefined)?.source;
  const keys = source?.group.keys;
  const raw = Array.isArray(keys) && source?.keyIndex !== undefined ? keys[source.keyIndex] : null;
  const memberModes = {} as MemberModes;
  const result = { ...form, memberModes };
  for (const field of memberFields) {
    const value = isRecord(raw) ? raw[memberWireFields[field]] : undefined;
    const empty =
      value === '' ||
      (Array.isArray(value) &&
        value.every((item) => field === 'excludedModelsText' && String(item).trim() === '*')) ||
      (isRecord(value) && !Object.keys(value).length);
    memberModes[field] = value == null ? 'inherit' : empty ? 'empty' : 'override';
    if (field === 'headers' && isRecord(value))
      result.headers = Object.entries(value).map(([key, v]) => ({ key, value: String(v) }));
    if (memberModes[field] !== 'override')
      Object.assign(result, { [field]: emptyMemberValues[field] });
  }
  return result;
}

export function setMemberMode(
  form: ProviderEntryFormInput,
  field: MemberField,
  mode: MemberMode
): ProviderEntryFormInput {
  if (!form.memberModes || form.memberModes[field] === mode) return form;
  return {
    ...form,
    [field]: field === 'priority' && mode === 'override' ? 0 : emptyMemberValues[field],
    memberModes: { ...form.memberModes, [field]: mode },
  };
}

/** Both probe surfaces resolve the exact same member against the live group draft. */
export function memberProbeSettings(shared: ProviderEntryFormInput, row?: MemberRowDraft) {
  const effective = <K extends 'proxyUrl' | 'headers' | 'models'>(field: K) =>
    row?.input.memberModes && row.input.memberModes[field] !== 'inherit'
      ? row.input[field]
      : shared[field];
  return {
    probeApiKey:
      row?.input.apiKey.trim() ||
      (row?.resource?.raw as ProviderKeyConfig | undefined)?.apiKey ||
      '',
    probeProxyUrl: effective('proxyUrl'),
    probeHeaders: effective('headers'),
    probeModels: effective('models'),
  };
}

export function readSharedForm(
  group: Record<string, unknown>,
  defaults: ProviderEntryFormInput
): ProviderEntryFormInput {
  const shared = readNativeGroupSettings(group);
  return {
    ...defaults,
    name: shared.name,
    baseUrl: shared.baseUrl,
    prefix: shared.prefix,
    proxyUrl: shared.proxyUrl,
    priority: shared.priority ? Number(shared.priority) : undefined,
    headers: Object.entries(group.headers ?? {}).map(([key, value]) => ({
      key,
      value: String(value),
    })),
    models: shared.models,
    runtimePolicy: shared.policy,
    connectionPool: normalizeConnectionPool(group['connection-pool']),
    disabled: shared.excludedModels.split('\n').some((rule) => rule.trim() === '*'),
    excludedModelsText: shared.excludedModels
      .split('\n')
      .filter((rule) => rule.trim() !== '*')
      .join('\n'),
  };
}

/** Only project controls the user changed; untouched raw defaults can contain future metadata. */
export function buildSharedSettings(
  family: NativeProviderFamily,
  group: Record<string, unknown>,
  initial: ProviderEntryFormInput,
  form: ProviderEntryFormInput
) {
  const draft = readNativeGroupSettings(group);
  for (const field of ['name', 'baseUrl', 'prefix', 'proxyUrl'] as const)
    draft[field] = form[field];
  if (form.priority !== initial.priority) draft.priority = String(form.priority ?? '');
  if (form.headers !== initial.headers)
    draft.headers = JSON.stringify(
      Object.fromEntries(
        form.headers
          .filter((entry) => entry.key.trim())
          .map((entry) => [entry.key.trim(), entry.value])
      )
    );
  if (form.models !== initial.models)
    draft.models = form.models.filter((entry) => entry.name.trim() || entry.alias?.trim());
  if (form.disabled !== initial.disabled || form.excludedModelsText !== initial.excludedModelsText)
    draft.excludedModels = [form.excludedModelsText, form.disabled ? '*' : '']
      .filter(Boolean)
      .join('\n');
  if (form.runtimePolicy) draft.policy = form.runtimePolicy;
  if (form.connectionPool !== initial.connectionPool) {
    const pool = form.connectionPool;
    draft.poolPresent = !!pool;
    draft.poolEnabled = String(pool?.enabled ?? '');
    draft.poolTimeout = pool?.idleConnTimeout ?? '';
    draft.poolLimit = String(pool?.maxIdleConnsPerHost ?? '');
  }
  return buildNativeGroupSettings(group, draft, family);
}
