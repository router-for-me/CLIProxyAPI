import { chmod, mkdir, open, readFile, rename, rm, writeFile } from 'node:fs/promises';
import { spawn } from 'node:child_process';
import { randomUUID } from 'node:crypto';
import { dirname } from 'node:path';
import { fileURLToPath } from 'node:url';

const sharedKeys = ['five_hour', 'seven_day', 'seven_day_oauth_apps'];
const modelFamilies = ['opus', 'sonnet', 'fable', 'haiku', 'mythos'];
const inactiveReset = '9999-12-31T23:59:59Z';
const refreshMS = 5 * 60_000;
const maxSourceBytes = 1024 * 1024;
const record = value => value && typeof value === 'object' && !Array.isArray(value);
const timestamp = value => typeof value === 'string' && value.length <= 64 && Number.isFinite(Date.parse(value));
const statusCode = value => Number.isInteger(value) && value >= 100 && value <= 599;
const fingerprint = value => typeof value === 'string' && /^[a-f0-9]{64}$/.test(value);

export function sanitizeUsage(input) {
  normalizeUsage(input);
  const result = {};
  const reset = value => value == null ? null : new Date(value).toISOString();
  const safeWindow = value => ({ utilization: value.utilization, resets_at: reset(value.resets_at),
    ...(value.locked_reason ? { locked_reason: 'restricted' } : {}) });
  for (const key of [...sharedKeys, ...modelFamilies.flatMap(name => [`five_hour_${name}`, `seven_day_${name}`])]) {
    if (input[key] != null) result[key] = safeWindow(input[key]);
  }
  if (input.limits != null) result.limits = input.limits.map(limit => {
    const family = scopedFamily(limit.scope);
    return { percent: limit.percent ?? limit.utilization, resets_at: reset(limit.resets_at),
      kind: ['five_hour', 'weekly', 'seven_day', 'seven_day_oauth_apps', 'five_hour_scoped', 'weekly_scoped'].includes(limit.kind) ? limit.kind : 'unknown',
      ...(limit.locked_reason ? { locked_reason: 'restricted' } : {}),
      ...(family ? { scope: { model: { id: family, display_name: family[0].toUpperCase() + family.slice(1) } } }
        : ['five_hour', 'seven_day', 'seven_day_oauth_apps'].includes(limit.scope?.type) ? { scope: { type: limit.scope.type } } : {}) };
  });
  if (record(input.extra_usage)) {
    const extra = {};
    if (typeof input.extra_usage.is_enabled === 'boolean') extra.is_enabled = input.extra_usage.is_enabled;
    for (const name of ['monthly_limit', 'used_credits', 'utilization']) {
      const value = input.extra_usage[name];
      if (typeof value === 'number' && Number.isFinite(value) && value >= 0) extra[name] = value;
    }
    result.extra_usage = extra;
  }
  if (!normalizeUsage(input).eligible && normalizeUsage(result).eligible) {
    result.limits = [...(result.limits || []), { kind: 'unknown', percent: 100, resets_at: inactiveReset }];
  }
  return result;
}

export function retryAfterMS(header, time) {
  if (!record(header)) return 0;
  const raw = Object.entries(header).find(([name]) => name.toLowerCase() === 'retry-after')?.[1];
  const values = Array.isArray(raw) ? raw : [raw];
  return Math.max(0, ...values.map(value => {
    if (typeof value !== 'string') return 0;
    const trimmed = value.trim();
    if (/^\d+(?:\.\d+)?$/.test(trimmed)) {
      const delay = Number(trimmed) * 1000;
      return Number.isFinite(delay) && delay <= 8.64e15 - time ? delay : 0;
    }
    return timestamp(trimmed) ? Math.max(0, Date.parse(trimmed) - time) : 0;
  }));
}

function previousState(source, time) {
  const state = { accounts: Object.create(null), retry_state: Object.create(null) };
  if (!record(source)) return state;
  if (record(source.accounts)) for (const [id, entry] of Object.entries(source.accounts)) {
    if (!record(entry) || !timestamp(entry.observed_at) || Date.parse(entry.observed_at) > time || typeof entry.auth_index !== 'string' || !entry.auth_index || !fingerprint(entry.credential_fingerprint)) continue;
    try {
      const usage = sanitizeUsage(entry.usage);
      state.accounts[id] = { ...normalizeUsage(usage), auth_index: entry.auth_index, credential_fingerprint: entry.credential_fingerprint, observed_at: entry.observed_at, usage };
    } catch {}
  }
  if (record(source.retry_state)) for (const [id, entry] of Object.entries(source.retry_state)) {
    if (!record(entry) || typeof entry.auth_index !== 'string' || !entry.auth_index || !fingerprint(entry.credential_fingerprint) || !Number.isInteger(entry.failures) || entry.failures < 1 || entry.failures > 31 || !timestamp(entry.next_attempt_at)) continue;
    state.retry_state[id] = { auth_index: entry.auth_index, credential_fingerprint: entry.credential_fingerprint, failures: entry.failures, next_attempt_at: entry.next_attempt_at,
      ...(statusCode(entry.last_status) ? { last_status: entry.last_status } : {}),
      ...(statusCode(entry.management_status) ? { management_status: entry.management_status } : {}) };
  }
  if (timestamp(source.global_retry_at)) state.global_retry_at = source.global_retry_at;
  return state;
}

