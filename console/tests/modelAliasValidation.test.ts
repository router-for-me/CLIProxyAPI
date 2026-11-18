import { describe, expect, test } from 'bun:test';
import { hasModelAliasConflict } from '../src/components/modelAlias/aliasValidation';

describe('model alias validation', () => {
  test('checks aliases case-insensitively while excluding the renamed node', () => {
    expect(hasModelAliasConflict(['Foo'], ' foo ')).toBe(true);
    expect(hasModelAliasConflict(['Foo'], 'foo', 'Foo')).toBe(false);
    expect(hasModelAliasConflict(['Foo', 'Bar'], 'FOO', 'Bar')).toBe(true);
  });
});
