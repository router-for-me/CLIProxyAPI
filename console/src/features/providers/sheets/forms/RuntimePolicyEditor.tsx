import { useId } from 'react';
import { useTranslation } from 'react-i18next';
import { Collapsible } from '@/components/ui/Collapsible';
import { Select } from '@/components/ui/Select';
import { FieldLabel } from '@/components/ui/FieldLabel';
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
    <Collapsible label={t(`${key}.title`)}>
      <div className={styles.section}>
        <div className={styles.field}>
          {/* The section's inherit/override explanation rides on its first field: an InfoTip
              cannot sit in the Collapsible summary (a button inside a button). */}
          <FieldLabel
            className={styles.labelLine}
            labelClassName={styles.label}
            htmlFor={`${id}-cooling`}
            label={t(`${key}.cooling`)}
            hint={t(`${key}.description`)}
            hintId={`${id}-cooling-hint`}
          />
          <Select
            id={`${id}-cooling`}
            ariaLabel={t(`${key}.cooling`)}
            ariaDescribedBy={`${id}-cooling-hint`}
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
          <FieldLabel
            className={styles.labelLine}
            labelClassName={styles.label}
            htmlFor={`${id}-retry`}
            label={t(`${key}.retry`)}
            hint={t(`${key}.retryHint`)}
            hintId={`${id}-retry-hint`}
          />
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
        </div>
        {supportsErrors && (
          <>
            <div className={styles.field}>
              <FieldLabel
                className={styles.labelLine}
                labelClassName={styles.label}
                htmlFor={`${id}-errors-mode`}
                label={t(`${key}.errorsMode`)}
                hint={
                  <>
                    {/* The space keeps the two sentences apart in the accessible description. */}
                    {t(`${key}.errorsHint`)} <br />
                    {t('providersPage.errorRules.hint')}
                  </>
                }
                hintId={`${id}-errors-hint`}
              />
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
            </div>
            {value.errorsMode === 'override' && (
              <ErrorRulesEditor
                rules={value.errorRules}
                onChange={(errorRules) => onChange({ ...value, errorRules })}
                disabled={disabled}
                showOrderHint={false}
              />
            )}
          </>
        )}
      </div>
    </Collapsible>
  );
}
