import test from 'node:test';
import assert from 'node:assert/strict';
import {
  groupSettingsBySection,
  inferInputType,
  setByPath,
  getByPath,
} from './DynamicSettingsCard.jsx';

// --- groupSettingsBySection ------------------------------------------------

test('groupSettingsBySection: groups server-binding keys', () => {
  const out = groupSettingsBySection({ port: 8317, host: '', tls: { enable: false } });
  assert.deepEqual(
    [...out.ServerBinding].sort(),
    ['host', 'port', 'tls'].sort(),
  );
});

test('groupSettingsBySection: groups redis prefix', () => {
  const out = groupSettingsBySection({
    'redis-usage-queue-retention-seconds': 60,
    host: '127.0.0.1',
  });
  assert.ok(out.Usage.includes('redis-usage-queue-retention-seconds'));
  assert.ok(out.ServerBinding.includes('host'));
});

test('groupSettingsBySection: groups remote-management under Server binding', () => {
  const out = groupSettingsBySection({
    'remote-management': { 'allow-remote': false },
  });
  assert.ok(out.ServerBinding.includes('remote-management'));
});

test('groupSettingsBySection: groups cooling/retry keys', () => {
  const out = groupSettingsBySection({
    'disable-cooling': false,
    'transient-error-cooldown-seconds': 30,
    'save-cooldown-status': true,
    'max-retry-interval': 5,
  });
  assert.ok(out.Cooling.includes('disable-cooling'));
  assert.ok(out.Cooling.includes('transient-error-cooldown-seconds'));
  assert.ok(out.Cooling.includes('save-cooldown-status'));
  assert.ok(out.Cooling.includes('max-retry-interval'));
});

test('groupSettingsBySection: groups logging keys', () => {
  const out = groupSettingsBySection({
    'logging-to-file': true,
    'logs-max-total-size-mb': 100,
    'error-logs-max-files': 5,
  });
  assert.ok(out.Logging.includes('logging-to-file'));
  assert.ok(out.Logging.includes('logs-max-total-size-mb'));
  assert.ok(out.Logging.includes('error-logs-max-files'));
});

test('groupSettingsBySection: puts unknown keys in Advanced', () => {
  const out = groupSettingsBySection({ someFutureField: 'foo' });
  assert.ok(out.Advanced.includes('someFutureField'));
});

test('groupSettingsBySection: returns empty groups when settings is empty', () => {
  const out = groupSettingsBySection({});
  for (const keys of Object.values(out)) {
    assert.equal(keys.length, 0);
  }
});

// --- inferInputType --------------------------------------------------------

test('inferInputType: detects booleans', () => {
  assert.equal(inferInputType(true, 'enabled'), 'bool');
  assert.equal(inferInputType(false, 'enabled'), 'bool');
});

test('inferInputType: detects numbers', () => {
  assert.equal(inferInputType(8317, 'port'), 'number');
  assert.equal(inferInputType(0, 'count'), 'number');
  assert.equal(inferInputType(0.5, 'threshold'), 'number');
});

test('inferInputType: detects passwords from key name', () => {
  assert.equal(inferInputType('', 'secret-key'), 'password');
  assert.equal(inferInputType('', 'api-key'), 'password');
  assert.equal(inferInputType('', 'auth-token'), 'password');
});

test('inferInputType: detects passwords from value pattern', () => {
  assert.equal(inferInputType('', 'oauth-client-secret'), 'password');
});

test('inferInputType: detects objects', () => {
  assert.equal(inferInputType({ a: 1 }, 'tls'), 'object');
});

test('inferInputType: detects arrays', () => {
  assert.equal(inferInputType([], 'models'), 'array');
  assert.equal(inferInputType(['a', 'b'], 'plist'), 'array');
});

test('inferInputType: falls back to text for strings', () => {
  assert.equal(inferInputType('hello', 'host'), 'text');
  assert.equal(inferInputType('', 'host'), 'text');
});

// --- setByPath / getByPath --------------------------------------------------

test('setByPath / getByPath: sets and gets nested paths', () => {
  const obj = {};
  setByPath(obj, 'tls.enable', true);
  setByPath(obj, 'tls.cert', '/etc/cert.pem');
  assert.equal(getByPath(obj, 'tls.enable'), true);
  assert.equal(getByPath(obj, 'tls.cert'), '/etc/cert.pem');
});

test('setByPath / getByPath: getByPath returns undefined for missing', () => {
  assert.equal(getByPath({}, 'a.b.c'), undefined);
});

test('setByPath: overwrites existing value at the path', () => {
  const obj = { port: 8317 };
  setByPath(obj, 'port', 9999);
  assert.equal(obj.port, 9999);
});

test('setByPath: handles dotted key names that match the path separator', () => {
  // remote-management.allow-remote should treat 'remote-management' as a
  // literal key (the only '.' is the path separator).
  const obj = {};
  setByPath(obj, 'remote-management.allow-remote', true);
  assert.deepEqual(obj, { 'remote-management': { 'allow-remote': true } });
});
