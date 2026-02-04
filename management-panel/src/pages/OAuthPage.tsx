import { useCallback, useEffect, useRef, useState, type ChangeEvent } from 'react';
import { useTranslation } from 'react-i18next';
import { Card } from '@/components/ui/Card';
import { Button } from '@/components/ui/Button';
import { Input } from '@/components/ui/Input';
import { useNotificationStore, useThemeStore } from '@/stores';
import { oauthApi, type OAuthProvider, type IFlowCookieAuthResponse } from '@/services/api/oauth';
import { vertexApi, type VertexImportResponse } from '@/services/api/vertex';
import styles from './OAuthPage.module.scss';
import iconCodexLight from '@/assets/icons/codex_light.svg';
import iconCodexDark from '@/assets/icons/codex_drak.svg';
import iconClaude from '@/assets/icons/claude.svg';
import iconAntigravity from '@/assets/icons/antigravity.svg';
import iconGemini from '@/assets/icons/gemini.svg';
import iconQwen from '@/assets/icons/qwen.svg';
import iconIflow from '@/assets/icons/iflow.svg';
import iconVertex from '@/assets/icons/vertex.svg';
import iconRovo from '@/assets/icons/rovo.svg';

interface ProviderState {
  url?: string;
  state?: string;
  status?: 'idle' | 'waiting' | 'success' | 'error';
  error?: string;
  polling?: boolean;
  projectId?: string;
  projectIdError?: string;
  callbackUrl?: string;
  callbackSubmitting?: boolean;
  callbackStatus?: 'success' | 'error';
  callbackError?: string;
}

interface IFlowCookieState {
  cookie: string;
  loading: boolean;
  result?: IFlowCookieAuthResponse;
  error?: string;
  errorType?: 'error' | 'warning';
}

interface VertexImportResult {
  projectId?: string;
  email?: string;
  location?: string;
  authFile?: string;
}

interface VertexImportState {
  file?: File;
  fileName: string;
  location: string;
  loading: boolean;
  error?: string;
  result?: VertexImportResult;
}

const PROVIDERS: { id: OAuthProvider; titleKey: string; hintKey: string; urlLabelKey: string; icon: string | { light: string; dark: string } }[] = [
  { id: 'codex', titleKey: 'auth_login.codex_oauth_title', hintKey: 'auth_login.codex_oauth_hint', urlLabelKey: 'auth_login.codex_oauth_url_label', icon: { light: iconCodexLight, dark: iconCodexDark } },
  { id: 'anthropic', titleKey: 'auth_login.anthropic_oauth_title', hintKey: 'auth_login.anthropic_oauth_hint', urlLabelKey: 'auth_login.anthropic_oauth_url_label', icon: iconClaude },
  { id: 'antigravity', titleKey: 'auth_login.antigravity_oauth_title', hintKey: 'auth_login.antigravity_oauth_hint', urlLabelKey: 'auth_login.antigravity_oauth_url_label', icon: iconAntigravity },
  { id: 'gemini-cli', titleKey: 'auth_login.gemini_cli_oauth_title', hintKey: 'auth_login.gemini_cli_oauth_hint', urlLabelKey: 'auth_login.gemini_cli_oauth_url_label', icon: iconGemini },
  { id: 'qwen', titleKey: 'auth_login.qwen_oauth_title', hintKey: 'auth_login.qwen_oauth_hint', urlLabelKey: 'auth_login.qwen_oauth_url_label', icon: iconQwen }
];

const CALLBACK_SUPPORTED: OAuthProvider[] = ['codex', 'anthropic', 'antigravity', 'gemini-cli'];
const getProviderI18nPrefix = (provider: OAuthProvider) => provider.replace('-', '_');
const getAuthKey = (provider: OAuthProvider, suffix: string) =>
  `auth_login.${getProviderI18nPrefix(provider)}_${suffix}`;

const getIcon = (icon: string | { light: string; dark: string }, theme: 'light' | 'dark') => {
  return typeof icon === 'string' ? icon : icon[theme];
};

