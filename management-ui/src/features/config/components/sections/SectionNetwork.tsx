import { useTranslation } from 'react-i18next';
import type { VisualConfigValues } from '@/types/visualConfig';
import { CONFIG_TAB_ICONS, SECTION_INDEX_LABELS } from '../../constants';
import type { ConfigSectionProps } from '../../types';
import { SectionCard } from '../SectionCard';
import { SelectSetting, SettingList, TextSetting, ToggleSetting } from '../fields/FieldPrimitives';
import { ProxyUrlField } from '../fields/sharedFields';
import { getValidationMessage } from '../blocks/shared';

const Icon = CONFIG_TAB_ICONS.network;

type RoutingStrategy = VisualConfigValues['routingStrategy'];
type DisableImageGeneration = VisualConfigValues['disableImageGeneration'];

/** 02 网络配置：代理、重试、路由策略、图像生成开关与网络行为开关。 */
export function SectionNetwork({
  values,
  validationErrors,
  disabled,
  animateIn,
  onChange,
}: ConfigSectionProps) {
  const { t } = useTranslation();

  const routingStrategyOptions: Array<{ value: RoutingStrategy; label: string }> = [
    {
      value: 'round-robin',
      label: t('config_management.visual.sections.network.strategy_round_robin'),
    },
    {
      value: 'weighted-round-robin',
      label: t('config_management.visual.sections.network.strategy_weighted_round_robin'),
    },
    {
      value: 'fill-first',
      label: t('config_management.visual.sections.network.strategy_fill_first'),
    },
  ];

  const disableImageGenerationOptions: Array<{ value: DisableImageGeneration; label: string }> = [
    {
      value: 'false',
      label: t('config_management.visual.sections.network.disable_image_generation_false'),
    },
    {
      value: 'true',
      label: t('config_management.visual.sections.network.disable_image_generation_true'),
    },
    {
      value: 'chat',
      label: t('config_management.visual.sections.network.disable_image_generation_chat'),
    },
    {
      value: 'passthrough',
      label: t('config_management.visual.sections.network.disable_image_generation_passthrough'),
    },
  ];

  return (
    <SectionCard
      indexLabel={SECTION_INDEX_LABELS.network}
      icon={<Icon size={16} />}
      title={t('config_management.visual.sections.network.title')}
      description={t('config_management.visual.sections.network.description')}
      animateIn={animateIn}
    >
      <SettingList>
        <ProxyUrlField values={values} disabled={disabled} onChange={onChange} wide />
        <TextSetting
          fieldId="requestRetry"
          size="sm"
          label={t('config_management.visual.sections.network.request_retry')}
          type="number"
          placeholder="3"
          value={values.requestRetry}
          onChange={(requestRetry) => onChange({ requestRetry })}
          disabled={disabled}
          error={getValidationMessage(t, validationErrors?.requestRetry)}
        />
        <TextSetting
          fieldId="maxRetryCredentials"
          size="sm"
          label={t('config_management.visual.sections.network.max_retry_credentials')}
          description={t('config_management.visual.sections.network.max_retry_credentials_hint')}
          type="number"
          placeholder="0"
          value={values.maxRetryCredentials}
          onChange={(maxRetryCredentials) => onChange({ maxRetryCredentials })}
          disabled={disabled}
          error={getValidationMessage(t, validationErrors?.maxRetryCredentials)}
        />
        <TextSetting
          fieldId="maxRetryInterval"
          size="sm"
          label={t('config_management.visual.sections.network.max_retry_interval')}
          description={t('config_management.visual.sections.network.max_retry_interval_hint')}
          type="number"
          placeholder="30"
          value={values.maxRetryInterval}
          onChange={(maxRetryInterval) => onChange({ maxRetryInterval })}
          disabled={disabled}
          error={getValidationMessage(t, validationErrors?.maxRetryInterval)}
        />
        <TextSetting
          fieldId="authAutoRefreshWorkers"
          size="sm"
          label={t('config_management.visual.sections.network.auth_auto_refresh_workers')}
          description={t(
            'config_management.visual.sections.network.auth_auto_refresh_workers_hint'
          )}
          type="number"
          placeholder="16"
          value={values.authAutoRefreshWorkers}
          onChange={(authAutoRefreshWorkers) => onChange({ authAutoRefreshWorkers })}
          disabled={disabled}
          error={getValidationMessage(t, validationErrors?.authAutoRefreshWorkers)}
        />
        <SelectSetting
          fieldId="routingStrategy"
          label={t('config_management.visual.sections.network.routing_strategy')}
          description={t('config_management.visual.sections.network.routing_strategy_hint')}
          value={values.routingStrategy}
          options={routingStrategyOptions}
          disabled={disabled}
          onChange={(routingStrategy) => onChange({ routingStrategy })}
        />
        <SelectSetting
          fieldId="disableImageGeneration"
          label={t('config_management.visual.sections.network.disable_image_generation')}
          description={t('config_management.visual.sections.network.disable_image_generation_hint')}
          value={values.disableImageGeneration}
          options={disableImageGenerationOptions}
          disabled={disabled}
          onChange={(disableImageGeneration) => onChange({ disableImageGeneration })}
        />
        <TextSetting
          fieldId="gptImage2BaseModel"
          label={t('config_management.visual.sections.network.gpt_image_2_base_model')}
          description={t('config_management.visual.sections.network.gpt_image_2_base_model_hint')}
          placeholder="gpt-5.4-mini"
          value={values.gptImage2BaseModel}
          onChange={(gptImage2BaseModel) => onChange({ gptImage2BaseModel })}
          disabled={disabled}
        />
        <TextSetting
          fieldId="routingSessionAffinityTTL"
          size="sm"
          label={t('config_management.visual.sections.network.session_affinity_ttl')}
          placeholder="1h"
          value={values.routingSessionAffinityTTL}
          onChange={(routingSessionAffinityTTL) => onChange({ routingSessionAffinityTTL })}
          disabled={disabled}
        />
      </SettingList>

      <SettingList>
        <ToggleSetting
          fieldId="routingSessionAffinitySubagents"
          label={t('config_management.visual.additions.routingSessionAffinitySubagents.label')}
          description={t('config_management.visual.additions.routingSessionAffinitySubagents.hint')}
          checked={values.routingSessionAffinitySubagents}
          disabled={disabled}
          onChange={(routingSessionAffinitySubagents) =>
            onChange({ routingSessionAffinitySubagents })
          }
        />
        <ToggleSetting
          fieldId="saveCooldownStatus"
          label={t('config_management.visual.additions.saveCooldownStatus.label')}
          description={t('config_management.visual.additions.saveCooldownStatus.hint')}
          checked={values.saveCooldownStatus}
          disabled={disabled}
          onChange={(saveCooldownStatus) => onChange({ saveCooldownStatus })}
        />
        <TextSetting
          fieldId="transientErrorCooldownSeconds"
          size="sm"
          label={t('config_management.visual.additions.transientErrorCooldownSeconds.label')}
          description={t('config_management.visual.additions.transientErrorCooldownSeconds.hint')}
          type="number"
          value={values.transientErrorCooldownSeconds}
          onChange={(transientErrorCooldownSeconds) => onChange({ transientErrorCooldownSeconds })}
          disabled={disabled}
          error={getValidationMessage(t, validationErrors?.transientErrorCooldownSeconds)}
        />
        <TextSetting
          fieldId="videoResultAuthCacheTTL"
          size="sm"
          label={t('config_management.visual.additions.videoResultAuthCacheTTL.label')}
          description={t('config_management.visual.additions.videoResultAuthCacheTTL.hint')}
          value={values.videoResultAuthCacheTTL}
          onChange={(videoResultAuthCacheTTL) => onChange({ videoResultAuthCacheTTL })}
          disabled={disabled}
          error={getValidationMessage(t, validationErrors?.videoResultAuthCacheTTL)}
        />
        <ToggleSetting
          fieldId="forceModelPrefix"
          label={t('config_management.visual.sections.network.force_model_prefix')}
          description={t('config_management.visual.sections.network.force_model_prefix_desc')}
          checked={values.forceModelPrefix}
          disabled={disabled}
          onChange={(forceModelPrefix) => onChange({ forceModelPrefix })}
        />
        <ToggleSetting
          fieldId="passthroughHeaders"
          label={t('config_management.visual.sections.network.passthrough_headers')}
          description={t('config_management.visual.sections.network.passthrough_headers_desc')}
          checked={values.passthroughHeaders}
          disabled={disabled}
          onChange={(passthroughHeaders) => onChange({ passthroughHeaders })}
        />
        <ToggleSetting
          fieldId="disableCooling"
          label={t('config_management.visual.sections.network.disable_cooling')}
          description={t('config_management.visual.sections.network.disable_cooling_desc')}
          checked={values.disableCooling}
          disabled={disabled}
          onChange={(disableCooling) => onChange({ disableCooling })}
        />
        <ToggleSetting
          fieldId="routingSessionAffinity"
          label={t('config_management.visual.sections.network.session_affinity')}
          checked={values.routingSessionAffinity}
          disabled={disabled}
          onChange={(routingSessionAffinity) => onChange({ routingSessionAffinity })}
        />
        <ToggleSetting
          fieldId="wsAuth"
          label={t('config_management.visual.sections.network.ws_auth')}
          description={t('config_management.visual.sections.network.ws_auth_desc')}
          checked={values.wsAuth}
          disabled={disabled}
          onChange={(wsAuth) => onChange({ wsAuth })}
        />
      </SettingList>
    </SectionCard>
  );
}
