import { describe, expect, test } from 'bun:test';
import { removeModelAt } from '../src/features/providers/modelEntries';
import type { ModelEntryInput } from '../src/features/providers/types';

describe('removeModelAt', () => {
  test('resets the last model to a fresh blank row without metadata', () => {
    const entry: ModelEntryInput = {
      name: 'model-a',
      alias: 'alias-a',
      sourceIndex: 4,
      priority: 2,
      image: true,
      thinkingJson: '{"budget":1024}',
    };
    const current = [entry];

    const next = removeModelAt(current, 0);

    expect(next).toEqual([{ name: '', alias: '' }]);
    expect(next[0]).not.toBe(entry);
    expect(current).toEqual([entry]);
    expect(entry.name).toBe('model-a');
    expect(entry.sourceIndex).toBe(4);
  });

  test('removes only the selected index and preserves remaining order and metadata', () => {
    const current: ModelEntryInput[] = [
      { name: 'same', alias: 'first', sourceIndex: 0 },
      { name: 'same', alias: 'second', sourceIndex: 1 },
      { name: 'last', alias: '', priority: 3 },
    ];
    const original = [...current];

    const next = removeModelAt(current, 1);

    expect(next).toEqual([original[0], original[2]]);
    expect(next[0]).toBe(original[0]);
    expect(next[1]).toBe(original[2]);
    expect(current).toEqual(original);
  });

  test('repeated removal of a blank row retains exactly one fresh blank row', () => {
    const first = removeModelAt([{ name: '', alias: '' }], 0);
    const second = removeModelAt(first, 0);

    expect(second).toEqual([{ name: '', alias: '' }]);
    expect(second[0]).not.toBe(first[0]);
  });

  test('empty input produces a blank row', () => {
    expect(removeModelAt([], 0)).toEqual([{ name: '', alias: '' }]);
  });
});
