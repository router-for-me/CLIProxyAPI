import test from 'node:test';
import assert from 'node:assert/strict';
import { parseProxyLine, formatTestStatus, validatePoolForm, effectivePreview } from './ProxyPoolsPage.jsx';

test('parseProxyLine: bare host:port becomes http URL', () => {
  assert.equal(parseProxyLine('1.2.3.4:8080'), 'http://1.2.3.4:8080');
});

test('parseProxyLine: user:pass@host:port', () => {
  assert.equal(parseProxyLine('u:p@h:3128'), 'http://u:p@h:3128');
});

test('parseProxyLine: full URL passes through', () => {
  assert.equal(parseProxyLine('socks5://h:1080'), 'socks5://h:1080');
});

test('parseProxyLine: https URL passes through (normalized with trailing slash)', () => {
  assert.equal(parseProxyLine('https://h:8443'), 'https://h:8443/');
});

test('parseProxyLine: garbage rejects', () => {
  assert.throws(() => parseProxyLine('nonsense'));
});

test('parseProxyLine: empty line rejects', () => {
  assert.throws(() => parseProxyLine('   '));
});

test('parseProxyLine: unsupported scheme rejects', () => {
  assert.throws(() => parseProxyLine('ftp://h:21'));
});

test('formatTestStatus maps statuses to labels', () => {
  assert.equal(formatTestStatus('active'), 'Active');
  assert.equal(formatTestStatus('error'), 'Error');
  assert.equal(formatTestStatus('unknown'), 'Not tested');
  assert.equal(formatTestStatus(''), 'Not tested');
  assert.equal(formatTestStatus(undefined), 'Not tested');
});

// ── validatePoolForm (inline per-field validation) ──────────────────────

function baseForm(overrides = {}) {
  return {
    name: 'pool-a',
    proxy_url: 'socks5://u:p@10.0.0.1:1080',
    no_proxy: '',
    type: 'http',
    is_active: true,
    strict_proxy: true,
    ...overrides,
  };
}

test('validatePoolForm: valid form yields no errors', () => {
  assert.deepEqual(validatePoolForm(baseForm()), {});
});

test('validatePoolForm: name required, minimum 2 chars', () => {
  assert.match(validatePoolForm(baseForm({ name: '' })).name, /required/i);
  assert.match(validatePoolForm(baseForm({ name: 'x' })).name, /at least 2/i);
});

test('validatePoolForm: url required and must be absolute', () => {
  assert.match(validatePoolForm(baseForm({ proxy_url: '' })).proxy_url, /required/i);
  assert.match(validatePoolForm(baseForm({ proxy_url: 'not a url' })).proxy_url, /absolute/i);
  assert.match(validatePoolForm(baseForm({ proxy_url: 'example.com:8080' })).proxy_url, /absolute/i);
});

test('validatePoolForm: http pool constrains schemes', () => {
  assert.match(validatePoolForm(baseForm({ proxy_url: 'ftp://h:21' })).proxy_url, /scheme/i);
  assert.equal(validatePoolForm(baseForm({ proxy_url: 'socks5h://h:1080' })).proxy_url, undefined);
  assert.equal(validatePoolForm(baseForm({ proxy_url: 'https://h:8443' })).proxy_url, undefined);
});

test('validatePoolForm: relay pools require https', () => {
  assert.match(validatePoolForm(baseForm({ type: 'cloudflare', proxy_url: 'http://relay.example' })).proxy_url, /https/i);
  assert.equal(validatePoolForm(baseForm({ type: 'deno', proxy_url: 'https://relay.deno.net' })).proxy_url, undefined);
});

test('validatePoolForm: no_proxy format rules', () => {
  assert.match(validatePoolForm(baseForm({ no_proxy: '*  ,api.example.com' })).no_proxy, /only/i);
  assert.equal(validatePoolForm(baseForm({ no_proxy: '.corp, x.example' })).no_proxy, undefined);
  assert.equal(validatePoolForm(baseForm({ no_proxy: '  ' })).no_proxy, undefined);
});

// ── effectivePreview (live stored-URL preview) ──────────────────────────

test('effectivePreview: http pool shows composite suffix with no_proxy and strict', () => {
  const out = effectivePreview(baseForm({ no_proxy: '.corp, x.example', strict_proxy: true }));
  assert.equal(out, 'socks5://u:p@10.0.0.1:1080?no_proxy=.corp%2Cx.example&strict=true');
});

test('effectivePreview: loose strict renders strict=false', () => {
  const out = effectivePreview(baseForm({ strict_proxy: false }));
  assert.equal(out, 'socks5://u:p@10.0.0.1:1080?strict=false');
});

test('effectivePreview: empty no_proxy omits the key entirely', () => {
  const out = effectivePreview(baseForm());
  assert.equal(out, 'socks5://u:p@10.0.0.1:1080?strict=true');
});

test('effectivePreview: relay pools render the plain https base', () => {
  const out = effectivePreview(baseForm({ type: 'cloudflare', proxy_url: 'https://relay.example.workers.dev', no_proxy: '.corp' }));
  assert.equal(out, 'https://relay.example.workers.dev/');
});

test('effectivePreview: invalid url yields empty string (no preview noise)', () => {
  assert.equal(effectivePreview(baseForm({ proxy_url: '' })), '');
});
