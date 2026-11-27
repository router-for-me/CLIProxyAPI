import { describe, expect, test } from 'bun:test';
import { createElement } from 'react';
import { renderToStaticMarkup } from 'react-dom/server';
import '../src/i18n/index';
import i18n from '../src/i18n';
import { QuotaTimeline } from '../src/features/quota/components/QuotaTimeline';
import { buildTimelineLanes, laneHasWindow } from '../src/features/quota/quotaTimelineModel';
import type { AntigravityQuotaState } from '../src/types';

const now = new Date(2026, 6, 29, 12).getTime();
const quota: AntigravityQuotaState = {
  status: 'success',
  groups: ['Gemini Models', 'Claude and GPT Models', 'Argon'].map((label, index) => ({
    id: `pool-${index}`,
    label,
    buckets: [
      {
        id: 'session',
        label: '5 Hour Limit',
        remainingFraction: 0.9 - index * 0.2,
        resetAtMs: now + (index + 1) * 3600000,
        periodHours: 5,
      },
      {
        id: 'weekly',
        label: 'Weekly Limit',
        remainingFraction: 0.6 - index * 0.2,
        resetAtMs: now + (index + 1) * 86400000,
        periodHours: 168,
      },
    ],
  })),
};
const input = {
  name: 'account.json',
  displayName: 'Account',
  provider: 'antigravity' as const,
  quota,
};

describe('Antigravity grouped timeline', () => {
  test('selects independent windows and percentages for every pool in both modes', () => {
    for (const maxPeriodHours of [5, 336]) {
      const lanes = buildTimelineLanes({ ...input, maxPeriodHours });
      expect(lanes).toHaveLength(3);
      expect(new Set(lanes.map((lane) => lane.name)).size).toBe(3);
      lanes.forEach((lane, index) => {
        const group = quota.groups[index];
        const bucket = group.buckets[maxPeriodHours === 5 ? 0 : 1];
        expect(lane.displayName).toBe(`Account · ${group.label}`);
        expect(lane.groupName).toBe(group.label);
        expect(lane.accountName).toBe('Account');
        expect(lane.anchorMs).toBe(bucket.resetAtMs!);
        expect(lane.periodHours).toBe(bucket.periodHours!);
        expect(lane.remaining).toBe(Math.round(bucket.remainingFraction * 100));
        expect(lane.limits).toHaveLength(2);
      });
    }
  });

  test('supports arbitrary group counts and stable keys when groups reorder', () => {
    const lanes = buildTimelineLanes(input);
    const reversed = buildTimelineLanes({
      ...input,
      quota: { ...quota, groups: [...quota.groups].reverse() } as AntigravityQuotaState,
    });
    expect(reversed.map((lane) => lane.name)).toEqual(lanes.map((lane) => lane.name).reverse());
    expect(
      buildTimelineLanes({ ...input, quota: { ...quota, groups: [] } as AntigravityQuotaState })
    ).toEqual([]);
    expect(buildTimelineLanes({ ...input, quota: undefined }).some(laneHasWindow)).toBe(false);
  });

  test('missing resets in one group do not hide the other groups', () => {
    const lanes = buildTimelineLanes({
      ...input,
      quota: {
        ...quota,
        groups: [{ id: 'empty', label: 'Empty', buckets: [] }, ...quota.groups],
      } as AntigravityQuotaState,
    }).filter(laneHasWindow);
    expect(lanes).toHaveLength(3);
  });

  test('other providers retain one lane per account', () => {
    expect(buildTimelineLanes({ ...input, provider: 'plugin' })).toHaveLength(1);
    expect(buildTimelineLanes({ ...input, provider: 'codex' })).toHaveLength(1);
  });

  test('renders each pool label separately from long account names in both views', () => {
    const accountName = 'a-very-long-account-name-that-would-hide-the-quota-group@example.com';
    for (const initialMode of ['weekly', 'session'] as const) {
      const markup = renderToStaticMarkup(
        createElement(QuotaTimeline, {
          entries: [{ type: 'antigravity', file: { name: 'account.json', type: 'antigravity' } }],
          quotaFor: () => quota,
          displayNameFor: () => accountName,
          resolvedTheme: 'light',
          now,
          initialMode,
        })
      );
      for (const label of [
        i18n.t('antigravity_quota.group_gemini_models'),
        i18n.t('antigravity_quota.group_claude_gpt_models'),
        'Argon',
      ]) {
        expect(markup).toContain(`title="${label}">${label}</span>`);
        // Credential is the primary line; the group follows on its own line.
        expect(markup).toContain(
          `title="${accountName}">${accountName}</span><span>${initialMode === 'session' ? '5h' : '7d'}</span></div><span title="${label}">${label}</span>`
        );
      }
      expect(
        markup.match(new RegExp(`title="${accountName}">${accountName}</span>`, 'g'))
      ).toHaveLength(3);
      expect(markup).not.toContain('role="status"');
    }
  });
});
