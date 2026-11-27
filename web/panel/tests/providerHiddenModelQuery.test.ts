import { expect, test } from 'bun:test';
import { getModelPickerView } from '@/features/providers/resourceView';

test('a retained model query cannot hide options once the search field disappears', () => {
  const models = Array.from({ length: 9 }, (_, i) => `model-${i}`);
  const query = ' MODEL-8 ';
  expect(getModelPickerView(models, query)).toEqual({
    showSearch: true,
    visibleModels: ['model-8'],
  });

  const remaining = models.slice(0, 8);
  expect(getModelPickerView(remaining, query)).toEqual({
    showSearch: false,
    visibleModels: remaining,
  });
});
