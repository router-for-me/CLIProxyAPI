import { describe, expect, test } from 'bun:test';
import { buildExcludedModels } from '../src/features/providers/useProviderWorkbench';

/**
 * `excluded-models: ['*']` 是「该 provider 已停用」的后端编码。
 * 它的唯一所有者是表单的 `disabled` 开关：载入时被剥离进该 flag，保存时仅凭该 flag 重新追加。
 *
 * 这些断言把该不变量钉死，好让排除模型的编辑面（textarea → ExcludedModelsPicker）
 * 无论怎么重写都不可能污染停用语义。
 */
describe('buildExcludedModels — the "*" disable-rule invariant', () => {
  test('appends "*" when disabled', () => {
    expect(buildExcludedModels('a\nb', true, 'gemini')).toEqual(['a', 'b', '*']);
  });

  test('omits "*" when not disabled', () => {
    expect(buildExcludedModels('a\nb', false, 'gemini')).toEqual(['a', 'b']);
  });

  test('a hand-typed "*" never duplicates the disable rule', () => {
    expect(buildExcludedModels('a\n*\nb', true, 'gemini')).toEqual(['a', 'b', '*']);
  });

  test('a hand-typed "*" never switches the provider to disabled', () => {
    expect(buildExcludedModels('a\n*\nb', false, 'gemini')).toEqual(['a', 'b']);
  });

  test('disabled with no rules yields exactly the disable rule', () => {
    expect(buildExcludedModels('', true, 'gemini')).toEqual(['*']);
  });

  test('no rules and not disabled yields undefined, not an empty array', () => {
    expect(buildExcludedModels('', false, 'gemini')).toBeUndefined();
  });

  test('openaiCompatibility never receives the disable rule', () => {
    expect(buildExcludedModels('a', true, 'openaiCompatibility')).toEqual(['a']);
    expect(buildExcludedModels('', true, 'openaiCompatibility')).toBeUndefined();
  });
});
