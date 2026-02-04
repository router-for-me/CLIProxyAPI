/**
 * Builder functions for constructing quota data structures.
 */

import type {
  AntigravityQuotaGroup,
  AntigravityQuotaGroupDefinition,
  AntigravityQuotaInfo,
  AntigravityModelsPayload,
  GeminiCliParsedBucket,
  GeminiCliQuotaBucketState,
} from '@/types';
import { ANTIGRAVITY_QUOTA_GROUPS, GEMINI_CLI_GROUP_LOOKUP } from './constants';
import { normalizeQuotaFraction } from './parsers';
import { isIgnoredGeminiCliModel } from './validators';

export function pickEarlierResetTime(current?: string, next?: string): string | undefined {
  if (!current) return next;
  if (!next) return current;
  const currentTime = new Date(current).getTime();
  const nextTime = new Date(next).getTime();
  if (Number.isNaN(currentTime)) return next;
  if (Number.isNaN(nextTime)) return current;
  return currentTime <= nextTime ? current : next;
}

export function minNullableNumber(current: number | null, next: number | null): number | null {
  if (current === null) return next;
  if (next === null) return current;
  return Math.min(current, next);
}

export function buildGeminiCliQuotaBuckets(
  buckets: GeminiCliParsedBucket[]
): GeminiCliQuotaBucketState[] {
  if (buckets.length === 0) return [];

  type GeminiCliQuotaBucketGroup = {
    id: string;
    label: string;
    tokenType: string | null;
    modelIds: string[];
    preferredModelId?: string;
    preferredBucket?: GeminiCliParsedBucket;
    fallbackRemainingFraction: number | null;
    fallbackRemainingAmount: number | null;
    fallbackResetTime: string | undefined;
  };

  const grouped = new Map<string, GeminiCliQuotaBucketGroup>();

  buckets.forEach((bucket) => {
    if (isIgnoredGeminiCliModel(bucket.modelId)) return;
    const group = GEMINI_CLI_GROUP_LOOKUP.get(bucket.modelId);
    const groupId = group?.id ?? bucket.modelId;
    const label = group?.label ?? bucket.modelId;
    const tokenKey = bucket.tokenType ?? '';
    const mapKey = `${groupId}::${tokenKey}`;
    const existing = grouped.get(mapKey);

    if (!existing) {
      const preferredModelId = group?.preferredModelId;
      const preferredBucket =
        preferredModelId && bucket.modelId === preferredModelId ? bucket : undefined;
      grouped.set(mapKey, {
        id: `${groupId}${tokenKey ? `-${tokenKey}` : ''}`,
        label,
        tokenType: bucket.tokenType,
        modelIds: [bucket.modelId],
        preferredModelId,
        preferredBucket,
        fallbackRemainingFraction: bucket.remainingFraction,
        fallbackRemainingAmount: bucket.remainingAmount,
        fallbackResetTime: bucket.resetTime,
      });
      return;
    }

    existing.fallbackRemainingFraction = minNullableNumber(
      existing.fallbackRemainingFraction,
      bucket.remainingFraction
    );
    existing.fallbackRemainingAmount = minNullableNumber(
      existing.fallbackRemainingAmount,
      bucket.remainingAmount
    );
    existing.fallbackResetTime = pickEarlierResetTime(existing.fallbackResetTime, bucket.resetTime);
    existing.modelIds.push(bucket.modelId);

    if (existing.preferredModelId && bucket.modelId === existing.preferredModelId) {
      existing.preferredBucket = bucket;
    }
  });

  return Array.from(grouped.values()).map((bucket) => {
    const uniqueModelIds = Array.from(new Set(bucket.modelIds));
    const preferred = bucket.preferredBucket;
    const remainingFraction = preferred
      ? preferred.remainingFraction
      : bucket.fallbackRemainingFraction;
    const remainingAmount = preferred ? preferred.remainingAmount : bucket.fallbackRemainingAmount;
    const resetTime = preferred ? preferred.resetTime : bucket.fallbackResetTime;
    return {
      id: bucket.id,
      label: bucket.label,
      remainingFraction,
      remainingAmount,
      resetTime,
      tokenType: bucket.tokenType,
      modelIds: uniqueModelIds,
    };
  });
}

