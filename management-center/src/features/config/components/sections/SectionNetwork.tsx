import { useId } from 'react';
import { useTranslation } from 'react-i18next';
import { Input } from '@/components/ui/Input';
import { Select } from '@/components/ui/Select';
import type { VisualConfigValues } from '@/types/visualConfig';
import { CONFIG_TAB_ICONS, SECTION_INDEX_LABELS } from '../../constants';
import type { ConfigSectionProps } from '../../types';
import { SectionCard } from '../SectionCard';
import {
  FieldAnchor,
  FieldGrid,
  FieldShell,
  FieldStack,
  ToggleRow,
} from '../fields/FieldPrimitives';
import { ProxyUrlField, SponsorHintSpacer } from '../fields/sharedFields';
import { getValidationMessage } from '../blocks/shared';

const Icon = CONFIG_TAB_ICONS.network;

/** 02 网络配置：代理、重试、路由策略、图像生成开关与网络行为开关。 */
export function SectionNetwork({
  values,
  validationErrors,
  disabled,
  animateIn,
  onChange,
}: ConfigSectionProps) {
  const { t } = useTranslation();
  const routingStrategyLabelId = useId();
  const routingStrategyHintId = `${routingStrategyLabelId}-hint`;
  const disableImageGenerationLabelId = useId();
  const disableImageGenerationHintId = `${disableImageGenerationLabelId}-hint`;

  const requestRetryError = getValidationMessage(t, validationErrors?.requestRetry);
  const maxRetryCredentialsError = getValidationMessage(t, validationErrors?.maxRetryCredentials);
  const maxRetryIntervalError = getValidationMessage(t, validationErrors?.maxRetryInterval);
  const authAutoRefreshWorkersError = getValidationMessage(
    t,
    validationErrors?.authAutoRefreshWorkers
  );

  const disableImageGenerationOptions = [
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
      <FieldStack>
        <FieldGrid>
          <ProxyUrlField values={values} disabled={disabled} onChange={onChange} />
          <FieldAnchor fieldId="requestRetry">
            <Input
              label={t('config_management.visual.sections.network.request_retry')}
              topExtra={<SponsorHintSpacer />}
              type="number"
              placeholder="3"
              value={values.requestRetry}
              onChange={(e) => onChange({ requestRetry: e.target.value })}
              disabled={disabled}
              error={requestRetryError}
            />
          </FieldAnchor>
          <FieldAnchor fieldId="maxRetryCredentials">
            <Input
              label={t('config_management.visual.sections.network.max_retry_credentials')}
              topExtra={<SponsorHintSpacer />}
              type="number"
              placeholder="0"
              value={values.maxRetryCredentials}
              onChange={(e) => onChange({ maxRetryCredentials: e.target.value })}
              disabled={disabled}
              hint={t('config_management.visual.sections.network.max_retry_credentials_hint')}
              error={maxRetryCredentialsError}
            />
          </FieldAnchor>
          <FieldAnchor fieldId="maxRetryInterval">
            <Input
              label={t('config_management.visual.sections.network.max_retry_interval')}
              type="number"
              placeholder="30"
              value={values.maxRetryInterval}
              onChange={(e) => onChange({ maxRetryInterval: e.target.value })}
              disabled={disabled}
              hint={t('config_management.visual.sections.network.max_retry_interval_hint')}
              error={maxRetryIntervalError}
            />
          </FieldAnchor>
          <FieldAnchor fieldId="authAutoRefreshWorkers">
            <Input
              label={t('config_management.visual.sections.network.auth_auto_refresh_workers')}
              type="number"
              placeholder="16"
              value={values.authAutoRefreshWorkers}
              onChange={(e) => onChange({ authAutoRefreshWorkers: e.target.value })}
              disabled={disabled}
              hint={t('config_management.visual.sections.network.auth_auto_refresh_workers_hint')}
              error={authAutoRefreshWorkersError}
            />
          </FieldAnchor>
          <FieldAnchor fieldId="routingStrategy">
            <FieldShell
              label={t('config_management.visual.sections.network.routing_strategy')}
              labelId={routingStrategyLabelId}
              hint={t('config_management.visual.sections.network.routing_strategy_hint')}
              hintId={routingStrategyHintId}
            >
              <Select
                value={values.routingStrategy}
                options={[
                  {
                    value: 'round-robin',
                    label: t('config_management.visual.sections.network.strategy_round_robin'),
                  },
                  {
                    value: 'weighted-round-robin',
                    label: t(
                      'config_management.visual.sections.network.strategy_weighted_round_robin'
                    ),
                  },
                  {
                    value: 'fill-first',
                    label: t('config_management.visual.sections.network.strategy_fill_first'),
                  },
                ]}
                id={`${routingStrategyLabelId}-select`}
                disabled={disabled}
                ariaLabelledBy={routingStrategyLabelId}
                ariaDescribedBy={routingStrategyHintId}
                onChange={(nextValue) =>
                  onChange({
                    routingStrategy: nextValue as VisualConfigValues['routingStrategy'],
                  })
                }
              />
            </FieldShell>
          </FieldAnchor>
          <FieldAnchor fieldId="disableImageGeneration">
            <FieldShell
              label={t('config_management.visual.sections.network.disable_image_generation')}
              labelId={disableImageGenerationLabelId}
              hint={t('config_management.visual.sections.network.disable_image_generation_hint')}
              hintId={disableImageGenerationHintId}
            >
              <Select
                value={values.disableImageGeneration}
                options={disableImageGenerationOptions}
                id={`${disableImageGenerationLabelId}-select`}
                disabled={disabled}
                ariaLabelledBy={disableImageGenerationLabelId}
                ariaDescribedBy={disableImageGenerationHintId}
                onChange={(nextValue) =>
                  onChange({
                    disableImageGeneration:
                      nextValue as VisualConfigValues['disableImageGeneration'],
                  })
                }
              />
            </FieldShell>
          </FieldAnchor>
          <FieldAnchor fieldId="gptImage2BaseModel">
            <Input
              label={t('config_management.visual.sections.network.gpt_image_2_base_model')}
              placeholder="gpt-5.4-mini"
              value={values.gptImage2BaseModel}
              onChange={(e) => onChange({ gptImage2BaseModel: e.target.value })}
              disabled={disabled}
              hint={t('config_management.visual.sections.network.gpt_image_2_base_model_hint')}
            />
          </FieldAnchor>
          <FieldAnchor fieldId="routingSessionAffinityTTL">
            <Input
              label={t('config_management.visual.sections.network.session_affinity_ttl')}
              placeholder="1h"
              value={values.routingSessionAffinityTTL}
              onChange={(e) => onChange({ routingSessionAffinityTTL: e.target.value })}
              disabled={disabled}
            />
          </FieldAnchor>
        </FieldGrid>

        <FieldGrid>
          <FieldAnchor fieldId="forceModelPrefix">
            <ToggleRow
              title={t('config_management.visual.sections.network.force_model_prefix')}
              description={t('config_management.visual.sections.network.force_model_prefix_desc')}
              checked={values.forceModelPrefix}
              disabled={disabled}
              onChange={(forceModelPrefix) => onChange({ forceModelPrefix })}
            />
          </FieldAnchor>
          <FieldAnchor fieldId="passthroughHeaders">
            <ToggleRow
              title={t('config_management.visual.sections.network.passthrough_headers')}
              description={t('config_management.visual.sections.network.passthrough_headers_desc')}
              checked={values.passthroughHeaders}
              disabled={disabled}
              onChange={(passthroughHeaders) => onChange({ passthroughHeaders })}
            />
          </FieldAnchor>
          <FieldAnchor fieldId="disableCooling">
            <ToggleRow
              title={t('config_management.visual.sections.network.disable_cooling')}
              description={t('config_management.visual.sections.network.disable_cooling_desc')}
              checked={values.disableCooling}
              disabled={disabled}
              onChange={(disableCooling) => onChange({ disableCooling })}
            />
          </FieldAnchor>
          <FieldAnchor fieldId="routingSessionAffinity">
            <ToggleRow
              title={t('config_management.visual.sections.network.session_affinity')}
              checked={values.routingSessionAffinity}
              disabled={disabled}
              onChange={(routingSessionAffinity) => onChange({ routingSessionAffinity })}
            />
          </FieldAnchor>
          <FieldAnchor fieldId="wsAuth">
            <ToggleRow
              title={t('config_management.visual.sections.network.ws_auth')}
              description={t('config_management.visual.sections.network.ws_auth_desc')}
              checked={values.wsAuth}
              disabled={disabled}
              onChange={(wsAuth) => onChange({ wsAuth })}
            />
          </FieldAnchor>
        </FieldGrid>
      </FieldStack>
    </SectionCard>
  );
}
