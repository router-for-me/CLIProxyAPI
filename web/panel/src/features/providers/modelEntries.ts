import type { ModelEntryInput } from './types';

/** Keep one fresh blank row when the last model is removed, without retaining metadata. */
export function removeModelAt(current: ModelEntryInput[], index: number): ModelEntryInput[] {
  const next = current.filter((_, entryIndex) => entryIndex !== index);
  return next.length ? next : [{ name: '', alias: '' }];
}
