import { describe, expect, spyOn, test } from 'bun:test';
import i18n from '@/i18n';
import { DEVIN_CONFIG } from '@/features/quota/providers/devin/data';
import { apiClient } from '@/services/api/client';
import { authFilesApi } from '@/services/api/authFiles';

describe('Devin quota Management API integration', () => {
  test('queries only api-call with a body token placeholder, not auth-file refresh or list', async () => {
    const post = spyOn(apiClient, 'post').mockResolvedValue({
      status_code: 200,
      header: {},
      body: JSON.stringify({
        userStatus: {
          planStatus: { dailyQuotaRemainingPercent: 54, weeklyQuotaRemainingPercent: 77 },
        },
      }),
    });
    const refresh = spyOn(authFilesApi, 'requestManualRefresh');
    const list = spyOn(authFilesApi, 'list');
    try {
      const result = await DEVIN_CONFIG.fetchQuota(
        { name: 'live.json', type: 'devin', auth_index: ' 42 ' },
        i18n.t
      );
      expect(post).toHaveBeenCalledTimes(1);
      expect(post.mock.calls[0][0]).toBe('/api-call');
      expect(post.mock.calls[0][1]).toMatchObject({
        authIndex: '42',
        method: 'POST',
        header: { 'Content-Type': 'application/json', 'Connect-Protocol-Version': '1' },
      });
      expect(refresh).not.toHaveBeenCalled();
      expect(list).not.toHaveBeenCalled();
      expect(result.windows.map((window) => window.remainingPercent)).toEqual([54, 77]);
    } finally {
      post.mockRestore();
      refresh.mockRestore();
      list.mockRestore();
    }
  });
});
