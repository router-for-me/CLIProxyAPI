import { expect, test } from 'bun:test';
import { interactionsToResource } from '@/features/providers/adapters';
import { connectionCardSummary } from '@/features/providers/connectionCardSummary';
import type { NativeSection } from '@/features/providers/types';

const group = {
  headers: { Authorization: 'fixture-header-secret' },
  priority: -3,
  models: [{ name: 'fixture-model' }, { name: 'another-model' }],
};
const section: NativeSection = {
  source: { group, groups: [group], groupIndex: 0 },
  name: 'fixture-name',
  keyCount: 0,
  members: [],
};

test('empty connections summarize group settings and have an idle status bar', () => {
  const summary = connectionCardSummary(section, new Map());
  expect(summary).toMatchObject({
    modelCount: 2,
    headerCount: 1,
    priority: -3,
    usage: { success: 0, failure: 0, statusData: { blocks: Array(20).fill('idle') } },
  });
  expect(JSON.stringify(summary)).not.toContain('fixture-');
});

test('combines lifetime totals and right-aligned recent buckets once per usage identity', () => {
  const apiKey = 'fixture-credential';
  const members = ['fixture-a', 'fixture-a', 'fixture-b', 'fixture-unused'].map((baseUrl, i) =>
    interactionsToResource({ apiKey, baseUrl }, i)
  );
  const entries = new Map([
    [
      `fixture-a|${apiKey}`,
      { success: 100, failed: 9, recentRequests: [{ success: 3, failed: 0 }] },
    ],
    [
      `fixture-b|${apiKey}`,
      {
        success: 4,
        failed: 20,
        recentRequests: [
          { success: 0, failed: 2 },
          { success: 0, failed: 1 },
        ],
      },
    ],
  ]);
  const summary = connectionCardSummary(
    { ...section, members, keyCount: members.length },
    new Map([['gemini-interactions', entries]])
  );
  expect(summary).toMatchObject({
    modelCount: 2,
    headerCount: 1,
    priority: -3,
    usage: {
      success: 104,
      failure: 29,
      statusData: { totalSuccess: 3, totalFailure: 3, successRate: 50 },
    },
  });
  expect(summary.usage.statusData.blocks.slice(-2)).toEqual(['failure', 'mixed']);
  expect(JSON.stringify(summary)).not.toContain('fixture-');
});
