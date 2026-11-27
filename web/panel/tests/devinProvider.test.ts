import { describe, expect, test } from 'bun:test';
import {
  getAuthFileIcon,
  OAUTH_PROVIDER_PRESETS,
  supportsAuthFileManualRefresh,
} from '@/features/authFiles/constants';
import { providerLabel } from '@/features/dashboard/utils';
import { PROVIDER_LOGOS } from '@/features/providers/brandLogos';
import { normalizeAuthFilesResponse } from '@/services/api/authFiles';
import { TYPE_COLORS } from '@/utils/quota/constants';

describe('Devin provider recognition', () => {
  test('registers Devin auth files without enabling manual refresh', () => {
    expect(OAUTH_PROVIDER_PRESETS).toContain('devin');
    expect(supportsAuthFileManualRefresh('devin')).toBe(false);
  });

  test('provides distinct light and dark logo assets plus quota colors', () => {
    const lightIcon = getAuthFileIcon('devin', 'light');
    const darkIcon = getAuthFileIcon('devin', 'dark');

    expect(lightIcon).toBeTruthy();
    expect(darkIcon).toBeTruthy();
    expect(darkIcon).not.toBe(lightIcon);
    expect(PROVIDER_LOGOS.devin.src).toBe(lightIcon);
    expect(PROVIDER_LOGOS.devin.darkSrc).toBe(darkIcon);
    expect(TYPE_COLORS.devin.light).toBeDefined();
    expect(TYPE_COLORS.devin.dark).toBeDefined();
    expect(providerLabel('devin', 'Unknown')).toBe('Devin');
  });

  test('preserves minimal Devin credentials and unknown fields during normalization', () => {
    const credential = {
      name: 'devin-user.json',
      type: 'devin',
      api_key: 'redacted-api-key',
      session_token: 'redacted-session-token',
      proxy_url: 'http://proxy.example',
      future_field: { enabled: true },
    };

    const [normalized] = normalizeAuthFilesResponse({ files: [credential] }, 1).files;

    expect(normalized).toMatchObject(credential);
  });
});
