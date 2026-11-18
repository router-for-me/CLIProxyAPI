import { describe, expect, test } from 'bun:test';
import { createElement } from 'react';
import { renderToStaticMarkup } from 'react-dom/server';
import i18n from '@/i18n';
import { SponsorCampaignCard } from '@/features/providers/components/SponsorCampaignCard';
import {
  APIKEY_FUN_DOUBLE11_ENDS_AT,
  APIKEY_FUN_DOUBLE11_URL,
  isApiKeyFunDouble11Active,
} from '@/features/providers/sponsor';
import { LANGUAGE_ORDER } from '@/utils/constants';

const DURING = Date.parse('2026-11-01T12:00:00+08:00');
const KEYS = ['badge', 'title', 'description', 'bonus', 'cta', 'ctaAria'] as const;

describe('APIKEY.FUN Double 11 campaign card', () => {
  test('campaign stays live through Nov 11 (Beijing time) and ends at midnight', () => {
    expect(isApiKeyFunDouble11Active(DURING)).toBe(true);
    expect(isApiKeyFunDouble11Active(Date.parse('2026-11-11T23:59:59+08:00'))).toBe(true);
    expect(isApiKeyFunDouble11Active(APIKEY_FUN_DOUBLE11_ENDS_AT)).toBe(false);
  });

  test('renders the bonus figure and an affiliate CTA opening in a new tab', () => {
    const markup = renderToStaticMarkup(createElement(SponsorCampaignCard, { now: DURING }));

    expect(APIKEY_FUN_DOUBLE11_URL).toBe('https://apikey.fan/activities/double11-2026?aff=AKCPA');
    expect(markup).toContain(`href="${APIKEY_FUN_DOUBLE11_URL}"`);
    expect(markup).toContain('target="_blank"');
    expect(markup).toContain('rel="noopener noreferrer"');
    expect(markup).toContain('11.11');
    expect(markup).toContain('APIKEY.FUN');
    expect(markup).not.toContain('{{');
    expect(markup).toMatch(/<section[^>]*aria-labelledby="[^"]+"/);
  });

  test('renders nothing once the campaign has ended', () => {
    const markup = renderToStaticMarkup(
      createElement(SponsorCampaignCard, { now: APIKEY_FUN_DOUBLE11_ENDS_AT + 1 })
    );
    expect(markup).toBe('');
  });

  test('copy is translated in every locale and the CTA name contains its visible label', async () => {
    const original = i18n.language;
    try {
      for (const locale of LANGUAGE_ORDER) {
        await i18n.changeLanguage(locale);
        for (const key of KEYS) {
          expect(i18n.exists(`providersPage.sponsor.double11.${key}`, { fallbackLng: [] })).toBe(
            true
          );
        }
        const cta = i18n.t('providersPage.sponsor.double11.cta');
        const aria = i18n.t('providersPage.sponsor.double11.ctaAria', { provider: 'APIKEY.FUN' });
        expect(aria.startsWith(cta)).toBe(true);
      }
    } finally {
      await i18n.changeLanguage(original);
    }
  });
});
