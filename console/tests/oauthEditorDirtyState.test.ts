import { describe, expect, test } from 'bun:test';
import {
  getModelAliasDraftSignature,
  getStringSetSignature,
} from '../src/features/authFiles/oauthEditorState';

describe('OAuth editor dirty state', () => {
  test('compares model selections independent of order', () => {
    expect(getStringSetSignature(['b', 'a'])).toBe(getStringSetSignature(['a', 'b']));
  });

  test('ignores generated row ids but preserves partial alias edits', () => {
    expect(getModelAliasDraftSignature([{ id: 'one', name: '', alias: '', fork: true }])).toBe(
      getModelAliasDraftSignature([])
    );
    expect(
      getModelAliasDraftSignature([{ id: 'one', name: 'partial', alias: '', fork: true }])
    ).not.toBe(getModelAliasDraftSignature([]));
  });
});
