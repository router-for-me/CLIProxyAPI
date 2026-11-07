import { describe, expect, test } from 'bun:test';
import { matchesAutocompleteQuery } from '../src/components/ui/autocompleteMatching';
import {
  buildCredentialUsagePath,
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

describe('usage filters and autocomplete resolution', () => {
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
