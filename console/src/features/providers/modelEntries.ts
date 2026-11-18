import type { ModelInfo } from '@/utils/models';
import type { ModelEntryInput } from './types';

/** Keep one fresh blank row when the last model is removed, without retaining metadata. */
export function removeModelAt(current: ModelEntryInput[], index: number): ModelEntryInput[] {
  const next = current.filter((_, entryIndex) => entryIndex !== index);
  return next.length ? next : [{ name: '', alias: '' }];
}

/** Discovery deduplicates new names, never existing rows with distinct aliases or metadata. */
export function mergeDiscoveredModels(
  current: ModelEntryInput[],
  incoming: ModelInfo[]
): ModelEntryInput[] {
  if (!incoming.length) return current;
  const seen = new Set(current.map((entry) => entry.name.trim()).filter(Boolean));
  const next = [...current];
  const placeholder = next.findIndex((entry) => !entry.name.trim() && !entry.alias?.trim());
  if (placeholder !== -1) next.splice(placeholder, 1);
  for (const model of incoming) {
    const name = model.name.trim();
    if (!name || seen.has(name)) continue;
    seen.add(name);
    next.push({ name, alias: model.alias?.trim() ?? '' });
  }
  return next.length ? next : [{ name: '', alias: '' }];
}
