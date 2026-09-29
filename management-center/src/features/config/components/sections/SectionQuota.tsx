import { useTranslation } from 'react-i18next';
import { CONFIG_TAB_ICONS, SECTION_INDEX_LABELS } from '../../constants';
import type { ConfigSectionProps } from '../../types';
import { SectionCard } from '../SectionCard';
import { FieldAnchor, FieldGrid, ToggleRow } from '../fields/FieldPrimitives';
import { QuotaSwitchPreviewModelToggle, QuotaSwitchProjectToggle } from '../fields/sharedFields';

const Icon = CONFIG_TAB_ICONS.quota;

/** 04 配额回退：配额耗尽时的回退策略（两个开关默认 true）。 */
export function SectionQuota({ values, disabled, animateIn, onChange }: ConfigSectionProps) {
  const { t } = useTranslation();

  return (
    <SectionCard
      indexLabel={SECTION_INDEX_LABELS.quota}
      icon={<Icon size={16} />}
      title={t('config_management.visual.sections.quota.title')}
      description={t('config_management.visual.sections.quota.description')}
      animateIn={animateIn}
    >
      <FieldGrid>
        <QuotaSwitchProjectToggle values={values} disabled={disabled} onChange={onChange} />
        <QuotaSwitchPreviewModelToggle values={values} disabled={disabled} onChange={onChange} />
        <FieldAnchor fieldId="quotaAntigravityCredits">
          <ToggleRow
            title={t('config_management.visual.sections.quota.antigravity_credits')}
            checked={values.quotaAntigravityCredits}
            disabled={disabled}
            onChange={(quotaAntigravityCredits) => onChange({ quotaAntigravityCredits })}
          />
        </FieldAnchor>
      </FieldGrid>
    </SectionCard>
  );
}
