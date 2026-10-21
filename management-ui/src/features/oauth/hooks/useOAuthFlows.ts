import { useCallback, useEffect, useRef, useState } from 'react';
import { useTranslation } from 'react-i18next';
import { useAuthStore, useNotificationStore } from '@/stores';
import { oauthApi, pluginsApi } from '@/services/api';
import { getErrorMessage, isRecord } from '@/utils/helpers';
import { notifyAuthFilesChanged } from '@/features/authFiles/authFilesEvents';
import { createOAuthAttempts, type OAuthAttempt } from '../oauthAttempts';
import { validateDevinCallback } from '../devinOAuth';
import { resolveCallbackUrl } from '../callbackUrl';
import {
  buildPluginOAuthProviderCards,
  type OAuthProviderCard,
  type PluginOAuthProviderCard,
} from '../providers';

export interface ProviderFlowState {
  url?: string;
  userCode?: string;
  state?: string;
  status?: 'waiting' | 'success' | 'error';
  error?: string;
  polling?: boolean;
  cancelling?: boolean;
  cancelError?: string;
  callbackUrl?: string;
  callbackSubmitting?: boolean;
  callbackStatus?: 'success' | 'error';
  callbackError?: string;
  /** 本地校验失败（未提交到后端），内联展示在输入框下。 */
  callbackValidation?: string;
}

interface UseOAuthFlowsOptions {
  providerCards: OAuthProviderCard[];
  /**
   * 该提供商的对话框是否正开着。开着时结果已在对话框内联展示，不再重复弹 toast；
   * 成功态也会保留到对话框关闭，而不是 5 秒后自动清空。
   */
  isFocused: (provider: string) => boolean;
}

const SUCCESS_RESET_DELAY_MS = 5000;
const POLL_INTERVAL_MS = 3000;

const getAuthKey = (provider: string, suffix: string) =>
  `auth_login.${provider.replace('-', '_')}_${suffix}`;

function getErrorStatus(error: unknown): number | undefined {
  if (!isRecord(error)) return undefined;
  return typeof error.status === 'number' ? error.status : undefined;
}

/** 已启用且声明 OAuth 能力的插件 → 追加到内置提供商之后。 */
export function usePluginOAuthProviders(apiBase: string) {
  const [pluginProviders, setPluginProviders] = useState<PluginOAuthProviderCard[]>([]);

  useEffect(() => {
    let cancelled = false;
    pluginsApi
      .list()
      .then((response) => {
        if (!cancelled)
          setPluginProviders(buildPluginOAuthProviderCards(response.plugins, apiBase));
      })
      .catch(() => {
        if (!cancelled) setPluginProviders([]);
      });
    return () => {
      cancelled = true;
    };
  }, [apiBase]);

  return pluginProviders;
}

/**
 * 每个提供商一条独立的登录尝试（attempt）：开始 → 轮询状态 → 成功/失败，
 * 可选的手动回调提交与 Devin 会话取消。尝试失效后，已发出的请求落地也不再写状态。
 */
