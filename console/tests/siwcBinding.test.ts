import { describe, expect, test } from 'bun:test';
import { readFileSync } from 'node:fs';
import {
  createSiwcStart,
  getSiwcBindingStatus,
  isSiwcCallbackValid,
  listSiwcTargets,
  supportsSiwcBinding,
} from '@/features/oauth/siwcBinding';
import { normalizeAuthFilesResponse } from '@/services/api/authFiles';
import { apiClient } from '@/services/api/client';
import { oauthApi } from '@/services/api/oauth';
import { createOAuthAttempts } from '@/features/oauth/oauthAttempts';

const callback = 'http://127.0.0.1:1455/auth/callback?code=secret&state=session&client_id=dynamic';

describe('SIWC binding', () => {
  test('requires backend capability and a Codex file', () => {
    const normalize = (extra: Record<string, unknown>) =>
      normalizeAuthFilesResponse({
        files: [{ name: 'codex.json', type: 'codex', ...extra }],
      }).files[0];
    expect(supportsSiwcBinding(normalize({}))).toBe(false);
    expect(supportsSiwcBinding(normalize({ siwc_bind_supported: 'true' }))).toBe(false);
    const file = normalize({
      siwc_bind_supported: true,
      use_siwc: true,
      siwc_bound: true,
      siwc_expires_at: '2030-01-01T00:00:00Z',
    });
    expect(supportsSiwcBinding(file)).toBe(true);
    expect(file.useSiwc).toBe(true);
    expect(file.siwcBound).toBe(true);
    expect(file.siwcExpiresAt).toBe('2030-01-01T00:00:00Z');
    expect(supportsSiwcBinding({ ...file, type: 'claude' })).toBe(false);
    expect(supportsSiwcBinding({ ...file, runtimeOnly: true })).toBe(false);
  });

  test('validates full loopback callback URLs, dynamic clients and OAuth errors', () => {
    expect(isSiwcCallbackValid(callback, 'session')).toBe(true);
    expect(isSiwcCallbackValid(`  ${callback}  `, 'session')).toBe(true);
    expect(
      isSiwcCallbackValid(
        'http://127.0.0.1:1455/auth/callback?state=session&error=access_denied',
        'session'
      )
    ).toBe(true);
    for (const value of [
      callback.replace('&client_id=dynamic', ''),
      callback.replace('code=secret&', ''),
      callback.replace('127.0.0.1', 'example.com'),
      callback.replace('127.0.0.1', 'localhost'),
      callback.replace(':1455', ':9999'),
      callback.replace('/auth/callback', '/callback'),
      callback.replace('http:', 'https:'),
      callback.replace('code=secret', 'code=%20'),
      callback.replace('client_id=dynamic', 'client_id=%20'),
      callback + '&code=other',
      callback + '&error=denied&error=other',
      callback + '&error=denied&client_id=other',
      callback + '&state=other',
      callback + '&client_id=other',
      callback + '#fragment',
      '/callback?state=session',
      callback.replace('http:', 'javascript:'),
      callback.replace('127.0.0.1', 'user@127.0.0.1'),
    ])
      expect(isSiwcCallbackValid(value, 'session')).toBe(false);
    expect(
      isSiwcCallbackValid(
        callback
          .replace('code=secret', 'code=%20secret%20')
          .replace('state=session', 'state=%20session%20'),
        'session'
      )
    ).toBe(true);
    expect(isSiwcCallbackValid(callback, 'other')).toBe(false);
    expect(isSiwcCallbackValid(callback, '')).toBe(false);
  });

  test('starts only the binding endpoint and forwards callback unchanged', async () => {
    const original = apiClient.post;
    const calls: unknown[][] = [];
    apiClient.post = (async (...args: unknown[]) => {
      calls.push(args);
      return { status: 'ok' };
    }) as typeof apiClient.post;
    const controller = new AbortController();
    try {
      await oauthApi.startSiwcBind('target.json', controller.signal);
      await oauthApi.submitCallback('codex', callback, controller.signal);
      expect(calls).toEqual([
        ['/oauth/siwc/bind', { name: 'target.json' }, { signal: controller.signal }],
        [
          '/oauth/callback',
          { provider: 'codex', redirect_url: callback },
          { signal: controller.signal },
        ],
      ]);
    } finally {
      apiClient.post = original;
    }
  });

  test('lists only bindable Codex credentials and derives status', () => {
    const { files } = normalizeAuthFilesResponse({
      files: [
        { name: 'b.json', type: 'codex', siwc_bind_supported: true, siwc_bound: true },
        { name: 'claude.json', type: 'claude', siwc_bind_supported: true },
        { name: 'a.json', type: 'codex', siwc_bind_supported: true },
        { name: 'c.json', type: 'codex' },
        {
          name: 'd.json',
          type: 'codex',
          siwc_bind_supported: true,
          siwc_bound: true,
          use_siwc: true,
        },
      ],
    });
    const targets = listSiwcTargets(files);
    expect(targets.map((file) => file.name)).toEqual(['a.json', 'b.json', 'd.json']);
    expect(targets.map(getSiwcBindingStatus)).toEqual(['unbound', 'bound', 'active']);
  });

  test('binding entry lives on the OAuth page, not on auth file cards', () => {
    const read = (path: string) => readFileSync(new URL(path, import.meta.url), 'utf8');
    const page = read('../src/features/oauth/OAuthPage.tsx');
    expect(page).toContain('<SiwcBindDialog');
    const card = read('../src/features/authFiles/components/AuthFileCard.tsx');
    expect(card).not.toContain('SiwcBindDialog');
    expect(card).not.toContain('siwc.rebind');
    expect(card).toContain('<SiwcStatusBadge');
  });

  test('target attempts are independent and invalidation suppresses stale completion', async () => {
    let tick: (() => void) | undefined;
    const attempts = createOAuthAttempts({
      setTimeout: (fn) => {
        tick = fn;
        return 1;
      },
      clearTimeout: () => {},
    });
    const first = attempts.begin('first.json');
    const second = attempts.begin('second.json');
    expect(first.isCurrent()).toBe(true);
    let deliver!: (value: string) => void;
    let completed = false;
    first.poll(
      () =>
        new Promise<string>((resolve) => {
          deliver = resolve;
        }),
      () => {
        completed = true;
        return false;
      },
      () => {},
      1
    );
    tick!();
    first.invalidate();
    deliver('ok');
    await Promise.resolve();
    expect(completed).toBe(false);
    expect(first.signal.aborted).toBe(true);
    expect(second.isCurrent()).toBe(true);
    attempts.invalidateAll();
    expect(second.signal.aborted).toBe(true);
  });

  test('dialog keeps cancellation, connection guards and completion refresh wired', () => {
    const source = readFileSync(
      new URL('../src/features/oauth/components/SiwcBindDialog.tsx', import.meta.url),
      'utf8'
    );
    for (const contract of [
      'attempts.begin(name)',
      'attempt.invalidate()',
      'sameConnection()',
      'current.isAuthenticated',
      'current.apiBase',
      'current.managementKey',
      'cancel: api.cancel',
      'startup.close()',
      'startRef.current?.start()',
      'authFilesApi',
      'listSiwcTargets(response.files)',
      'setRun((value) => value + 1)',
      '}, [name, run]);',
      'type="radio"',
      'notifyAuthFilesChanged()',
      'error.status === 404',
      "'unsupported'",
      "oauthApi.submitCallback('codex', callback, attempt.signal)",
    ])
      expect(source).toContain(contract);
    expect(source).not.toContain('startAuth(');
    expect(source).not.toContain('localStorage');
    for (const locale of ['en', 'zh-CN', 'zh-TW', 'ru', 'vi', 'ko']) {
      const messages = JSON.parse(
        readFileSync(new URL(`../src/i18n/locales/${locale}.json`, import.meta.url), 'utf8')
      ).siwc;
      expect(Object.keys(messages)).toHaveLength(32);
      expect(
        Object.values(messages).every((value) => typeof value === 'string' && value.length > 0)
      ).toBe(true);
    }
  });
});

