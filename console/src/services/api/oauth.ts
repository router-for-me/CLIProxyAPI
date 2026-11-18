/**
 * OAuth and device-code login API
 */

import { apiClient } from './client';
import {
  isManagementOAuthProviderKey,
  normalizeManagementOAuthProviderKey,
} from '@/utils/providerKeys';

export type BuiltInOAuthProvider =
  'codex' | 'anthropic' | 'antigravity' | 'kimi' | 'kimi-ai' | 'xai' | 'devin' | 'meta';

export interface OAuthStartResponse {
  url: string;
  state?: string;
  user_code?: string;
  flow?: string;
  expires_in?: number;
}

export interface OAuthCallbackResponse {
  status: 'ok';
}

export interface OAuthCancelResponse {
  status: 'ok';
  cancelled: boolean;
}

const WEBUI_SUPPORTED = new Set<string>(['codex', 'claude', 'antigravity', 'xai', 'devin']);

const normalizeProviderForManagementPath = (provider: string): string => {
  const key = normalizeManagementOAuthProviderKey(provider);
  if (!isManagementOAuthProviderKey(key)) {
    throw new Error('Invalid OAuth provider');
  }
  return key === 'anthropic' ? 'claude' : key;
};

export interface OAuthStartOptions {
  /**
   * Session-only proxy for this login and the credential it produces. The backend accepts it
   * only in a POST body; blank values keep the original GET request.
   */
  proxyUrl?: string;
}

export interface SiwcBindResponse {
  status: 'ok';
  url: string;
  state: string;
  flow: 'siwc_bind';
  expires_in: number;
}

export const oauthApi = {
  startSiwcBind: (name: string, signal?: AbortSignal) =>
    apiClient.post<SiwcBindResponse>('/oauth/siwc/bind', { name }, signal ? { signal } : undefined),

  startAuth: (provider: string, signal?: AbortSignal, options: OAuthStartOptions = {}) => {
    const providerKey = normalizeProviderForManagementPath(provider);
    const params: Record<string, string | boolean> = { provider: providerKey };
    if (WEBUI_SUPPORTED.has(providerKey)) {
      params.is_webui = true;
    }
    const proxyUrl = options.proxyUrl?.trim();
    if (proxyUrl) {
      // The backend rejects proxy_url in the query string, so everything moves to the body.
      return apiClient.post<OAuthStartResponse>(
        '/oauth/auth-url',
        { ...params, proxy_url: proxyUrl },
        signal ? { signal } : undefined
      );
    }
    return apiClient.get<OAuthStartResponse>('/oauth/auth-url', {
      params,
      ...(signal ? { signal } : {}),
    });
  },

  getAuthStatus: (state: string, signal?: AbortSignal) =>
    apiClient.get<{ status: 'ok' | 'wait' | 'error'; error?: string }>(`/oauth/status`, {
      params: { state },
      ...(signal ? { signal } : {}),
    }),

  cancelSession: (state: string, signal?: AbortSignal) =>
    apiClient.delete<OAuthCancelResponse>('/oauth/session', {
      params: { state },
      ...(signal ? { signal } : {}),
    }),

  submitCallback: (provider: string, redirectUrl: string, signal?: AbortSignal) => {
    const providerKey = normalizeProviderForManagementPath(provider);
    return apiClient.post<OAuthCallbackResponse>(
      '/oauth/callback',
      { provider: providerKey, redirect_url: redirectUrl },
      signal ? { signal } : undefined
    );
  },
};

/** Start and cleanup must use the same connection, even after the dialog closes. */
export function captureSiwcBindingApi() {
  const client = apiClient.captureConnection();
  return {
    start: (name: string) => client.post<SiwcBindResponse>('/oauth/siwc/bind', { name }),
    cancel: (state: string) => client.delete('/oauth/session', { params: { state } }),
  };
}