export function OAuthPage() {
  const { t } = useTranslation();
  const { showNotification } = useNotificationStore();
  const resolvedTheme = useThemeStore((state) => state.resolvedTheme);
  const [states, setStates] = useState<Record<OAuthProvider, ProviderState>>({} as Record<OAuthProvider, ProviderState>);
  const [iflowCookie, setIflowCookie] = useState<IFlowCookieState>({ cookie: '', loading: false });
  const [vertexState, setVertexState] = useState<VertexImportState>({
    fileName: '',
    location: '',
    loading: false
  });
  const timers = useRef<Record<string, number>>({});
  const vertexFileInputRef = useRef<HTMLInputElement | null>(null);

  const clearTimers = useCallback(() => {
    Object.values(timers.current).forEach((timer) => window.clearInterval(timer));
    timers.current = {};
  }, []);

  useEffect(() => {
    return () => {
      clearTimers();
    };
  }, [clearTimers]);

  const updateProviderState = (provider: OAuthProvider, next: Partial<ProviderState>) => {
    setStates((prev) => ({
      ...prev,
      [provider]: { ...(prev[provider] ?? {}), ...next }
    }));
  };

  const startPolling = (provider: OAuthProvider, state: string) => {
    if (timers.current[provider]) {
      clearInterval(timers.current[provider]);
    }
    const timer = window.setInterval(async () => {
      try {
        const res = await oauthApi.getAuthStatus(state);
        if (res.status === 'ok') {
          updateProviderState(provider, { status: 'success', polling: false });
          showNotification(t(getAuthKey(provider, 'oauth_status_success')), 'success');
          window.clearInterval(timer);
          delete timers.current[provider];
        } else if (res.status === 'error') {
          updateProviderState(provider, { status: 'error', error: res.error, polling: false });
          showNotification(
            `${t(getAuthKey(provider, 'oauth_status_error'))} ${res.error || ''}`,
            'error'
          );
          window.clearInterval(timer);
          delete timers.current[provider];
        }
      } catch (err: any) {
        updateProviderState(provider, { status: 'error', error: err?.message, polling: false });
        window.clearInterval(timer);
        delete timers.current[provider];
      }
    }, 3000);
    timers.current[provider] = timer;
  };

  const startAuth = async (provider: OAuthProvider) => {
    const projectId = provider === 'gemini-cli' ? (states[provider]?.projectId || '').trim() : undefined;
    // 项目 ID 现在是可选的，如果不输入将自动选择第一个可用项目
    if (provider === 'gemini-cli') {
      updateProviderState(provider, { projectIdError: undefined });
    }
    updateProviderState(provider, {
      status: 'waiting',
      polling: true,
      error: undefined,
      callbackStatus: undefined,
      callbackError: undefined,
      callbackUrl: ''
    });
    try {
      const res = await oauthApi.startAuth(
        provider,
        provider === 'gemini-cli' ? { projectId: projectId || undefined } : undefined
      );
      updateProviderState(provider, { url: res.url, state: res.state, status: 'waiting', polling: true });
      if (res.state) {
        startPolling(provider, res.state);
      }
    } catch (err: any) {
      updateProviderState(provider, { status: 'error', error: err?.message, polling: false });
      showNotification(`${t(getAuthKey(provider, 'oauth_start_error'))} ${err?.message || ''}`, 'error');
    }
  };

  const copyLink = async (url?: string) => {
    if (!url) return;
    try {
      await navigator.clipboard.writeText(url);
      showNotification(t('notification.link_copied'), 'success');
    } catch {
      showNotification('Copy failed', 'error');
    }
  };

  const submitCallback = async (provider: OAuthProvider) => {
    const redirectUrl = (states[provider]?.callbackUrl || '').trim();
    if (!redirectUrl) {
      showNotification(t('auth_login.oauth_callback_required'), 'warning');
      return;
    }
    updateProviderState(provider, {
      callbackSubmitting: true,
      callbackStatus: undefined,
      callbackError: undefined
    });
    try {
      await oauthApi.submitCallback(provider, redirectUrl);
      updateProviderState(provider, { callbackSubmitting: false, callbackStatus: 'success' });
      showNotification(t('auth_login.oauth_callback_success'), 'success');
    } catch (err: any) {
      const errorMessage =
        err?.status === 404
          ? t('auth_login.oauth_callback_upgrade_hint', {
              defaultValue: 'Please update CLI Proxy API or check the connection.'
            })
          : err?.message;
      updateProviderState(provider, {
        callbackSubmitting: false,
        callbackStatus: 'error',
        callbackError: errorMessage
      });
      const notificationMessage = errorMessage
        ? `${t('auth_login.oauth_callback_error')} ${errorMessage}`
        : t('auth_login.oauth_callback_error');
      showNotification(notificationMessage, 'error');
    }
  };

  const submitIflowCookie = async () => {
    const cookie = iflowCookie.cookie.trim();
    if (!cookie) {
      showNotification(t('auth_login.iflow_cookie_required'), 'warning');
      return;
    }
    setIflowCookie((prev) => ({
      ...prev,
      loading: true,
      error: undefined,
      errorType: undefined,
      result: undefined
    }));
    try {
      const res = await oauthApi.iflowCookieAuth(cookie);
      if (res.status === 'ok') {
        setIflowCookie((prev) => ({ ...prev, loading: false, result: res }));
        showNotification(t('auth_login.iflow_cookie_status_success'), 'success');
      } else {
        setIflowCookie((prev) => ({
          ...prev,
          loading: false,
          error: res.error,
          errorType: 'error'
        }));
        showNotification(`${t('auth_login.iflow_cookie_status_error')} ${res.error || ''}`, 'error');
      }
    } catch (err: any) {
      if (err?.status === 409) {
        const message = t('auth_login.iflow_cookie_config_duplicate');
        setIflowCookie((prev) => ({ ...prev, loading: false, error: message, errorType: 'warning' }));
        showNotification(message, 'warning');
        return;
      }
      setIflowCookie((prev) => ({ ...prev, loading: false, error: err?.message, errorType: 'error' }));
      showNotification(`${t('auth_login.iflow_cookie_start_error')} ${err?.message || ''}`, 'error');
    }
  };

  const handleVertexFilePick = () => {
    vertexFileInputRef.current?.click();
  };

  const handleVertexFileChange = (event: ChangeEvent<HTMLInputElement>) => {
    const file = event.target.files?.[0];
    if (!file) return;
    if (!file.name.endsWith('.json')) {
      showNotification(t('vertex_import.file_required'), 'warning');
      event.target.value = '';
      return;
    }
    setVertexState((prev) => ({
      ...prev,
      file,
      fileName: file.name,
      error: undefined,
      result: undefined
    }));
    event.target.value = '';
  };

  const handleVertexImport = async () => {
    if (!vertexState.file) {
      const message = t('vertex_import.file_required');
      setVertexState((prev) => ({ ...prev, error: message }));
      showNotification(message, 'warning');
      return;
    }
    const location = vertexState.location.trim();
    setVertexState((prev) => ({ ...prev, loading: true, error: undefined, result: undefined }));
    try {
      const res: VertexImportResponse = await vertexApi.importCredential(
        vertexState.file,
        location || undefined
      );
      const result: VertexImportResult = {
        projectId: res.project_id,
        email: res.email,
        location: res.location,
        authFile: res['auth-file'] ?? res.auth_file
      };
      setVertexState((prev) => ({ ...prev, loading: false, result }));
      showNotification(t('vertex_import.success'), 'success');
    } catch (err: any) {
      const message = err?.message || '';
      setVertexState((prev) => ({
        ...prev,
        loading: false,
        error: message || t('notification.upload_failed')
      }));
      const notification = message
        ? `${t('notification.upload_failed')}: ${message}`
        : t('notification.upload_failed');
      showNotification(notification, 'error');
    }
  };

  return (
    <div className={styles.container}>
      <h1 className={styles.pageTitle}>{t('nav.oauth', { defaultValue: 'OAuth' })}</h1>

      <div className={styles.content}>
        {PROVIDERS.map((provider) => {
          const state = states[provider.id] || {};
          const canSubmitCallback = CALLBACK_SUPPORTED.includes(provider.id) && Boolean(state.url);
          return (
            <div key={provider.id}>
              <Card
                title={
                  <span className={styles.cardTitle}>
                    <img
                      src={getIcon(provider.icon, resolvedTheme)}
                      alt=""
                      className={styles.cardTitleIcon}
                    />
                    {t(provider.titleKey)}
                  </span>
                }
                extra={
                  <Button onClick={() => startAuth(provider.id)} loading={state.polling}>
                    {t('common.login')}
                  </Button>
                }
              >
                <div className="hint">{t(provider.hintKey)}</div>
                {provider.id === 'gemini-cli' && (
                  <div className={styles.geminiProjectField}>
                    <Input
                      label={t('auth_login.gemini_cli_project_id_label')}
                      hint={t('auth_login.gemini_cli_project_id_hint')}
                      value={state.projectId || ''}
                      error={state.projectIdError}
                      onChange={(e) =>
                        updateProviderState(provider.id, {
                          projectId: e.target.value,
                          projectIdError: undefined
                        })
                      }
                      placeholder={t('auth_login.gemini_cli_project_id_placeholder')}
                    />
                  </div>
                )}
                {state.url && (
                  <div className={`connection-box ${styles.authUrlBox}`}>
                    <div className={styles.authUrlLabel}>{t(provider.urlLabelKey)}</div>
                    <div className={styles.authUrlValue}>{state.url}</div>
                    <div className={styles.authUrlActions}>
                      <Button variant="secondary" size="sm" onClick={() => copyLink(state.url!)}>
                        {t(getAuthKey(provider.id, 'copy_link'))}
                      </Button>
                      <Button
                        variant="secondary"
                        size="sm"
                        onClick={() => window.open(state.url, '_blank', 'noopener,noreferrer')}
                      >
                        {t(getAuthKey(provider.id, 'open_link'))}
                      </Button>
                    </div>
                  </div>
                )}
                {canSubmitCallback && (
                  <div className={styles.callbackSection}>
                    <Input
                      label={t('auth_login.oauth_callback_label')}
                      hint={t('auth_login.oauth_callback_hint')}
                      value={state.callbackUrl || ''}
                      onChange={(e) =>
                        updateProviderState(provider.id, {
                          callbackUrl: e.target.value,
                          callbackStatus: undefined,
                          callbackError: undefined
                        })
                      }
                      placeholder={t('auth_login.oauth_callback_placeholder')}
                    />
                    <div className={styles.callbackActions}>
                      <Button
                        variant="secondary"
                        size="sm"
                        onClick={() => submitCallback(provider.id)}
                        loading={state.callbackSubmitting}
                      >
                        {t('auth_login.oauth_callback_button')}
                      </Button>
                    </div>
                    {state.callbackStatus === 'success' && state.status === 'waiting' && (
                      <div className="status-badge success" style={{ marginTop: 8 }}>
                        {t('auth_login.oauth_callback_status_success')}
                      </div>
                    )}
                    {state.callbackStatus === 'error' && (
                      <div className="status-badge error" style={{ marginTop: 8 }}>
                        {t('auth_login.oauth_callback_status_error')} {state.callbackError || ''}
                      </div>
                    )}
                  </div>
                )}
                {state.status && state.status !== 'idle' && (
                  <div className="status-badge" style={{ marginTop: 8 }}>
                    {state.status === 'success'
                      ? t(getAuthKey(provider.id, 'oauth_status_success'))
                      : state.status === 'error'
                        ? `${t(getAuthKey(provider.id, 'oauth_status_error'))} ${state.error || ''}`
                        : t(getAuthKey(provider.id, 'oauth_status_waiting'))}
                  </div>
                )}
              </Card>
            </div>
          );
        })}

        {/* Rovo (Atlassian) Login */}
        <Card
          title={
            <span className={styles.cardTitle}>
              <img src={iconRovo} alt="" className={styles.cardTitleIcon} />
              {t('auth_login.rovo_title', { defaultValue: 'Rovo (Atlassian)' })}
            </span>
          }
          extra={
            <Button onClick={() => window.open('/rovo-auth.html', '_blank', 'noopener,noreferrer')}>
              {t('auth_login.rovo_open_button', { defaultValue: 'Open Auth Page' })}
            </Button>
          }
        >
          <div className="hint">
            {t('auth_login.rovo_hint', {
              defaultValue: 'Add Atlassian Rovo credentials to access Claude, GPT-5, and other AI models through Atlassian Rovo.'
            })}
          </div>
          <div className="hint" style={{ marginTop: 8 }}>
            {t('auth_login.rovo_instructions', {
              defaultValue: 'Click the button to open the Rovo authentication page where you can enter your Atlassian email and API token.'
            })}
          </div>

        </Card>

        {/* Vertex JSON 登录 */}
        <Card
          title={
            <span className={styles.cardTitle}>
              <img src={iconVertex} alt="" className={styles.cardTitleIcon} />
              {t('vertex_import.title')}
            </span>
          }
          extra={
            <Button onClick={handleVertexImport} loading={vertexState.loading}>
              {t('vertex_import.import_button')}
            </Button>
          }
        >
          <div className="hint">{t('vertex_import.description')}</div>
          <Input
            label={t('vertex_import.location_label')}
            hint={t('vertex_import.location_hint')}
            value={vertexState.location}
            onChange={(e) =>
              setVertexState((prev) => ({
                ...prev,
                location: e.target.value
              }))
            }
            placeholder={t('vertex_import.location_placeholder')}
          />
          <div className="form-group">
            <label>{t('vertex_import.file_label')}</label>
            <div className={styles.filePicker}>
              <Button variant="secondary" size="sm" onClick={handleVertexFilePick}>
                {t('vertex_import.choose_file')}
              </Button>
              <div
                className={`${styles.fileName} ${
                  vertexState.fileName ? '' : styles.fileNamePlaceholder
                }`.trim()}
              >
                {vertexState.fileName || t('vertex_import.file_placeholder')}
              </div>
            </div>
            <div className="hint">{t('vertex_import.file_hint')}</div>
            <input
              ref={vertexFileInputRef}
              type="file"
              accept=".json,application/json"
              style={{ display: 'none' }}
              onChange={handleVertexFileChange}
            />
          </div>
          {vertexState.error && (
            <div className="status-badge error" style={{ marginTop: 8 }}>
              {vertexState.error}
            </div>
          )}
          {vertexState.result && (
            <div className="connection-box" style={{ marginTop: 12 }}>
              <div className="label">{t('vertex_import.result_title')}</div>
              <div className="key-value-list">
                {vertexState.result.projectId && (
                  <div className="key-value-item">
                    <span className="key">{t('vertex_import.result_project')}</span>
                    <span className="value">{vertexState.result.projectId}</span>
                  </div>
                )}
                {vertexState.result.email && (
                  <div className="key-value-item">
                    <span className="key">{t('vertex_import.result_email')}</span>
                    <span className="value">{vertexState.result.email}</span>
                  </div>
                )}
                {vertexState.result.location && (
                  <div className="key-value-item">
                    <span className="key">{t('vertex_import.result_location')}</span>
                    <span className="value">{vertexState.result.location}</span>
                  </div>
                )}
                {vertexState.result.authFile && (
                  <div className="key-value-item">
                    <span className="key">{t('vertex_import.result_file')}</span>
                    <span className="value">{vertexState.result.authFile}</span>
                  </div>
                )}
              </div>
            </div>
          )}
        </Card>

        {/* iFlow Cookie 登录 */}
        <Card
          title={
            <span className={styles.cardTitle}>
              <img src={iconIflow} alt="" className={styles.cardTitleIcon} />
              {t('auth_login.iflow_cookie_title')}
            </span>
          }
          extra={
            <Button onClick={submitIflowCookie} loading={iflowCookie.loading}>
              {t('auth_login.iflow_cookie_button')}
            </Button>
          }
        >
          <div className="hint">{t('auth_login.iflow_cookie_hint')}</div>
          <div className="hint" style={{ marginTop: 4 }}>
            {t('auth_login.iflow_cookie_key_hint')}
          </div>
          <div className="form-item" style={{ marginTop: 12 }}>
            <label className="label">{t('auth_login.iflow_cookie_label')}</label>
            <Input
              value={iflowCookie.cookie}
              onChange={(e) => setIflowCookie((prev) => ({ ...prev, cookie: e.target.value }))}
              placeholder={t('auth_login.iflow_cookie_placeholder')}
            />
          </div>
          {iflowCookie.error && (
            <div
              className={`status-badge ${iflowCookie.errorType === 'warning' ? 'warning' : 'error'}`}
              style={{ marginTop: 8 }}
            >
              {iflowCookie.errorType === 'warning'
                ? t('auth_login.iflow_cookie_status_duplicate')
                : t('auth_login.iflow_cookie_status_error')}{' '}
              {iflowCookie.error}
            </div>
          )}
          {iflowCookie.result && iflowCookie.result.status === 'ok' && (
            <div className="connection-box" style={{ marginTop: 12 }}>
              <div className="label">{t('auth_login.iflow_cookie_result_title')}</div>
              <div className="key-value-list">
                {iflowCookie.result.email && (
                  <div className="key-value-item">
                    <span className="key">{t('auth_login.iflow_cookie_result_email')}</span>
                    <span className="value">{iflowCookie.result.email}</span>
                  </div>
                )}
                {iflowCookie.result.expired && (
                  <div className="key-value-item">
                    <span className="key">{t('auth_login.iflow_cookie_result_expired')}</span>
                    <span className="value">{iflowCookie.result.expired}</span>
                  </div>
                )}
                {iflowCookie.result.saved_path && (
                  <div className="key-value-item">
                    <span className="key">{t('auth_login.iflow_cookie_result_path')}</span>
                    <span className="value">{iflowCookie.result.saved_path}</span>
                  </div>
                )}
                {iflowCookie.result.type && (
                  <div className="key-value-item">
                    <span className="key">{t('auth_login.iflow_cookie_result_type')}</span>
                    <span className="value">{iflowCookie.result.type}</span>
                  </div>
                )}
              </div>
            </div>
          )}
        </Card>
      </div>
    </div>
  );
}
