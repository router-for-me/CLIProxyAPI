import { useCallback } from 'react';
import { useTranslation } from 'react-i18next';
import { Collapsible } from '@/components/ui/Collapsible';
import type { PayloadFilterRule, PayloadRule } from '@/types/visualConfig';
import { CONFIG_TAB_ICONS, SECTION_INDEX_LABELS } from '../../constants';
import type { ConfigSectionProps } from '../../types';
import { SectionCard } from '../SectionCard';
import { FieldAnchor, FieldStack } from '../fields/FieldPrimitives';
import { PayloadFilterRulesEditor } from '../blocks/PayloadFilterRulesEditor';
import { PayloadRulesEditor } from '../blocks/PayloadRulesEditor';

const Icon = CONFIG_TAB_ICONS.payload;

export type SectionPayloadProps = ConfigSectionProps & {
  /** 有载荷校验错误时折叠组带 key 重挂载并强制展开，把错误带到眼前。 */
  hasPayloadValidationErrors: boolean;
};

/** 07 Payload 配置：默认值 / 原始 JSON / 覆盖 / 过滤 五个规则组。 */
export function SectionPayload({
  values,
  disabled,
  animateIn,
  hasPayloadValidationErrors,
  onChange,
}: SectionPayloadProps) {
  const { t } = useTranslation();
  const payloadValidationKey = hasPayloadValidationErrors ? 'payload-errors' : 'payload-ok';

  const handlePayloadDefaultRulesChange = useCallback(
    (payloadDefaultRules: PayloadRule[]) => onChange({ payloadDefaultRules }),
    [onChange]
  );
  const handlePayloadDefaultRawRulesChange = useCallback(
    (payloadDefaultRawRules: PayloadRule[]) => onChange({ payloadDefaultRawRules }),
    [onChange]
  );
  const handlePayloadOverrideRulesChange = useCallback(
    (payloadOverrideRules: PayloadRule[]) => onChange({ payloadOverrideRules }),
    [onChange]
  );
  const handlePayloadOverrideRawRulesChange = useCallback(
    (payloadOverrideRawRules: PayloadRule[]) => onChange({ payloadOverrideRawRules }),
    [onChange]
  );
  const handlePayloadFilterRulesChange = useCallback(
    (payloadFilterRules: PayloadFilterRule[]) => onChange({ payloadFilterRules }),
    [onChange]
  );

  return (
    <SectionCard
      indexLabel={SECTION_INDEX_LABELS.payload}
      icon={<Icon size={16} />}
      title={t('config_management.visual.sections.payload.title')}
      description={t('config_management.visual.sections.payload.description')}
      animateIn={animateIn}
    >
      <FieldStack>
        <FieldAnchor fieldId="payloadDefaultRules">
          <Collapsible
            key={`payloadDefaultRules-${payloadValidationKey}`}
            label={t('config_management.visual.sections.payload.default_rules')}
            hint={t('config_management.visual.sections.payload.default_rules_desc')}
            defaultOpen={hasPayloadValidationErrors}
          >
            <PayloadRulesEditor
              value={values.payloadDefaultRules}
              disabled={disabled}
              onChange={handlePayloadDefaultRulesChange}
            />
          </Collapsible>
        </FieldAnchor>

        <FieldAnchor fieldId="payloadDefaultRawRules">
          <Collapsible
            key={`payloadDefaultRawRules-${payloadValidationKey}`}
            label={t('config_management.visual.sections.payload.default_raw_rules')}
            hint={t('config_management.visual.sections.payload.default_raw_rules_desc')}
            defaultOpen={hasPayloadValidationErrors}
          >
            <PayloadRulesEditor
              value={values.payloadDefaultRawRules}
              disabled={disabled}
              rawJsonValues
              onChange={handlePayloadDefaultRawRulesChange}
            />
          </Collapsible>
        </FieldAnchor>

        <FieldAnchor fieldId="payloadOverrideRules">
          <Collapsible
            key={`payloadOverrideRules-${payloadValidationKey}`}
            label={t('config_management.visual.sections.payload.override_rules')}
            hint={t('config_management.visual.sections.payload.override_rules_desc')}
            defaultOpen={hasPayloadValidationErrors}
          >
            <PayloadRulesEditor
              value={values.payloadOverrideRules}
              disabled={disabled}
              protocolFirst
              onChange={handlePayloadOverrideRulesChange}
            />
          </Collapsible>
        </FieldAnchor>

        <FieldAnchor fieldId="payloadOverrideRawRules">
          <Collapsible
            key={`payloadOverrideRawRules-${payloadValidationKey}`}
            label={t('config_management.visual.sections.payload.override_raw_rules')}
            hint={t('config_management.visual.sections.payload.override_raw_rules_desc')}
            defaultOpen={hasPayloadValidationErrors}
          >
            <PayloadRulesEditor
              value={values.payloadOverrideRawRules}
              disabled={disabled}
              protocolFirst
              rawJsonValues
              onChange={handlePayloadOverrideRawRulesChange}
            />
          </Collapsible>
        </FieldAnchor>

        <FieldAnchor fieldId="payloadFilterRules">
          <Collapsible
            key={`payloadFilterRules-${payloadValidationKey}`}
            label={t('config_management.visual.sections.payload.filter_rules')}
            hint={t('config_management.visual.sections.payload.filter_rules_desc')}
            defaultOpen={hasPayloadValidationErrors}
          >
            <PayloadFilterRulesEditor
              value={values.payloadFilterRules}
              disabled={disabled}
              onChange={handlePayloadFilterRulesChange}
            />
          </Collapsible>
        </FieldAnchor>
      </FieldStack>
    </SectionCard>
  );
}
