import { isRecord } from '@/utils/helpers';

/**
 * Native API-key credentials have no `disabled` field: a key is off when its effective
 * `excluded-models` contains `*`. A key's own list replaces the group list as a whole, so
 * the group rule alone cannot reach keys that carry their own list.
 */
const DISABLE_ALL = '*';

const isDisableAll = (item: unknown) => String(item ?? '').trim() === DISABLE_ALL;

const withRule = (items: readonly unknown[], disabled: boolean) => {
  const rest = items.filter((item) => !isDisableAll(item));
  return disabled ? [...rest, DISABLE_ALL] : rest;
};

const ownList = (key: Record<string, unknown>) =>
  Array.isArray(key['excluded-models']) ? (key['excluded-models'] as unknown[]) : null;

const groupList = (group: Record<string, unknown>) =>
  Array.isArray(group['excluded-models']) ? (group['excluded-models'] as unknown[]) : [];

export function isNativeKeyDisabled(
  group: Record<string, unknown>,
  key: Record<string, unknown>
): boolean {
  return (ownList(key) ?? groupList(group)).some(isDisableAll);
}

export interface NativeDisableIntent {
  /** Turn every key of the group on or off; applied before per-key intents. */
  all?: boolean;
  /** Desired state per final key index; `undefined` leaves that key alone. */
  keys?: ReadonlyArray<boolean | undefined>;
}

/**
 * Apply on/off intents to a raw native group with as few overrides as possible:
 * the group rule covers inheriting keys, and a key only gains its own list when it must
 * differ from the group.
 */
export function applyNativeDisabled(
  group: Record<string, unknown>,
  intent: NativeDisableIntent
): Record<string, unknown> {
  const next: Record<string, unknown> = { ...group };
  let keys: unknown[] = Array.isArray(group.keys) ? [...group.keys] : [];

  if (intent.all !== undefined) {
    const all = intent.all;
    const shared = withRule(groupList(next), all);
    if (
      shared.length ||
      (Array.isArray(next['excluded-models']) && !next['excluded-models'].length)
    )
      next['excluded-models'] = shared;
    else delete next['excluded-models'];
    keys = keys.map((key) => {
      if (!isRecord(key)) return key;
      const own = ownList(key);
      return own ? { ...key, 'excluded-models': withRule(own, all) } : key;
    });
  }

  intent.keys?.forEach((disabled, index) => {
    const key = keys[index];
    if (disabled === undefined || !isRecord(key)) return;
    if (isNativeKeyDisabled(next, key) === disabled) return;
    keys[index] = {
      ...key,
      'excluded-models': withRule(ownList(key) ?? groupList(next), disabled),
    };
  });

  next.keys = keys;
  return next;
}
