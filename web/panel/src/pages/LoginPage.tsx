import { useEffect, useMemo, useRef, useState, useCallback } from 'react';
import { Navigate, useNavigate, useLocation } from 'react-router-dom';
import { useTranslation } from 'react-i18next';
import { Select } from '@/components/ui/Select';
import { useAuthStore, useLanguageStore, useNotificationStore } from '@/stores';
import {
  getLoginTransitionTimings,
  useLoginTransitionStore,
} from '@/stores/useLoginTransitionStore';
import { prefersReducedMotion } from '@/hooks/motion';
import { detectApiBaseFromLocation, normalizeApiBase } from '@/utils/connection';
import { LANGUAGE_LABEL_KEYS, LANGUAGE_ORDER } from '@/utils/constants';
import { isSupportedLanguage } from '@/utils/language';
import { INLINE_LOGO_JPEG } from '@/assets/logoInline';
import type { ApiError } from '@/types';
import { LegacyBackendError } from '@/services/api/legacyBackendProbe';
import { DotMatrixHero } from './login/DotMatrixHero';
import { LoginForm } from './login/LoginForm';
import { resolveLoginRedirect } from '@/router/loginRedirect';
import styles from './LoginPage.module.scss';

export type LoginPhase = 'idle' | 'connecting' | 'success' | 'exiting';

/** 左上角品牌名：产品全称，所有语言下保持英文，不走 i18n */
export const LOGIN_BRAND_NAME = 'CLI Proxy API Management Center';

/** 语言下拉层最小宽度：最长的语言名（繁體中文（台灣））单行显示 */
const LANGUAGE_DROPDOWN_MIN_WIDTH = 180;

