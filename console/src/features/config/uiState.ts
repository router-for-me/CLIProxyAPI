// 配置页 UI 状态的纯函数层：状态机、徽章分桶、脏字段归属、localStorage 读取。
// 全部无副作用，由 tests/configUiState.test.ts 覆盖。

import type { VisualConfigValidationErrors } from '@/types/visualConfig';
import {
  COMMON_FIELD_IDS,
  CONFIG_SECTION_IDS,
  CONFIG_TAB_IDS,
  FIELD_VALUE_KEYS,
  SECTION_VALIDATION_FIELDS,
  type ConfigEditorMode,
  type ConfigTabId,
} from './constants';
import { CONFIG_FIELD_SEARCH_INDEX, type VisualSectionId } from './searchIndex';

/** 可视化编辑器暴露的配置项总数（头部 meta 行的「N 项配置」）。 */
export const CONFIG_FIELD_COUNT = CONFIG_FIELD_SEARCH_INDEX.length;

/** 叶值键（= useVisualConfig dirtyFields 的键）→ fieldId 反查表。 */
const VALUE_KEY_TO_FIELD_ID: ReadonlyMap<string, string> = (() => {
  const map = new Map<string, string>();
  for (const [fieldId, valueKeys] of Object.entries(FIELD_VALUE_KEYS)) {
    for (const valueKey of valueKeys) map.set(valueKey, fieldId);
  }
  return map;
})();

const FIELD_ID_TO_SECTION: ReadonlyMap<string, VisualSectionId> = new Map(
  CONFIG_FIELD_SEARCH_INDEX.map((entry) => [entry.fieldId, entry.sectionId])
);

const COMMON_FIELD_ID_SET: ReadonlySet<string> = new Set<string>(COMMON_FIELD_IDS);

/** 常用 tab 渲染的字段对应的叶值键集合（校验错误归属常用 tab 时用）。 */
const COMMON_VALUE_KEYS: ReadonlySet<string> = new Set(
  COMMON_FIELD_IDS.flatMap((fieldId) => [...(FIELD_VALUE_KEYS[fieldId] ?? [])])
);

/** 脏字段集合 → 点亮脏点的 tabs。常用字段同时点亮 common 与其正典分区（两处都渲染它）。 */
export function resolveDirtyTabs(dirtyFields: ReadonlySet<string>): ReadonlySet<ConfigTabId> {
  const tabs = new Set<ConfigTabId>();
  for (const valueKey of dirtyFields) {
    const fieldId = VALUE_KEY_TO_FIELD_ID.get(valueKey);
    if (!fieldId) continue;
    const sectionId = FIELD_ID_TO_SECTION.get(fieldId);
    if (sectionId) tabs.add(sectionId);
    if (COMMON_FIELD_ID_SET.has(fieldId)) tabs.add('common');
  }
  return tabs;
}

/** 每个 tab 的校验错误数（错误徽章）。payload 的校验以旗标计 1。 */
export function countSectionErrors(
  validationErrors: VisualConfigValidationErrors | undefined,
  hasPayloadValidationErrors: boolean
): Record<ConfigTabId, number> {
  const counts = Object.fromEntries(CONFIG_TAB_IDS.map((tabId) => [tabId, 0])) as Record<
    ConfigTabId,
    number
  >;
  for (const sectionId of CONFIG_SECTION_IDS) {
    counts[sectionId] = SECTION_VALIDATION_FIELDS[sectionId].reduce(
      (total, field) => total + (validationErrors?.[field] ? 1 : 0),
      0
    );
  }
  if (hasPayloadValidationErrors) counts.payload += 1;
  counts.common = Object.entries(validationErrors ?? {}).reduce(
    (total, [field, error]) => total + (error && COMMON_VALUE_KEYS.has(field) ? 1 : 0),
    0
  );
  return counts;
}

/** 全页校验错误总数（头部 meta 行）。 */
export function countTotalErrors(
  validationErrors: VisualConfigValidationErrors | undefined,
  hasPayloadValidationErrors: boolean
): number {
  const fieldErrors = Object.values(validationErrors ?? {}).filter(Boolean).length;
  return fieldErrors + (hasPayloadValidationErrors ? 1 : 0);
}

export type ConfigStatusKey =
  | 'disconnected'
  | 'loading'
  | 'load_failed'
  | 'yaml_error'
  | 'validation_blocked'
  | 'saving'
  | 'dirty'
  | 'synced';

export type ConfigStatusTone = 'error' | 'warning' | 'busy' | 'muted' | 'ok';

export type ConfigStatus = {
  key: ConfigStatusKey;
  /** 完整状态文案的 i18n 键。 */
  labelKey: string;
  /** 移动端短文案的 i18n 键。validation_blocked 的短键在 config_management 顶层（历史路径 bug 的修正）。 */
  shortLabelKey: string;
  tone: ConfigStatusTone;
};

