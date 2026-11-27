import { describe, expect, test } from 'bun:test';
import {
  resolveAntigravityBucketLabelKey,
  resolveAntigravityGroupLabelKey,
  resolveAntigravityGroupModels,
} from '@/features/quota/providers/antigravity/labels';
import { buildAntigravityQuotaGroups } from '@/utils/quota/builders';

describe('Antigravity quota label keys', () => {
  test('matches group names case-insensitively', () => {
    expect(resolveAntigravityGroupLabelKey('Gemini Models')).toBe('group_gemini_models');
    expect(resolveAntigravityGroupLabelKey(' Claude and  GPT models ')).toBe(
      'group_claude_gpt_models'
    );
    expect(resolveAntigravityGroupLabelKey('Image Models')).toBeNull();
  });

  test('appends Gemini Argon to the Gemini group model list', () => {
    expect(
      resolveAntigravityGroupModels(
        'Gemini Models',
        'Models within this group: Gemini Flash, Gemini Pro'
      )
    ).toEqual(['Gemini Flash', 'Gemini Pro', 'Gemini Argon']);
    expect(
      resolveAntigravityGroupModels(
        'Gemini Models',
        'Models within this group: Gemini Flash, gemini argon'
      )
    ).toEqual(['Gemini Flash', 'gemini argon']);
  });

  test('leaves other groups and non-list descriptions untouched', () => {
    expect(
      resolveAntigravityGroupModels(
        'Claude and GPT models',
        'Models within this group: Claude Opus, Claude Sonnet, GPT-OSS'
      )
    ).toEqual(['Claude Opus', 'Claude Sonnet', 'GPT-OSS']);
    expect(resolveAntigravityGroupModels('Gemini Models', 'Shared Gemini quota')).toBeNull();
    expect(resolveAntigravityGroupModels('Gemini Models', undefined)).toBeNull();
  });

  test('ignores the upstream "Remaining" suffix on bucket names', () => {
    expect(resolveAntigravityBucketLabelKey('Five Hour Limit Remaining', '5h')).toBe(
      'five_hour_limit'
    );
    expect(resolveAntigravityBucketLabelKey('Weekly Limit Remaining', 'weekly')).toBe(
      'weekly_limit'
    );
    expect(resolveAntigravityBucketLabelKey('5-hour limit', undefined)).toBe('five_hour_limit');
  });

  test('falls back to the structured window for unknown bucket names', () => {
    expect(resolveAntigravityBucketLabelKey('Rolling quota', '5h')).toBe('five_hour_limit');
    expect(resolveAntigravityBucketLabelKey('Rolling quota', 'Weekly')).toBe('weekly_limit');
    expect(resolveAntigravityBucketLabelKey('Rolling quota', 'hourly')).toBeNull();
    expect(resolveAntigravityBucketLabelKey('Rolling quota', undefined)).toBeNull();
  });

  test('resolves every group and bucket from the current upstream payload', () => {
    const bucket = (id: string, name: string, window: string) => ({
      bucketId: id,
      displayName: name,
      window,
      resetTime: '2026-10-23T02:43:12Z',
      remainingFraction: 1,
    });
    const groups = buildAntigravityQuotaGroups({
      groups: [
        {
          displayName: 'Gemini Models',
          buckets: [
            bucket('gemini-weekly', 'Weekly Limit Remaining', 'weekly'),
            bucket('gemini-5h', 'Five Hour Limit Remaining', '5h'),
          ],
        },
        {
          displayName: 'Claude and GPT models',
          buckets: [
            bucket('3p-weekly', 'Weekly Limit Remaining', 'weekly'),
            bucket('3p-5h', 'Five Hour Limit Remaining', '5h'),
          ],
        },
      ],
    });

    expect(groups.map((group) => resolveAntigravityGroupLabelKey(group.label))).toEqual([
      'group_gemini_models',
      'group_claude_gpt_models',
    ]);
    expect(
      groups.flatMap((group) =>
        group.buckets.map((b) => resolveAntigravityBucketLabelKey(b.label, b.window))
      )
    ).toEqual(['five_hour_limit', 'weekly_limit', 'five_hour_limit', 'weekly_limit']);
  });
});
