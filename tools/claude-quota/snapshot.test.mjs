import { test } from 'node:test';
import assert from 'node:assert/strict';

import { mkdtemp, readFile, rm, writeFile } from 'node:fs/promises';
import { join } from 'node:path';
import { tmpdir } from 'node:os';
import { spawn, spawnSync } from 'node:child_process';
import { once } from 'node:events';
import { collectAndPublish, collectSnapshot, normalizeUsage } from './snapshot.mjs';

const window = (utilization, resets_at = '2026-11-03T07:00:00Z') => ({ utilization, resets_at });
const usage = () => ({ five_hour: window(0, null), seven_day: window(0), seven_day_opus: null, seven_day_sonnet: null });

test('a failed account query leaves healthy account observations available', async () => {
  const mock = async (url, options) => {
    if (url.endsWith('/auth-files')) return new Response(JSON.stringify({ files: [
      { type: 'claude', id: 'failed', auth_index: 'failed' },
      { type: 'claude', id: 'healthy', auth_index: 'healthy' },
    ] }));
    const account = JSON.parse(options.body).authIndex;
    return new Response(JSON.stringify(account === 'failed' ? { status_code: 401, body: '{}' } : { status_code: 200, body: JSON.stringify(usage()) }));
  };
  const result = await collectSnapshot('http://example', 'synthetic-key', mock);
  assert.deepEqual(Object.keys(result.accounts), ['healthy']);
});

test('current percent-based limits block Fable without confusing breakdown or paid usage', () => {
  const input = {
    ...usage(),
    limits: [{ kind: 'weekly_scoped', percent: 100, resets_at: '2026-11-03T07:00:00Z', scope: { model: { id: null, display_name: 'Fable' } } }],
    seven_day_breakdown: { items: [] },
    extra_usage: { utilization: 100, resets_at: null },
  };
  const result = normalizeUsage(input);
  assert.equal(result.eligible, true);
  assert.deepEqual(result.blocked_model_families, ['fable']);
});

test('eligibility respects shared weekly, five-hour and OAuth limits', () => {
  for (const key of ['five_hour', 'seven_day', 'seven_day_oauth_apps']) {
    const input = usage(); input[key] = window(100);
    assert.equal(normalizeUsage(input).eligible, false, key);
  }
  assert.equal(normalizeUsage(usage()).eligible, true);
});

test('model-specific limits and missing resets are handled safely', () => {
  const input = usage(); input.seven_day_opus = window(100);
  assert.deepEqual(normalizeUsage(input).blocked_model_families, ['opus']);
  input.five_hour = window(20, null);
  assert.throws(() => normalizeUsage(input));
});

test('snapshot maps credential IDs and fails closed on upstream errors', async () => {
  const mock = async url => url.endsWith('/auth-files') ? new Response(JSON.stringify({files: [{type:'claude', id:'account', auth_index:'index'}]})) : new Response(JSON.stringify({status_code:200, body:JSON.stringify(usage())}));
  assert.equal((await collectSnapshot('http://example', 'synthetic-key', mock)).accounts.account.eligible, true);
  const failed = async url => url.endsWith('/auth-files') ? new Response(JSON.stringify({files: [{type:'claude', id:'account', auth_index:'index'}]})) : new Response(JSON.stringify({status_code:429, body:'{}'}));
  await assert.rejects(collectSnapshot('http://example', 'synthetic-key', failed));
});

// All fixtures and mock credentials are synthetic; no live requests are made.
const expanded = JSON.parse(await readFile(new URL('./fixtures/expanded-usage.json', import.meta.url), 'utf8'));
const deferred = () => {
  let resolve;
  const promise = new Promise(done => { resolve = done; });
  return { promise, resolve };
};
const fakeFetch = (onList = () => {}, value = usage()) => async url => {
  if (url.endsWith('/auth-files')) {
    await onList();
    return Response.json({ files: [{ type: 'claude', id: 'synthetic-account', auth_index: 'synthetic-index' }] });
  }
  return Response.json({ status_code: 200, body: JSON.stringify(value) });
};

test('expanded model scopes include Fable, ID-only and display-name-only families', () => {
  assert.deepEqual(normalizeUsage(expanded).blocked_model_families, ['fable', 'opus']);
  assert.equal(normalizeUsage(expanded).eligible, true);
  for (const model of [{ id: 'claude-fable-5' }, { display_name: 'Claude Sonnet 4.6' }]) {
    const input = usage();
    input.limits = [{ ...window(0), locked_reason: 'synthetic restriction', scope: { model } }];
    assert.equal(normalizeUsage(input).blocked_model_families.length, 1);
  }
  const input = usage(); input.seven_day_fable = window(100);
  assert.deepEqual(normalizeUsage(input).blocked_model_families, ['fable']);
});

