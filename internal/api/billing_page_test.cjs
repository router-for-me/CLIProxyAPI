// Run with: node --test internal/api/billing_page_test.cjs
const test = require('node:test');
const assert = require('node:assert/strict');
const vm = require('node:vm');
const fs = require('node:fs');
const path = require('node:path');
const html = fs.readFileSync(path.join(__dirname, '../../static/billing-token-page.html'), 'utf8');
function source(name) {
  const line = html.split('\n').find(line => line.startsWith(`async function ${name}(`));
  assert.ok(line, `missing ${name}`);
  return line;
}
function deferred() {
  let resolve, reject;
  const promise = new Promise((yes, no) => { resolve = yes; reject = no; });
  return { promise, resolve, reject };
}
test('billing starts in parallel and renders before quota completes', async () => {
  const quota = deferred();
  const calls = [], rendered = [], statuses = [];
  const context = vm.createContext({
    loadQuotaAware() { calls.push('quota'); return quota.promise; },
    async api(url) { calls.push(url); return { tokens: [{ token_id: 'test' }], mode: 'observe' }; },
    render(data) { rendered.push(data); },
    setStatus(...args) { statuses.push(args); }
  });
  vm.runInContext(source('load'), context);
  const done = context.load();
  assert.deepEqual(calls, ['quota', '/billing/api-tokens']);
  await new Promise(resolve => setImmediate(resolve));
  assert.equal(rendered.length, 1);
  assert.match(statuses.at(-1)[0], /Loaded 1 tokens/);
  quota.reject(new Error('quota unavailable'));
  await done;
  assert.equal(rendered.length, 1);
});
test('billing failure remains local while quota can succeed', async () => {
  let quotaLoaded = false;
  const statuses = [];
  const context = vm.createContext({
    async loadQuotaAware() { quotaLoaded = true; },
    async api() { throw new Error('billing unavailable'); },
    render() { assert.fail('must not render failed data'); },
    setStatus(...args) { statuses.push(args); }
  });
  vm.runInContext(source('load'), context);
  await context.load();
  assert.equal(quotaLoaded, true);
  assert.match(statuses.at(-1)[0], /Billing load failed: billing unavailable/);
});
test('API requests have a bounded timeout and clean up the timer', async () => {
  let timeout, cleared = false;
  const context = vm.createContext({
    state: {}, authState: () => ({ apiBase: 'http://localhost' }), headers: () => ({}),
    AbortController,
    setTimeout(fn, ms) { assert.equal(ms, 15000); timeout = fn; return 1; },
    clearTimeout(id) { assert.equal(id, 1); cleared = true; },
    fetch(url, opts) { return new Promise((resolve, reject) => {
      opts.signal.addEventListener('abort', () => reject(Object.assign(new Error('aborted'), { name: 'AbortError' })));
    }); }
  });
  vm.runInContext(source('api'), context);
  const result = context.api('/billing/api-tokens');
  timeout();
  await assert.rejects(result, /Request timed out after 15 seconds/);
  assert.equal(cleared, true);
});
test('inline billing script parses', () => {
  const inline = html.match(/<script>([\s\S]*?)<\/script>/);
  assert.ok(inline);
  new vm.Script(inline[1]);
});
