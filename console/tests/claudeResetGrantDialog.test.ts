import { beforeAll, expect, test } from 'bun:test';
import { createElement } from 'react';
import { renderToStaticMarkup } from 'react-dom/server';
import i18n from '@/i18n';
import { ClaudeResetGrantChoices } from '@/features/quota/providers/claude/ClaudeResetGrantDialog';
import type { AnthropicResetGrant } from '@/services/api/claudeResetGrants';

const grant: AnthropicResetGrant = {
  id: 'weekly',
  label: 'Weekly coupon',
  resetsLeft: 2,
  resetsTotal: 3,
  startsAt: null,
  endsAt: null,
  clears: ['seven_day'],
  paused: false,
  usableNow: true,
  useRequiresLimit: false,
  percentUsed: { seven_day: 50 },
  resetType: 'weekly',
};
const grants = [
  grant,
  {
    ...grant,
    id: 'fable',
    label: 'Fable coupon',
    givesBackWeekly: true,
    percentUsed: { model: 0 },
  },
];
const render = (selected: string | null = null, canRedeemGrant = (id: string) => id === 'weekly') =>
  renderToStaticMarkup(
    createElement(ClaudeResetGrantChoices, {
      grants,
      selected,
      canRedeemGrant,
      onSelect: () => {},
    })
  );

beforeAll(async () => {
  await i18n.changeLanguage('en');
});

test('radio cards begin unselected and show kind, count, expiry and Fable semantics', () => {
  const markup = render();
  expect(markup.match(/type="radio"/g)).toHaveLength(2);
  expect(markup).not.toContain('checked=""');
  expect(markup).not.toContain('<button');
  expect(markup).toContain('<legend>Choose a reset grant</legend>');
  expect(markup).toContain('Weekly reset');
  expect(markup).toContain('2 / 3 resets remaining');
  expect(markup).toContain('Expiry unknown');
  expect(markup).toContain('No Fable usage to reset.');
  expect(markup).toContain('Other weekly usage stays unchanged.');
  // Only the Fable grant spends differently from its label; ordinary grants carry no note.
  expect(markup.match(/Other weekly usage stays unchanged\./g)).toHaveLength(1);
  expect(markup).not.toContain('Uses exactly this reset grant.');
  expect(markup.match(/disabled=""/g)).toHaveLength(1);
});

test('explicit choice checks only one radio and pending retry can select only original grant', () => {
  const markup = render('fable', (id) => id === 'fable');
  expect(markup.match(/checked=""/g)).toHaveLength(1);
  expect(markup).toMatch(/checked=""[^>]*value="fable"/);
  expect(markup.match(/disabled=""/g)).toHaveLength(1);
});

test.each(['en', 'zh-CN', 'zh-TW', 'ru', 'vi', 'ko', 'tr'])(
  'dialog strings exist in %s',
  async (lang) => {
    const locale = await Bun.file(`src/i18n/locales/${lang}.json`).json();
    for (const key of [
      'choose_grant',
      'no_grants',
      'grant_expiry',
      'grant_unavailable',
      'kind_weekly',
      'kind_session',
      'kind_full',
      'kind_fable',
      'kind_unknown',
      'selected_grant',
      'no_usage',
      'fable_hint',
    ]) {
      expect(typeof locale.claude_reset[key]).toBe('string');
      expect(locale.claude_reset[key].length).toBeGreaterThan(0);
    }
  }
);
