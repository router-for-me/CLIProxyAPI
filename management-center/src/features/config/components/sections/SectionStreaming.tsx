import { useId } from 'react';
import { useTranslation } from 'react-i18next';
import { Input } from '@/components/ui/Input';
import { CONFIG_TAB_ICONS, SECTION_INDEX_LABELS } from '../../constants';
import type { ConfigSectionProps } from '../../types';
import { SectionCard } from '../SectionCard';
import {
  FieldAnchor,
  FieldControl,
  FieldGrid,
  FieldShell,
  FieldStack,
  InlinePill,
} from '../fields/FieldPrimitives';
import { getValidationMessage } from '../blocks/shared';

const Icon = CONFIG_TAB_ICONS.streaming;

/** 05 流式传输：keepalive 与 bootstrap 重试；nonstream-keepalive-interval 是顶层 YAML 键。 */
export function SectionStreaming({
  values,
  validationErrors,
  disabled,
  animateIn,
  onChange,
}: ConfigSectionProps) {
  const { t } = useTranslation();
  const keepaliveInputId = useId();
  const keepaliveHintId = `${keepaliveInputId}-hint`;
  const keepaliveErrorId = `${keepaliveInputId}-error`;
  const nonstreamKeepaliveInputId = useId();
  const nonstreamKeepaliveHintId = `${nonstreamKeepaliveInputId}-hint`;
  const nonstreamKeepaliveErrorId = `${nonstreamKeepaliveInputId}-error`;

  const keepaliveError = getValidationMessage(t, validationErrors?.['streaming.keepaliveSeconds']);
  const bootstrapRetriesError = getValidationMessage(
    t,
    validationErrors?.['streaming.bootstrapRetries']
  );
  const nonstreamKeepaliveError = getValidationMessage(
    t,
    validationErrors?.['streaming.nonstreamKeepaliveInterval']
  );
  const isKeepaliveDisabled = !keepaliveError && Number(values.streaming.keepaliveSeconds) <= 0;
  const isNonstreamKeepaliveDisabled =
    !nonstreamKeepaliveError && Number(values.streaming.nonstreamKeepaliveInterval) <= 0;

  return (
    <SectionCard
      indexLabel={SECTION_INDEX_LABELS.streaming}
      icon={<Icon size={16} />}
      title={t('config_management.visual.sections.streaming.title')}
      description={t('config_management.visual.sections.streaming.description')}
      animateIn={animateIn}
    >
      <FieldStack>
        <FieldGrid>
          <FieldAnchor fieldId="streamingKeepaliveSeconds">
            <FieldShell
              label={t('config_management.visual.sections.streaming.keepalive_seconds')}
              htmlFor={keepaliveInputId}
              hint={t('config_management.visual.sections.streaming.keepalive_hint')}
              hintId={keepaliveHintId}
              error={keepaliveError}
              errorId={keepaliveErrorId}
            >
              <FieldControl>
                <input
                  id={keepaliveInputId}
                  className="input"
                  type="number"
                  placeholder="0"
                  value={values.streaming.keepaliveSeconds}
                  onChange={(e) =>
                    onChange({
                      streaming: {
                        ...values.streaming,
                        keepaliveSeconds: e.target.value,
                      },
                    })
                  }
                  disabled={disabled}
                />
                {isKeepaliveDisabled ? (
                  <InlinePill>
                    {t('config_management.visual.sections.streaming.disabled')}
                  </InlinePill>
                ) : null}
              </FieldControl>
            </FieldShell>
          </FieldAnchor>

          <FieldAnchor fieldId="streamingBootstrapRetries">
            <Input
              label={t('config_management.visual.sections.streaming.bootstrap_retries')}
              type="number"
              placeholder="1"
              value={values.streaming.bootstrapRetries}
              onChange={(e) =>
                onChange({
                  streaming: {
                    ...values.streaming,
                    bootstrapRetries: e.target.value,
                  },
                })
              }
              disabled={disabled}
              hint={t('config_management.visual.sections.streaming.bootstrap_hint')}
              error={bootstrapRetriesError}
            />
          </FieldAnchor>
        </FieldGrid>

        <FieldGrid>
          <FieldAnchor fieldId="streamingNonstreamKeepalive">
            <FieldShell
              label={t('config_management.visual.sections.streaming.nonstream_keepalive')}
              htmlFor={nonstreamKeepaliveInputId}
              hint={t('config_management.visual.sections.streaming.nonstream_keepalive_hint')}
              hintId={nonstreamKeepaliveHintId}
              error={nonstreamKeepaliveError}
              errorId={nonstreamKeepaliveErrorId}
            >
              <FieldControl>
                <input
                  id={nonstreamKeepaliveInputId}
                  className="input"
                  type="number"
                  placeholder="0"
                  value={values.streaming.nonstreamKeepaliveInterval}
                  onChange={(e) =>
                    onChange({
                      streaming: {
                        ...values.streaming,
                        nonstreamKeepaliveInterval: e.target.value,
                      },
                    })
                  }
                  disabled={disabled}
                />
                {isNonstreamKeepaliveDisabled ? (
                  <InlinePill>
                    {t('config_management.visual.sections.streaming.disabled')}
                  </InlinePill>
                ) : null}
              </FieldControl>
            </FieldShell>
          </FieldAnchor>
        </FieldGrid>
      </FieldStack>
    </SectionCard>
  );
}