describe('SIWC startup lifecycle', () => {
  function setup() {
    let resolve!: (value: { state: string }) => void;
    let reject!: (error: unknown) => void;
    let starts = 0;
    const cancelled: string[] = [];
    const results: string[] = [];
    const errors: unknown[] = [];
    const pending = new Promise<{ state: string }>((yes, no) => {
      resolve = yes;
      reject = no;
    });
    const controller = createSiwcStart({
      start: () => {
        starts++;
        return pending;
      },
      cancel: async (state) => {
        cancelled.push(state);
      },
      onResult: ({ state }) => results.push(state),
      onError: (error) => errors.push(error),
    });
    return { controller, resolve, reject, cancelled, results, errors, starts: () => starts };
  }

  test('effect setup/cleanup replay never starts a request', async () => {
    const first = setup();
    first.controller.close();
    await first.controller.start();
    const second = setup();
    expect(first.starts()).toBe(0);
    expect(second.starts()).toBe(0);
    second.controller.close();
  });

  test('double click starts once and close cancels a late session exactly once', async () => {
    const flow = setup();
    const started = flow.controller.start();
    await flow.controller.start();
    flow.controller.close();
    flow.resolve({ state: 'original-session' });
    await started;
    flow.controller.close();
    expect(flow.starts()).toBe(1);
    expect(flow.cancelled).toEqual(['original-session']);
    expect(flow.results).toEqual([]);
  });

  test('active close cancels, completed close does not', async () => {
    for (const completed of [false, true]) {
      const flow = setup();
      const started = flow.controller.start();
      flow.resolve({ state: 'session' });
      await started;
      expect(flow.results).toEqual(['session']);
      if (completed) flow.controller.complete();
      flow.controller.close();
      expect(flow.cancelled).toEqual(completed ? [] : ['session']);
    }
  });

  test('late rejection is inert while active errors are reported', async () => {
    for (const closed of [false, true]) {
      const flow = setup();
      const started = flow.controller.start();
      if (closed) flow.controller.close();
      flow.reject(new Error('network lost'));
      await started;
      expect(flow.errors).toHaveLength(closed ? 0 : 1);
      expect(flow.cancelled).toEqual([]);
    }
  });
});
