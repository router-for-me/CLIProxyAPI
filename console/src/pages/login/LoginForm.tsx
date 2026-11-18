import { useId, useRef, type FormEvent } from 'react';
import { useTranslation } from 'react-i18next';
import { SelectionCheckbox } from '@/components/ui/SelectionCheckbox';
import { IconAlertTriangle, IconCheck, IconEye, IconEyeOff } from '@/components/ui/icons';
import styles from './LoginForm.module.scss';

export interface LoginFormProps {
  /** 输入框中的连接地址（可能为空，此时回退到 detectedBase） */
  apiBase: string;
  detectedBase: string;
  editingBase: boolean;
  managementKey: string;
  showKey: boolean;
  rememberPassword: boolean;
  loading: boolean;
  success?: boolean;
  error: string;
  onApiBaseChange: (value: string) => void;
  onEditingBaseChange: (value: boolean) => void;
  onManagementKeyChange: (value: string) => void;
  onShowKeyChange: (value: boolean) => void;
  onRememberPasswordChange: (value: boolean) => void;
  onSubmit: () => void;
}

export function LoginForm({
  apiBase,
  detectedBase,
  editingBase,
  managementKey,
  showKey,
  rememberPassword,
  loading,
  success = false,
  error,
  onApiBaseChange,
  onEditingBaseChange,
  onManagementKeyChange,
  onShowKeyChange,
  onRememberPasswordChange,
  onSubmit,
}: LoginFormProps) {
  const { t } = useTranslation();
  const baseId = useId();
  const keyId = useId();
  const errorId = useId();
  const keyInputRef = useRef<HTMLInputElement | null>(null);
  const locked = loading || success;

  const handleSubmit = (event: FormEvent<HTMLFormElement>) => {
    event.preventDefault();
    if (!locked) onSubmit();
  };

  const toggleKeyLabel = showKey ? t('login.hide_key') : t('login.show_key');

  return (
    <form className={styles.form} onSubmit={handleSubmit} noValidate>
      <div className={styles.field} data-stagger>
        <div className={styles.labelRow}>
          <label className={styles.label} htmlFor={editingBase ? baseId : undefined}>
            {editingBase ? t('login.custom_connection_label') : t('login.connection_current')}
          </label>
          <button
            type="button"
            className={styles.textButton}
            aria-expanded={editingBase}
            aria-controls={baseId}
            disabled={locked}
            onClick={() => {
              const next = !editingBase;
              onEditingBaseChange(next);
              if (!next) keyInputRef.current?.focus();
            }}
          >
            {editingBase ? t('login.connection_done') : t('login.connection_edit')}
          </button>
        </div>
        {editingBase ? (
          <input
            id={baseId}
            className={`${styles.input} ${styles.mono}`}
            type="url"
            inputMode="url"
            autoFocus
            autoComplete="url"
            spellCheck={false}
            placeholder={t('login.custom_connection_placeholder')}
            readOnly={locked}
            value={apiBase}
            onChange={(event) => onApiBaseChange(event.target.value)}
          />
        ) : (
          <div
            id={baseId}
            className={`${styles.readout} ${styles.mono}`}
            title={apiBase || detectedBase}
          >
            <span className={styles.readoutText}>{apiBase || detectedBase}</span>
          </div>
        )}
      </div>

      <div className={styles.field} data-stagger>
        <label className={styles.label} htmlFor={keyId}>
          {t('login.management_key_label')}
        </label>
        <div className={styles.inputWrap}>
          <input
            ref={keyInputRef}
            id={keyId}
            className={`${styles.input} ${styles.inputWithAction}`}
            type={showKey ? 'text' : 'password'}
            name="cpa-management-key"
            autoComplete="current-password"
            autoFocus={!editingBase}
            spellCheck={false}
            placeholder={t('login.management_key_placeholder')}
            readOnly={locked}
            value={managementKey}
            aria-invalid={Boolean(error) || undefined}
            aria-describedby={error ? errorId : undefined}
            onChange={(event) => onManagementKeyChange(event.target.value)}
          />
          <button
            type="button"
            className={styles.iconButton}
            onClick={() => onShowKeyChange(!showKey)}
            aria-label={toggleKeyLabel}
            aria-pressed={showKey}
            title={toggleKeyLabel}
          >
            {showKey ? <IconEyeOff size={16} /> : <IconEye size={16} />}
          </button>
        </div>
      </div>

      <div className={styles.options} data-stagger>
        <SelectionCheckbox
          checked={rememberPassword}
          onChange={onRememberPasswordChange}
          label={t('login.remember_password_label')}
          labelClassName={styles.checkboxLabel}
        />
      </div>

      <div className={styles.actions} data-stagger>
        <button
          type="submit"
          className={styles.submit}
          disabled={locked}
          aria-busy={loading}
          data-state={success ? 'success' : loading ? 'loading' : undefined}
        >
          {success ? (
            <span key="success" className={styles.submitContent} role="status">
              <IconCheck size={16} className={styles.successIcon} />
              <span>{t('common.connected_status')}</span>
            </span>
          ) : (
            <span key="idle" className={styles.submitContent}>
              {loading && <span className="loading-spinner" aria-hidden="true" />}
              <span>{loading ? t('login.submitting') : t('login.submit_button')}</span>
            </span>
          )}
        </button>
        {error && (
          <p id={errorId} className={styles.error} role="alert" title={error}>
            <IconAlertTriangle size={14} aria-hidden="true" />
            <span>{error}</span>
          </p>
        )}
      </div>
    </form>
  );
}
