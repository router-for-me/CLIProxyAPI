import {
  canChooseResetGrant,
  resetGrantKind,
} from '../src/features/quota/providers/claude/resetGrantChoice';

import { afterEach, describe, expect, spyOn, test } from 'bun:test';
import { apiClient } from '../src/services/api/client';
import {
  anthropicResetGrantBlocker,
  claimClaudeResetGrant,
  parseAnthropicResetGrantStatus,
  readClaudeOrganization,
  readClaudeResetGrants,
  AnthropicResetGrantUnknownOutcome,
  AnthropicResetGrantError,
  ANTHROPIC_RESET_RESULTS,
  ANTHROPIC_RESET_REQUEST_ID_RE,
  type AnthropicResetSettledCode,
} from '../src/services/api/claudeResetGrants';
import { apiCallApi, type ApiCallRequest } from '../src/services/api/apiCall';
import type { AuthFileCooldownResetResponse } from '../src/services/api/authFiles';
import {
  createResetGrantOperations,
  ResetGrantPreflightError,
  resetGrantFailureMessageKey,
  RETRY_WINDOW_MS,
} from '../src/features/quota/providers/claude/resetGrantOperations';

const organization = '11111111-2222-3333-4444-555555555555';
const grant = { id: 'test-grant', resets_total: 2, resets_left: 2, usable_now: true };
const block = { eligible: true, at_limit: true, grants: [grant] };
const status = () => parseAnthropicResetGrantStatus(block)!;
const originalRequest = apiCallApi.request;
afterEach(() => {
  apiCallApi.request = originalRequest;
});

describe('Claude reset grant fail-closed parsing', () => {
  test('valid block and refusing defaults', () => {
    expect(status().grants[0].useRequiresLimit).toBe(true);
    expect(anthropicResetGrantBlocker(status(), grant.id)).toBeNull();
    expect(anthropicResetGrantBlocker({ ...status(), atLimit: false }, grant.id)).toBe(
      'not_limited'
    );
    expect(anthropicResetGrantBlocker({ ...status(), eligible: false }, grant.id)).toBe(
      'ineligible'
    );
    const missing = parseAnthropicResetGrantStatus({
      ...block,
      grants: [{ ...grant, usable_now: undefined }],
    })!;
    expect(anthropicResetGrantBlocker(missing, grant.id)).toBe('not_usable');
  });
  test('rejects malformed blocks and grants', () => {
    for (const value of [
      undefined,
      {},
      { ...block, eligible: 1 },
      { ...block, grants: {} },
      { ...block, grants: [grant, grant] },
      { ...block, cooldown_until: 'bad' },
    ]) {
      expect(parseAnthropicResetGrantStatus(value)).toBeNull();
    }
    for (const fields of [
      { id: '../bad' },
      { resets_left: -1 },
      { resets_left: 3 },
      { resets_total: 1.5 },
      { paused: 'false' },
      { starts_at: 'bad' },
      { clears: {} },
    ]) {
      expect(
        parseAnthropicResetGrantStatus({ ...block, grants: [{ ...grant, ...fields }] })
      ).toBeNull();
    }
  });
});

test('exact proxied GET/profile/claim contract, no provider token in frontend', async () => {
  const calls: ApiCallRequest[] = [];
  apiCallApi.request = async (request) => {
    calls.push(request);
    const body =
      request.method === 'POST'
        ? { result: 'reset' }
        : request.url.endsWith('profile')
          ? { organization: { uuid: organization } }
          : { cedar_ember: block };
    return { statusCode: 200, body, bodyText: '', header: {} };
  };
  await readClaudeResetGrants('index');
  expect(await readClaudeOrganization('index')).toBe(organization);
  expect(await claimClaudeResetGrant('index', organization, grant.id, 'request-1')).toBe('reset');
  expect(calls[0].url).toBe('https://api.anthropic.com/api/oauth/usage?cedar_ember=1&skip_spend=1');
  expect(calls[1].url).toBe('https://api.anthropic.com/api/oauth/profile');
  expect(calls[2].url).toBe(
    `https://api.anthropic.com/api/organizations/${organization}/reset_rate_limits`
  );
  expect(JSON.parse(calls[2].data!)).toEqual({
    program: 'cedar_ember',
    grant_id: grant.id,
    request_id: 'request-1',
  });
  for (const call of calls) {
    expect(call.authIndex).toBe('index');
    expect(call.header?.Authorization).toBe('Bearer $TOKEN$');
    expect(call.header?.['anthropic-beta']).toBe('oauth-2025-04-20');
    expect(call.header?.['User-Agent']).toBe('claude-cli/2.1.280 (external, cli)');
  }
});

