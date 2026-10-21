import { useTranslation } from 'react-i18next';
import { CONFIG_TAB_ICONS } from '../../constants';
import type { ConfigSectionProps } from '../../types';
import { getValidationMessage } from '../blocks/shared';
import { SectionCard } from '../SectionCard';
import { SettingList } from '../fields/FieldPrimitives';
import {
  ApiKeysField,
  DebugToggle,
  HostField,
  LoggingToFileToggle,
  PortField,
  ProxyUrlField,
} from '../fields/sharedFields';

const Icon = CONFIG_TAB_ICONS.common;

/**
 * 「常用」tab：原简单模式的 6 个高频字段，别名视图（不占分区序号）。
 * 渲染源与正典分区共享（sharedFields），数据同为 useVisualConfig 一份状态。
 */
export function SectionCommon({
  values,
  validationErrors,
  disabled,
  animateIn,
  onChange,
}: ConfigSectionProps) {
  const { t } = useTranslation();
  const portError = getValidationMessage(t, validationErrors?.port);
  const fieldProps = { values, disabled, onChange };

  return (
    <SectionCard
      icon={<Icon size={16} />}
      title={t('config_management.visual.sections.common.title')}
      description={t('config_management.visual.sections.common.description')}
      animateIn={animateIn}
    >
      <SettingList>
        <HostField {...fieldProps} />
        <PortField {...fieldProps} error={portError} />
        <ProxyUrlField {...fieldProps} wide />
      </SettingList>

      <ApiKeysField {...fieldProps} />

      <SettingList>
        <DebugToggle {...fieldProps} />
        <LoggingToFileToggle {...fieldProps} />
      </SettingList>
    </SectionCard>
  );
}
