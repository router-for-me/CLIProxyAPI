import type { ModelInfo } from '@/utils/models';
import type { ModelEntryInput } from './types';

/** Discovery selection is per upstream name, not per alias row. */
export function discoveryCatalog(models: ModelInfo[]): ModelInfo[] {
  const seen = new Set<string>();
  return models.flatMap((model) => {
    const name = model.name.trim();
    if (!name || seen.has(name)) return [];
    seen.add(name);
    return [{ ...model, name }];
  });
}

/** Explicit choices survive reloads and parent edits; untouched names follow the parent. */
export function resolveDiscoverySelection(
  catalog: ModelInfo[],
  existingNames: Set<string>,
  choices: ReadonlyMap<string, boolean>
): Set<string> {
  const existing = new Set([...existingNames].map((name) => name.trim()));
  return new Set(
    discoveryCatalog(catalog)
      .filter(({ name }) => choices.get(name) ?? existing.has(name))
      .map(({ name }) => name)
  );
}

/** Only visible names are changed, preserving staged choices hidden by search or reload. */
export function setDiscoveryChoices(
  choices: ReadonlyMap<string, boolean>,
  names: string[],
  checked: boolean
): Map<string, boolean> {
  const next = new Map(choices);
  for (const name of names) {
    if (name.trim()) next.set(name.trim(), checked);
  }
  return next;
}

export function discoverySelectionDiff(
  catalog: ModelInfo[],
  existingNames: Set<string>,
  selected: Set<string>
): { added: number; removed: number } {
  const existing = new Set([...existingNames].map((name) => name.trim()));
  const picked = new Set([...selected].map((name) => name.trim()));
  let added = 0;
  let removed = 0;
  for (const { name } of discoveryCatalog(catalog)) {
    if (picked.has(name) && !existing.has(name)) added++;
    if (!picked.has(name) && existing.has(name)) removed++;
  }
  return { added, removed };
}

/** Retain every alias and its metadata; discovery may remove only names in its catalog. */
export function syncDiscoveredModels(
  current: ModelEntryInput[],
  picked: ModelInfo[],
  catalog: ModelInfo[]
): ModelEntryInput[] {
  const available = new Set(discoveryCatalog(catalog).map(({ name }) => name));
  const selected = discoveryCatalog(picked).filter(({ name }) => available.has(name));
  const selectedNames = new Set(selected.map(({ name }) => name));
  const next = current.filter(
    ({ name }) => !available.has(name.trim()) || selectedNames.has(name.trim())
  );
  const retained = new Set(next.map(({ name }) => name.trim()));
  const additions = selected.filter(({ name }) => !retained.has(name));
  if (additions.length) {
    // Replace only plain editor placeholders, never a row carrying metadata.
    for (let index = next.length - 1; index >= 0; index--) {
      const row = next[index];
      if (
        !row.name.trim() &&
        !row.alias?.trim() &&
        Object.keys(row).every((key) => key === 'name' || key === 'alias')
      )
        next.splice(index, 1);
    }
    next.push(
      ...additions.map((model) => ({ name: model.name, alias: model.alias?.trim() ?? '' }))
    );
  }
  return next.length ? next : [{ name: '', alias: '' }];
}