test('claim terminal mappings and ambiguous outcomes', async () => {
  for (const result of ANTHROPIC_RESET_RESULTS) {
    apiCallApi.request = async () => ({
      statusCode: 200,
      body: { result },
      bodyText: '',
      header: {},
    });
    expect(await claimClaudeResetGrant('a', organization, grant.id, 'r')).toBe(result);
  }
  for (const [statusCode, code] of [
    [401, 'auth_error'],
    [403, 'auth_error'],
    [429, 'rate_limited'],
  ] as const) {
    apiCallApi.request = async () => ({ statusCode, body: null, bodyText: '', header: {} });
    expect(await claimClaudeResetGrant('a', organization, grant.id, 'r')).toBe(code);
  }
  for (const statusCode of [200, 400, 500]) {
    apiCallApi.request = async () => ({
      statusCode,
      body: { result: 'unexpected' },
      bodyText: '',
      header: {},
    });
    await expect(claimClaudeResetGrant('a', organization, grant.id, 'r')).rejects.toBeInstanceOf(
      AnthropicResetGrantUnknownOutcome
    );
  }
  apiCallApi.request = async () => {
    throw new Error('private upstream detail');
  };
  await expect(claimClaudeResetGrant('a', organization, grant.id, 'r')).rejects.toThrow(
    'outcome is unknown'
  );
});

async function withGlobalCrypto(source: unknown, run: () => Promise<void>) {
  const descriptor = Object.getOwnPropertyDescriptor(globalThis, 'crypto');
  try {
    Object.defineProperty(globalThis, 'crypto', { configurable: true, value: source });
    await run();
  } finally {
    if (descriptor) Object.defineProperty(globalThis, 'crypto', descriptor);
    else Reflect.deleteProperty(globalThis, 'crypto');
  }
}

function mockDefaultResetRequests(result: 'reset' | 'unknown' = 'reset') {
  const calls: ApiCallRequest[] = [];
  const fullBlock = {
    ...block,
    ineligible_reason: null,
    next_grant_id: grant.id,
    weekly_resets_at: null,
    cooldown_until: null,
    grants: [
      {
        ...grant,
        label: 'Test grant',
        resets_total: 1,
        resets_left: 1,
        starts_at: '2000-01-01T00:00:00Z',
        ends_at: '2099-01-01T00:00:00Z',
        clears: ['five_hour', 'seven_day', 'seven_day_overage_included'],
        paused: false,
        use_requires_limit: false,
        percent_used: { five_hour: 0, seven_day: 100, seven_day_overage_included: 0 },
        blocking: [],
        arm: null,
      },
    ],
  };
  apiCallApi.request = async (request) => {
    calls.push(request);
    const body =
      request.method === 'POST'
        ? { result }
        : request.url.endsWith('/profile')
          ? {
              account: { uuid: 'aaaaaaaa-bbbb-cccc-dddd-eeeeeeeeeeee' },
              organization: { uuid: organization },
            }
          : { cedar_ember: fullBlock };
    return { statusCode: 200, body, bodyText: '', header: {} };
  };
  return calls;
}

