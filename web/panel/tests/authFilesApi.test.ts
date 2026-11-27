import { describe, expect, spyOn, test } from 'bun:test';
import { authFilesApi } from '@/services/api/authFiles';
import { apiClient } from '@/services/api/client';

// auth_index forwarding and token discarding are covered in authFileRefreshAll.test.ts.
describe('auth files API boundaries', () => {
  test('refreshing by filename posts only the name and never edits metadata', async () => {
    const post = spyOn(apiClient, 'post').mockResolvedValue({ access_token: 'secret' });
    const patch = spyOn(apiClient, 'patch').mockResolvedValue({});
    try {
      await expect(authFilesApi.requestManualRefresh('devin.json')).resolves.toBeUndefined();
      expect(post).toHaveBeenCalledTimes(1);
      expect(post).toHaveBeenCalledWith('/credentials/refresh', { name: 'devin.json' });
      expect(patch).not.toHaveBeenCalled();
    } finally {
      post.mockRestore();
      patch.mockRestore();
    }
  });

  test('passes an optional identity lookup to list and normalizes its response', async () => {
    const get = spyOn(apiClient, 'get').mockResolvedValue({
      files: [{ name: 'devin.json', provider: 'devin', auth_index: 12 }],
    });
    try {
      const result = await authFilesApi.list({ name: 'devin.json', authIndex: '12' });

      expect(get).toHaveBeenCalledWith('/credentials', {
        params: { name: 'devin.json', auth_index: '12' },
      });
      expect(result.files[0]?.authIndex).toBe('12');
    } finally {
      get.mockRestore();
    }
  });

  test('uses the unfiltered list endpoint when no lookup is supplied', async () => {
    const get = spyOn(apiClient, 'get').mockResolvedValue({ files: [] });
    try {
      await authFilesApi.list();
      expect(get).toHaveBeenCalledWith('/credentials', undefined);
    } finally {
      get.mockRestore();
    }
  });

  test('propagates refresh and list errors without a metadata fallback', async () => {
    const refreshError = new Error('refresh failed');
    const post = spyOn(apiClient, 'post').mockRejectedValue(refreshError);
    const patch = spyOn(apiClient, 'patch').mockResolvedValue({});
    try {
      await expect(authFilesApi.requestManualRefresh('devin.json', '2')).rejects.toBe(refreshError);
      expect(patch).not.toHaveBeenCalled();
    } finally {
      post.mockRestore();
      patch.mockRestore();
    }

    const listError = new Error('list failed');
    const get = spyOn(apiClient, 'get').mockRejectedValue(listError);
    try {
      await expect(authFilesApi.list({ name: 'devin.json', authIndex: '2' })).rejects.toBe(
        listError
      );
    } finally {
      get.mockRestore();
    }
  });
});
