import { describe, expect, test } from 'bun:test';
import { matchesAutocompleteQuery } from '../src/components/ui/autocompleteMatching';
import {
  buildCredentialUsagePath,
  buildModelCandidateQuery,
  buildUsageFilterQuery,
  resolveUsageKey,
  resolveUsageModel,
  type UsageKeyOption,
} from '../src/features/sharing/usageFilters';

const models = ['deepseek-flash', 'deepseek-flash（123-123/deepseek-flash）', 'kimi-k3'];
const keys: UsageKeyOption[] = [
  { label: '@alice', consumer: 'alice' },
  { label: '@bob', consumer: 'bob' },
  { label: '手机（@alice）', keyId: 'phone-id' },
  { label: '工作（@alice）', keyId: 'work-id' },
  { label: '手机（@bob）', keyId: 'bob-phone-id' },
];

describe('usage autocomplete candidates', () => {
  test('candidate query ignores the applied model filter and pagination', () => {
    const parsed = new URLSearchParams(
      buildModelCandidateQuery(
        'scope=contributed&model=deepseek-flash&page=3&page_size=50&channel_id=c1&credential_id=cred-1&from=2026-10-01T00%3A00%3A00.000Z',
        'contributed',
        'alice'
      )
    );
    expect(parsed.get('model')).toBeNull();
    expect(parsed.get('page')).toBeNull();
    expect(parsed.get('page_size')).toBeNull();
    expect(parsed.get('scope')).toBeNull();
    expect(parsed.get('channel_id')).toBe('c1');
    expect(parsed.get('credential_id')).toBe('cred-1');
    expect(parsed.get('from')).toBe('2026-10-01T00:00:00.000Z');
    expect(parsed.get('owner')).toBe('alice');
    expect(parsed.get('consumer')).toBeNull();
  });

  test('candidate query equals the main filter query when no model filter is applied', () => {
    // This equality is what lets the usage page reuse its main summary as the
    // model candidate source instead of issuing a duplicate summary request.
    const cases: [string, 'mine' | 'contributed' | 'site', string][] = [
      [
        'scope=site&channel_id=c1&consumer=bob&from=2026-10-01T00:00:00.000Z&to=2026-10-02T00:00:00.000Z',
        'site',
        'alice',
      ],
      ['key_id=k9&page=4&page_size=100', 'mine', 'alice'],
      ['scope=contributed&key_id=k9&key=phone', 'contributed', 'alice'],
    ];
    for (const [query, scope, me] of cases) {
      expect(buildModelCandidateQuery(query, scope, me)).toBe(
        buildUsageFilterQuery(query, scope, me)
      );
    }
  });

  test('applying a model diverges the candidate query from the main filter query', () => {
    const query = 'model=kimi-k3&channel_id=c1&credential_id=cred-1';
    expect(buildModelCandidateQuery(query, 'mine', 'alice')).not.toBe(
      buildUsageFilterQuery(query, 'mine', 'alice')
    );
    const main = new URLSearchParams(buildUsageFilterQuery(query, 'mine', 'alice'));
    expect(main.get('model')).toBe('kimi-k3');
    expect(main.get('credential_id')).toBe('cred-1');
  });

  test('pagination changes do not alter the candidate query', () => {
    expect(buildModelCandidateQuery('model=x&page=2&page_size=100', 'mine', 'alice')).toBe(
      buildModelCandidateQuery('model=x&page=9&page_size=50', 'mine', 'alice')
    );
  });

  test('candidate query keeps scope identity for mine and site', () => {
    const mine = new URLSearchParams(buildModelCandidateQuery('model=x', 'mine', 'alice'));
    expect(mine.get('consumer')).toBe('alice');
    expect(mine.get('owner')).toBeNull();
    const site = new URLSearchParams(buildModelCandidateQuery('model=x', 'site', 'alice'));
    expect(site.get('consumer')).toBeNull();
    expect(site.get('owner')).toBeNull();
  });

  test('partial text narrows model candidates without becoming a model filter', () => {
    expect(models.filter((model) => matchesAutocompleteQuery(model, 'FLASH'))).toEqual(
      models.slice(0, 2)
    );
    expect(resolveUsageModel('FLASH', models)).toBeNull();
    expect(models.filter((model) => matchesAutocompleteQuery(model, '123-123'))).toEqual([
      models[1],
    ]);
    expect(resolveUsageModel('123-123', models)).toBeNull();
  });

  test('Key and member fragments narrow candidates but do not resolve to list filters', () => {
    expect(keys.filter((key) => matchesAutocompleteQuery(key.label, '手机'))).toHaveLength(2);
    expect(keys.filter((key) => matchesAutocompleteQuery(key.label, '@ali'))).toHaveLength(3);
    expect(resolveUsageKey('手机', keys)).toBeNull();
    expect(resolveUsageKey('@ali', keys)).toBeNull();
  });

  test('complete Key and member options resolve to exact identities', () => {
    expect(resolveUsageKey('手机（@alice）', keys)).toEqual(keys[2]);
    expect(resolveUsageKey('手机(@bob)', keys)).toEqual(keys[4]);
    expect(resolveUsageKey('@alice', keys)).toEqual(keys[0]);
    expect(resolveUsageKey('@ALICE', keys)).toEqual(keys[0]);
  });

  test('complete model choices accept both parentheses and normalize to the candidate', () => {
    expect(matchesAutocompleteQuery(models[1], 'flash(123-123/')).toBe(true);
    expect(resolveUsageModel('deepseek-flash(123-123/deepseek-flash)', models)).toBe(models[1]);
    expect(resolveUsageModel('deepseek-flash', models)).toBe(models[0]);
  });

  test('clearing removes the filter and saved model selections survive empty candidates', () => {
    expect(resolveUsageKey('', keys)).toEqual({ label: '' });
    expect(resolveUsageModel('', models, models[1])).toBe('');
    expect(resolveUsageModel(models[1], [], models[1])).toBe(models[1]);
    expect(resolveUsageModel('flash', [], models[1])).toBeNull();
    expect(resolveUsageKey('不存在', keys)).toBeNull();
  });

  test('credential deep links preserve exact IDs and select the appropriate scope', () => {
    const ownerPath = buildCredentialUsagePath(
      { id: 'cred / 中文#1', owner: 'alice' },
      'alice',
      false
    );
    const ownerParams = new URLSearchParams(ownerPath.slice(ownerPath.indexOf('?') + 1));
    expect(ownerPath.startsWith('/usage?')).toBe(true);
    expect(ownerParams.get('credential_id')).toBe('cred / 中文#1');
    expect(ownerParams.get('scope')).toBe('contributed');

    const adminParams = new URLSearchParams(
      buildCredentialUsagePath({ id: 'cred-1', owner: 'alice' }, 'bob', true).split('?')[1]
    );
    expect(adminParams.get('scope')).toBe('site');

    const memberPath = buildCredentialUsagePath({ id: 'cred-1', owner: 'alice' }, 'bob', false);
    expect(new URLSearchParams(memberPath.split('?')[1]).get('scope')).toBeNull();
    expect(buildCredentialUsagePath({ owner: 'alice' }, 'alice', false)).toBe('');
  });
});