describe('Claude reset grant default request ID integration', () => {
  test('HTTP crypto without randomUUID still dispatches a valid reset claim', async () => {
    using cooldown = spyOn(apiClient, 'post').mockResolvedValue({
      status: 'ok',
      auth_index: 'index',
    });
    const calls = mockDefaultResetRequests();
    let randomCalls = 0;
    await withGlobalCrypto(
      {
        getRandomValues(bytes: Uint8Array) {
          randomCalls++;
          bytes.fill(10);
          return bytes;
        },
      },
      async () => {
        const operations = createResetGrantOperations();
        expect(await operations.run('account', 'index', grant.id)).toEqual({
          code: 'reset',
          unresolved: false,
        });
        expect(calls.map((call) => call.method)).toEqual(['GET', 'GET', 'POST']);
        expect(calls[0].url).toEndWith('/api/oauth/profile');
        expect(calls[1].url).toEndWith('/api/oauth/usage?cedar_ember=1&skip_spend=1');
        expect(calls[2].url).toBe(
          `https://api.anthropic.com/api/organizations/${organization}/reset_rate_limits`
        );
        const payload = JSON.parse(calls[2].data!);
        expect(payload).toEqual({
          program: 'cedar_ember',
          grant_id: grant.id,
          request_id: '0a'.repeat(16),
        });
        expect(payload.request_id).toMatch(/^[0-9a-f]{32}$/);
        expect(payload.request_id).toMatch(ANTHROPIC_RESET_REQUEST_ID_RE);
        expect(operations.inspect('account')).toMatchObject({
          requestId: payload.request_id,
          code: 'reset',
        });
        expect(randomCalls).toBe(1);
        expect(cooldown).toHaveBeenCalledWith('/routing/cooldown/reset', { auth_index: 'index' });
      }
    );
  });

  test('HTTP fallback reuses one secure request ID after an unknown claim outcome', async () => {
    const calls = mockDefaultResetRequests('unknown');
    let randomCalls = 0;
    await withGlobalCrypto(
      {
        getRandomValues(bytes: Uint8Array) {
          randomCalls++;
          bytes.fill(randomCalls);
          return bytes;
        },
      },
      async () => {
        const operations = createResetGrantOperations();
        await expect(operations.run('account', 'index', grant.id)).rejects.toBeInstanceOf(
          AnthropicResetGrantUnknownOutcome
        );
        const originalId = operations.inspect('account')?.requestId;
        expect(originalId).toMatch(/^[0-9a-f]{32}$/);
        await expect(operations.run('account', 'index', grant.id)).rejects.toBeInstanceOf(
          AnthropicResetGrantUnknownOutcome
        );
        const posts = calls.filter((call) => call.method === 'POST');
        expect(posts).toHaveLength(2);
        expect(posts.map((call) => JSON.parse(call.data!).request_id)).toEqual([
          originalId,
          originalId,
        ]);
        expect(calls.map((call) => call.method)).toEqual(['GET', 'GET', 'POST', 'GET', 'POST']);
        expect(operations.inspect('account')?.code).toBeUndefined();
        expect(randomCalls).toBe(1);
      }
    );
  });

  test('missing secure randomness prevents spending and journaling without leaving busy state', async () => {
    const calls = mockDefaultResetRequests();
    await withGlobalCrypto({}, async () => {
      const operations = createResetGrantOperations();
      for (let attempt = 0; attempt < 2; attempt++) {
        const result = operations.run('account', 'index', grant.id);
        await expect(result).rejects.toBeInstanceOf(ResetGrantPreflightError);
        await expect(result).rejects.toMatchObject({
          stage: 'request_id',
          reason: 'generation_failed',
        });
        expect(operations.inspect('account')).toBeUndefined();
      }
      expect(calls.map((call) => call.method)).toEqual(['GET', 'GET', 'GET', 'GET']);
    });
  });
});

function setup() {
  let revision = 1;
  let now = 100;
  let count = 0;
  const ids: string[] = [];
  const cleared: string[] = [];
  const deps = {
    revision: () => revision,
    now: () => now,
    requestId: () => `request-${++count}`,
    readStatus: async () => status(),
    readOrganization: async () => organization,
    resetCooldown: async (authIndex: string): Promise<AuthFileCooldownResetResponse> => {
      cleared.push(authIndex);
      return { status: 'ok', auth_index: authIndex, models: [] };
    },
    claim: async (
      _auth: string,
      _org: string,
      _grant: string,
      id: string
    ): Promise<AnthropicResetSettledCode> => {
      ids.push(id);
      throw new AnthropicResetGrantUnknownOutcome();
    },
  };
  return {
    deps,
    ids,
    cleared,
    nextSession: () => {
      revision++;
    },
    advance: () => {
      now += RETRY_WINDOW_MS;
    },
  };
}

test('confirmed Claude resets clear only the redeemed credential cooldown before returning', async () => {
  for (const code of ['reset', 'already_used'] as const) {
    const h = setup();
    h.deps.claim = async (_auth, _org, _grant, id) => {
      expect(h.cleared).toEqual([]);
      h.ids.push(id);
      return code;
    };
    const operations = createResetGrantOperations(h.deps);
    expect(await operations.run('account', 'credential-a', grant.id)).toEqual({
      code,
      unresolved: false,
    });
    expect(h.cleared).toEqual(['credential-a']);
    expect(h.ids).toEqual(['request-1']);
    expect(operations.inspect('account')?.code).toBe(code);
  }
});

