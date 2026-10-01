// Unit tests for the Default user fallback picker on the API token detail page.
//
// The dashboard has no React-component testing harness (only pure function
// tests run under node:test, see Quota.test.jsx). We follow that convention by
// exporting the pure filter used by the Internal User dropdown and asserting
// its behavior here; the component render path is exercised by `npm run build`.

import test from 'node:test';
import assert from 'node:assert/strict';
import { filterInternalUsers } from './ApiTokenDetailPage.jsx';

const USERS = [
  { id: 'u-1', user_alias: 'alice', user_email: 'alice@example.com' },
  { id: 'u-2', user_alias: 'bob', user_email: 'bob@corp.io' },
  { id: 'u-3', user_alias: '', user_email: 'carol@example.com' },
];

test('filterInternalUsers: empty query returns every user', () => {
  assert.deepEqual(filterInternalUsers(USERS, ''), USERS);
  assert.deepEqual(filterInternalUsers(USERS, '   '), USERS);
});

test('filterInternalUsers: matches alias, email, and id case-insensitively', () => {
  assert.deepEqual(filterInternalUsers(USERS, 'ALICE').map((u) => u.id), ['u-1']);
  assert.deepEqual(filterInternalUsers(USERS, 'corp.io').map((u) => u.id), ['u-2']);
  assert.deepEqual(filterInternalUsers(USERS, 'U-3').map((u) => u.id), ['u-3']);
});

test('filterInternalUsers: returns [] when nothing matches', () => {
  assert.deepEqual(filterInternalUsers(USERS, 'nobody'), []);
});

test('filterInternalUsers: tolerates null/undefined input', () => {
  assert.deepEqual(filterInternalUsers(null, 'x'), []);
  assert.deepEqual(filterInternalUsers(undefined, ''), []);
});
