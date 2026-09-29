import { describe, expect, test } from 'bun:test';
import { createElement } from 'react';
import { renderToStaticMarkup } from 'react-dom/server';
import i18n from '../src/i18n/index';
import { normalizeAuthFilesResponse } from '../src/services/api/authFiles';
import { normalizeAuthFileCooldowns } from '../src/services/api/authFileCooldowns';
import {
  cooldownReasonKey,
  cooldownRemainingSeconds,
  summarizeCooldowns,
} from '../src/features/authFiles/cooldowns';
import { AuthFileCooldownSection } from '../src/features/authFiles/components/AuthFileCooldownSection';
import type { AuthFileCooldownSnapshot, AuthFilesResponse } from '../src/types/authFile';

const observedAt = '2026-07-17T10:00:00.000Z';
const receivedAtMs = Date.now();
const modelRecord = {
  scope: 'model',
  model_key: 'model-a',
  reason: 'quota',
  retry_at: '2026-07-17T10:00:32Z',
  remaining_seconds: 32,
  backoff_level: 6,
  http_status: 429,
};
const normalize = (value: unknown) => normalizeAuthFileCooldowns(value, observedAt, receivedAtMs);
const snapshot = normalize([modelRecord])!;
const record = snapshot.records![0];
const render = (value?: AuthFileCooldownSnapshot) =>
  renderToStaticMarkup(createElement(AuthFileCooldownSection, { snapshot: value }));

function response(files: Record<string, unknown>[]): AuthFilesResponse {
  return { observed_at: observedAt, files } as unknown as AuthFilesResponse;
}

describe('cooldown API normalization', () => {
  test('distinguishes old servers, unknown state, and known empty state', () => {
    expect(normalize(undefined)).toBeUndefined();
    expect(normalize(null)?.records).toBeNull();
    expect(normalize([])?.records).toEqual([]);
  });

  test('normalizes model fields, diagnostic zero, and response observation time', () => {
    const result = normalizeAuthFilesResponse(
      response([{ name: 'a.json', cooldowns: [{ ...modelRecord, backoff_level: 0 }] }]),
      receivedAtMs
    );
    expect(result.observedAt).toBe(observedAt);
    expect(result.files[0].cooldownSnapshot).toEqual({
      observedAt,
      receivedAtMs,
      records: [{ ...record, backoffLevel: 0 }],
    });
    expect(record).toEqual({
      scope: 'model',
      modelKey: 'model-a',
      reason: 'quota',
      retryAt: '2026-07-17T10:00:32.000Z',
      remainingSeconds: 32,
      backoffLevel: 6,
      httpStatus: 429,
    });
  });

  test('preserves credential and model restrictions without fabricating diagnostics', () => {
    const result = normalize([
      {
        scope: 'credential',
        reason: 'credential_quota',
        retry_at: modelRecord.retry_at,
        remaining_seconds: 10,
      },
      modelRecord,
    ])!;
    expect(result.records).toHaveLength(2);
    expect(result.records![0].modelKey).toBeUndefined();
    expect(result.records![0].httpStatus).toBeUndefined();
    expect(result.records![0].backoffLevel).toBeUndefined();
    expect(summarizeCooldowns(result, receivedAtMs)).toMatchObject({
      modelCount: 1,
      credentialWide: true,
      earliestSeconds: 10,
    });
    expect(summarizeCooldowns(result, receivedAtMs + 11_000)).toMatchObject({
      modelCount: 1,
      credentialWide: false,
      earliestSeconds: 21,
    });
  });

  test('treats malformed records as unknown rather than known empty', () => {
    for (const value of [
      {},
      'invalid',
      [null],
      [{ ...modelRecord, scope: 'future' }],
      [{ ...modelRecord, model_key: '' }],
      [{ ...modelRecord, retry_at: 'invalid' }],
      [{ ...modelRecord, remaining_seconds: 0 }],
      [{ ...modelRecord, remaining_seconds: -1 }],
      [{ ...modelRecord, remaining_seconds: 1.5 }],
      [{ ...modelRecord, remaining_seconds: Infinity }],
      [modelRecord, { ...modelRecord, scope: 'future' }],
    ]) {
      expect(normalize(value)?.records).toBeNull();
    }
    const diagnostics = normalize([{ ...modelRecord, backoff_level: -1, http_status: 200 }]);
    expect(diagnostics?.records?.[0].backoffLevel).toBeUndefined();
    expect(diagnostics?.records?.[0].httpStatus).toBeUndefined();
  });

  test('duplicate merging does not replace [] or null with a stale restriction', () => {
    for (const cooldowns of [[], null]) {
      const result = normalizeAuthFilesResponse(
        response([
          { name: 'a.json', source: 'file', path: '/synthetic/a.json', cooldowns },
          { name: 'a.json', source: 'memory', cooldowns: [modelRecord] },
        ]),
        receivedAtMs
      );
      expect(result.files).toHaveLength(1);
      expect(result.files[0].cooldownSnapshot?.records).toEqual(cooldowns);
    }
  });

  test('tolerates missing/invalid observed_at without inventing a server time', () => {
    const result = normalizeAuthFilesResponse(
      {
        observed_at: 'bad-date',
        files: [{ name: 'a.json', cooldowns: [modelRecord] }],
      },
      receivedAtMs
    );
    expect(result.observedAt).toBeUndefined();
    expect(result.files[0].cooldownSnapshot?.records).toHaveLength(1);
  });
});