test('refused or unknown reset outcomes never clear routing cooldowns', async () => {
  const h = setup();
  const operations = createResetGrantOperations(h.deps);
  await expect(operations.run('account', 'a', grant.id)).rejects.toThrow();
  expect(h.cleared).toEqual([]);
  for (const code of [...ANTHROPIC_RESET_RESULTS, 'auth_error', 'rate_limited'] as const) {
    if (code === 'reset' || code === 'already_used') continue;
    h.deps.claim = async () => code;
    await operations.run('account', 'a', grant.id);
    expect(h.cleared).toEqual([]);
  }
});

test('cooldown clear failures keep the reset settled and report manual recovery', async () => {
  for (const failure of ['network', 'status', 'credential']) {
    const h = setup();
    h.deps.claim = async (_auth, _org, _grant, id) => {
      h.ids.push(id);
      return 'reset';
    };
    h.deps.resetCooldown = async (authIndex) => {
      h.cleared.push(authIndex);
      if (failure === 'network') throw new Error('unavailable');
      return JSON.parse(
        JSON.stringify({
          status: failure === 'status' ? 'error' : 'ok',
          auth_index: failure === 'credential' ? 'other' : authIndex,
          models: [],
        })
      );
    };
    const operations = createResetGrantOperations(h.deps);
    expect(await operations.run('account', 'a', grant.id)).toEqual({
      code: 'cooldown_clear_failed',
      unresolved: false,
    });
    expect(operations.inspect('account')?.code).toBe('reset');
    expect(h.ids).toEqual(['request-1']);
    expect(h.cleared).toEqual(['a']);
  }
});

test('connection changes after a claim prevent cooldown clearing on the replacement connection', async () => {
  const h = setup();
  h.deps.claim = async () => {
    h.nextSession();
    return 'reset';
  };
  const operations = createResetGrantOperations(h.deps);
  await expect(operations.run('account', 'a', grant.id)).rejects.toThrow('session');
  expect(h.cleared).toEqual([]);
  expect(operations.inspect('account')).toBeUndefined();
});

test('a connection change during cooldown clearing cannot report success for the new session', async () => {
  const h = setup();
  h.deps.claim = async () => 'reset';
  h.deps.resetCooldown = async (authIndex) => {
    h.nextSession();
    return { status: 'ok', auth_index: authIndex, models: [] };
  };
  const operations = createResetGrantOperations(h.deps);
  await expect(operations.run('account', 'a', grant.id)).rejects.toThrow('session');
  expect(operations.inspect('account')).toBeUndefined();
});

test('unknown outcome retains request ID across retries; expiry blocks', async () => {
  const h = setup();
  const operations = createResetGrantOperations(h.deps);
  await expect(operations.run('account', 'a', grant.id)).rejects.toThrow();
  await expect(operations.run('account', 'a', grant.id)).rejects.toThrow();
  expect(h.ids).toEqual(['request-1', 'request-1']);
  await expect(operations.run('account', 'a', 'other-grant')).rejects.toThrow('expired');
  h.advance();
  await expect(operations.run('account', 'a', grant.id)).rejects.toThrow('expired');
  expect(h.ids).toHaveLength(2);
});

test('fresh spend rechecks eligibility and blocks concurrent clicks', async () => {
  const h = setup();
  h.deps.readStatus = async () => ({ ...status(), eligible: false });
  const operations = createResetGrantOperations(h.deps);
  const first = operations.run('account', 'a', grant.id);
  await expect(operations.run('account', 'a', grant.id)).rejects.toThrow('busy');
  await expect(first).rejects.toThrow('blocked');
  expect(h.ids).toHaveLength(0);
});

test('session change during profile read prevents POST and isolates journal', async () => {
  const h = setup();
  const operations = createResetGrantOperations(h.deps);
  await expect(operations.run('account', 'a', grant.id)).rejects.toThrow();
  h.nextSession();
  expect(operations.inspect('account')).toBeUndefined();
  h.deps.readOrganization = async () => {
    h.nextSession();
    return organization;
  };
  await expect(operations.run('account', 'a', grant.id)).rejects.toThrow('session');
  expect(h.ids).toHaveLength(1);
});

