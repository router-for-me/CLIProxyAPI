import test from 'node:test';
import assert from 'node:assert/strict';
import { parseProxyLine, formatTestStatus } from './ProxyPoolsPage.jsx';

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
