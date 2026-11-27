import { describe, expect, test } from 'bun:test';
import { applyNativeDisabled, isNativeKeyDisabled } from '@/services/api/nativeDisabled';

const states = (group: Record<string, unknown>) =>
  (group.keys as Record<string, unknown>[]).map((key) => isNativeKeyDisabled(group, key));

describe('native key on/off', () => {
  test('a group switch uses the group rule and reaches keys that carry their own list', () => {
    const group = {
      'excluded-models': ['gpt-old'],
      keys: [{ 'api-key': 'a' }, { 'api-key': 'b', 'excluded-models': ['x'] }],
    };
    const off = applyNativeDisabled(group, { all: true });
    expect(off['excluded-models']).toEqual(['gpt-old', '*']);
    // The inheriting key gains no override; the overriding key gets the rule in its own list.
    expect((off.keys as Record<string, unknown>[])[0]).toEqual({ 'api-key': 'a' });
    expect((off.keys as Record<string, unknown>[])[1]['excluded-models']).toEqual(['x', '*']);
    expect(states(off)).toEqual([true, true]);

    const on = applyNativeDisabled(off, { all: false });
    expect(on['excluded-models']).toEqual(['gpt-old']);
    expect(states(on)).toEqual([true, true].map(() => false));
    expect(group.keys[0]).toEqual({ 'api-key': 'a' });
  });

  test('turning a group on drops an otherwise empty group list', () => {
    const on = applyNativeDisabled({ 'excluded-models': [' * '], keys: [{}] }, { all: false });
    expect('excluded-models' in on).toBe(false);
    expect(states(on)).toEqual([false]);
  });

  test('enabling keys leaves an explicit empty group exclusions list intact', () => {
    const group = { 'excluded-models': [], keys: [{ 'excluded-models': ['*'] }] };
    expect(applyNativeDisabled(group, { all: false })).toEqual({
      'excluded-models': [],
      keys: [{ 'excluded-models': [] }],
    });
  });

  test('a single key only gains its own list when it must differ from the group', () => {
    const group = { 'excluded-models': ['m'], keys: [{ 'api-key': 'a' }, { 'api-key': 'b' }] };
    const next = applyNativeDisabled(group, { keys: [true, false] });
    const keys = next.keys as Record<string, unknown>[];
    expect(keys[0]['excluded-models']).toEqual(['m', '*']);
    expect(keys[1]).toEqual({ 'api-key': 'b' });
    expect(states(next)).toEqual([true, false]);
  });

  test('per-key intents apply after the group switch', () => {
    const group = { keys: [{ 'api-key': 'a' }, { 'api-key': 'b' }, { 'api-key': 'c' }] };
    const next = applyNativeDisabled(group, { all: true, keys: [undefined, false] });
    const keys = next.keys as Record<string, unknown>[];
    expect(next['excluded-models']).toEqual(['*']);
    expect(keys[1]['excluded-models']).toEqual([]);
    expect(states(next)).toEqual([true, false, true]);
  });
});