test('retry binds original organization', async () => {
  const h = setup();
  const operations = createResetGrantOperations(h.deps);
  await expect(operations.run('account', 'a', grant.id)).rejects.toThrow();
  h.deps.readOrganization = async () => 'aaaaaaaa-2222-3333-4444-555555555555';
  await expect(operations.run('account', 'a', grant.id)).rejects.toThrow('identity');
  expect(h.ids).toHaveLength(1);
});

test('a retry refusal keeps the ambiguous original operation open', async () => {
  const h = setup();
  const operations = createResetGrantOperations(h.deps);
  await expect(operations.run('account', 'a', grant.id)).rejects.toThrow();
  h.deps.claim = async (_auth, _org, _grant, id) => {
    h.ids.push(id);
    return 'rate_limited';
  };
  expect(await operations.run('account', 'a', grant.id)).toEqual({
    code: 'rate_limited',
    unresolved: true,
  });
  expect(operations.inspect('account')?.code).toBeUndefined();
  h.deps.claim = async (_auth, _org, _grant, id) => {
    h.ids.push(id);
    return 'already_used';
  };
  expect(await operations.run('account', 'a', grant.id)).toEqual({
    code: 'already_used',
    unresolved: false,
  });
  expect(h.ids).toEqual(['request-1', 'request-1', 'request-1']);
  expect(h.cleared).toEqual(['a']);
});

test('no retry after the deadline expires during profile lookup', async () => {
  const h = setup();
  const operations = createResetGrantOperations(h.deps);
  await expect(operations.run('account', 'a', grant.id)).rejects.toThrow();
  h.deps.readOrganization = async () => {
    h.advance();
    return organization;
  };
  await expect(operations.run('account', 'a', grant.id)).rejects.toThrow('expired');
  expect(h.ids).toHaveLength(1);
});

test('date, pause, limit, balance, usability and cooldown gates refuse new spending', async () => {
  for (const fields of [
    { paused: true },
    { resetsLeft: 0 },
    { usableNow: false },
    { startsAt: '2099-01-01T00:00:00Z' },
    { endsAt: '1970-01-01T00:00:00Z' },
  ]) {
    const h = setup();
    h.deps.readStatus = async () => ({
      ...status(),
      grants: [{ ...status().grants[0], ...fields }],
    });
    await expect(createResetGrantOperations(h.deps).run('account', 'a', grant.id)).rejects.toThrow(
      'blocked'
    );
    expect(h.ids).toHaveLength(0);
  }
  for (const fields of [{ atLimit: false }, { cooldownUntil: '2099-01-01T00:00:00Z' }]) {
    const h = setup();
    h.deps.readStatus = async () => ({ ...status(), ...fields });
    await expect(createResetGrantOperations(h.deps).run('account', 'a', grant.id)).rejects.toThrow(
      'blocked'
    );
    expect(h.ids).toHaveLength(0);
  }
});

test('a stale claim answer cannot settle the replacement session operation', async () => {
  const h = setup();
  const operations = createResetGrantOperations(h.deps);
  h.deps.claim = async () => {
    h.nextSession();
    expect(operations.inspect('account')).toBeUndefined();
    return 'reset';
  };
  await expect(operations.run('account', 'a', grant.id)).rejects.toThrow('session');
  expect(operations.inspect('account')).toBeUndefined();
});

test('preflight read failures identify the stage without exposing upstream details', async () => {
  for (const stage of ['organization', 'status'] as const) {
    for (const failure of [new Error('SECRET'), new AnthropicResetGrantError('auth')]) {
      const h = setup();
      const fail = async (): Promise<never> => {
        throw failure;
      };
      if (stage === 'organization') h.deps.readOrganization = fail;
      else h.deps.readStatus = fail;
      const operations = createResetGrantOperations(h.deps);
      try {
        await operations.run('account', 'a', grant.id);
        throw new Error('Expected preflight failure');
      } catch (error) {
        expect(error).toBeInstanceOf(ResetGrantPreflightError);
        expect((error as ResetGrantPreflightError).stage).toBe(stage);
        expect((error as Error).message).not.toContain('SECRET');
        expect(resetGrantFailureMessageKey(error)).toBe(`claude_reset.failure_${stage}`);
      }
      expect(h.ids).toHaveLength(0);
      expect(operations.inspect('account')).toBeUndefined();
    }
  }
  expect(resetGrantFailureMessageKey(new Error('SECRET'))).toBe('claude_reset.blocked');
});

