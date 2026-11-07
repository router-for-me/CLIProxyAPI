import { describe, expect, test } from 'bun:test';
import { loginRedirectTarget } from '../src/router/location';

describe('login redirect location', () => {
  test('restores pathname, query filters and hash together', () => {
    expect(
      loginRedirectTarget({
        from: {
          pathname: '/usage',
          search: '?credential_id=cred-1&scope=site&model=kimi-k3',
          hash: '#records',
        },
      })
    ).toEqual({
      pathname: '/usage',
      search: '?credential_id=cred-1&scope=site&model=kimi-k3',
      hash: '#records',
    });
  });

  test('defaults missing state and location parts without inventing filters', () => {
    expect(loginRedirectTarget(null)).toEqual({ pathname: '/', search: '', hash: '' });
    expect(loginRedirectTarget({ from: { pathname: '/usage' } })).toEqual({
      pathname: '/usage',
      search: '',
      hash: '',
    });
  });
});
