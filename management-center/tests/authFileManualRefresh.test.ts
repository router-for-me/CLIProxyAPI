import { describe, expect, spyOn, test } from 'bun:test';
import { authFilesApi } from '../src/services/api/authFiles';
import { apiClient } from '../src/services/api/client';

describe('single credential manual refresh', () => {
  test('posts only the selected filename to the dedicated refresh endpoint', async () => {
    const post = spyOn(apiClient, 'post').mockResolvedValue({ ok: true });
    const patch = spyOn(apiClient, 'patch').mockResolvedValue({});
    try {
      const name = 'codex+account@example.com.json';
      await authFilesApi.requestManualRefresh(name);

      expect(post).toHaveBeenCalledTimes(1);
      expect(post).toHaveBeenCalledWith('/auth-files/refresh', { name });
      expect(patch).not.toHaveBeenCalled();
    } finally {
      post.mockRestore();
      patch.mockRestore();
    }
  });

  test('propagates refresh failures without falling back to metadata edits', async () => {
    const error = new Error('refresh failed');
    const post = spyOn(apiClient, 'post').mockRejectedValue(error);
    const patch = spyOn(apiClient, 'patch').mockResolvedValue({});
    try {
      await expect(authFilesApi.requestManualRefresh('codex.json')).rejects.toThrow(
        'refresh failed'
      );
      expect(post).toHaveBeenCalledTimes(1);
      expect(patch).not.toHaveBeenCalled();
    } finally {
      post.mockRestore();
      patch.mockRestore();
    }
  });
});