test('request ID generation failure is diagnosed before journaling or dispatch', async () => {
  const h = setup();
  h.deps.requestId = () => {
    throw new Error('crypto unavailable');
  };
  const operations = createResetGrantOperations(h.deps);
  await expect(operations.run('account', 'a', grant.id)).rejects.toMatchObject({
    stage: 'request_id',
    reason: 'generation_failed',
  });
  expect(operations.inspect('account')).toBeUndefined();
  expect(h.ids).toHaveLength(0);
});

test('expiry and cooldown failures have distinct safe diagnostic codes', async () => {
  for (const [fields, reason] of [
    [{ startsAt: '2099-01-01T00:00:00Z' }, 'blocked_not_started'],
    [{ endsAt: '1970-01-01T00:00:00Z' }, 'blocked_grant_expired'],
    [{ paused: true }, 'blocked_paused'],
  ] as const) {
    const h = setup();
    h.deps.readStatus = async () => ({
      ...status(),
      grants: [{ ...status().grants[0], ...fields }],
    });
    await expect(
      createResetGrantOperations(h.deps).run('account', 'a', grant.id)
    ).rejects.toMatchObject({
      stage: 'eligibility',
      reason,
    });
    expect(h.ids).toHaveLength(0);
  }
  const h = setup();
  h.deps.readStatus = async () => ({ ...status(), cooldownUntil: '2099-01-01T00:00:00Z' });
  await expect(
    createResetGrantOperations(h.deps).run('account', 'a', grant.id)
  ).rejects.toMatchObject({
    stage: 'eligibility',
    reason: 'blocked_cooldown',
  });
  expect(h.ids).toHaveLength(0);
});

test('read authentication refusals retain a safe diagnostic code', async () => {
  for (const statusCode of [401, 403]) {
    apiCallApi.request = async () => ({ statusCode, header: {}, body: {}, bodyText: '' });
    await expect(readClaudeOrganization('a')).rejects.toMatchObject({ code: 'auth' });
    await expect(readClaudeResetGrants('a')).rejects.toMatchObject({ code: 'auth' });
  }
});

test('all grant messages and confirmation are translated in six locales', async () => {
  const locales = await Promise.all(
    ['en', 'zh-CN', 'zh-TW', 'ru', 'vi', 'ko'].map(
      async (locale) => (await Bun.file(`src/i18n/locales/${locale}.json`).json()).claude_reset
    )
  );
  for (const locale of locales) {
    expect(Object.keys(locale).sort()).toEqual(Object.keys(locales[0]).sort());
    for (const stage of ['organization', 'status', 'eligibility', 'request_id', 'operation']) {
      expect(locale[`failure_${stage}`]).toContain('{{code}}');
    }
    expect(locale.confirm_text).toContain('{{name}}');
    expect(locale.retry_confirm).toContain('{{name}}');
    expect(typeof locale.remaining).toBe('string');
    expect(locale.count).toContain('{{left}}');
    for (const key of [
      ...ANTHROPIC_RESET_RESULTS,
      'auth_error',
      'rate_limited',
      'unknown',
      'expired',
    ]) {
      expect(typeof locale[key]).toBe('string');
    }
  }
});

test('card requires an explicit usable grant without recommendation or fallback', () => {
  const base = status();
  const a = { ...base.grants[0], id: 'a' };
  const b = { ...a, id: 'b' };
  const multiple = { ...base, grants: [b, a], nextGrantId: 'b' };
  for (const id of ['a', 'b']) expect(canChooseResetGrant(multiple, undefined, id, 0)).toBe(true);
  for (const id of [undefined, 'missing', { type: 'click' }, '']) {
    expect(canChooseResetGrant(multiple, undefined, id, 0)).toBe(false);
  }
  for (const changed of [
    { resetsLeft: 0 },
    { usableNow: false },
    { paused: true },
    { startsAt: new Date(1000).toISOString() },
    { endsAt: new Date(0).toISOString() },
  ]) {
    expect(
      canChooseResetGrant({ ...base, grants: [{ ...a, ...changed }] }, undefined, 'a', 0)
    ).toBe(false);
  }
  for (const changed of [
    { eligible: false },
    { atLimit: false },
    { cooldownUntil: new Date(1000).toISOString() },
  ]) {
    expect(canChooseResetGrant({ ...multiple, ...changed }, undefined, 'a', 0)).toBe(false);
  }
  expect(canChooseResetGrant(null, undefined, 'a', 0)).toBe(false);
  expect(canChooseResetGrant(null, 'a', 'a', 0)).toBe(true);
  expect(canChooseResetGrant(multiple, 'a', 'b', 0)).toBe(false);
});

