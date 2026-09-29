import { describe, expect, spyOn, test } from 'bun:test';
import { apiClient } from '@/services/api/client';
import { oauthApi, type BuiltInOAuthProvider } from '@/services/api/oauth';

describe('Devin Management OAuth v7.3.1 contract', () => {
  test('starts a built-in Devin flow with web UI mode', async () => {
    const provider: BuiltInOAuthProvider = 'devin';
    const response = {
      status: 'ok',
      url: 'https://app.devin.ai/auth/cli/continue',
      state: 'test-state',
    };
    const get = spyOn(apiClient, 'get').mockResolvedValue(response);
    try {
      expect(await oauthApi.startAuth(provider)).toEqual(response);
      expect(get).toHaveBeenCalledWith('/devin-auth-url', { params: { is_webui: true } });
    } finally {
      get.mockRestore();
    }
  });

  test('submits the complete remote callback without rewriting its port or TLS mode', async () => {
    const post = spyOn(apiClient, 'post').mockResolvedValue({ status: 'ok' });
    const redirectUrl = 'https://127.0.0.1:9443/devin/callback?code=fixture-code&state=test-state';
    try {
      await oauthApi.submitCallback('devin', redirectUrl);
      expect(post).toHaveBeenCalledWith(
        '/oauth-callback',
        { provider: 'devin', redirect_url: redirectUrl },
        undefined
      );
    } finally {
      post.mockRestore();
    }
  });

  test('polls using only the original attempt state', async () => {
    const get = spyOn(apiClient, 'get').mockResolvedValue({ status: 'wait' });
    try {
      expect(await oauthApi.getAuthStatus('test-state')).toEqual({ status: 'wait' });
      expect(get).toHaveBeenCalledWith('/get-auth-status', { params: { state: 'test-state' } });
    } finally {
      get.mockRestore();
    }
  });

  for (const cancelled of [true, false]) {
    test(`preserves the server cancellation result (${cancelled})`, async () => {
      const remove = spyOn(apiClient, 'delete').mockResolvedValue({ status: 'ok', cancelled });
      try {
        expect(await oauthApi.cancelSession('test-state')).toEqual({ status: 'ok', cancelled });
        expect(remove).toHaveBeenCalledWith('/oauth-session', { params: { state: 'test-state' } });
      } finally {
        remove.mockRestore();
      }
    });
  }

  test('forwards attempt abort signals to every OAuth request', async () => {
    const signal = new AbortController().signal;
    const get = spyOn(apiClient, 'get').mockResolvedValue({});
    const post = spyOn(apiClient, 'post').mockResolvedValue({});
    const remove = spyOn(apiClient, 'delete').mockResolvedValue({});
    try {
      await oauthApi.startAuth('devin', signal);
      expect(get).toHaveBeenLastCalledWith('/devin-auth-url', {
        params: { is_webui: true },
        signal,
      });
      await oauthApi.getAuthStatus('test-state', signal);
      expect(get).toHaveBeenLastCalledWith('/get-auth-status', {
        params: { state: 'test-state' },
        signal,
      });
      await oauthApi.submitCallback('devin', 'fixture-callback', signal);
      expect(post).toHaveBeenCalledWith(
        '/oauth-callback',
        {
          provider: 'devin',
          redirect_url: 'fixture-callback',
        },
        { signal }
      );
      await oauthApi.cancelSession('test-state', signal);
      expect(remove).toHaveBeenCalledWith('/oauth-session', {
        params: { state: 'test-state' },
        signal,
      });
    } finally {
      get.mockRestore();
      post.mockRestore();
      remove.mockRestore();
    }
  });

  test('propagates cancellation failures instead of claiming success', async () => {
    const error = new Error('connection lost');
    const remove = spyOn(apiClient, 'delete').mockRejectedValue(error);
    try {
      await expect(oauthApi.cancelSession('test-state')).rejects.toBe(error);
    } finally {
      remove.mockRestore();
    }
  });
});
