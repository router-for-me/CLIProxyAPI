import { useId } from 'react';
import { useTranslation } from 'react-i18next';
import { Collapsible } from '@/components/ui/Collapsible';
import { Select } from '@/components/ui/Select';
import type { RuntimePolicyDraft } from '../../runtimePolicy';
import { ErrorRulesEditor } from './ErrorRulesEditor';
import styles from './sharedForm.module.scss';

export interface RuntimePolicyEditorProps {
  value: RuntimePolicyDraft;
  onChange: (value: RuntimePolicyDraft) => void;
  disabled: boolean;
  supportsErrors?: boolean;
}

export function RuntimePolicyEditor({
  value,
  onChange,
  disabled,
  supportsErrors = true,
}: RuntimePolicyEditorProps) {
  const { t } = useTranslation();
  const id = useId();
  const key = 'providersPage.runtimePolicy';

  return (
    <Collapsible label={t(`${key}.title`)} aria-describedby={`${id}-description`}>
      <div className={styles.section}>
        <p id={`${id}-description`} className={styles.sectionDesc}>
          {t(`${key}.description`)}
        </p>
        <div className={styles.field}>
          <label htmlFor={`${id}-cooling`} className={styles.label}>
            {t(`${key}.cooling`)}
          </label>
          <Select
            id={`${id}-cooling`}
            ariaLabel={t(`${key}.cooling`)}
            value={value.cooling}
            disabled={disabled}
            options={[
              { value: 'inherit', label: t(`${key}.inherit`) },
              { value: 'enabled', label: t(`${key}.coolingEnabled`) },
              { value: 'disabled', label: t(`${key}.coolingDisabled`) },
            ]}
            onChange={(cooling) =>
              onChange({ ...value, cooling: cooling as RuntimePolicyDraft['cooling'] })
            }
          />
        </div>
        <div className={styles.field}>
          <label htmlFor={`${id}-retry`} className={styles.label}>
            {t(`${key}.retry`)}
          </label>
          <input
            id={`${id}-retry`}
            className={styles.input}
            type="text"
            value={value.retry}
            disabled={disabled}
            placeholder={t(`${key}.inherit`)}
            aria-describedby={`${id}-retry-hint`}
            onChange={(event) => onChange({ ...value, retry: event.target.value })}
          />
          <span id={`${id}-retry-hint`} className={styles.labelHint}>
            {t(`${key}.retryHint`)}
          </span>
        </div>
        {supportsErrors && (
          <>
            <div className={styles.field}>
              <label htmlFor={`${id}-errors-mode`} className={styles.label}>
                {t(`${key}.errorsMode`)}
              </label>
              <Select
                id={`${id}-errors-mode`}
                ariaLabel={t(`${key}.errorsMode`)}
                value={value.errorsMode}
                disabled={disabled}
                ariaDescribedBy={`${id}-errors-hint`}
                options={[
                  { value: 'inherit', label: t(`${key}.inherit`) },
                  { value: 'override', label: t(`${key}.override`) },
                ]}
                onChange={(errorsMode) =>
                  onChange({ ...value, errorsMode: errorsMode as RuntimePolicyDraft['errorsMode'] })
                }
              />
              <span id={`${id}-errors-hint`} className={styles.labelHint}>
                {t(`${key}.errorsHint`)}
              </span>
            </div>
            {value.errorsMode === 'override' && (
              <ErrorRulesEditor
                rules={value.errorRules}
                onChange={(errorRules) => onChange({ ...value, errorRules })}
                disabled={disabled}
              />
            )}
          </>
        )}
      </div>
    </Collapsible>
  );
}
