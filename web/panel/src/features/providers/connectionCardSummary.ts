import { getProviderUsageKey, type ProviderRecentUsageMap } from '@/components/providers/utils';
import { isNativeKeyDisabled } from '@/services/api/nativeDisabled';
import { normalizeNativeProviderGroup } from '@/services/api/nativeProviderGroups';
import { isRecord } from '@/utils/helpers';
import {
  buildRecentRequestCompositeKey,
  mergeRecentRequestBucketGroups,
  statusBarDataFromRecentRequests,
  type RecentRequestBucket,
} from '@/utils/recentRequests';
import type { ResourceUsage } from './resourceView';
import type { NativeSection } from './types';

export function connectionCardSummary(
  section: NativeSection,
  usage: ProviderRecentUsageMap
): {
  modelCount: number;
  headerCount: number;
  priority: number;
  activeKeyCount: number;
  usage: ResourceUsage;
} {
  const raw = section.source.group;
  const group = normalizeNativeProviderGroup(raw);
  const rawKeys = Array.isArray(raw.keys) ? raw.keys.filter(isRecord) : [];
  const activeKeyCount = rawKeys.filter((key) => !isNativeKeyDisabled(raw, key)).length;
  // A native section belongs to one provider. Within that namespace, usage is keyed
  // by effective endpoint and credential, not by the member's position or auth index.
  const first = section.members[0];
  const entries = first && usage.get(getProviderUsageKey(first.brand));
  const seen = new Set<string>();
  const buckets: RecentRequestBucket[][] = [];
  let success = 0;
  let failure = 0;
  if (entries) {
    for (const member of section.members) {
      if (!member.apiKey?.trim()) continue;
      const identity = buildRecentRequestCompositeKey(member.baseUrl, member.apiKey);
      if (seen.has(identity)) continue;
      seen.add(identity);
      const entry = entries.get(identity);
      if (!entry) continue;
      success += entry.success;
      failure += entry.failed;
      buckets.push(entry.recentRequests);
    }
  }

  return {
    modelCount: group.models?.length ?? 0,
    headerCount: Object.keys(group.headers ?? {}).length,
    priority: group.priority ?? 0,
    activeKeyCount,
    usage: {
      success,
      failure,
      statusData: statusBarDataFromRecentRequests(mergeRecentRequestBucketGroups(buckets)),
    },
  };
}