export type ConfigStatusInput = {
  disconnected: boolean;
  loading: boolean;
  loadFailed: boolean;
  yamlError: boolean;
  validationBlocked: boolean;
  saving: boolean;
  dirty: boolean;
};

/** 悬浮保存栏 / 状态文案的状态机。优先级自上而下，与旧页 getStatusText 分支序一致。 */
export function resolveStatus(input: ConfigStatusInput): ConfigStatus {
  if (input.disconnected) {
    return {
      key: 'disconnected',
      labelKey: 'config_management.status_disconnected',
      shortLabelKey: 'config_management.status_disconnected_short',
      tone: 'muted',
    };
  }
  if (input.loading) {
    return {
      key: 'loading',
      labelKey: 'config_management.status_loading',
      shortLabelKey: 'config_management.status_loading_short',
      tone: 'busy',
    };
  }
  if (input.loadFailed) {
    return {
      key: 'load_failed',
      labelKey: 'config_management.status_load_failed',
      shortLabelKey: 'config_management.status_load_failed_short',
      tone: 'error',
    };
  }
  if (input.yamlError) {
    return {
      key: 'yaml_error',
      labelKey: 'config_management.visual_mode_unavailable',
      shortLabelKey: 'config_management.visual_mode_unavailable_short',
      tone: 'error',
    };
  }
  if (input.validationBlocked) {
    return {
      key: 'validation_blocked',
      labelKey: 'config_management.visual.validation.validation_blocked',
      shortLabelKey: 'config_management.validation_blocked_short',
      tone: 'error',
    };
  }
  if (input.saving) {
    return {
      key: 'saving',
      labelKey: 'config_management.status_saving',
      shortLabelKey: 'config_management.status_saving_short',
      tone: 'busy',
    };
  }
  if (input.dirty) {
    return {
      key: 'dirty',
      labelKey: 'config_management.status_dirty',
      shortLabelKey: 'config_management.status_dirty_short',
      tone: 'warning',
    };
  }
  return {
    key: 'synced',
    labelKey: 'config_management.status_loaded',
    shortLabelKey: 'config_management.status_loaded_short',
    tone: 'ok',
  };
}

export type HeaderMetaSegment = {
  key: 'fields' | ConfigStatusKey | 'dirty_source' | 'errors';
  labelKey: string;
  count?: number;
  tone: 'muted' | 'warning' | 'error' | 'ok';
};

export type HeaderMetaInput = {
  fieldCount: number;
  status: ConfigStatus;
  dirtyCount: number;
  sourceDirty: boolean;
  errorCount: number;
};

/**
 * 头部 ▍mono meta 行直接消费页面状态机，避免 Header 与保存栏各自推导连接/加载状态。
 * 字段总数常驻；阻断状态优先，编辑状态再补充待保存和校验错误数量。
 */
export function buildHeaderMeta(input: HeaderMetaInput): HeaderMetaSegment[] {
  const segments: HeaderMetaSegment[] = [
    {
      key: 'fields',
      labelKey: 'config_management.meta_fields',
      count: input.fieldCount,
      tone: 'muted',
    },
  ];
  const { status } = input;

  if (
    status.key === 'disconnected' ||
    status.key === 'loading' ||
    status.key === 'load_failed' ||
    status.key === 'saving'
  ) {
    segments.push({
      key: status.key,
      labelKey: status.labelKey,
      tone: status.tone === 'busy' ? 'muted' : status.tone,
    });
    return segments;
  }
  if (status.key === 'yaml_error') {
    segments.push({
      key: status.key,
      labelKey: status.shortLabelKey,
      tone: 'error',
    });
  }
  if (input.sourceDirty) {
    segments.push({
      key: 'dirty_source',
      labelKey: 'config_management.meta_dirty_source',
      tone: 'warning',
    });
  } else if (input.dirtyCount > 0) {
    segments.push({
      key: 'dirty',
      labelKey: 'config_management.meta_dirty',
      count: input.dirtyCount,
      tone: 'warning',
    });
  }
  if (input.errorCount > 0) {
    segments.push({
      key: 'errors',
      labelKey: 'config_management.meta_errors',
      count: input.errorCount,
      tone: 'error',
    });
  }
  if (status.key === 'synced') {
    segments.push({ key: status.key, labelKey: 'config_management.meta_synced', tone: 'ok' });
  }
  return segments;
}

/** localStorage 读取：非法/陈旧值回退默认。 */
export function readSavedMode(raw: string | null): ConfigEditorMode {
  return raw === 'source' ? 'source' : 'visual';
}

export function readSavedSection(raw: string | null): ConfigTabId {
  return raw !== null && (CONFIG_TAB_IDS as readonly string[]).includes(raw)
    ? (raw as ConfigTabId)
    : 'common';
}
