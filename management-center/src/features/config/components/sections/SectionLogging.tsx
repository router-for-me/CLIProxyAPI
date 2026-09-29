import { useTranslation } from 'react-i18next';
import { Input } from '@/components/ui/Input';
import { CONFIG_TAB_ICONS, SECTION_INDEX_LABELS } from '../../constants';
import type { ConfigSectionProps } from '../../types';
import { SectionCard } from '../SectionCard';
import { FieldAnchor, FieldGrid, FieldStack, ToggleRow } from '../fields/FieldPrimitives';
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
  const logsMaxSizeError = getValidationMessage(t, validationErrors?.logsMaxTotalSizeMb);
  const errorLogsMaxFilesError = getValidationMessage(t, validationErrors?.errorLogsMaxFiles);
  const redisUsageQueueRetentionError = getValidationMessage(
    t,
    validationErrors?.redisUsageQueueRetentionSeconds
  );

  return (
    <SectionCard
      indexLabel={SECTION_INDEX_LABELS.logging}
      icon={<Icon size={16} />}
      title={t('config_management.visual.sections.logging.title')}
      description={t('config_management.visual.sections.logging.description')}
      animateIn={animateIn}
    >
      <FieldStack>
        <FieldGrid>
          <DebugToggle values={values} disabled={disabled} onChange={onChange} />
          <FieldAnchor fieldId="commercialMode">
            <ToggleRow
              title={t('config_management.visual.sections.system.commercial_mode')}
              description={t('config_management.visual.sections.system.commercial_mode_desc')}
              checked={values.commercialMode}
              disabled={disabled}
              onChange={(commercialMode) => onChange({ commercialMode })}
            />
          </FieldAnchor>
          <LoggingToFileToggle values={values} disabled={disabled} onChange={onChange} />
        </FieldGrid>

        <FieldGrid>
          <FieldAnchor fieldId="logsMaxTotalSizeMb">
            <Input
              label={t('config_management.visual.sections.system.logs_max_size')}
              type="number"
              placeholder="0"
              value={values.logsMaxTotalSizeMb}
              onChange={(e) => onChange({ logsMaxTotalSizeMb: e.target.value })}
              disabled={disabled}
              error={logsMaxSizeError}
            />
          </FieldAnchor>
          <FieldAnchor fieldId="errorLogsMaxFiles">
            <Input
              label={t('config_management.visual.sections.system.error_logs_max_files')}
              type="number"
              placeholder="10"
              value={values.errorLogsMaxFiles}
              onChange={(e) => onChange({ errorLogsMaxFiles: e.target.value })}
              disabled={disabled}
              error={errorLogsMaxFilesError}
            />
          </FieldAnchor>
          <FieldAnchor fieldId="redisUsageQueueRetentionSeconds">
            <Input
              label={t('config_management.visual.sections.system.redis_usage_retention')}
              type="number"
              min={1}
              max={3600}
              placeholder="60"
              value={values.redisUsageQueueRetentionSeconds}
              onChange={(e) => onChange({ redisUsageQueueRetentionSeconds: e.target.value })}
              disabled={disabled}
              hint={t('config_management.visual.sections.system.redis_usage_retention_hint')}
              error={redisUsageQueueRetentionError}
            />
          </FieldAnchor>
        </FieldGrid>

        <FieldGrid>
          <FieldAnchor fieldId="usageStatisticsEnabled">
            <ToggleRow
              title={t('config_management.visual.sections.system.usage_statistics_enabled')}
              description={t(
                'config_management.visual.sections.system.usage_statistics_enabled_desc'
              )}
              checked={values.usageStatisticsEnabled}
              disabled={disabled}
              onChange={(usageStatisticsEnabled) => onChange({ usageStatisticsEnabled })}
            />
          </FieldAnchor>
        </FieldGrid>
      </FieldStack>
    </SectionCard>
  );
}
