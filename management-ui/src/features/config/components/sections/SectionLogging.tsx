import { useTranslation } from 'react-i18next';
import { CONFIG_TAB_ICONS, SECTION_INDEX_LABELS } from '../../constants';
import type { ConfigSectionProps } from '../../types';
import { SectionCard } from '../SectionCard';
import { SettingList, TextSetting, ToggleSetting } from '../fields/FieldPrimitives';
import { DebugToggle, LoggingToFileToggle } from '../fields/sharedFields';
import { getValidationMessage } from '../blocks/shared';

const Icon = CONFIG_TAB_ICONS.logging;

/** 03 日志与诊断：调试、商业模式（重启生效）、日志输出与使用统计。 */
export function SectionLogging({
  values,
  validationErrors,
  disabled,
  animateIn,
  onChange,
}: ConfigSectionProps) {
  const { t } = useTranslation();
  const fieldProps = { values, disabled, onChange };

  return (
    <SectionCard
      indexLabel={SECTION_INDEX_LABELS.logging}
      icon={<Icon size={16} />}
      title={t('config_management.visual.sections.logging.title')}
      description={t('config_management.visual.sections.logging.description')}
      animateIn={animateIn}
    >
      <SettingList>
        <DebugToggle {...fieldProps} />
        <ToggleSetting
          fieldId="commercialMode"
          label={t('config_management.visual.sections.system.commercial_mode')}
          description={t('config_management.visual.sections.system.commercial_mode_desc')}
          checked={values.commercialMode}
          disabled={disabled}
          onChange={(commercialMode) => onChange({ commercialMode })}
        />
        <LoggingToFileToggle {...fieldProps} />
      </SettingList>

      <SettingList>
        <TextSetting
          fieldId="logsMaxTotalSizeMb"
          size="sm"
          label={t('config_management.visual.sections.system.logs_max_size')}
          type="number"
          placeholder="0"
          value={values.logsMaxTotalSizeMb}
          onChange={(logsMaxTotalSizeMb) => onChange({ logsMaxTotalSizeMb })}
          disabled={disabled}
          error={getValidationMessage(t, validationErrors?.logsMaxTotalSizeMb)}
        />
        <TextSetting
          fieldId="errorLogsMaxFiles"
          size="sm"
          label={t('config_management.visual.sections.system.error_logs_max_files')}
          type="number"
          placeholder="10"
          value={values.errorLogsMaxFiles}
          onChange={(errorLogsMaxFiles) => onChange({ errorLogsMaxFiles })}
          disabled={disabled}
          error={getValidationMessage(t, validationErrors?.errorLogsMaxFiles)}
        />
        <TextSetting
          fieldId="redisUsageQueueRetentionSeconds"
          size="sm"
          label={t('config_management.visual.sections.system.redis_usage_retention')}
          description={t('config_management.visual.sections.system.redis_usage_retention_hint')}
          type="number"
          min={1}
          max={3600}
          placeholder="60"
          value={values.redisUsageQueueRetentionSeconds}
          onChange={(redisUsageQueueRetentionSeconds) =>
            onChange({ redisUsageQueueRetentionSeconds })
          }
          disabled={disabled}
          error={getValidationMessage(t, validationErrors?.redisUsageQueueRetentionSeconds)}
        />
      </SettingList>

      <SettingList>
        <ToggleSetting
          fieldId="usageStatisticsEnabled"
          label={t('config_management.visual.sections.system.usage_statistics_enabled')}
          description={t('config_management.visual.sections.system.usage_statistics_enabled_desc')}
          checked={values.usageStatisticsEnabled}
          disabled={disabled}
          onChange={(usageStatisticsEnabled) => onChange({ usageStatisticsEnabled })}
        />
      </SettingList>
    </SectionCard>
  );
}