test('expanded shared and unknown active exhaustion cannot imply eligibility', () => {
  for (const scope of [undefined, {}, { type: 'five_hour' }, { type: 'seven_day' }, { type: 'seven_day_oauth_apps' }, { model: { id: 'future-unknown' } }]) {
    const input = usage(); input.limits = [{ ...window(100), scope }];
    assert.equal(normalizeUsage(input).eligible, false);
    input.limits[0] = { ...window(0), locked_reason: 'synthetic restriction', scope };
    assert.equal(normalizeUsage(input).eligible, false);
    input.limits[0] = { ...window(5), scope };
    assert.equal(normalizeUsage(input).eligible, true);
  }
  const input = usage(); input.seven_day_unknown = window(100);
  assert.equal(normalizeUsage(input).eligible, false);
  input.limits = {};
  assert.throws(() => normalizeUsage(input), /invalid limits/);
});

test('freshness starts before the first request rather than at publication', async () => {
  let clock = new Date('2026-10-28T10:00:00Z');
  const snapshot = await collectSnapshot('http://synthetic', 'synthetic-key', fakeFetch(() => {
    clock = new Date('2026-10-28T10:10:00Z');
  }), () => clock);
  assert.equal(snapshot.observed_at, '2026-10-28T10:00:00.000Z');
  assert.equal(clock - new Date(snapshot.observed_at), 600_000);
});

test('collection and publication hold one kernel lock and preserve ordering', async t => {
  const dir = await mkdtemp(join(tmpdir(), 'claude-quota-test-'));
  t.after(() => rm(dir, { recursive: true, force: true }));
  const path = join(dir, 'snapshot.json');
  const entered = deferred(); const release = deferred(); const events = [];
  const first = collectAndPublish(path, 'http://synthetic', 'synthetic-key', {
    now: () => new Date('2026-10-28T10:00:00Z'),
    fetcher: fakeFetch(async () => { events.push('first'); entered.resolve(); await release.promise; }),
  });
  await entered.promise;
  // Probe the actual OS lock while the first collection is suspended.
  assert.equal(spawnSync('flock', ['--nonblock', `${path}.lock`, 'true']).status, 1);
  const second = collectAndPublish(path, 'http://synthetic', 'synthetic-key', {
    now: () => new Date('2026-10-28T10:01:00Z'),
    fetcher: fakeFetch(async () => {
      events.push('second');
      assert.equal(JSON.parse(await readFile(path, 'utf8')).observed_at, '2026-10-28T10:00:00.000Z');
    }),
  });
  release.resolve();
  await Promise.all([first, second]);
  assert.deepEqual(events, ['first', 'second']);
  assert.equal(JSON.parse(await readFile(path, 'utf8')).observed_at, '2026-10-28T10:01:00.000Z');
});

test('collection failure preserves the previous snapshot and releases the lock', async t => {
  const dir = await mkdtemp(join(tmpdir(), 'claude-quota-test-'));
  t.after(() => rm(dir, { recursive: true, force: true }));
  const path = join(dir, 'snapshot.json');
  await writeFile(path, 'previous snapshot');
  await assert.rejects(collectAndPublish(path, 'http://synthetic', 'synthetic-key', { fetcher: async () => { throw new Error('synthetic failure'); } }));
  assert.equal(await readFile(path, 'utf8'), 'previous snapshot');
  await collectAndPublish(path, 'http://synthetic', 'synthetic-key', { fetcher: fakeFetch() });
  assert.equal(JSON.parse(await readFile(path, 'utf8')).accounts['synthetic-account'].eligible, true);
});

test('process crash releases lock without PID files or stale-lock deletion', async t => {
  const dir = await mkdtemp(join(tmpdir(), 'claude-quota-test-'));
  t.after(() => rm(dir, { recursive: true, force: true }));
  const path = join(dir, 'snapshot.json');
  const source = `import { collectAndPublish } from ${JSON.stringify(new URL('./snapshot.mjs', import.meta.url).href)};
    await collectAndPublish(process.argv[1], 'http://synthetic', 'synthetic-key', {
      fetcher: async () => { process.stdout.write('locked'); await new Promise(() => { setInterval(() => {}, 1000); }); }
    });`;
  const child = spawn(process.execPath, ['--input-type=module', '-e', source, path], { stdio: ['ignore', 'pipe', 'inherit'] });
  t.after(() => child.kill('SIGKILL'));
  await once(child.stdout, 'data');
  assert.equal(spawnSync('flock', ['--nonblock', `${path}.lock`, 'true']).status, 1);
  const exited = once(child, 'exit'); child.kill('SIGKILL'); await exited;
  await collectAndPublish(path, 'http://synthetic', 'synthetic-key', { fetcher: fakeFetch() });
  assert.equal(JSON.parse(await readFile(path, 'utf8')).accounts['synthetic-account'].eligible, true);
});