describe('cooldown timer semantics', () => {
  test('uses receipt-relative seconds even with server/client clock skew', () => {
    expect(cooldownRemainingSeconds(record, receivedAtMs, receivedAtMs)).toBe(32);
    expect(cooldownRemainingSeconds(record, receivedAtMs, receivedAtMs + 1_001)).toBe(31);
    expect(cooldownRemainingSeconds(record, receivedAtMs, receivedAtMs - 5_000)).toBe(32);
    expect(cooldownRemainingSeconds(record, receivedAtMs, receivedAtMs + 32_000)).toBe(0);
    expect(cooldownRemainingSeconds(record, receivedAtMs, receivedAtMs + 100_000)).toBe(0);
    expect(
      cooldownRemainingSeconds({ ...record, backoffLevel: 100 }, receivedAtMs, receivedAtMs)
    ).toBe(32);
  });

  test('keeps expired rows for confirmation instead of claiming recovery', () => {
    const result = summarizeCooldowns(snapshot, receivedAtMs + 33_000);
    expect(result).toMatchObject({ modelCount: 0, credentialWide: false, earliestSeconds: 0 });
    expect(result.rows).toHaveLength(1);
    expect(result.rows[0].remainingSeconds).toBe(0);
  });

  test('new snapshots replace, rather than retain, prior timers', () => {
    const first = normalizeAuthFilesResponse(
      response([{ name: 'a.json', cooldowns: [modelRecord] }]),
      receivedAtMs
    );
    const cleared = normalizeAuthFilesResponse(
      response([{ name: 'a.json', cooldowns: [] }]),
      receivedAtMs + 1000
    );
    expect(first.files[0].cooldownSnapshot?.records).toHaveLength(1);
    expect(cleared.files[0].cooldownSnapshot?.records).toEqual([]);
  });

  test('future reason codes use a generic label, never a dynamic translation path', () => {
    expect(normalize([{ ...modelRecord, reason: 'future_reason' }])?.records?.[0].reason).toBe(
      'future_reason'
    );
    for (const reason of ['future_reason', '__proto__', 'constructor', '<script>']) {
      expect(cooldownReasonKey(reason)).toBe('auth_files.cooldown_reason_unknown');
    }
  });
});

describe('cooldown section rendering', () => {
  test('does not add a panel for old servers or known-empty state', () => {
    expect(render()).toBe('');
    expect(render(normalize([]))).toBe('');
    expect(render(normalize(null))).toContain(i18n.t('auth_files.cooldown_unknown'));
  });

  test('offers native keyboard-accessible disclosure with scope, reason and deadline', () => {
    const markup = render(snapshot);
    expect(markup).toContain('<details');
    expect(markup).toContain('<summary');
    expect(markup).not.toContain(' open=""');
    expect(markup).toContain('model-a');
    expect(markup).toContain(i18n.t('auth_files.cooldown_reason_quota'));
    expect(markup).toContain('429');
    expect(markup).toContain('dateTime="2026-07-17T10:00:32.000Z"');
    expect(markup).toContain(i18n.t('auth_files.cooldown_note'));
  });

  test('shows elapsed / refresh confirmation and safely escapes model keys', () => {
    const expired = { ...snapshot, receivedAtMs: receivedAtMs - 100_000 };
    expect(render(expired)).toContain(i18n.t('auth_files.cooldown_elapsed'));
    expect(render(expired)).toContain(i18n.t('auth_files.cooldown_refresh_hint'));
    const markup = render(normalize([{ ...modelRecord, model_key: '<script>example</script>' }]));
    expect(markup).not.toContain('<script>');
    expect(markup).toContain('&lt;script&gt;');
  });

  test('translates every new label in all four locales, including future reasons', () => {
    const reference = i18n.getResource('zh-CN', 'translation', 'auth_files') as Record<
      string,
      string
    >;
    const keys = Object.keys(reference).filter(
      (key) => key.startsWith('cooldown_') || key.startsWith('card_')
    );
    for (const lng of ['en', 'zh-CN', 'zh-TW', 'ru']) {
      const local = i18n.getResource(lng, 'translation', 'auth_files') as Record<string, string>;
      for (const key of keys) {
        // Inspect the locale itself so Chinese fallback cannot hide a missing translation.
        expect(typeof (local[key] ?? local[`${key}_other`])).toBe('string');
        for (const count of [1, 2, 5]) {
          const result = i18n.t(`auth_files.${key}`, {
            lng,
            count,
            name: 'sample.json',
            time: '32s',
            status: 429,
            level: 6,
          });
          expect(result).not.toContain('auth_files.');
          expect(result).not.toContain('{{');
        }
      }
    }
  });
});
