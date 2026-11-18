import type { VisualConfigValidationErrors, VisualConfigValues } from '@/types/visualConfig';

/** 分区组件的统一签名：受控于 useVisualConfig 的表单值 + 补丁式 onChange。 */
export type ConfigSectionProps = {
  values: VisualConfigValues;
  validationErrors?: VisualConfigValidationErrors;
  disabled: boolean;
  /** 仅首载入场为 true（页面挂载时捕获），tab 切换不重播。 */
  animateIn?: boolean;
  onChange: (patch: Partial<VisualConfigValues>) => void;
};
