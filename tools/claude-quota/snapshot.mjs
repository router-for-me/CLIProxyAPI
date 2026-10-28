import { chmod, mkdir, open, readFile, rename, rm, writeFile } from 'node:fs/promises';
import { spawn } from 'node:child_process';
import { randomUUID } from 'node:crypto';
import { dirname } from 'node:path';
import { fileURLToPath } from 'node:url';

const sharedKeys = ['five_hour', 'seven_day', 'seven_day_oauth_apps'];
const modelFamilies = ['opus', 'sonnet', 'fable', 'haiku', 'mythos'];
const inactiveReset = '9999-12-31T23:59:59Z';

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

export async function collectSnapshot(base, key, fetcher = fetch, now = () => new Date()) {
  const observed_at = now().toISOString();
  const headers = { Authorization: `Bearer ${key}`, 'Content-Type': 'application/json' };
  const listResponse = await fetcher(`${base}/auth-files`, { headers, signal: AbortSignal.timeout(10000) });
  if (!listResponse.ok) throw new Error(`auth list HTTP ${listResponse.status}`);
  const list = await listResponse.json();
  if (!Array.isArray(list.files)) throw new Error('invalid auth list');
  const accounts = {};
  const credentials = list.files.filter(item => item.type === 'claude' && !item.disabled);
  for (const account of credentials) {
    if (!account.id || !account.auth_index) throw new Error('invalid Claude credential metadata');
    try {
      const result = await fetcher(`${base}/api-call`, {
        method: 'POST', headers, signal: AbortSignal.timeout(15000),
        body: JSON.stringify({ authIndex: account.auth_index, method: 'GET', url: 'https://api.anthropic.com/api/oauth/usage', header: { Authorization: 'Bearer $TOKEN$', 'anthropic-beta': 'oauth-2025-04-20', 'User-Agent': 'claude-cli/2.1.280 (external, cli)', 'Content-Type': 'application/json' } }),
      });
      if (!result.ok) throw new Error(`quota request HTTP ${result.status}`);
      const response = await result.json();
      if (response.status_code !== 200) throw new Error(`quota upstream HTTP ${response.status_code}`);
      accounts[account.id] = normalizeUsage(JSON.parse(response.body));
    } catch {
      continue;
    }
  }
  if (credentials.length > 0 && Object.keys(accounts).length === 0) throw new Error('no Claude quota observations available');
  return { observed_at, accounts };
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

export async function collectAndPublish(path, base, key, { fetcher = fetch, now = () => new Date() } = {}) {
  await mkdir(dirname(path), { recursive: true, mode: 0o700 });
  const lock = await open(`${path}.lock`, 'a', 0o600);
  try {
    await new Promise((resolve, reject) => {
      const child = spawn('flock', ['--exclusive', '3'], { stdio: ['ignore', 'ignore', 'ignore', lock.fd] });
      child.once('error', reject);
      child.once('exit', (code, signal) => code === 0 ? resolve() : reject(new Error(`snapshot lock failed (${signal || code})`)));
    });
    const snapshot = await collectSnapshot(base, key, fetcher, now);
    await publishSnapshot(path, snapshot);
    return snapshot;
  } finally {
    await lock.close();
  }
}

async function main() {
  const key = (await readFile(process.env.CLIPROXY_MANAGEMENT_KEY_FILE, 'utf8')).trim();
  const snapshot = await collectAndPublish(process.env.CLAUDE_QUOTA_SNAPSHOT, 'http://127.0.0.1:8317/v0/management', key);
  console.log(`Published Claude quota snapshot for ${Object.keys(snapshot.accounts).length} account(s)`);
}

if (process.argv[1] === fileURLToPath(import.meta.url)) main().catch(error => { console.error(error.message); process.exitCode = 1; });
