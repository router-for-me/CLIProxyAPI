import { useId } from 'react';
import { useTranslation } from 'react-i18next';
import { Select } from '@/components/ui/Select';
import { SelectionCheckbox } from '@/components/ui/SelectionCheckbox';
import { FieldLabel } from '@/components/ui/FieldLabel';
import { getProviderModelCapabilities } from '../../descriptors';
import { THINKING_LEVELS } from '../../thinkingLevels';
import type { ModelEntryInput, ProviderBrand } from '../../types';
import styles from './sharedForm.module.scss';

interface ModelAdvancedFieldsProps {
  entry: ModelEntryInput;
  providerBrand: ProviderBrand;
  disabled: boolean;
  supportsThinking: boolean;
  onUpdate: (patch: Partial<ModelEntryInput>) => void;
}

export function ModelAdvancedFields({
  entry,
  providerBrand,
  disabled,
  supportsThinking,
  onUpdate,
}: ModelAdvancedFieldsProps) {
  const { t } = useTranslation();
  const id = useId();
  const capabilities = getProviderModelCapabilities(providerBrand);
  const enabled = entry.thinkingEnabled ?? Boolean(entry.thinkingJson?.trim());
  const levels = entry.thinkingLevels ?? [];
  const textField = (
    key:
      | 'displayName'
      | 'maxContextLength'
      | 'inputModalitiesText'
      | 'outputModalitiesText'
      | 'thinkingMin'
      | 'thinkingMax',
    numeric = false,
    budget = false
  ) => {
    const hint =
      key === 'maxContextLength'
        ? t('providersPage.modelOptions.contextHint')
        : key === 'inputModalitiesText' || key === 'outputModalitiesText'
          ? t('providersPage.modelOptions.modalitiesHint')
          : undefined;
    const hintId = hint ? `${id}-${key}-hint` : undefined;
    return (
      <div className={styles.field}>
        <FieldLabel
          className={styles.labelLine}
          labelClassName={styles.label}
          htmlFor={`${id}-${key}`}
          label={t(`providersPage.modelOptions.${key}`)}
          hint={hint}
          hintId={hintId}
        />
        <input
          id={`${id}-${key}`}
          aria-describedby={hintId}
          className={styles.input}
          value={entry[key] ?? ''}
          inputMode={numeric ? 'numeric' : undefined}
          disabled={disabled || (budget && !enabled)}
          onChange={(event) =>
            onUpdate({
              [key]: event.target.value,
              ...(key === 'inputModalitiesText' ? { inputModalitiesTouched: true } : {}),
              ...(key === 'outputModalitiesText' ? { outputModalitiesTouched: true } : {}),
              ...(budget ? { thinkingBudgetTouched: true } : {}),
            })
          }
        />
      </div>
    );
  };
  const checkbox = (
    key:
      | 'forceMapping'
      | 'isCompat'
      | 'supportConfigurationUpdate'
      | 'useMaxCompletionTokens'
      | 'thinkingZeroAllowed'
      | 'thinkingDynamicAllowed',
    budget = false
  ) => (
    <label className={styles.checkboxRow}>
      <input
        type="checkbox"
        className={styles.checkboxBox}
        checked={entry[key] === true}
        disabled={disabled || (budget && !enabled)}
        onChange={(event) =>
          onUpdate({
            [key]: event.target.checked,
            ...(budget ? { thinkingBudgetTouched: true } : {}),
          })
        }
      />
      <span className={styles.checkboxText}>{t(`providersPage.modelOptions.${key}`)}</span>
    </label>
  );
  return (
    <>
      {textField('displayName')}
      {capabilities.maxContextLength ? textField('maxContextLength', true) : null}
      {checkbox('forceMapping')}
      {capabilities.isCompat ? checkbox('isCompat') : null}
      {capabilities.configurationUpdate ? checkbox('supportConfigurationUpdate') : null}
      {capabilities.modalities ? (
        <>
          {textField('inputModalitiesText')}
          {textField('outputModalitiesText')}
          {checkbox('useMaxCompletionTokens')}
        </>
      ) : null}
      {supportsThinking ? (
        <fieldset className={styles.thinkingFieldset}>
          <legend className={styles.label}>{t('providersPage.form.thinkingConfig')}</legend>
          <div className={styles.field}>
            <FieldLabel
              className={styles.labelLine}
              labelClassName={styles.label}
              htmlFor={`${id}-thinking-mode`}
              label={t('providersPage.modelOptions.thinkingMode')}
              hint={
                entry.thinkingJson?.trim() ? (
                  <>
                    {t('providersPage.modelOptions.thinkingHint')} <br />
                    {t('providersPage.form.thinkingExistingHint')}
                  </>
                ) : (
                  t('providersPage.modelOptions.thinkingHint')
                )
              }
              hintId={`${id}-thinking-mode-hint`}
            />
            <Select
              id={`${id}-thinking-mode`}
              ariaLabel={t('providersPage.modelOptions.thinkingMode')}
              value={enabled ? 'custom' : 'default'}
              disabled={disabled}
              options={[
                { value: 'default', label: t('providersPage.modelOptions.useDefault') },
                { value: 'custom', label: t('providersPage.modelOptions.custom') },
              ]}
              ariaDescribedBy={`${id}-thinking-mode-hint`}
              onChange={(mode) => onUpdate({ thinkingEnabled: mode === 'custom' })}
            />
          </div>
          <div className={styles.fieldRow}>
            {textField('thinkingMin', true, true)}
            {textField('thinkingMax', true, true)}
          </div>
          {checkbox('thinkingZeroAllowed', true)}
          {checkbox('thinkingDynamicAllowed', true)}
          <div className={styles.thinkingLevelGrid}>
            {THINKING_LEVELS.map((level) => (
              <SelectionCheckbox
                key={level}
                checked={levels.includes(level)}
                disabled={disabled || !enabled}
                onChange={() =>
                  onUpdate({
                    thinkingEnabled: true,
                    thinkingLevelsTouched: true,
                    thinkingLevels: levels.includes(level)
                      ? levels.filter((item) => item !== level)
                      : THINKING_LEVELS.filter((item) => item === level || levels.includes(item)),
                  })
                }
                className={`${styles.thinkingLevelOption} ${
                  levels.includes(level) ? styles.thinkingLevelOptionSelected : ''
                }`}
                labelClassName={styles.thinkingLevelLabel}
                label={
                  <>
                    <span>{t(`providersPage.form.thinkingLevels.${level}`)}</span>
                    <code>{level}</code>
                  </>
                }
              />
            ))}
          </div>
        </fieldset>
      ) : null}
    </>
  );
}
