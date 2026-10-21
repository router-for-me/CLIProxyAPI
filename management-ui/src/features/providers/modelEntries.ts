import type { ModelInfo } from '@/utils/models';
import type { ModelEntryInput } from './types';

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