test('card retains one dialog trigger with explicit selection and no second confirmation', async () => {
  const card = await Bun.file('src/features/quota/components/QuotaCard.tsx').text();
  const hook = await Bun.file('src/features/quota/providers/claude/ClaudeResetGrants.tsx').text();
  const dialog = await Bun.file(
    'src/features/quota/providers/claude/ClaudeResetGrantDialog.tsx'
  ).text();
  expect(card).toContain('quotaClasses.codexPlanValue}>{claudeReset.count');
  expect(card.match(/onClick=\{claudeReset.openDialog\}/g)).toHaveLength(1);
  expect(card).toContain('<ClaudeResetGrantDialog');
  expect(card).not.toContain('onRedeem=');
  expect(hook).not.toContain('showConfirmation');
  expect(hook).not.toContain('selectResetGrant');
  expect(hook).toContain('setDialog({ key, selected: null })');
  expect(hook).toContain('if (!open || !selected || !canRedeemGrant(selected) || lock.current');
  expect(hook).toContain(
    "if (!current() || useAuthStore.getState().connectionStatus !== 'connected') return"
  );
  expect(dialog).toContain('<Modal');
  expect(dialog).toContain('closeDisabled={props.busy}');
  expect(dialog).toContain('!props.selected || !props.canRedeemGrant(props.selected)');
});

test('cooldown clear failures reuse existing messages without new locale keys', async () => {
  const hook = await Bun.file('src/features/quota/providers/claude/ClaudeResetGrants.tsx').text();
  expect(hook).toContain("answer.code === 'cooldown_clear_failed'");
  expect(hook).toContain("t('auth_files.cooldown_reset_failed', {");
  expect(hook).toContain("message: t('common.unknown_error')");
  for (const language of ['en', 'zh-CN', 'zh-TW', 'ru', 'vi', 'ko', 'tr']) {
    const locale = await Bun.file(`src/i18n/locales/${language}.json`).json();
    expect(locale.claude_reset.cooldown_clear_failed).toBeUndefined();
    expect(locale.auth_files.cooldown_reset_failed).toContain('{{name}}');
    expect(locale.auth_files.cooldown_reset_failed).toContain('{{message}}');
    expect(typeof locale.common.unknown_error).toBe('string');
  }
});

test('account reads map upstream 429 to a sanitized typed rate-limit failure', async () => {
  let calls = 0;
  apiCallApi.request = async () => {
    calls += 1;
    return { statusCode: 429, body: 'private upstream detail', bodyText: '', header: {} };
  };
  for (const read of [readClaudeResetGrants, readClaudeOrganization]) {
    await expect(read('index')).rejects.toMatchObject({
      name: 'AnthropicResetGrantError',
      code: 'rate_limited',
      message: 'Anthropic reset-grant read failed (rate_limited)',
    });
  }
  expect(calls).toBe(2); // Neither read retries.
});

