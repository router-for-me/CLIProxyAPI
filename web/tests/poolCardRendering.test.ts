import { beforeAll, describe, expect, test } from 'bun:test';
import { createElement } from 'react';
import { renderToStaticMarkup } from 'react-dom/server';
import i18n from '@/i18n';
import { PoolCard } from '@/features/quota/components/PoolCard';
import type { PoolProvider } from '@/features/quota/pool/constants';
import { buildPoolCardModel } from '@/features/quota/pool/model';
import type { PoolCardModel, PoolModelInput } from '@/features/quota/pool/types';
import { MINUTE_MS } from '@/utils/time/durations';
import { NOW, TZ, claudeFiles, poolModelInput } from './fixtures/quotaPoolFixture';

const buildModel = (provider: PoolProvider, overrides: Partial<PoolModelInput> = {}) => {
  const model = buildPoolCardModel(poolModelInput(provider, overrides));

  if (!model) throw new Error(`no ${provider} pool model`);

  return model;
};

const renderCard = (model: PoolCardModel, showEmails = false) =>
  renderToStaticMarkup(
    createElement(PoolCard, {
      chartWidth: 1100,
      locale: 'en',
      metric: 'remaining',
      model,
      resolvedTheme: 'dark',
      showEmails,
      timeZone: TZ,
    })
  );

beforeAll(async () => {
  await i18n.changeLanguage('en');
});

describe('PoolCard', () => {
  test('renders the Claude headline and stats', () => {
    const markup = renderCard(buildModel('claude'));

    for (const text of [
      '555%',
      'of 600%',
      '6 accounts',
      'Pool empty',
      'Not by Nov 9',
      'First empty',
      'in 15h 49m',
      'Next reset',
      '+100%',
      'Tue 10:00 AM',
      'Burn, last 3h',
      '3.7%/h',
    ]) {
      expect(markup).toContain(text);
    }
  });

  test('masks the first-empty account unless emails are shown', () => {
    const masked = renderCard(buildModel('claude'));
    const shown = renderCard(buildModel('claude'), true);

    expect(masked).not.toContain('user-a@example.com');
    expect(masked).not.toContain('claude-a@example.com');
    expect(masked).not.toContain('@example.com');
    expect(masked).toContain('Account 1');
    expect(shown).toContain('user-a@example.com');
    expect(shown).not.toContain('Account 1');
  });

  test('renders the Codex headline with every account at 100%', () => {
    const markup = renderCard(buildModel('codex'));

    for (const text of ['270%', 'of 300%', 'Sun 4:39 PM', '+300%', '3 accounts', 'in 2d 10h']) {
      expect(markup).toContain(text);
    }
    expect(markup).not.toContain('2.5x');
  });

  test('shows unknown stats and a lower-bound reset without history', () => {
    const markup = renderCard(buildModel('claude', { history: null }));

    expect(markup).toContain('title="Needs recorded history"');
    expect(markup).toContain('≥ +42%');
    expect(markup).not.toContain('%/h<');
  });

  test('names the same minute in Pool empty and the First empty countdown', () => {
    for (const seconds of [0, 25, 40]) {
      const model = buildModel('claude', {
        files: claudeFiles().slice(0, 1),
        now: NOW + seconds * 1000,
      });
      const { firstEmpty, poolEmpty } = model.stats;
      const markup = renderCard(model);

      expect(poolEmpty.kind === 'at' && poolEmpty.at).toBe(NOW + (15 * 60 + 49) * MINUTE_MS);
      expect(firstEmpty.kind === 'at' && firstEmpty.at).toBe(
        poolEmpty.kind === 'at' && poolEmpty.at
      );
      expect(model.remaining.emptyAt).toBe(poolEmpty.kind === 'at' ? poolEmpty.at : null);
      expect(markup).toContain('<dd>Mon 7:41 PM</dd>');
      expect(markup).toContain('Empty Mon 7:41 PM');
      expect(markup).toContain('in 15h 49m');
    }
  });

  test('counts files without data', () => {
    const input = poolModelInput('claude');
    const files = [...input.files, { id: 'claude-g', name: 'claude-g.json', type: 'claude' }];
    const markup = renderCard(buildModel('claude', { files }));

    expect(markup).toContain('6 accounts · 1 without data');
  });
});
