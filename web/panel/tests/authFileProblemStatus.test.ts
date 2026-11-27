import { describe, expect, test } from 'bun:test';
import { isProblemAuthFile } from '../src/features/authFiles/constants';
import type { AuthFileItem } from '../src/types';

const authFile = (overrides: Partial<AuthFileItem> = {}): AuthFileItem => ({
  name: 'credential.json',
  type: 'codex',
  ...overrides,
});

describe('auth file problem status', () => {
  test('does not classify a deliberately disabled credential as a problem', () => {
    expect(
      isProblemAuthFile(
        authFile({
          disabled: true,
          status: 'disabled',
          statusMessage: 'disabled via management API',
        })
      )
    ).toBe(false);
  });

  test('also respects the backend disabled status when the boolean is absent', () => {
    expect(
      isProblemAuthFile(
        authFile({
          status: ' DISABLED ',
          statusMessage: 'disabled via management API',
        })
      )
    ).toBe(false);
  });

  test('ignores healthy status messages', () => {
    expect(isProblemAuthFile(authFile({ status: 'active', statusMessage: 'ok' }))).toBe(false);
  });

  test('detects warning messages, unavailable credentials, and error status', () => {
    expect(isProblemAuthFile(authFile({ statusMessage: 'quota exhausted' }))).toBe(true);
    expect(isProblemAuthFile(authFile({ unavailable: true }))).toBe(true);
    expect(isProblemAuthFile(authFile({ status: 'error' }))).toBe(true);
  });
});
