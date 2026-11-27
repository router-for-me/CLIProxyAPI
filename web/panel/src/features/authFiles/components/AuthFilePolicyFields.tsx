import { useId } from 'react';
import { useTranslation } from 'react-i18next';
import { Input } from '@/components/ui/Input';
import { Select } from '@/components/ui/Select';
import { Button } from '@/components/ui/Button';
import { ErrorRulesEditor } from '@/features/providers/sheets/forms/ErrorRulesEditor';
import { ModelEntriesEditor } from '@/features/providers/sheets/forms/ModelEntriesEditor';
import { FieldLabel } from '@/components/ui/FieldLabel';
import styles from './AuthFilePolicyFields.module.scss';
import {
  credentialPolicyError,
  type CredentialPolicyDraft,
  type CredentialPolicyField,
  type CredentialPolicyValues,
  type PolicyMode,
} from '../credentialPolicy';

export function AuthFilePolicyFields({
  draft,
  disabled,
  onChange,
}: {
  draft: CredentialPolicyDraft;
  disabled: boolean;
  onChange: <K extends CredentialPolicyField>(field: K, value: CredentialPolicyValues[K]) => void;
}) {
  const { t } = useTranslation();
  const id = useId();
  const error = credentialPolicyError(draft);
  // 继承/自定义的语义（回退到哪里、0 与空列表意味着什么）收进标签后的 InfoTip，
  // 下拉框通过 aria-describedby 指向说明副本。
  const modeSelect = (field: CredentialPolicyField, label: string, hint: string) => (
    <>
      <FieldLabel
        as="label"
        label={t(label)}
        id={`${id}-${field}`}
        hint={t(hint)}
        hintId={`${id}-${field}-hint`}
      />
      <Select
        ariaLabelledBy={`${id}-${field}`}
        ariaDescribedBy={`${id}-${field}-hint`}
        value={draft[field].mode}
        options={[
          { value: 'inherit', label: t('auth_files.policy_inherit') },
          { value: 'custom', label: t('auth_files.policy_custom') },
        ]}
        disabled={disabled}
        onChange={(mode) => onChange(field, { ...draft[field], mode: mode as PolicyMode })}
      />
    </>
  );
  return (
    <div className={styles.policyFields}>
      <div className="form-group">
        {modeSelect(
          'requestRetry',
          'auth_files.policy_retry_label',
          'auth_files.policy_retry_hint'
        )}
        {draft.requestRetry.mode === 'custom' && (
          <Input
            label={t('auth_files.policy_retry_count')}
            type="number"
            min={0}
            step={1}
            value={draft.requestRetry.value}
            disabled={disabled}
            error={error === 'auth_files.policy_retry_invalid' ? t(error) : undefined}
            onChange={(event) =>
              onChange('requestRetry', { ...draft.requestRetry, value: event.target.value })
            }
          />
        )}
      </div>
      {(['modelAliases', 'errorRules'] as const).map((field) => {
        const key = field === 'modelAliases' ? 'aliases' : 'rules';
        const policy = draft[field];
        return (
          <div className="form-group" key={field}>
            {modeSelect(field, `auth_files.policy_${key}_label`, `auth_files.policy_${key}_hint`)}
            {policy.mode === 'custom' &&
              (policy.rows === null ? (
                <div className={styles.unsupported}>
                  <p className="hint">{t('auth_files.policy_unsupported')}</p>
                  <Button
                    variant="secondary"
                    disabled={disabled}
                    onClick={() => onChange(field, { mode: 'custom', rows: [] })}
                  >
                    {t('auth_files.policy_replace')}
                  </Button>
                </div>
              ) : field === 'modelAliases' ? (
                <ModelEntriesEditor
                  oauthAliasOnly
                  models={draft.modelAliases.rows ?? []}
                  supportsImage={false}
                  supportsThinking={false}
                  mutating={disabled}
                  removeDisabled={false}
                  onUpdate={(index, patch) =>
                    onChange('modelAliases', {
                      mode: 'custom',
                      rows: draft.modelAliases.rows!.map((row, i) =>
                        i === index ? { ...row, ...patch } : row
                      ),
                    })
                  }
                  onAdd={() =>
                    onChange('modelAliases', {
                      mode: 'custom',
                      rows: [...draft.modelAliases.rows!, { name: '', alias: '' }],
                    })
                  }
                  onRemove={(index) =>
                    onChange('modelAliases', {
                      mode: 'custom',
                      rows: draft.modelAliases.rows!.filter((_, i) => i !== index),
                    })
                  }
                />
              ) : (
                <ErrorRulesEditor
                  rules={draft.errorRules.rows ?? []}
                  disabled={disabled}
                  onChange={(rows) => onChange('errorRules', { mode: 'custom', rows })}
                />
              ))}
          </div>
        );
      })}
      {error && (
        <div className="error-box" role="alert">
          {t(error)}
        </div>
      )}
    </div>
  );
}