export function useOAuthFlows({ providerCards, isFocused }: UseOAuthFlowsOptions) {
  const { t } = useTranslation();
  const { showNotification } = useNotificationStore();
  const [states, setStates] = useState<Record<string, ProviderFlowState>>({});
  /** 本次会话新增的凭证数（含 Vertex 导入），换连接时归零。 */
  const [addedCount, setAddedCount] = useState(0);
  const recordCredentialAdded = useCallback(() => setAddedCount((count) => count + 1), []);
  const attempts = useRef(
    createOAuthAttempts({
      setTimeout: (callback, delay) => window.setTimeout(callback, delay),
      clearTimeout: (timer) => window.clearTimeout(timer),
    })
  );

  const clearTimers = useCallback(() => {
    attempts.current.invalidateAll();
  }, []);

  useEffect(() => {
    // Invalidate synchronously on connection changes, including a new key on
    // the same server. Never send cleanup requests through the new connection.
    const unsubscribe = useAuthStore.subscribe((current, previous) => {
      if (
        current.apiBase !== previous.apiBase ||
        current.managementKey !== previous.managementKey ||
        current.isAuthenticated !== previous.isAuthenticated
      ) {
        clearTimers();
        setStates({});
        setAddedCount(0);
      }
    });
    return () => {
      unsubscribe();
      clearTimers();
    };
  }, [clearTimers]);

  const providerText = (provider: string, suffix: string) => {
    const card = providerCards.find((item) => item.id === provider);
    return card?.kind === 'plugin'
      ? t(`auth_login.plugin_${suffix}`, { name: card.title })
      : t(getAuthKey(provider, suffix));
  };

  /** 对话框没开时，toast 是唯一的结果反馈。 */
  const notify = (provider: string, message: string, type: 'success' | 'error' | 'warning') => {
    if (!isFocused(provider)) showNotification(message, type);
  };

  const updateProviderState = (provider: string, next: Partial<ProviderFlowState>) => {
    setStates((prev) => ({
      ...prev,
      [provider]: { ...(prev[provider] ?? {}), ...next },
    }));
  };

  const resetProviderAttempt = (provider: string) => {
    attempts.current.get(provider)?.invalidate();
    setStates((prev) => ({ ...prev, [provider]: {} }));
  };

  const completeProviderAuth = (provider: string) => {
    const resetAttempt = attempts.current.begin(provider);
    notifyAuthFilesChanged();
    recordCredentialAdded();
    updateProviderState(provider, {
      url: undefined,
      state: undefined,
      status: 'success',
      error: undefined,
      polling: false,
      cancelling: false,
      cancelError: undefined,
      callbackUrl: '',
      callbackSubmitting: false,
      callbackStatus: undefined,
      callbackError: undefined,
      callbackValidation: undefined,
    });
    // 后台完成时磁贴短暂显示「已添加」；对话框开着则保留成功页，关闭时再清。
    resetAttempt.schedule(() => {
      if (!isFocused(provider)) resetProviderAttempt(provider);
    }, SUCCESS_RESET_DELAY_MS);
  };

  const startPolling = (provider: string, state: string, attempt: OAuthAttempt) => {
    attempt.poll(
      () => oauthApi.getAuthStatus(state, attempt.signal),
      (res) => {
        if (res.status === 'ok') {
          completeProviderAuth(provider);
          notify(provider, providerText(provider, 'oauth_status_success'), 'success');
        } else if (res.status === 'error') {
          if (provider === 'devin') {
            // Expired, denied and cancelled states cannot accept another callback.
            attempt.invalidate();
            updateProviderState(provider, {
              url: undefined,
              state: undefined,
              callbackUrl: '',
              callbackSubmitting: false,
              callbackStatus: undefined,
              callbackError: undefined,
              callbackValidation: undefined,
            });
          }
          updateProviderState(provider, { status: 'error', error: res.error, polling: false });
          notify(
            provider,
            `${providerText(provider, 'oauth_status_error')} ${res.error || ''}`,
            'error'
          );
        }
        return res.status === 'wait';
      },
      (err) => {
        updateProviderState(provider, {
          status: 'error',
          error: getErrorMessage(err),
          polling: false,
        });
      },
      POLL_INTERVAL_MS
    );
  };

  const cancelAuth = async (provider: string) => {
    const state = states[provider]?.state;
    if (provider !== 'devin' || !state || states[provider]?.cancelling) return;
    // Replace the attempt before DELETE so late polls/callback submissions cannot
    // overwrite the cancellation result or a subsequent login.
    const attempt = attempts.current.begin(provider);
    updateProviderState(provider, {
      cancelling: true,
      cancelError: undefined,
      polling: true,
      callbackSubmitting: false,
      callbackStatus: undefined,
      callbackError: undefined,
      callbackValidation: undefined,
    });
    try {
      const result = await oauthApi.cancelSession(state, attempt.signal);
      if (!attempt.isCurrent()) return;
      if (result.cancelled) {
        resetProviderAttempt(provider);
        notify(provider, t('auth_login.devin_oauth_cancelled'), 'success');
        return;
      }
      // A completed or expired session returns cancelled=false. Read its real
      // status rather than claiming cancellation or losing a completed login.
    } catch (err: unknown) {
      if (!attempt.isCurrent()) return;
      const message = getErrorMessage(err);
      updateProviderState(provider, { cancelError: message });
      notify(provider, `${t('auth_login.devin_oauth_cancel_error')} ${message}`, 'error');
    }
    updateProviderState(provider, {
      cancelling: false,
      status: 'waiting',
      error: undefined,
    });
    startPolling(provider, state, attempt);
  };

  const startAuth = async (provider: string) => {
    // A network error can stop polling while the server is still waiting. Require
    // explicit cancellation before replacing that Devin session.
    if (provider === 'devin' && states[provider]?.state) return;
    const attempt = attempts.current.begin(provider);
    updateProviderState(provider, {
      url: undefined,
      userCode: undefined,
      state: undefined,
      status: 'waiting',
      polling: true,
      cancelling: false,
      cancelError: undefined,
      error: undefined,
      callbackStatus: undefined,
      callbackError: undefined,
      callbackValidation: undefined,
      callbackUrl: '',
      callbackSubmitting: false,
    });
    try {
      const res = await oauthApi.startAuth(provider, attempt.signal);
      if (!attempt.isCurrent()) return;
      if (!res.state) {
        const message = t('auth_login.missing_state');
        updateProviderState(provider, {
          url: res.url,
          state: undefined,
          status: 'error',
          error: message,
          polling: false,
        });
        notify(provider, message, 'error');
        return;
      }
      updateProviderState(provider, {
        url: res.url,
        userCode: res.user_code,
        state: res.state,
        status: 'waiting',
        polling: true,
      });
      startPolling(provider, res.state, attempt);
    } catch (err: unknown) {
      if (!attempt.isCurrent()) return;
      const message = getErrorMessage(err);
      updateProviderState(provider, { status: 'error', error: message, polling: false });
      notify(
        provider,
        `${providerText(provider, 'oauth_start_error')}${message ? ` ${message}` : ''}`,
        'error'
      );
    }
  };

  const setCallbackInput = (provider: string, value: string) => {
    updateProviderState(provider, {
      callbackUrl: value,
      callbackStatus: undefined,
      callbackError: undefined,
      callbackValidation: undefined,
    });
  };

  const submitCallback = async (provider: string) => {
    const attempt = attempts.current.get(provider);
    if (!attempt?.isCurrent()) return;
    if (
      provider === 'devin' &&
      (states[provider]?.cancelling || states[provider]?.status !== 'waiting')
    ) {
      return;
    }
    const rejectInput = (key: string) =>
      updateProviderState(provider, { callbackValidation: t(key) });
    const callbackInput = (states[provider]?.callbackUrl || '').trim();
    if (!callbackInput) {
      rejectInput(
        provider === 'xai'
          ? 'auth_login.xai_callback_required'
          : 'auth_login.oauth_callback_required'
      );
      return;
    }
    if (provider === 'devin') {
      const callbackError = validateDevinCallback(callbackInput, states[provider]?.state);
      if (callbackError) {
        rejectInput(`auth_login.devin_callback_${callbackError}`);
        return;
      }
    }
    const redirectUrl = resolveCallbackUrl(provider, callbackInput, states[provider]?.state);
    if (!redirectUrl) {
      rejectInput(
        provider === 'xai' ? 'auth_login.xai_callback_state_missing' : 'auth_login.missing_state'
      );
      return;
    }
    updateProviderState(provider, {
      callbackSubmitting: true,
      callbackStatus: undefined,
      callbackError: undefined,
      callbackValidation: undefined,
    });
    try {
      await oauthApi.submitCallback(provider, redirectUrl, attempt.signal);
      if (!attempt.isCurrent()) return;
      updateProviderState(provider, { callbackSubmitting: false, callbackStatus: 'success' });
      notify(provider, t('auth_login.oauth_callback_success'), 'success');
      // The backend only accepts callbacks for pending logins. If a failed status
      // request stopped polling, resume it so the card can report the outcome.
      const loginState = states[provider]?.state;
      if (loginState && !attempt.isPolling()) {
        updateProviderState(provider, { status: 'waiting', error: undefined, polling: true });
        startPolling(provider, loginState, attempt);
      }
    } catch (err: unknown) {
      if (!attempt.isCurrent()) return;
      const status = getErrorStatus(err);
      const message = getErrorMessage(err);
      const errorMessage =
        status === 404
          ? t('auth_login.oauth_callback_upgrade_hint', {
              defaultValue: 'Please update CLI Proxy API or check the connection.',
            })
          : message || undefined;
      updateProviderState(provider, {
        callbackSubmitting: false,
        callbackStatus: 'error',
        callbackError: errorMessage,
      });
      notify(
        provider,
        errorMessage
          ? `${t('auth_login.oauth_callback_error')} ${errorMessage}`
          : t('auth_login.oauth_callback_error'),
        'error'
      );
    }
  };

  /** 关闭对话框时收起成功页，磁贴回到可再次登录的初始态。 */
  const dismissSuccess = (provider: string) => {
    if (states[provider]?.status === 'success') resetProviderAttempt(provider);
  };

  return {
    states,
    addedCount,
    recordCredentialAdded,
    providerText,
    startAuth,
    cancelAuth,
    submitCallback,
    setCallbackInput,
    dismissSuccess,
  };
}