/**
 * 将 API 错误转换为本地化的用户友好消息
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

  // 根据 HTTP 状态码判断
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

  // 根据 axios 错误码判断
  if (code === 'ECONNABORTED' || message.toLowerCase().includes('timeout')) {
    return t('login.error_timeout');
  }
  if (code === 'ERR_NETWORK' || message.toLowerCase().includes('network error')) {
    return t('login.error_network');
  }
  if (code === 'ERR_CERT_AUTHORITY_INVALID' || message.toLowerCase().includes('certificate')) {
    return t('login.error_ssl');
  }

  // 检查 CORS 错误
  if (message.toLowerCase().includes('cors') || message.toLowerCase().includes('cross-origin')) {
    return t('login.error_cors');
  }

  // 默认错误消息
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

  const detectedBase = useMemo(() => detectApiBaseFromLocation(), []);
  // 首帧即用已保存的连接信息填好表单：没有可恢复会话时表单直接出现，不闪启动画面
  const [apiBase, setApiBase] = useState(() => useAuthStore.getState().apiBase || detectedBase);
  const [managementKey, setManagementKey] = useState(
    () => useAuthStore.getState().managementKey || ''
  );
  const [showCustomBase, setShowCustomBase] = useState(false);
  const [showKey, setShowKey] = useState(false);
  const [rememberPassword, setRememberPassword] = useState(() => {
    const { rememberPassword: remembered, managementKey: key } = useAuthStore.getState();
    return remembered || Boolean(key);
  });
  const [phase, setPhase] = useState<LoginPhase>('idle');
  // 仅在确实会自动登录时显示启动画面
  const [autoLoading, setAutoLoading] = useState(() =>
    useAuthStore.getState().hasRestorableSession()
  );
  const [autoLoginSuccess, setAutoLoginSuccess] = useState(false);
  const [error, setError] = useState('');

  const startReveal = useLoginTransitionStore((state) => state.startReveal);
  const exitTimersRef = useRef<number[]>([]);
  const loading = phase === 'connecting';
  const redirectTarget = resolveLoginRedirect(location.state);

  useEffect(
    () => () => {
      exitTimersRef.current.forEach((timer) => window.clearTimeout(timer));
    },
    []
  );

  const leaveTo = useCallback(
    (redirect: string, hold: number) => {
      const timings = getLoginTransitionTimings(prefersReducedMotion());
      const schedule = (callback: () => void, delay: number) => {
        exitTimersRef.current.push(window.setTimeout(callback, delay));
      };
      schedule(() => setPhase('exiting'), hold);
      schedule(() => {
        startReveal();
        navigate(redirect, { replace: true });
      }, hold + timings.exit);
    },
    [navigate, startReveal]
  );
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
    const splashStartedAt = performance.now();
    const init = async () => {
      try {
        const autoLoggedIn = await restoreSession();
        if (autoLoggedIn) {
          setAutoLoginSuccess(true);
          // 每次打开都会经过启动画面：网络快时补足最短停留，避免一闪而过；网络慢时不再额外等待
          const { splashMin } = getLoginTransitionTimings(prefersReducedMotion());
          const elapsed = performance.now() - splashStartedAt;
          leaveTo(redirectTarget, Math.max(0, splashMin - elapsed));
        } else {
          // 按最新的已保存信息回填表单（restoreSession 可能迁移了旧版存储）。
          // 自动登录被 401 拒绝时全局会登出并清空存储，此时保留首帧填入的地址与密钥，
          // 用户修正后即可直接重试，自定义地址也不会丢失。
          const stored = useAuthStore.getState();
          setApiBase((previous) => stored.apiBase || previous || detectedBase);
          setManagementKey((previous) => stored.managementKey || previous);
          setRememberPassword(
            (previous) => stored.rememberPassword || Boolean(stored.managementKey) || previous
          );
        }
      } finally {
        setAutoLoading(false);
      }
    };

    init();
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, []);

  const handleSubmit = useCallback(async () => {
    if (phase !== 'idle') return;
    if (!managementKey.trim()) {
      setError(t('login.error_required'));
      return;
    }

    const baseToUse = apiBase ? normalizeApiBase(apiBase) : detectedBase;
    // 先进入 connecting：登录成功瞬间 isAuthenticated 变真时不会被立即重定向打断
    setPhase('connecting');
    setError('');
    try {
      await login({
        apiBase: baseToUse,
        managementKey: managementKey.trim(),
        rememberPassword,
      });
    } catch (err: unknown) {
      const message = getLocalizedErrorMessage(err, t);
      setPhase('idle');
      setError(message);
      showNotification(`${t('notification.login_failed')}: ${message}`, 'error');
      return;
    }
    setPhase('success');
    leaveTo(redirectTarget, getLoginTransitionTimings(prefersReducedMotion()).successHold);
  }, [
    apiBase,
    detectedBase,
    leaveTo,
    login,
    managementKey,
    phase,
    redirectTarget,
    rememberPassword,
    showNotification,
    t,
  ]);

  if (isAuthenticated && !autoLoading && !autoLoginSuccess && phase === 'idle') {
    return <Navigate to={redirectTarget} replace />;
  }

  const showSplash = autoLoading || autoLoginSuccess;

  return (
    <div className={styles.container} data-phase={phase === 'exiting' ? 'exiting' : undefined}>
      <div className={styles.brandPanel}>
        <DotMatrixHero />
      </div>

      <div className={styles.formPanel}>
        {/* 自动登录启动页自带大徽标与品牌名，此时不再渲染顶栏，避免重复 */}
        {!showSplash && (
          <header className={styles.topBar}>
            <div className={styles.brandMark}>
              <img src={INLINE_LOGO_JPEG} alt="" className={styles.brandLogo} />
              <span className={styles.brandName} title={LOGIN_BRAND_NAME}>
                {LOGIN_BRAND_NAME}
              </span>
            </div>
            <Select
              className={styles.languageSelect}
              dropdownClassName={styles.languageDropdown}
              dropdownMinWidth={LANGUAGE_DROPDOWN_MIN_WIDTH}
              value={language}
              options={languageOptions}
              onChange={handleLanguageChange}
              fullWidth={false}
              size="sm"
              ariaLabel={t('language.switch')}
            />
          </header>
        )}

        <main className={styles.main}>
          {showSplash ? (
            <div className={styles.splash} role="status" aria-live="polite">
              <img src={INLINE_LOGO_JPEG} alt="" className={styles.splashLogo} />
              <div className={styles.splashText}>
                <h1 className={styles.splashTitle}>{t('splash.title')}</h1>
                <p className={styles.splashSubtitle}>{t('splash.subtitle')}</p>
              </div>
              <div className={styles.splashLoader} aria-hidden="true">
                <div className={styles.splashLoaderBar} />
              </div>
            </div>
          ) : (
            <div className={styles.column}>
              <h1 className={styles.title}>{t('splash.subtitle')}</h1>
              <LoginForm
                apiBase={apiBase}
                detectedBase={detectedBase}
                editingBase={showCustomBase}
                managementKey={managementKey}
                showKey={showKey}
                rememberPassword={rememberPassword}
                loading={loading}
                success={phase === 'success' || phase === 'exiting'}
                error={error}
                onApiBaseChange={setApiBase}
                onEditingBaseChange={setShowCustomBase}
                onManagementKeyChange={setManagementKey}
                onShowKeyChange={setShowKey}
                onRememberPasswordChange={setRememberPassword}
                onSubmit={handleSubmit}
              />
            </div>
          )}
        </main>
      </div>
    </div>
  );
}
