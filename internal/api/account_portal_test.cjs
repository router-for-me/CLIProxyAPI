#!/usr/bin/env node
'use strict';

const assert = require('assert');
const fs = require('fs');
const path = require('path');
const vm = require('vm');
const { test } = require('node:test');

const root = path.resolve(__dirname, '..', '..');
const htmlPath = path.join(root, 'static', 'accounts.html');
const html = fs.readFileSync(htmlPath, 'utf8');

function scripts() {
  const out = [];
  const re = /<script(?![^>]*\bsrc=)[^>]*>([\s\S]*?)<\/script>/gi;
  let m;
  while ((m = re.exec(html))) out.push(m[1]);
  return out;
}

function makeElement(id) {
  const listeners = Object.create(null);
  const element = {
    id,
    value: '',
    textContent: '',
    className: '',
    disabled: false,
    children: [],
    style: {},
    classList: {
      classes: new Set(),
      toggle(cls, force) {
        if (force === undefined ? !this.classes.has(cls) : force) this.classes.add(cls);
        else this.classes.delete(cls);
      },
      add(cls) { this.classes.add(cls); },
      remove(cls) { this.classes.delete(cls); },
      contains(cls) { return this.classes.has(cls); }
    },
    setAttribute() {},
    append(...nodes) { this.children.push(...nodes); },
    replaceChildren(...nodes) { this.children = nodes; },
    querySelectorAll() { return []; },
    addEventListener(type, handler) { listeners[type] = handler; },
    dispatch(type, event) { return listeners[type](event); },
    _listeners: listeners
  };
  return element;
}

function makeSandbox(fetchImpl, preload = {}) {
  const ids = [
    'loginCard','appCards','adminPanel','sessionBadge','logoutBtn','loginStatus','meStatus',
    'passwordStatus','adminStatus','profile','usersBody','loginForm','passwordForm','createUserForm',
    'refreshUsers','username','password','currentPassword','newPassword','newUsername','newUserPassword'
  ];
  const elements = Object.fromEntries(ids.map(id => [id, makeElement(id)]));
  const store = new Map(Object.entries(preload).map(([k, v]) => [String(k), String(v)]));
  const windowListeners = Object.create(null);
  const sandbox = {
    console,
    Uint8Array,
    TextEncoder,
    TextDecoder,
    setTimeout(fn) { return fn; },
    clearTimeout() {},
    btoa(v) { return Buffer.from(v, 'binary').toString('base64'); },
    atob(v) { return Buffer.from(v, 'base64').toString('binary'); },
    prompt() { return ''; },
    addEventListener(type, handler) { (windowListeners[type] || (windowListeners[type] = [])).push(handler); },
    removeEventListener(type, handler) { const list = windowListeners[type] || []; const i = list.indexOf(handler); if (i >= 0) list.splice(i, 1); },
    dispatchEvent(event) { for (const handler of windowListeners[event.type] || []) handler(event); return true; },
    navigator: { userAgent: 'account-test' },
    location: { origin: 'http://localhost:8080', host: 'localhost:8080', replace(path) { this.replacedWith = path; } },
    AbortController: class { constructor() { this.signal = {}; } abort() { this.signal.aborted = true; } },
    localStorage: {
      getItem(k) { return store.has(k) ? store.get(k) : null; },
      setItem(k, v) { store.set(String(k), String(v)); },
      removeItem(k) { store.delete(String(k)); },
      _dump() { return Object.fromEntries(store); }
    },
    document: {
      getElementById(id) { return elements[id] || (elements[id] = makeElement(id)); },
      createElement(tag) { const el = makeElement(tag); el.tagName = String(tag).toUpperCase(); return el; }
    },
    fetch: fetchImpl,
    Response: undefined
  };
  sandbox.window = sandbox;
  sandbox.globalThis = sandbox;
  sandbox.__elements = elements;
  sandbox.__windowListeners = windowListeners;
  return sandbox;
}

const inlineScripts = scripts();

function storedSession(token, user = { username: 'alice', role: 'user', disabled: false }) {
  return JSON.stringify({ token, expires_at: '2099-01-01T00:00:00Z', user, apiBase: 'http://localhost:8080', saved_at: '2026-01-01T00:00:00Z' });
}

