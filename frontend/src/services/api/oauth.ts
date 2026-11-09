/**
 * OAuth and device-code login API
 */

import { apiClient } from './client';
import { sharingPath } from './sharing';
import { useAuthStore } from '@/stores/useAuthStore';
import { useChannelStore } from '@/features/sharing/useChannelStore';
const oauthPath = (path: string) => (useAuthStore.getState().account ? sharingPath(path) : path);
import {
  isManagementOAuthProviderKey,
  normalizeManagementOAuthProviderKey,
} from '@/utils/providerKeys';

export type BuiltInOAuthProvider =
  | 'codex'
  | 'anthropic'
  | 'antigravity'
  | 'kiro'
  | 'kimi'
  | 'kimi-ai'
  | 'qoder'
  | 'qoder-cn'
  | 'xai'
  | 'devin'
  | 'meta'
  | 'zcode';

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

export const oauthApi = {
  startAuth: (provider: string, signal?: AbortSignal, extraParams?: Record<string, string>) => {
    const providerKey = normalizeProviderForManagementPath(provider);
    const params: Record<string, string | boolean> = { ...extraParams, provider: providerKey };
    if (useAuthStore.getState().account) {
      const channel = useChannelStore.getState().selected;
      if (!channel) throw new Error('Select a channel before authorizing a subscription');
      params.channel_id = channel;
    }
    if (WEBUI_SUPPORTED.has(providerKey)) {
      params.is_webui = true;
    }
    return apiClient.get<OAuthStartResponse>(oauthPath('/oauth/auth-url'), {
      params,
      ...(signal ? { signal } : {}),
    });
  },

  getAuthStatus: (state: string, signal?: AbortSignal) =>
    apiClient.get<{ status: 'ok' | 'wait' | 'error'; error?: string }>(oauthPath('/oauth/status'), {
      params: { state },
      ...(signal ? { signal } : {}),
    }),

  cancelSession: (state: string, signal?: AbortSignal) =>
    apiClient.delete<OAuthCancelResponse>(oauthPath('/oauth/session'), {
      params: { state },
      ...(signal ? { signal } : {}),
    }),

  submitCallback: (provider: string, redirectUrl: string, signal?: AbortSignal) => {
    const providerKey = normalizeProviderForManagementPath(provider);
    return apiClient.post<OAuthCallbackResponse>(
      oauthPath('/oauth/callback'),
      { provider: providerKey, redirect_url: redirectUrl },
      signal ? { signal } : undefined
    );
  },
};
