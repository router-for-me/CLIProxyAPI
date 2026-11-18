import { describe, expect, test } from 'bun:test';
import {
  filterAutocompleteOptions,
  normalizeAutocompleteOptions,
} from '../src/components/ui/autocompleteOptions';

const options = normalizeAutocompleteOptions([
  'claude-sonnet-4-6',
  'claude-opus-4-1',
  { value: 'gpt-5', label: 'OpenAI flagship' },
]);

describe('autocomplete option filtering', () => {
  test('keeps every option reachable when reopened with a selected value', () => {
    expect(filterAutocompleteOptions(options, 'claude-sonnet-4-6', false)).toEqual(options);
  });

  test('narrows by value or label while typing', () => {
    expect(filterAutocompleteOptions(options, 'OPUS', true).map((opt) => opt.value)).toEqual([
      'claude-opus-4-1',
    ]);
    expect(filterAutocompleteOptions(options, 'flagship', true).map((opt) => opt.value)).toEqual([
      'gpt-5',
    ]);
  });

  test('shows everything when the typed query is blank', () => {
    expect(filterAutocompleteOptions(options, '  ', true)).toEqual(options);
  });

  test('normalizes string and object options', () => {
    expect(normalizeAutocompleteOptions(['a', { value: 'b' }])).toEqual([
      { value: 'a', label: 'a' },
      { value: 'b', label: 'b' },
    ]);
  });
});
