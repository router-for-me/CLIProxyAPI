import React, { useEffect, useMemo, useState, useCallback } from 'react';
import { Navigate, useNavigate, useLocation } from 'react-router-dom';
import { useTranslation } from 'react-i18next';
import { Button } from '@/components/ui/Button';
import { Input } from '@/components/ui/Input';
import { Select } from '@/components/ui/Select';
import { SelectionCheckbox } from '@/components/ui/SelectionCheckbox';
import { IconEye, IconEyeOff } from '@/components/ui/icons';
import {
  useAuthStore,
  useBrandingStore,
  useLanguageStore,
  useNotificationStore,
} from '@/stores';
import { detectApiBaseFromLocation, normalizeApiBase } from '@/utils/connection';
import { LANGUAGE_LABEL_KEYS, LANGUAGE_ORDER } from '@/utils/constants';
import { isSupportedLanguage } from '@/utils/language';
import { INLINE_LOGO_JPEG } from '@/assets/logoInline';
import type { ApiError } from '@/types';
import { LegacyBackendError } from '@/services/api/legacyBackendProbe';
import styles from './LoginPage.module.scss';
import { AccountLoginForm } from '@/features/sharing/AccountLoginForm';
import { loginRedirectTarget } from '@/router/location';
import { sharingApi } from '@/services/api/sharing';
import { apiClient } from '@/services/api/client';

/**
 * Converts an API error into a localized user-friendly message.
 */
function getLocalizedErrorMessage(error: unknown, t: (key: string) => string): string {
  if (error instanceof LegacyBackendError) return t('login.error_legacy_backend');
  const apiError = error as Partial<ApiError>;
  const status = typeof apiError.status === 'number' ? apiError.status : undefined;
  const code = typeof apiError.code === 'string' ? apiError.code : undefined;
  const message =
    error instanceof Error
      ? error.message
      : typeof apiError.message === 'string'
        ? apiError.message
        : typeof error === 'string'
          ? error
          : '';

  const withHttpStatus = (summary: string) => {
    if (!status) {
      return summary;
    }

    const genericAxiosMessage = `Request failed with status code ${status}`;
    const detail = message.trim();
    const backendDetail =
      detail && detail !== genericAxiosMessage
        ? ` (${t('login.error_backend_detail')}: ${detail})`
        : '';

    return `HTTP ${status}: ${summary}${backendDetail}`;
  };

  // Classify by HTTP status.
  if (status === 401) {
    return withHttpStatus(t('login.error_unauthorized'));
  }
  if (status === 403) {
    return withHttpStatus(t('login.error_forbidden'));
  }
  if (status === 404) {
    return withHttpStatus(t('login.error_not_found'));
  }
  if (status && status >= 500) {
    return withHttpStatus(t('login.error_server'));
  }

  // Classify by axios error code.
  if (code === 'ECONNABORTED' || message.toLowerCase().includes('timeout')) {
    return t('login.error_timeout');
  }
  if (code === 'ERR_NETWORK' || message.toLowerCase().includes('network error')) {
    return t('login.error_network');
  }
  if (code === 'ERR_CERT_AUTHORITY_INVALID' || message.toLowerCase().includes('certificate')) {
    return t('login.error_ssl');
  }

  // Detect CORS errors.
  if (message.toLowerCase().includes('cors') || message.toLowerCase().includes('cross-origin')) {
    return t('login.error_cors');
  }

  // Fallback message.
  return withHttpStatus(t('login.error_invalid'));
}