function validateWindow(window, label) {
  if (!window || typeof window !== 'object' || typeof window.utilization !== 'number' || !Number.isFinite(window.utilization) || window.utilization < 0) throw new Error(`invalid ${label} utilization`);
  if (window.resets_at != null && (typeof window.resets_at !== 'string' || !Number.isFinite(Date.parse(window.resets_at)))) throw new Error(`invalid ${label} reset`);
  if (window.utilization > 0 && !window.resets_at) throw new Error(`missing ${label} reset`);
}

const exhausted = window => window.utilization >= 100 || Boolean(window.locked_reason);

function scopedFamily(scope) {
  const model = scope?.model;
  if (!model || typeof model !== 'object') return null;
  const labels = [model.id, model.display_name].filter(value => typeof value === 'string');
  const matches = modelFamilies.filter(family => labels.some(label => new RegExp(`(^|[^a-z])${family}([^a-z]|$)`, 'i').test(label)));
  return matches.length === 1 ? matches[0] : null;
}

export function normalizeUsage(usage) {
  if (!usage || typeof usage !== 'object' || !usage.seven_day || !usage.five_hour) throw new Error('missing shared quota windows');
  let eligible = true;
  const blocked = new Set();
  for (const [key, window] of Object.entries(usage)) {
    if (window == null || (!sharedKeys.includes(key) && !key.startsWith('seven_day_') && !key.startsWith('five_hour_'))) continue;
    if (!sharedKeys.includes(key) && !modelFamilies.some(family => key === `seven_day_${family}` || key === `five_hour_${family}`) && typeof window.utilization !== 'number' && !window.locked_reason) continue;
    validateWindow(window, key);
    if (!exhausted(window)) continue;
    const family = modelFamilies.find(name => key === `seven_day_${name}` || key === `five_hour_${name}`);
    if (family) blocked.add(family);
    else eligible = false;
  }
  if (usage.limits != null) {
    if (!Array.isArray(usage.limits)) throw new Error('invalid limits');
    for (const limit of usage.limits) {
      const window = { ...limit, utilization: limit.percent ?? limit.utilization };
      validateWindow(window, 'limit');
      if (!exhausted(window)) continue;
      const family = scopedFamily(limit.scope);
      if (family) blocked.add(family);
      else eligible = false;
    }
  }
  return {
    eligible,
    weekly_reset: usage.seven_day.resets_at || inactiveReset,
    five_hour_reset: usage.five_hour.resets_at || inactiveReset,
    blocked_model_families: [...blocked].sort(),
  };
}

