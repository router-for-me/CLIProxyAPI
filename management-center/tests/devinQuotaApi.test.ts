import { describe, expect, spyOn, test } from 'bun:test';
import { authFilesApi } from '@/services/api/authFiles';
import { apiClient } from '@/services/api/client';

describe('Devin auth-file quota API boundaries', () => {
  test('discards the complete Auth refresh response and sends the optional auth index', async () => {
    const completeAuth = {
      id: 'credential-id',
      access_token: 'must-not-propagate',
      refresh_token: 'must-not-propagate',
    };
    const post = spyOn(apiClient, 'post').mockResolvedValue(completeAuth);
    try {
      const result = await authFilesApi.requestManualRefresh('devin.json', 'auth-7');

      expect(result).toBeUndefined();
      expect(post).toHaveBeenCalledTimes(1);
      expect(post).toHaveBeenCalledWith('/auth-files/refresh', {
        name: 'devin.json',
        auth_index: 'auth-7',
      });
      expect(result).not.toBe(completeAuth);
    } finally {
      post.mockRestore();
    }
  });

  test('omits auth_index when refreshing by filename only', async () => {
    const post = spyOn(apiClient, 'post').mockResolvedValue({ access_token: 'secret' });
    try {
      await expect(authFilesApi.requestManualRefresh('devin.json')).resolves.toBeUndefined();
      expect(post).toHaveBeenCalledWith('/auth-files/refresh', { name: 'devin.json' });
    } finally {
      post.mockRestore();
    }
  });

  test('passes an optional identity lookup to list and normalizes its response', async () => {
    const get = spyOn(apiClient, 'get').mockResolvedValue({
      files: [{ name: 'devin.json', provider: 'devin', auth_index: 12 }],
    });
    try {
      const result = await authFilesApi.list({ name: 'devin.json', authIndex: '12' });

      expect(get).toHaveBeenCalledWith('/auth-files', {
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
      expect(get).toHaveBeenCalledWith('/auth-files', undefined);
    } finally {
      get.mockRestore();
    }
  });

  test('does not swallow refresh or list errors', async () => {
    const refreshError = new Error('refresh failed');
    const post = spyOn(apiClient, 'post').mockRejectedValue(refreshError);
    try {
      await expect(authFilesApi.requestManualRefresh('devin.json', '2')).rejects.toBe(refreshError);
    } finally {
      post.mockRestore();
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
