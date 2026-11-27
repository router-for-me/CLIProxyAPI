import type { Config, ProviderKeyConfig } from '@/types';
import { isRecord } from '@/utils/helpers';
import { normalizeNativeProviderGroup, type NativeProviderFamily } from '@/services/api/providers';
import type { ProviderRecentUsageMap } from '@/components/providers/utils';
import {
  getResourceRecentSuccess,
  matchesResourceFilter,
  type ResourceListQuery,
} from './resourceView';
import type { NativeSection, ProviderBrand, ProviderResource } from './types';

const nativeFamilies: ReadonlySet<ProviderBrand> = new Set([
  'gemini',
  'interactions',
  'codex',
  'meta',
  'xai',
  'claude',
  'vertex',
]);
export const isNativeFamily = (brand: ProviderBrand): brand is NativeProviderFamily =>
  nativeFamilies.has(brand);

export function buildNativeSections(
  config: Config,
  family: NativeProviderFamily,
  resources: ProviderResource[]
): NativeSection[] {
  const groups = config.providerGroups?.[family];
  if (!Array.isArray(groups)) return [];
  const byIndex = new Map<number, ProviderResource[]>();
  for (const resource of resources) {
    const index = (resource.raw as ProviderKeyConfig).source?.groupIndex;
    if (index === undefined) continue;
    const bucket = byIndex.get(index);
    if (bucket) bucket.push(resource);
    else byIndex.set(index, [resource]);
  }
  return groups.flatMap((group, groupIndex) => {
    if (!isRecord(group)) return [];
    const members = byIndex.get(groupIndex) ?? [];
    const keyCount = Array.isArray(group.keys) ? group.keys.length : 0;
    // Partner credentials already have a dedicated bundle tab. Keep genuinely empty groups.
    if (!members.length && keyCount) return [];
    const { name, baseUrl, prefix } = normalizeNativeProviderGroup(group);
    return [
      {
        source: { groupIndex, group, groups },
        name: name || `#${groupIndex + 1}`,
        baseUrl,
        prefix,
        keyCount,
        members,
      },
    ];
  });
}

export function selectVisibleSections(
  sections: NativeSection[],
  query: ResourceListQuery,
  usage: ProviderRecentUsageMap
): NativeSection[] {
  const needle = query.filter.trim().toLowerCase();
  const visible = sections.filter((section) => {
    const groupMatch = [section.name, section.baseUrl, section.prefix].some((value) =>
      value?.toLowerCase().includes(needle)
    );
    if (groupMatch && !query.selectedModels.size) return true;
    return section.members.some(
      (member) =>
        (groupMatch || matchesResourceFilter(member, needle)) &&
        (!query.selectedModels.size ||
          member.models.some((model) => query.selectedModels.has(model)))
    );
  });
  // A member match selects the connection; it must not hide siblings from its editor.
  const score = (section: NativeSection) =>
    query.sortBy === 'priority'
      ? Number(section.source.group.priority) || 0
      : section.members.reduce((sum, member) => sum + getResourceRecentSuccess(member, usage), 0);
  return visible
    .map((section) => ({ section, score: query.sortBy === 'name' ? 0 : score(section) }))
    .sort((a, b) => {
      const difference =
        query.sortBy === 'name' ? a.section.name.localeCompare(b.section.name) : a.score - b.score;
      const order = difference || a.section.source.groupIndex - b.section.source.groupIndex;
      return query.sortDir === 'asc' ? order : -order;
    })
    .map(({ section }) => section);
}