export async function collectSnapshot(base, key, fetcher = fetch, now = () => new Date(), previous = {}, { random = Math.random } = {}) {
  const observed_at = now().toISOString();
  const prior = previousState(previous, Date.parse(observed_at));
  const headers = { Authorization: `Bearer ${key}`, 'Content-Type': 'application/json' };
  const nativeAPI = base.endsWith('/v8/management');
  const listResponse = await fetcher(`${base}/${nativeAPI ? 'quotas/claude/credentials' : 'auth-files'}`, { headers, signal: AbortSignal.timeout(10000) });
  if (!listResponse.ok) throw new Error(`auth list HTTP ${listResponse.status}`);
  const list = await listResponse.json();
  if (!Array.isArray(list.files)) throw new Error('invalid auth list');
  const accounts = Object.create(null);
  const retry_state = Object.create(null);
  let global_retry_at = prior.global_retry_at;
  if (Date.parse(global_retry_at) <= now().getTime()) global_retry_at = undefined;
  const credentials = list.files.filter(item => item.type === 'claude' && !item.disabled);
  for (const account of credentials) {
    if (typeof account.id !== 'string' || !account.id || typeof account.auth_index !== 'string' || !account.auth_index || !fingerprint(account.credential_fingerprint)) throw new Error('invalid Claude credential metadata');
    if (prior.accounts[account.id]?.auth_index === account.auth_index && prior.accounts[account.id]?.credential_fingerprint === account.credential_fingerprint) accounts[account.id] = prior.accounts[account.id];
    if (prior.retry_state[account.id]?.auth_index === account.auth_index && prior.retry_state[account.id]?.credential_fingerprint === account.credential_fingerprint) retry_state[account.id] = prior.retry_state[account.id];
  }
  for (const account of credentials) {
    const time = now().getTime();
    if (Date.parse(global_retry_at) > time) break;
    if (Date.parse(retry_state[account.id]?.next_attempt_at) > time || Date.parse(accounts[account.id]?.observed_at) + refreshMS > time) continue;
    const requestStart = new Date(time).toISOString();
    let managementStatus, upstreamStatus, retryAfter = 0;
    try {
      const result = await fetcher(`${base}/${nativeAPI ? 'requests/api-call' : 'api-call'}`, {
        method: 'POST', headers, signal: AbortSignal.timeout(15000),
        body: JSON.stringify({ authIndex: account.auth_index, method: 'GET', url: 'https://api.anthropic.com/api/oauth/usage', header: { Authorization: 'Bearer $TOKEN$', 'anthropic-beta': 'oauth-2025-04-20', 'User-Agent': 'claude-code/2.1.273', 'Content-Type': 'application/json' } }),
      });
      managementStatus = result.status;
      retryAfter = retryAfterMS(Object.fromEntries(result.headers.entries()), now().getTime());
      if (!result.ok) throw new Error(`quota request HTTP ${result.status}`);
      const response = await result.json();
      upstreamStatus = statusCode(response?.status_code) ? response.status_code : undefined;
      retryAfter = Math.max(retryAfter, retryAfterMS(response?.header, now().getTime()));
      if (response.status_code !== 200) throw new Error(`quota upstream HTTP ${response.status_code}`);
      const usage = sanitizeUsage(JSON.parse(response.body));
      accounts[account.id] = { ...normalizeUsage(usage), auth_index: account.auth_index, credential_fingerprint: account.credential_fingerprint, observed_at: requestStart, usage };
      delete retry_state[account.id];
    } catch {
      const failures = Math.min(31, (retry_state[account.id]?.failures || 0) + 1);
      const jitter = Math.max(0, Math.min(1, Number(random()) || 0));
      const delay = Math.max(retryAfter, Math.min(15 * 60_000, 60_000 * 2 ** (failures - 1) * (1 + jitter * 0.2)));
      const next_attempt_at = new Date(now().getTime() + delay).toISOString();
      retry_state[account.id] = { auth_index: account.auth_index, credential_fingerprint: account.credential_fingerprint, failures, next_attempt_at,
        ...(statusCode(upstreamStatus) ? { last_status: upstreamStatus } : {}),
        ...(statusCode(managementStatus) ? { management_status: managementStatus } : {}) };
      if (upstreamStatus === 429 || managementStatus === 429) { global_retry_at = next_attempt_at; break; }
    }
  }
  return { observed_at, accounts, retry_state, ...(global_retry_at ? { global_retry_at } : {}) };
}

export async function publishSnapshot(path, snapshot) {
  await mkdir(dirname(path), { recursive: true, mode: 0o700 });
  const temporary = `${path}.${process.pid}.${randomUUID()}.tmp`;
  try {
    await writeFile(temporary, JSON.stringify(snapshot), { mode: 0o600, flag: 'wx' });
    await chmod(temporary, 0o600);
    await rename(temporary, path);
  } finally {
    await rm(temporary, { force: true });
  }
}

export async function collectAndPublish(path, base, key, { fetcher = fetch, now = () => new Date(), random = Math.random } = {}) {
  await mkdir(dirname(path), { recursive: true, mode: 0o700 });
  const lock = await open(`${path}.lock`, 'a', 0o600);
  try {
    await new Promise((resolve, reject) => {
      const child = spawn('flock', ['--exclusive', '3'], { stdio: ['ignore', 'ignore', 'ignore', lock.fd] });
      child.once('error', reject);
      child.once('exit', (code, signal) => code === 0 ? resolve() : reject(new Error(`snapshot lock failed (${signal || code})`)));
    });
    let previous = {};
    try {
      const source = await open(path, 'r');
      try {
        if ((await source.stat()).size > maxSourceBytes) throw new Error('snapshot too large');
        const buffer = Buffer.alloc(maxSourceBytes + 1);
        const { bytesRead } = await source.read(buffer, 0, buffer.length, 0);
        if (bytesRead > maxSourceBytes) throw new Error('snapshot too large');
        previous = JSON.parse(buffer.subarray(0, bytesRead).toString('utf8'));
      } finally { await source.close(); }
    } catch {}
    const snapshot = await collectSnapshot(base, key, fetcher, now, previous, { random });
    await publishSnapshot(path, snapshot);
    return snapshot;
  } finally {
    await lock.close();
  }
}

async function main() {
  const key = (await readFile(process.env.CLIPROXY_MANAGEMENT_KEY_FILE, 'utf8')).trim();
  const snapshot = await collectAndPublish(process.env.CLAUDE_QUOTA_SNAPSHOT, 'http://127.0.0.1:8317/v8/management', key);
  console.log(`Published Claude quota snapshot for ${Object.keys(snapshot.accounts).length} account(s)`);
}

if (process.argv[1] === fileURLToPath(import.meta.url)) main().catch(error => { console.error(error.message); process.exitCode = 1; });