export function LoginPage() {
  const { t } = useTranslation();
  const navigate = useNavigate();
  const location = useLocation();
  const { showNotification } = useNotificationStore();
  const language = useLanguageStore((state) => state.language);
  const setLanguage = useLanguageStore((state) => state.setLanguage);
  const isAuthenticated = useAuthStore((state) => state.isAuthenticated);
  const login = useAuthStore((state) => state.login);
  const restoreSession = useAuthStore((state) => state.restoreSession);
  const storedBase = useAuthStore((state) => state.apiBase);
  const storedKey = useAuthStore((state) => state.managementKey);
  const storedRememberPassword = useAuthStore((state) => state.rememberPassword);
  const siteName = useBrandingStore((state) => state.siteName);
  const siteSubtitle = useBrandingStore((state) => state.siteSubtitle);
  const siteLogo = useBrandingStore((state) => state.siteLogo);
  const fetchBranding = useBrandingStore((state) => state.fetchBranding);

  const [apiBase, setApiBase] = useState('');
  const [managementKey, setManagementKey] = useState('');
  const [showCustomBase, setShowCustomBase] = useState(false);
  const [showKey, setShowKey] = useState(false);
  const [rememberPassword, setRememberPassword] = useState(false);
  const [loading, setLoading] = useState(false);
  const [autoLoading, setAutoLoading] = useState(true);
  const [autoLoginSuccess, setAutoLoginSuccess] = useState(false);
  const [error, setError] = useState('');
  const [sharing, setSharing] = useState<{ needs_setup: boolean } | null>(null);

  const detectedBase = useMemo(() => detectApiBaseFromLocation(), []);
  const languageOptions = useMemo(
    () =>
      LANGUAGE_ORDER.map((lang) => ({
        value: lang,
        label: t(LANGUAGE_LABEL_KEYS[lang]),
      })),
    [t]
  );
  const handleLanguageChange = useCallback(
    (selectedLanguage: string) => {
      if (!isSupportedLanguage(selectedLanguage)) {
        return;
      }
      setLanguage(selectedLanguage);
    },
    [setLanguage]
  );

  useEffect(() => {
    const init = async () => {
      try {
        const autoLoggedIn = await restoreSession();
        if (autoLoggedIn) {
          setAutoLoginSuccess(true);
          // Delay the redirect so the success animation is visible.
          setTimeout(() => {
            navigate(loginRedirectTarget(location.state), { replace: true });
          }, 1500);
        } else {
          setApiBase(storedBase || detectedBase);
          setManagementKey(storedKey || '');
          setRememberPassword(storedRememberPassword || Boolean(storedKey));
        }
      } finally {
        // When auto-login succeeds showSplash stays driven by autoLoginSuccess,
        // so loading can end unconditionally.
        setAutoLoading(false);
      }
    };

    apiClient.setConfig({ apiBase: detectedBase, managementKey: storedKey || '' });
    void fetchBranding();
    void sharingApi
      .status()
      .then(setSharing)
      .catch(() => setSharing(null))
      .finally(() => void init());
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, []);

  const handleSubmit = useCallback(async () => {
    if (!managementKey.trim()) {
      setError(t('login.error_required'));
      return;
    }

    const baseToUse = apiBase ? normalizeApiBase(apiBase) : detectedBase;
    setLoading(true);
    setError('');
    try {
      await login({
        apiBase: baseToUse,
        managementKey: managementKey.trim(),
        rememberPassword,
      });
      showNotification(t('common.connected_status'), 'success');
      navigate('/', { replace: true });
    } catch (err: unknown) {
      const message = getLocalizedErrorMessage(err, t);
      setError(message);
      showNotification(`${t('notification.login_failed')}: ${message}`, 'error');
    } finally {
      setLoading(false);
    }
  }, [
    apiBase,
    detectedBase,
    login,
    managementKey,
    navigate,
    rememberPassword,
    showNotification,
    t,
  ]);

  const handleSubmitKeyDown = useCallback(
    (event: React.KeyboardEvent) => {
      if (event.key === 'Enter' && !loading) {
        event.preventDefault();
        handleSubmit();
      }
    },
    [loading, handleSubmit]
  );

  if (isAuthenticated && !autoLoading && !autoLoginSuccess) {
    return <Navigate to={loginRedirectTarget(location.state)} replace />;
  }

  // Show the splash animation while auto-login is pending or has succeeded.
  const showSplash = autoLoading || autoLoginSuccess;

  const brandLogo = siteLogo || INLINE_LOGO_JPEG;
  const splashTitle = siteName || t('splash.title');
  const splashSubtitle = siteSubtitle || t('splash.subtitle');
  const brandWords = siteName
    ? siteName.split(/\s+/).filter(Boolean)
    : ['CLI', 'PROXY', 'API'];

  return (
    <div className={styles.container}>
      {/* Left brand panel */}
      <div className={styles.brandPanel}>
        <div className={styles.brandContent}>
          {brandWords.map((word) => (
            <span key={word} className={styles.brandWord}>
              {word}
            </span>
          ))}
          {siteSubtitle ? <span className={styles.brandSubtitle}>{siteSubtitle}</span> : null}
        </div>
      </div>

      {/* Right interaction panel */}
      <div className={styles.formPanel}>
        {showSplash ? (
          /* Splash animation */
          <div className={styles.splashContent}>
            <img src={brandLogo} alt="" className={styles.splashLogo} />
            <h1 className={styles.splashTitle}>{splashTitle}</h1>
            <p className={styles.splashSubtitle}>{splashSubtitle}</p>
            <div className={styles.splashLoader}>
              <div className={styles.splashLoaderBar} />
            </div>
          </div>
        ) : (
          /* Login form */
          <div className={styles.formContent}>
            {/* Logo */}
            <img src={brandLogo} alt="" className={styles.logo} />

            {/* Login card */}
            <div className={styles.loginCard}>
              <div className={styles.loginHeader}>
                <div className={styles.titleRow}>
                  <div className={styles.title}>{t('title.login')}</div>
                  <Select
                    className={styles.languageSelect}
                    value={language}
                    options={languageOptions}
                    onChange={handleLanguageChange}
                    fullWidth={false}
                    ariaLabel={t('language.switch')}
                  />
                </div>
                <div className={styles.subtitle}>
                  {t(sharing ? 'sharing.login_subtitle' : 'login.subtitle')}
                </div>
              </div>

              {sharing ? (
                <AccountLoginForm apiBase={detectedBase} needsSetup={sharing.needs_setup} />
              ) : (
                <>
                  <div className={styles.connectionBox}>
                    <div className={styles.label}>{t('login.connection_current')}</div>
                    <div className={styles.value}>{apiBase || detectedBase}</div>
                    <div className={styles.hint}>{t('login.connection_auto_hint')}</div>
                  </div>

                  <div className={styles.toggleAdvanced}>
                    <SelectionCheckbox
                      checked={showCustomBase}
                      onChange={setShowCustomBase}
                      ariaLabel={t('login.custom_connection_label')}
                      label={t('login.custom_connection_label')}
                      labelClassName={styles.toggleLabel}
                    />
                  </div>

                  {showCustomBase && (
                    <Input
                      label={t('login.custom_connection_label')}
                      placeholder={t('login.custom_connection_placeholder')}
                      value={apiBase}
                      onChange={(e) => setApiBase(e.target.value)}
                      hint={t('login.custom_connection_hint')}
                    />
                  )}

                  <Input
                    autoFocus
                    label={t('login.management_key_label')}
                    placeholder={t('login.management_key_placeholder')}
                    type={showKey ? 'text' : 'password'}
                    name="cpa-management-key"
                    autoComplete="current-password"
                    value={managementKey}
                    onChange={(e) => setManagementKey(e.target.value)}
                    onKeyDown={handleSubmitKeyDown}
                    rightElement={
                      <button
                        type="button"
                        className="btn btn-ghost btn-sm"
                        onClick={() => setShowKey((prev) => !prev)}
                        aria-label={
                          showKey
                            ? t('login.hide_key', { defaultValue: '隐藏密钥' })
                            : t('login.show_key', { defaultValue: '显示密钥' })
                        }
                        title={
                          showKey
                            ? t('login.hide_key', { defaultValue: '隐藏密钥' })
                            : t('login.show_key', { defaultValue: '显示密钥' })
                        }
                      >
                        {showKey ? <IconEyeOff size={16} /> : <IconEye size={16} />}
                      </button>
                    }
                  />

                  <div className={styles.toggleAdvanced}>
                    <SelectionCheckbox
                      checked={rememberPassword}
                      onChange={setRememberPassword}
                      ariaLabel={t('login.remember_password_label')}
                      label={t('login.remember_password_label')}
                      labelClassName={styles.toggleLabel}
                    />
                  </div>

                  <Button fullWidth onClick={handleSubmit} loading={loading}>
                    {loading ? t('login.submitting') : t('login.submit_button')}
                  </Button>

                  {error && <div className={styles.errorBox}>{error}</div>}
                </>
              )}
            </div>
          </div>
        )}
      </div>
    </div>
  );
}
