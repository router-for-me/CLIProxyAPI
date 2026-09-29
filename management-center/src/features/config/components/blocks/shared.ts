// 区块编辑器共享的纯工具（载荷/规则部分从旧 VisualConfigEditorBlocks 原样迁出，
// 独立成文件以规避组件文件导出非组件的 react-refresh 限制）。

import type { useTranslation } from 'react-i18next';
import type {
  PayloadModelEntry,
  PayloadParamValidationErrorCode,
  VisualConfigValidationErrorCode,
} from '@/types/visualConfig';
import { VISUAL_CONFIG_PROTOCOL_OPTIONS } from '@/hooks/useVisualConfig';

export function getValidationMessage(
  t: ReturnType<typeof useTranslation>['t'],
  errorCode?: VisualConfigValidationErrorCode | PayloadParamValidationErrorCode
) {
  if (!errorCode) return undefined;
  return t(`config_management.visual.validation.${errorCode}`);
}

export function buildProtocolOptions(
  t: ReturnType<typeof useTranslation>['t'],
  rules: Array<{ models: PayloadModelEntry[] }>
) {
  const options: Array<{ value: string; label: string }> = VISUAL_CONFIG_PROTOCOL_OPTIONS.map(
    (option) => ({
      value: option.value,
      label: t(option.labelKey, { defaultValue: option.defaultLabel }),
    })
  );
  const seen = new Set<string>(options.map((option) => option.value));

  for (const rule of rules) {
    for (const model of rule.models) {
      const protocol = model.protocol;
      if (!protocol || !protocol.trim() || seen.has(protocol)) continue;
      seen.add(protocol);
      options.push({ value: protocol, label: protocol });
    }
  }

  return options;
}

/** API Key 强度条相邻两段点亮的间隔；4 段全亮 = 135ms + 段内 160ms，整组仍在 300ms 内 */
export const SEGMENT_STAGGER_MS = 45;

/**
 * 强度条段填充的起跑延迟：只有本次新增的段排队，已亮的段和熄灭都不延迟。
 * 因此「生成」一次点亮四段是依次的波，而键入让强度 +1 段是即时的。
 */
export function segmentFillDelayMs(
  index: number,
  segments: number,
  previousSegments: number
): number {
  const filled = index < segments;
  if (!filled || index < previousSegments) return 0;
  return (index - Math.max(previousSegments, 0)) * SEGMENT_STAGGER_MS;
}
