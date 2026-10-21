import { describe, expect, test } from 'bun:test';
import {
  XAI_GROK_CLIENT_VERSION,
  XAI_GROK_USER_AGENT,
  XAI_REQUEST_HEADERS,
} from '@/utils/quota/constants';

describe('Grok quota client headers', () => {
  test('meets the upstream minimum client version of 1.0.13', () => {
    expect(XAI_GROK_CLIENT_VERSION).toMatch(/^\d+\.\d+\.\d+$/);
    const [major, minor, patch] = XAI_GROK_CLIENT_VERSION.split('.').map(Number);
    expect(major > 1 || (major === 1 && (minor > 0 || (minor === 0 && patch >= 13)))).toBe(
      true
    );
    expect(XAI_REQUEST_HEADERS['x-grok-client-version']).toBe(XAI_GROK_CLIENT_VERSION);
  });

  test('keeps both user-agent versions aligned with the client version header', () => {
    expect(XAI_GROK_USER_AGENT).toBe(
      `grok-pager/${XAI_GROK_CLIENT_VERSION} grok-shell/${XAI_GROK_CLIENT_VERSION} (macos; aarch64)`
    );
    expect(XAI_REQUEST_HEADERS['user-agent']).toBe(XAI_GROK_USER_AGENT);
    expect(XAI_REQUEST_HEADERS['x-xai-token-auth']).toBe('xai-grok-cli');
    expect(XAI_REQUEST_HEADERS.Authorization).toBe('Bearer $TOKEN$');
  });
});
