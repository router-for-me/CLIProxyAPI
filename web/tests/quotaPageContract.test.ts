/**
 * Source-level contracts of the pooled Quota page: the section order, the single
 * registrations, masking of the accounts grid, and the removed ledger view.
 */

import { describe, expect, test } from 'bun:test';
import { readFileSync } from 'node:fs';

const page = readFileSync('src/features/quota/QuotaPage.tsx', 'utf8');
const constants = readFileSync('src/features/quota/constants.ts', 'utf8');
const resetGrants = readFileSync(
  'src/features/quota/providers/claude/ClaudeResetGrants.tsx',
  'utf8'
);
const actions = readFileSync('src/features/quota/hooks/useQuotaActions.ts', 'utf8');

const countOf = (source: string, needle: string) => source.split(needle).length - 1;

describe('quota page contract', () => {
  test('renders header, tabs, pool controls, pool cards, then the accounts toolbar', () => {
    const markers = [
      '<QuotaHeader',
      '<ProviderTabs',
      '<PoolControls',
      '<PoolCard',
      '<div className={styles.toolbar}>',
    ];
    const positions = markers.map((marker) => page.indexOf(marker));

    for (const position of positions) {
      expect(position).toBeGreaterThan(-1);
    }

    expect([...positions].sort((left, right) => left - right)).toEqual(positions);
  });

  test('registers the header refresh and the history hook exactly once', () => {
    expect(countOf(page, 'useHeaderRefresh(')).toBe(1);
    expect(countOf(page, 'usePoolHistory(')).toBe(1);
  });

  test('masks the accounts grid unless emails are shown', () => {
    expect(countOf(page, 'maskName={!showEmails}')).toBe(1);
  });

  test('no longer references the ledger, summary cards, timeline or view modes', () => {
    for (const removed of [
      'QuotaLedger',
      'ProviderSummaryCards',
      'QuotaTimeline',
      'QUOTA_VIEW_MODES',
    ]) {
      expect(page).not.toContain(removed);
    }

    expect(constants).not.toContain('QUOTA_VIEW_MODES');
  });

  test('uses the display label in confirmations and notifications', () => {
    expect(resetGrants).toContain('name: label ?? file.name');
    expect(actions).not.toContain('{ name: file.name');
  });
});
