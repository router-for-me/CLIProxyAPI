import test from 'node:test';
import assert from 'node:assert/strict';
import { parseProxyLine, formatTestStatus, validatePoolForm, effectivePreview, summarizeBulkResults, togglePoolSelection, previewImportLines, healthCheckFlash } from './ProxyPoolsPage.jsx';

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

// ── summarizeBulkResults (aggregate flash message) ──────────────────────

test('summarizeBulkResults: all clean', () => {
  assert.equal(summarizeBulkResults({ done: 3, ok: 3, skipped: 0, failed: 0 }), '3 deleted');
});

test('summarizeBulkResults: with bound skips', () => {
  const out = summarizeBulkResults({ done: 3, ok: 1, skipped: 2, failed: 0, skippedLabel: 'bound' });
  assert.match(out, /1 deleted/);
  assert.match(out, /2 skipped \(bound\)/);
  assert.ok(!/failed/.test(out));
});

test('summarizeBulkResults: failures surface the count', () => {
  const out = summarizeBulkResults({ done: 3, ok: 1, skipped: 0, failed: 2 });
  assert.match(out, /1 deleted/);
  assert.match(out, /2 failed/);
});

test('summarizeBulkResults: nothing succeeded reports the failure plainly', () => {
  const out = summarizeBulkResults({ done: 2, ok: 0, skipped: 0, failed: 2 });
  assert.match(out, /^Deleted 0/);
  assert.match(out, /2 failed/);
});

test('summarizeBulkResults: test variant uses tested wording', () => {
  const out = summarizeBulkResults({ done: 4, ok: 3, skipped: 0, failed: 1, verb: 'tested' });
  assert.match(out, /3 tested/);
  assert.match(out, /1 failed/);
});

// ── togglePoolSelection (selection helper) ──────────────────────────────

test('togglePoolSelection: adds an unselected id', () => {
  assert.deepEqual(togglePoolSelection([1, 2], 3), [1, 2, 3]);
});

test('togglePoolSelection: removes a selected id', () => {
  assert.deepEqual(togglePoolSelection([1, 2, 3], 2), [1, 3]);
});

test('togglePoolSelection: tolerates duplicates and preserves order', () => {
  // Duplicate 5 collapses to one; toggling 2 removes it.
  assert.deepEqual(togglePoolSelection([5, 2, 5], 2), [5]);
  assert.deepEqual(togglePoolSelection([5, 2], 2), [5]);
  // Toggling 5 (present) removes it; toggling it again (absent) appends.
  assert.deepEqual(togglePoolSelection([5, 2, 5], 5), [2]);
  assert.deepEqual(togglePoolSelection([2], 5), [2, 5]);
});

// ── previewImportLines (batch import live preview) ──────────────────────

test('previewImportLines: valid lines parse to ok chips with URLs', () => {
  const out = previewImportLines('1.2.3.4:8080\nsocks5://h:1080');
  assert.equal(out.length, 2);
  assert.equal(out[0].line, 1);
  assert.equal(out[0].ok, true);
  assert.equal(out[0].url, 'http://1.2.3.4:8080');
  assert.equal(out[1].ok, true);
  assert.ok(!out[1].error);
});

test('previewImportLines: invalid lines carry the error', () => {
  const out = previewImportLines('garbage');
  assert.equal(out.length, 1);
  assert.equal(out[0].ok, false);
  assert.match(out[0].error, /expected host:port/i);
});

test('previewImportLines: duplicate URLs are marked dup', () => {
  const out = previewImportLines('1.2.3.4:8080\n1.2.3.4:8080');
  assert.equal(out[0].dup, false);
  assert.equal(out[1].dup, true);
  assert.equal(out[1].ok, true);
});

test('previewImportLines: blank and whitespace-only lines are skipped', () => {
  const out = previewImportLines('\n   \n1.2.3.4:8080\n');
  assert.equal(out.length, 1);
  assert.equal(out[0].line, 3);
});

test('previewImportLines: empty input yields empty array', () => {
  assert.deepEqual(previewImportLines(''), []);
});

// ── healthCheckFlash (completion message) ───────────────────────────────

test('healthCheckFlash: complete with all healthy', () => {
  assert.equal(healthCheckFlash({ ok: 4, failed: 0, stopped: false }), 'Health check complete: 4 healthy');
});

test('healthCheckFlash: complete with failures', () => {
  assert.equal(healthCheckFlash({ ok: 4, failed: 2, stopped: false }), 'Health check complete: 4 healthy, 2 failed');
});

test('healthCheckFlash: stopped prefixes the interruption', () => {
  assert.equal(healthCheckFlash({ ok: 2, failed: 1, stopped: true }), 'Health check stopped: 2 healthy, 1 failed');
});

test('healthCheckFlash: all failed still reads plainly', () => {
  assert.equal(healthCheckFlash({ ok: 0, failed: 3, stopped: false }), 'Health check complete: 0 healthy, 3 failed');
});