async function flush(times = 6) {
  for (let i = 0; i < times; i++) await Promise.resolve();
}

function runPortal(sandbox, filename = 'accounts-inline.js') {
  vm.runInNewContext(inlineScripts[0], sandbox, { filename });
}


test('has exactly one dependency-free inline script and no external script/link assets', () => {
  assert.strictEqual(inlineScripts.length, 1);
  assert(!/<script[^>]+src=/i.test(html), 'external script found');
  assert(!/<link[^>]+href=/i.test(html), 'external stylesheet/link found');
});

test('inline JavaScript parses successfully', () => {
  new Function(inlineScripts[0]);
});

test('uses safe DOM rendering primitives instead of HTML injection', () => {
  assert(!/\.innerHTML\s*=|insertAdjacentHTML|document\.write\s*\(/.test(html));
  assert(/textContent\s*=/.test(html));
  assert(/replaceChildren\s*\(/.test(html));
});

test('documents and uses expected account and management bridge storage keys', () => {
  for (const key of ['cpa-account-session', 'cli-proxy-auth', 'isLoggedIn', 'managementKey']) {
    assert(html.includes(key), 'missing key ' + key);
  }
  assert(html.includes('Storage contract:'), 'missing storage contract documentation');
});

test('implements the required account API routes and methods', () => {
  for (const fragment of [
    "'/login'", "'/me'", "'/logout'", "'/password'", "'/users'", "'/users/'+encodeURIComponent(username)",
    "method:'POST'", "method:'PUT'", "method:'PATCH'"
  ]) {
    assert(html.includes(fragment), 'missing API fragment ' + fragment);
  }
  assert(html.includes("Authorization='Bearer '+token") || html.includes("headers.Authorization='Bearer '+token"));
});

test('does not put tokens in URLs or persist password fields', () => {
  assert(!/token\s*[=:]\s*[^;\n]*location\.(search|href|hash)|URLSearchParams\([^)]*token/i.test(html), 'possible token in URL');
  assert(!/localStorage\.setItem\([^)]*password/i.test(html), 'password stored in localStorage');
  assert(!/sessionStorage\.setItem\([^)]*password/i.test(html), 'password stored in sessionStorage');
});

test('simulated login stores only session token and bridges Bearer auth to existing WebUI storage', async () => {
  const calls = [];
  const sandbox = makeSandbox(async (url, opts) => {
    calls.push({ url, opts });
    if (url.endsWith('/v0/management/accounts/login')) {
      assert.strictEqual(opts.method, 'POST');
      assert.deepStrictEqual(JSON.parse(opts.body), { username: 'alice', password: 'secret-password' });
      return { ok: true, text: async () => JSON.stringify({ token: 'sess-token-123', expires_at: '2099-01-01T00:00:00Z', user: { username: 'alice', role: 'user', disabled: false } }) };
    }
    if (url.endsWith('/v0/management/accounts/me')) {
      assert.strictEqual(opts.headers.Authorization, 'Bearer sess-token-123');
      return { ok: true, text: async () => JSON.stringify({ user: { username: 'alice', role: 'user', disabled: false } }) };
    }
    throw new Error('unexpected fetch ' + url);
  });
  vm.runInNewContext(inlineScripts[0], sandbox, { filename: 'accounts-inline.js' });
  sandbox.__elements.username.value = 'alice';
  sandbox.__elements.password.value = 'secret-password';
  await sandbox.__elements.loginForm.dispatch('submit', { preventDefault() {} });
  await Promise.resolve();
  const dump = sandbox.localStorage._dump();
  assert.strictEqual(JSON.parse(dump['cpa-account-session']).token, 'sess-token-123');
  assert.strictEqual(dump.isLoggedIn, 'true');
  assert(dump['cli-proxy-auth'], 'missing cli-proxy-auth bridge');
  assert(dump.managementKey, 'missing legacy managementKey bridge');
  // Decode with the existing management.html/billing page algorithm, not the portal's implementation.
  const key = Buffer.from('cli-proxy-api-webui::secure-storage|localhost:8080|account-test');
  const decode = value => {
    const bytes = Buffer.from(value.slice('enc::v1::'.length), 'base64');
    for (let i = 0; i < bytes.length; i++) bytes[i] ^= key[i % key.length];
    return JSON.parse(bytes.toString());
  };
  assert.strictEqual(decode(dump['cli-proxy-auth']).state.managementKey, 'sess-token-123');
  assert.strictEqual(decode(dump.managementKey), 'sess-token-123');
  assert(!Object.values(dump).join('\n').includes('secret-password'), 'password leaked into storage');
  assert.strictEqual(sandbox.location.replacedWith, '/management.html', 'successful login must enter the console');
  assert.strictEqual(calls.length, 1, 'login should not wait for profile/user-list requests');
});


test('failed login stays on the account page', async () => {
  const sandbox = makeSandbox(async () => ({ok:false,status:401,text:async()=>JSON.stringify({error:'invalid username or password'})}));
  vm.runInNewContext(inlineScripts[0], sandbox);
  sandbox.__elements.username.value='alice';
  sandbox.__elements.password.value='incorrect';
  await sandbox.__elements.loginForm.dispatch('submit',{preventDefault(){}});
  assert.strictEqual(sandbox.location.replacedWith, undefined);
  assert.strictEqual(sandbox.localStorage.getItem('cpa-account-session'), null);
});

test('existing session can return to account management without redirecting', async () => {
  const sandbox = makeSandbox(async () => ({ok:true,text:async()=>JSON.stringify({user:{username:'alice',role:'user',disabled:false}})}));
  sandbox.localStorage.setItem('cpa-account-session', JSON.stringify({token:'existing-session',user:{username:'alice',role:'user'}}));
  vm.runInNewContext(inlineScripts[0], sandbox);
  await Promise.resolve();
  assert.strictEqual(sandbox.location.replacedWith, undefined);
});

for (const status of [401, 403]) test('expired /me HTTP '+status+' clears the account session regardless of error message', async () => {
  const sandbox = makeSandbox(async (url, opts) => {
    if (url.endsWith('/v0/management/accounts/me')) {
      assert.strictEqual(opts.headers.Authorization, 'Bearer expired-token');
      return { ok: false, status, text: async () => JSON.stringify({ error: 'invalid management key' }) };
    }
    if (url.endsWith('/v0/management/accounts/logout')) {
      assert.strictEqual(opts.keepalive, true);
      assert.strictEqual(opts.headers.Authorization, 'Bearer expired-token');
      return { ok: true, status: 204, text: async () => '' };
    }
    throw new Error('unexpected fetch ' + url);
  }, { 'cpa-account-session': storedSession('expired-token') });

  runPortal(sandbox);
  await flush();

  assert.strictEqual(sandbox.localStorage.getItem('cpa-account-session'), null);
  assert.strictEqual(sandbox.__elements.sessionBadge.textContent, '未登录');
  assert.match(sandbox.__elements.meStatus.textContent, /invalid management key/);
});

test('transient /me network failure preserves the existing session', async () => {
  const sandbox = makeSandbox(async (url, opts) => {
    assert(url.endsWith('/v0/management/accounts/me'));
    assert.strictEqual(opts.headers.Authorization, 'Bearer still-valid-token');
    throw new Error('temporary network outage');
  }, { 'cpa-account-session': storedSession('still-valid-token') });

  runPortal(sandbox);
  await flush();

  assert.strictEqual(JSON.parse(sandbox.localStorage.getItem('cpa-account-session')).token, 'still-valid-token');
  assert.strictEqual(sandbox.__elements.sessionBadge.textContent, 'alice · user');
  assert.match(sandbox.__elements.meStatus.textContent, /temporary network outage/);
});

test('storage logout updates the current tab and clears profile/user rows without API logout', async () => {
  const calls = [];
  const sandbox = makeSandbox(async (url) => {
    calls.push(url);
    throw new Error('no network calls expected for cross-tab logout');
  }, { 'cpa-account-session': storedSession('cross-tab-token', { username: 'root', role: 'admin', disabled: false }) });

  runPortal(sandbox);
  sandbox.localStorage.removeItem('cpa-account-session');
  sandbox.dispatchEvent({ type: 'storage', key: 'cpa-account-session', newValue: null, oldValue: storedSession('cross-tab-token') });
  await flush();

  assert.strictEqual(sandbox.__elements.sessionBadge.textContent, '未登录');
  assert.strictEqual(sandbox.__elements.profile.children.length, 0);
  assert.strictEqual(sandbox.__elements.usersBody.children.length, 1);
  assert.strictEqual(sandbox.__elements.usersBody.children[0].children[0].textContent, '暂无数据');
  assert(!calls.some(url => String(url).endsWith('/v0/management/accounts/logout')), 'storage logout must not revoke from every tab');
});

test('storage replacement renders the new session and fetches profile with the new token', async () => {
  const calls = [];
  const sandbox = makeSandbox(async (url, opts) => {
    calls.push({ url, auth: opts.headers.Authorization });
    if (url.endsWith('/v0/management/accounts/me')) {
      if (opts.headers.Authorization === 'Bearer old-token') {
        return { ok: true, status: 200, text: async () => JSON.stringify({ user: { username: 'alice', role: 'user', disabled: false } }) };
      }
      assert.strictEqual(opts.headers.Authorization, 'Bearer new-token');
      return { ok: true, status: 200, text: async () => JSON.stringify({ user: { username: 'bob', role: 'admin', disabled: false } }) };
    }
    if (url.endsWith('/v0/management/accounts/users')) {
      assert.strictEqual(opts.headers.Authorization, 'Bearer new-token');
      return { ok: true, status: 200, text: async () => JSON.stringify({ users: [] }) };
    }
    throw new Error('unexpected fetch ' + url);
  }, { 'cpa-account-session': storedSession('old-token') });

  runPortal(sandbox);
  await flush();
  const replacement = storedSession('new-token', { username: 'bob', role: 'admin', disabled: false });
  sandbox.localStorage.setItem('cpa-account-session', replacement);
  sandbox.dispatchEvent({ type: 'storage', key: 'cpa-account-session', newValue: replacement, oldValue: storedSession('old-token') });
  await flush();

  assert.strictEqual(JSON.parse(sandbox.localStorage.getItem('cpa-account-session')).token, 'new-token');
  assert.strictEqual(sandbox.__elements.sessionBadge.textContent, 'bob · admin');
  assert(calls.some(call => call.url.endsWith('/v0/management/accounts/me') && call.auth === 'Bearer new-token'));
});

test('delayed /me response cannot resurrect a logged-out session', async () => {
  let resolveMe;
  const sandbox = makeSandbox(async (url, opts) => {
    if (url.endsWith('/v0/management/accounts/me')) {
      assert.strictEqual(opts.headers.Authorization, 'Bearer slow-token');
      return await new Promise(resolve => { resolveMe = resolve; });
    }
    if (url.endsWith('/v0/management/accounts/logout')) {
      return { ok: true, status: 204, text: async () => '' };
    }
    throw new Error('unexpected fetch ' + url);
  }, { 'cpa-account-session': storedSession('slow-token') });

  runPortal(sandbox);
  await flush();
  assert(resolveMe, 'initial /me request was not started');

  sandbox.localStorage.removeItem('cpa-account-session');
  sandbox.dispatchEvent({ type: 'storage', key: 'cpa-account-session', newValue: null, oldValue: storedSession('slow-token') });
  resolveMe({ ok: true, status: 200, text: async () => JSON.stringify({ user: { username: 'stale', role: 'admin', disabled: false } }) });
  await flush();

  assert.strictEqual(sandbox.localStorage.getItem('cpa-account-session'), null);
  assert.strictEqual(sandbox.__elements.sessionBadge.textContent, '未登录');
  assert.strictEqual(sandbox.__elements.profile.children.length, 0);
});


test('same-session storage updates preserve already rendered admin rows', async () => {
 const user={username:'admin',role:'admin',disabled:false};
 const sandbox=makeSandbox(async url=>({ok:true,text:async()=>JSON.stringify(url.endsWith('/me')?{user}:{users:[user]})}),{'cpa-account-session':storedSession('admin-token',user)});
 runPortal(sandbox);await flush();
 const row=sandbox.__elements.usersBody.children[0];
 sandbox.dispatchEvent({type:'storage',key:'cpa-account-session'});
 assert.strictEqual(sandbox.__elements.usersBody.children[0],row);
});
