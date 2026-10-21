import { useTranslation } from 'react-i18next';
import { CONFIG_TAB_ICONS, SECTION_INDEX_LABELS } from '../../constants';
import type { ConfigSectionProps } from '../../types';
import { SectionCard } from '../SectionCard';
import { SettingList, TextSetting } from '../fields/FieldPrimitives';
import { getValidationMessage } from '../blocks/shared';

const Icon = CONFIG_TAB_ICONS.streaming;

/** 04 流式传输：keepalive 与 bootstrap 重试；nonstream-keepalive-interval 是顶层 YAML 键。 */
export function SectionStreaming({
  values,
  validationErrors,
  disabled,
  animateIn,
  onChange,
}: ConfigSectionProps) {
  const { t } = useTranslation();
  const { streaming } = values;
  const keepaliveError = getValidationMessage(t, validationErrors?.['streaming.keepaliveSeconds']);
  const nonstreamKeepaliveError = getValidationMessage(
    t,
    validationErrors?.['streaming.nonstreamKeepaliveInterval']
  );
  // 0 / 空即关闭：输入框内右侧标「已禁用」，校验出错时让位给错误信息。
  const disabledBadge = t('config_management.visual.sections.streaming.disabled');
  const isKeepaliveOff = !keepaliveError && Number(streaming.keepaliveSeconds) <= 0;
  const isNonstreamKeepaliveOff =
    !nonstreamKeepaliveError && Number(streaming.nonstreamKeepaliveInterval) <= 0;

  return (
    <SectionCard
      indexLabel={SECTION_INDEX_LABELS.streaming}
      icon={<Icon size={16} />}
      title={t('config_management.visual.sections.streaming.title')}
      description={t('config_management.visual.sections.streaming.description')}
      animateIn={animateIn}
    >
      <SettingList>
        <TextSetting
          fieldId="streamingKeepaliveSeconds"
          size="sm"
          label={t('config_management.visual.sections.streaming.keepalive_seconds')}
          description={t('config_management.visual.sections.streaming.keepalive_hint')}
          type="number"
          placeholder="0"
          value={streaming.keepaliveSeconds}
          onChange={(keepaliveSeconds) =>
            onChange({ streaming: { ...streaming, keepaliveSeconds } })
          }
          disabled={disabled}
          error={keepaliveError}
          adornment={isKeepaliveOff ? disabledBadge : undefined}
        />
        <TextSetting
          fieldId="streamingBootstrapRetries"
          size="sm"
          label={t('config_management.visual.sections.streaming.bootstrap_retries')}
          description={t('config_management.visual.sections.streaming.bootstrap_hint')}
          type="number"
          placeholder="1"
          value={streaming.bootstrapRetries}
          onChange={(bootstrapRetries) =>
            onChange({ streaming: { ...streaming, bootstrapRetries } })
          }
          disabled={disabled}
          error={getValidationMessage(t, validationErrors?.['streaming.bootstrapRetries'])}
        />
      </SettingList>

      <SettingList>
        <TextSetting
          fieldId="streamingNonstreamKeepalive"
          size="sm"
          label={t('config_management.visual.sections.streaming.nonstream_keepalive')}
          description={t('config_management.visual.sections.streaming.nonstream_keepalive_hint')}
          type="number"
          placeholder="0"
          value={streaming.nonstreamKeepaliveInterval}
          onChange={(nonstreamKeepaliveInterval) =>
            onChange({ streaming: { ...streaming, nonstreamKeepaliveInterval } })
          }
          disabled={disabled}
          error={nonstreamKeepaliveError}
          adornment={isNonstreamKeepaliveOff ? disabledBadge : undefined}
        />
      </SettingList>
    </SectionCard>
  );
}
