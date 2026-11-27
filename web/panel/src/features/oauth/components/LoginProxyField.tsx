import { useEffect, useId, useRef, useState } from 'react';
import { useTranslation } from 'react-i18next';
import { IconChevronDown, IconNetwork } from '@/components/ui/icons';
import { checkLoginProxyUrl, maskProxyUrl, shouldShowLoginProxyError } from '../loginProxy';
import styles from './LoginProxyField.module.scss';

export interface LoginProxyFieldProps {
  value: string;
  /** Trimmed value of the last login refused because of the proxy, if any. */
  rejectedValue: string | null;
  onChange: (value: string) => void;
}

/**
 * Optional page-level proxy for built-in and plugin OAuth logins. Collapsed by default so the one-click
 * tile flow stays the obvious path; while a value is set the collapsed row keeps showing it,
 * so an active proxy cannot be forgotten.
 */
export function LoginProxyField({ value, rejectedValue, onChange }: LoginProxyFieldProps) {
  const { t } = useTranslation();
  const panelId = useId();
  const inputId = useId();
  const errorId = useId();
  const inputRef = useRef<HTMLInputElement>(null);
  const [expanded, setExpanded] = useState(rejectedValue !== null);
  const [touched, setTouched] = useState(false);
  const focusOnExpand = useRef(false);

  const check = checkLoginProxyUrl(value);
  const active = check.kind !== 'empty';
  const showError = shouldShowLoginProxyError(value, { touched, rejectedValue });
  const invalid = check.kind === 'invalid' || showError;

  useEffect(() => {
    if (expanded && focusOnExpand.current) {
      focusOnExpand.current = false;
      inputRef.current?.focus();
    }
  }, [expanded]);

  // A refused login reveals the field so its inline error is visible once the dialog closes.
  const [seenRejectedValue, setSeenRejectedValue] = useState(rejectedValue);
  if (rejectedValue !== seenRejectedValue) {
    setSeenRejectedValue(rejectedValue);
    if (rejectedValue !== null) setExpanded(true);
  }

  const toggle = () => {
    focusOnExpand.current = !expanded;
    setExpanded((open) => !open);
  };

  const clear = () => {
    onChange('');
    setTouched(false);
    inputRef.current?.focus();
  };

  // Nothing set: the collapsed chip alone says it is optional, so no status is shown.
  const statusText = !active
    ? null
    : invalid
      ? t('auth_login.login_proxy_status_invalid')
      : maskProxyUrl(check.kind === 'valid' ? check.url : value);

  return (
    <div
      className={styles.root}
      data-active={active || undefined}
      data-invalid={(active && invalid) || undefined}
      data-reveal
    >
      <button
        type="button"
        className={styles.toggle}
        aria-expanded={expanded}
        aria-controls={panelId}
        onClick={toggle}
      >
        <IconNetwork size={14} className={styles.toggleIcon} aria-hidden="true" />
        <span className={styles.toggleLabel}>{t('auth_login.login_proxy_toggle')}</span>
        {statusText && (
          <span className={styles.toggleStatus}>
            <span className={styles.statusDot} aria-hidden="true" />
            <span className={styles.statusText}>{statusText}</span>
          </span>
        )}
        <IconChevronDown size={14} className={styles.chevron} aria-hidden="true" />
      </button>

      <div id={panelId} className={styles.panel} hidden={!expanded}>
        <label className={styles.label} htmlFor={inputId}>
          {t('auth_login.login_proxy_label')}
        </label>
        <div className={styles.row}>
          <input
            ref={inputRef}
            id={inputId}
            className={`input ${styles.input}`}
            type="text"
            inputMode="url"
            value={value}
            onChange={(event) => onChange(event.target.value)}
            onBlur={() => setTouched(true)}
            placeholder={t('auth_login.login_proxy_placeholder')}
            autoComplete="off"
            autoCapitalize="off"
            autoCorrect="off"
            spellCheck={false}
            aria-invalid={showError}
            aria-describedby={showError ? errorId : undefined}
          />
          {value && (
            <button type="button" className={styles.clear} onClick={clear}>
              {t('auth_login.login_proxy_clear')}
            </button>
          )}
        </div>
        {showError && (
          <p id={errorId} className={styles.error} role="alert">
            {t('auth_login.login_proxy_invalid')}
          </p>
        )}
      </div>
    </div>
  );
}
