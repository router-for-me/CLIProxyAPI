import { useId } from 'react';
import { useTranslation } from 'react-i18next';
import { Select } from '@/components/ui/Select';
import type { ConnectionPool } from '@/types/provider';
import { FormDisclosure } from './FormSection';
import styles from './sharedForm.module.scss';

interface OptionalBooleanProps {
  label: string;
  value?: boolean;
  disabled: boolean;
  onChange: (value: boolean | undefined) => void;
}

export function OptionalBooleanField({ label, value, disabled, onChange }: OptionalBooleanProps) {
  const { t } = useTranslation();
  const id = useId();
  return (
    <div className={styles.field}>
      <label className={styles.label} htmlFor={id}>
        {label}
      </label>
      <Select
        id={id}
        ariaLabel={label}
        value={String(value ?? '')}
        disabled={disabled}
        options={[
          { value: '', label: t('providersPage.runtimePolicy.inherit') },
          { value: 'true', label: t('providersPage.toast.enabled') },
          { value: 'false', label: t('providersPage.toast.disabled') },
        ]}
        onChange={(mode) => onChange(mode === '' ? undefined : mode === 'true')}
      />
    </div>
  );
}

export function ConnectionPoolEditor({
  value,
  disabled,
  onChange,
}: {
  value?: ConnectionPool;
  disabled: boolean;
  onChange: (pool?: ConnectionPool) => void;
}) {
  const { t } = useTranslation();
  const id = useId();
  return (
    <FormDisclosure label={t('providersPage.nativeKey.pool')}>
      <div className={styles.field}>
        <label className={styles.label} htmlFor={id}>
          {t('providersPage.nativeKey.pool')}
        </label>
        <Select
          id={id}
          ariaLabel={t('providersPage.nativeKey.pool')}
          disabled={disabled}
          value={value ? 'override' : 'inherit'}
          options={['inherit', 'override'].map((mode) => ({
            value: mode,
            label: t(`providersPage.runtimePolicy.${mode}`),
          }))}
          onChange={(mode) => onChange(mode === 'inherit' ? undefined : {})}
        />
      </div>
      <p className={styles.sectionDesc}>{t('providersPage.nativeKey.poolHint')}</p>
      {value && (
        <>
          <OptionalBooleanField
            label={t('providersPage.nativeKey.reuse')}
            disabled={disabled}
            value={value.enabled}
            onChange={(enabled) => onChange({ ...value, enabled })}
          />
          <label className={styles.field}>
            <span className={styles.label}>{t('providersPage.nativeKey.timeout')}</span>
            <input
              className={styles.input}
              disabled={disabled}
              placeholder="300s"
              value={value.idleConnTimeout ?? ''}
              onChange={(e) => onChange({ ...value, idleConnTimeout: e.target.value })}
            />
          </label>
          <label className={styles.field}>
            <span className={styles.label}>{t('providersPage.nativeKey.maxIdle')}</span>
            <input
              className={styles.input}
              disabled={disabled}
              type="number"
              step="1"
              value={value.maxIdleConnsPerHost ?? ''}
              onChange={(e) =>
                onChange({
                  ...value,
                  maxIdleConnsPerHost: e.target.value === '' ? undefined : Number(e.target.value),
                })
              }
            />
          </label>
        </>
      )}
    </FormDisclosure>
  );
}
