import { useId } from 'react';
import { useTranslation } from 'react-i18next';
import { Collapsible } from '@/components/ui/Collapsible';
import { Select } from '@/components/ui/Select';
import type { ProviderBehaviorOptions } from '@/types/provider';
import { FieldLabel } from '@/components/ui/FieldLabel';
import { getProviderBehaviorCapabilities } from '../../descriptors';
import type { ProviderBrand } from '../../types';
import { CheckboxField } from './CheckboxField';
import styles from './sharedForm.module.scss';

interface ProviderBehaviorEditorProps {
  brand: ProviderBrand;
  value: ProviderBehaviorOptions;
  onChange: (patch: Partial<ProviderBehaviorOptions>) => void;
  disabled: boolean;
}

export function ProviderBehaviorEditor({
  brand,
  value,
  onChange,
  disabled,
}: ProviderBehaviorEditorProps) {
  const { t } = useTranslation();
  const id = useId();
  const capabilities = getProviderBehaviorCapabilities(brand);
  if (!Object.values(capabilities).some(Boolean)) return null;
  const checkbox = (key: 'alphaSearch' | 'rebuildMidSystemMessage' | 'supportPromptCacheKey') => (
    <CheckboxField
      label={t(`providersPage.behavior.${key}`)}
      hint={t(`providersPage.behavior.${key}Hint`)}
      checked={value[key] === true}
      disabled={disabled}
      onChange={(checked) => onChange({ [key]: checked })}
    />
  );
  return (
    <Collapsible label={t('providersPage.behavior.title')}>
      <div className={styles.section}>
        {capabilities.alphaSearch ? checkbox('alphaSearch') : null}
        {capabilities.disableCodexCloaking ? (
          <div className={styles.field}>
            <FieldLabel
              className={styles.labelLine}
              labelClassName={styles.label}
              htmlFor={`${id}-cloak`}
              label={t('providersPage.behavior.codexCloaking')}
              hint={t('providersPage.behavior.codexCloakingHint')}
              hintId={`${id}-cloak-hint`}
            />
            <Select
              id={`${id}-cloak`}
              ariaLabel={t('providersPage.behavior.codexCloaking')}
              value={
                value.disableCodexCloaking === undefined
                  ? 'default'
                  : value.disableCodexCloaking
                    ? 'disabled'
                    : 'enabled'
              }
              disabled={disabled}
              ariaDescribedBy={`${id}-cloak-hint`}
              options={[
                { value: 'default', label: t('providersPage.behavior.useDefault') },
                { value: 'enabled', label: t('providersPage.behavior.cloakingEnabled') },
                { value: 'disabled', label: t('providersPage.behavior.cloakingDisabled') },
              ]}
              onChange={(mode) =>
                onChange({
                  disableCodexCloaking: mode === 'default' ? undefined : mode === 'disabled',
                })
              }
            />
          </div>
        ) : null}
        {capabilities.rebuildMidSystemMessage ? checkbox('rebuildMidSystemMessage') : null}
        {capabilities.supportPromptCacheKey ? checkbox('supportPromptCacheKey') : null}
      </div>
    </Collapsible>
  );
}
