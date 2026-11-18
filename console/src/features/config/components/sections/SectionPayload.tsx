import { useCallback, useMemo, useState, type SyntheticEvent } from 'react';
import { useTranslation } from 'react-i18next';
import { Collapsible } from '@/components/ui/Collapsible';
import {
  getPayloadReplaceValidationError,
  hasPayloadParamValidationErrors,
} from '@/hooks/useVisualConfig';
import type { PayloadFilterRule, PayloadReplaceRule, PayloadRule } from '@/types/visualConfig';
import { CONFIG_TAB_ICONS, SECTION_INDEX_LABELS } from '../../constants';
import type { ConfigSectionProps } from '../../types';
import { SectionCard } from '../SectionCard';
import { FieldAnchor, FieldStack } from '../fields/FieldPrimitives';
import { PayloadFilterRulesEditor } from '../blocks/PayloadFilterRulesEditor';
import { PayloadReplaceRulesEditor } from '../blocks/PayloadReplaceRulesEditor';
import { PayloadRulesEditor } from '../blocks/PayloadRulesEditor';

const Icon = CONFIG_TAB_ICONS.payload;

type ValidatedGroup =
  | 'payloadDefaultRules'
  | 'payloadDefaultRawRules'
  | 'payloadOverrideRules'
  | 'payloadOverrideRawRules'
  | 'payloadReplaceRules';

/** 06 Payload 配置：默认值 / 原始 JSON / 覆盖 / 替换 / 过滤 六个规则组。 */
export function SectionPayload({ values, disabled, animateIn, onChange }: ConfigSectionProps) {
  const { t } = useTranslation();
  const [opened, setOpened] = useState<Partial<Record<ValidatedGroup, boolean>>>({});

  // 校验错误按组计算：只展开出错的那一组，不牵连其它抽屉。
  const groupErrors = useMemo<Record<ValidatedGroup, boolean>>(
    () => ({
      payloadDefaultRules: hasPayloadParamValidationErrors(values.payloadDefaultRules),
      payloadDefaultRawRules: hasPayloadParamValidationErrors(values.payloadDefaultRawRules),
      payloadOverrideRules: hasPayloadParamValidationErrors(values.payloadOverrideRules),
      payloadOverrideRawRules: hasPayloadParamValidationErrors(values.payloadOverrideRawRules),
      payloadReplaceRules: values.payloadReplaceRules.some((rule) =>
        Boolean(getPayloadReplaceValidationError(rule))
      ),
    }),
    [
      values.payloadDefaultRules,
      values.payloadDefaultRawRules,
      values.payloadOverrideRules,
      values.payloadOverrideRawRules,
      values.payloadReplaceRules,
    ]
  );

  // 受控展开且不带 key：错误出现时把该组带到眼前，错误消失后不重挂载、不在用户手下收起。
  const groupProps = (group: ValidatedGroup) => ({
    open: Boolean(opened[group]) || groupErrors[group],
    onToggle: (event: SyntheticEvent<HTMLDetailsElement>) => {
      const next = event.currentTarget.open;
      setOpened((current) => (current[group] === next ? current : { ...current, [group]: next }));
    },
  });

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

  const handlePayloadReplaceRulesChange = useCallback(
    (payloadReplaceRules: PayloadReplaceRule[]) => onChange({ payloadReplaceRules }),
    [onChange]
  );

  return (
    <SectionCard
      indexLabel={SECTION_INDEX_LABELS.payload}
      icon={<Icon size={16} />}
      title={t('config_management.visual.sections.payload.title')}
      animateIn={animateIn}
    >
      <FieldStack>
        <FieldAnchor fieldId="payloadDefaultRules">
          <Collapsible
            label={t('config_management.visual.sections.payload.default_rules')}
            {...groupProps('payloadDefaultRules')}
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
            label={t('config_management.visual.sections.payload.default_raw_rules')}
            {...groupProps('payloadDefaultRawRules')}
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
            label={t('config_management.visual.sections.payload.override_rules')}
            {...groupProps('payloadOverrideRules')}
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
            label={t('config_management.visual.sections.payload.override_raw_rules')}
            {...groupProps('payloadOverrideRawRules')}
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

        <FieldAnchor fieldId="payloadReplaceRules">
          <Collapsible
            label={t('config_management.visual.payload_replace.title')}
            {...groupProps('payloadReplaceRules')}
          >
            <PayloadReplaceRulesEditor
              value={values.payloadReplaceRules}
              disabled={disabled}
              onChange={handlePayloadReplaceRulesChange}
            />
          </Collapsible>
        </FieldAnchor>

        <FieldAnchor fieldId="payloadFilterRules">
          <Collapsible label={t('config_management.visual.sections.payload.filter_rules')}>
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