describe('explicit reset kinds and model refill usage', () => {
  test('preserves type and weekly give-back without treating it as a full reset', () => {
    for (const type of ['weekly', 'session', 'full']) {
      const parsed = parseAnthropicResetGrantStatus({
        ...block,
        grants: [{ ...grant, type, gives_back_weekly: false }],
      })!;
      expect(resetGrantKind(parsed.grants[0])).toBe(type);
      expect(parsed.grants[0].givesBackWeekly).toBe(false);
    }
    const parsed = parseAnthropicResetGrantStatus({
      ...block,
      grants: [{ ...grant, type: 'full', gives_back_weekly: true }],
    })!;
    expect(resetGrantKind(parsed.grants[0])).toBe('fable');
    expect(resetGrantKind(status().grants[0])).toBe('unknown');
    expect(
      parseAnthropicResetGrantStatus({
        ...block,
        grants: [{ ...grant, gives_back_weekly: 'true' }],
      })
    ).toBeNull();
  });

  test('only known zero usage blocks a model refill, including future model windows', () => {
    for (const [percent_used, blocked] of [
      [{ seven_day_overage_included: 0 }, true],
      [{ seven_day: 0, future_model: 0 }, true],
      [{ seven_day: 0, future_model: 0.5 }, false],
      [{ seven_day: 0, future_model: null }, false],
      [{}, false],
      [undefined, false],
    ] as const) {
      const parsed = parseAnthropicResetGrantStatus({
        ...block,
        grants: [{ ...grant, gives_back_weekly: true, percent_used }],
      })!;
      expect(anthropicResetGrantBlocker(parsed, grant.id)).toBe(blocked ? 'no_usage' : null);
    }
    const weekly = parseAnthropicResetGrantStatus({
      ...block,
      grants: [{ ...grant, type: 'weekly', percent_used: { seven_day: 0 } }],
    })!;
    expect(anthropicResetGrantBlocker(weekly, grant.id)).toBeNull();
  });

  test.each(['weekly-choice', 'model-choice'])(
    'preflight and mock claim spend exactly %s even when another grant is recommended',
    async (chosen) => {
      const calls: ApiCallRequest[] = [];
      const grants = [
        { ...grant, id: 'weekly-choice', type: 'weekly' },
        {
          ...grant,
          id: 'model-choice',
          type: 'full',
          gives_back_weekly: true,
          percent_used: { seven_day_overage_included: 35 },
        },
      ];
      apiCallApi.request = async (request) => {
        calls.push(request);
        return {
          statusCode: 200,
          header: {},
          bodyText: '',
          body:
            request.method === 'POST'
              ? { result: 'reset' }
              : request.url.endsWith('profile')
                ? { organization: { uuid: organization } }
                : { cedar_ember: { ...block, next_grant_id: 'weekly-choice', grants } },
        };
      };
      const operations = createResetGrantOperations({
        ...setup().deps,
        readOrganization: readClaudeOrganization,
        readStatus: readClaudeResetGrants,
        claim: claimClaudeResetGrant,
      });
      expect(calls).toHaveLength(0);
      await operations.run('account', 'index', chosen);
      const posts = calls.filter((call) => call.method === 'POST');
      expect(posts).toHaveLength(1);
      expect(JSON.parse(posts[0].data!)).toEqual({
        program: 'cedar_ember',
        grant_id: chosen,
        request_id: 'request-1',
      });
    }
  );

  test('fresh preflight refuses a now-empty model refill before any claim', async () => {
    const h = setup();
    h.deps.readStatus = async () =>
      parseAnthropicResetGrantStatus({
        ...block,
        grants: [
          { ...grant, gives_back_weekly: true, percent_used: { seven_day_overage_included: 0 } },
        ],
      })!;
    await expect(
      createResetGrantOperations(h.deps).run('account', 'index', grant.id)
    ).rejects.toMatchObject({ stage: 'eligibility', reason: 'blocked_no_usage' });
    expect(h.ids).toEqual([]);
  });

  test('uncertain model refill retains type and retries the same grant/request after discovery loses it', async () => {
    const h = setup();
    const refill = parseAnthropicResetGrantStatus({
      ...block,
      grants: [{ ...grant, type: 'full', gives_back_weekly: true }],
    })!;
    h.deps.readStatus = async () => refill;
    const claims: string[][] = [];
    h.deps.claim = async (_auth, _org, selected, requestId) => {
      claims.push([selected, requestId]);
      throw new AnthropicResetGrantUnknownOutcome();
    };
    const operations = createResetGrantOperations(h.deps);
    await expect(operations.run('account', 'index', grant.id)).rejects.toThrow();
    const pending = operations.inspect('account')!;
    expect(resetGrantKind(pending.grant)).toBe('fable');
    h.deps.readStatus = async () => ({ ...status(), grants: [] });
    await expect(operations.run('account', 'index', 'another-grant')).rejects.toThrow();
    await expect(operations.run('account', 'index', grant.id)).rejects.toThrow();
    expect(claims).toEqual([
      [grant.id, 'request-1'],
      [grant.id, 'request-1'],
    ]);
  });
});