export function getAntigravityQuotaInfo(entry?: AntigravityQuotaInfo): {
  remainingFraction: number | null;
  resetTime?: string;
  displayName?: string;
} {
  if (!entry) {
    return { remainingFraction: null };
  }
  const quotaInfo = entry.quotaInfo ?? entry.quota_info ?? {};
  const remainingValue =
    quotaInfo.remainingFraction ?? quotaInfo.remaining_fraction ?? quotaInfo.remaining;
  const remainingFraction = normalizeQuotaFraction(remainingValue);
  const resetValue = quotaInfo.resetTime ?? quotaInfo.reset_time;
  const resetTime = typeof resetValue === 'string' ? resetValue : undefined;
  const displayName = typeof entry.displayName === 'string' ? entry.displayName : undefined;

  return {
    remainingFraction,
    resetTime,
    displayName,
  };
}

export function findAntigravityModel(
  models: AntigravityModelsPayload,
  identifier: string
): { id: string; entry: AntigravityQuotaInfo } | null {
  const direct = models[identifier];
  if (direct) {
    return { id: identifier, entry: direct };
  }

  const match = Object.entries(models).find(([, entry]) => {
    const name = typeof entry?.displayName === 'string' ? entry.displayName : '';
    return name.toLowerCase() === identifier.toLowerCase();
  });
  if (match) {
    return { id: match[0], entry: match[1] };
  }

  return null;
}

export function buildAntigravityQuotaGroups(
  models: AntigravityModelsPayload
): AntigravityQuotaGroup[] {
  const groups: AntigravityQuotaGroup[] = [];
  let geminiProResetTime: string | undefined;
  const [claudeDef, geminiProDef, flashDef, flashLiteDef, cuDef, geminiFlashDef, imageDef] =
    ANTIGRAVITY_QUOTA_GROUPS;

  const buildGroup = (
    def: AntigravityQuotaGroupDefinition,
    overrideResetTime?: string
  ): AntigravityQuotaGroup | null => {
    const matches = def.identifiers
      .map((identifier) => findAntigravityModel(models, identifier))
      .filter((entry): entry is { id: string; entry: AntigravityQuotaInfo } => Boolean(entry));

    const quotaEntries = matches
      .map(({ id, entry }) => {
        const info = getAntigravityQuotaInfo(entry);
        const remainingFraction = info.remainingFraction ?? (info.resetTime ? 0 : null);
        if (remainingFraction === null) return null;
        return {
          id,
          remainingFraction,
          resetTime: info.resetTime,
          displayName: info.displayName,
        };
      })
      .filter((entry): entry is NonNullable<typeof entry> => entry !== null);

    if (quotaEntries.length === 0) return null;

    const remainingFraction = Math.min(...quotaEntries.map((entry) => entry.remainingFraction));
    const resetTime =
      overrideResetTime ?? quotaEntries.map((entry) => entry.resetTime).find(Boolean);
    const displayName = quotaEntries.map((entry) => entry.displayName).find(Boolean);
    const label = def.labelFromModel && displayName ? displayName : def.label;

    return {
      id: def.id,
      label,
      models: quotaEntries.map((entry) => entry.id),
      remainingFraction,
      resetTime,
    };
  };

  const claudeGroup = buildGroup(claudeDef);
  if (claudeGroup) {
    groups.push(claudeGroup);
  }

  const geminiProGroup = buildGroup(geminiProDef);
  if (geminiProGroup) {
    geminiProResetTime = geminiProGroup.resetTime;
    groups.push(geminiProGroup);
  }

  const flashGroup = buildGroup(flashDef);
  if (flashGroup) {
    groups.push(flashGroup);
  }

  const flashLiteGroup = buildGroup(flashLiteDef);
  if (flashLiteGroup) {
    groups.push(flashLiteGroup);
  }

  const cuGroup = buildGroup(cuDef);
  if (cuGroup) {
    groups.push(cuGroup);
  }

  const geminiFlashGroup = buildGroup(geminiFlashDef);
  if (geminiFlashGroup) {
    groups.push(geminiFlashGroup);
  }

  const imageGroup = buildGroup(imageDef, geminiProResetTime);
  if (imageGroup) {
    groups.push(imageGroup);
  }

  return groups;
}
