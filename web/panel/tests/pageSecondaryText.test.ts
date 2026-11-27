// Secondary text on the dashboard and system pages: definitions live in InfoTips after their
// labels, restating subtitles and descriptions are gone, and destructive-action notes stay
// visible. Static markup only; hover, focus and tap behaviour and the visual layout need a
// browser.

import { afterAll, describe, expect, test } from 'bun:test';
import { readFileSync } from 'node:fs';
import { createElement } from 'react';
import { renderToStaticMarkup } from 'react-dom/server';
import { MemoryRouter } from 'react-router-dom';
import i18n from '../src/i18n/index';
import { DashboardPage } from '../src/features/dashboard/DashboardPage';
import { SystemPage } from '../src/pages/SystemPage';

// Injected by Vite at build time; the system page reads it during render.
(globalThis as { __APP_VERSION__?: string }).__APP_VERSION__ = '0.0.0-test';

const initialLanguage = i18n.language;
afterAll(async () => {
  await i18n.changeLanguage(initialLanguage);
});

const renderPage = (component: () => ReturnType<typeof createElement> | null) =>
  renderToStaticMarkup(createElement(MemoryRouter, null, createElement(component)));
const infoTipNames = (html: string) =>
  (html.match(/<button\b[^>]*data-info-tip=""[^>]*>/g) ?? []).map(
    (tag) => tag.match(/aria-label="([^"]*)"/)?.[1]
  );
const read = (path: string) => readFileSync(path, 'utf8');

const stripSvg = (html: string) => html.replace(/<svg\b[\s\S]*?<\/svg>/g, '<svg/>');
/** Markup of the first element opened by `marker` up to the end of its section. */
const sectionFrom = (html: string, marker: string) => {
  const rest = html.slice(html.indexOf(marker));
  return rest.slice(0, rest.indexOf('</section>'));
};

describe('dashboard', () => {
  test('stat tiles are a label and a number; definitions sit in InfoTips', async () => {
    await i18n.changeLanguage('en');
    const html = renderPage(DashboardPage);
    expect(infoTipNames(html)).toEqual([
      `More information about ${i18n.t('dashboard.stat_provider_keys')}`,
      `More information about ${i18n.t('dashboard.stat_models')}`,
    ]);
    expect(html).toContain(`hidden="">${i18n.t('dashboard.stat_provider_keys_hint')}</span>`);
    expect(html).toContain(`hidden="">${i18n.t('dashboard.stat_models_hint')}</span>`);

    // Disconnected (no meters): every tile ends at its number, with no caption line under it.
    const stats = stripSvg(sectionFrom(html, `aria-label="${i18n.t('dashboard.stats_aria')}"`));
    const tiles = stats.match(/<article\b[\s\S]*?<\/article>/g) ?? [];
    expect(tiles).toHaveLength(4);
    for (const tile of tiles) expect(tile).toMatch(/<\/strong><\/article>$/);
  });

  test('section headers are eyebrow + title, call-to-action cards are icon + title', async () => {
    await i18n.changeLanguage('en');
    const html = stripSvg(renderPage(DashboardPage));
    for (const title of ['traffic_title', 'fleet_title', 'cta_title']) {
      expect(html).toMatch(
        new RegExp(
          `<header[^>]*><span>[^<]*</span><h2>${i18n.t(`dashboard.${title}`)}</h2></header>`
        )
      );
    }
    const ctas = sectionFrom(html, i18n.t('dashboard.cta_title')).match(/<a\b[\s\S]*?<\/a>/g) ?? [];
    expect(ctas).toHaveLength(6);
    for (const card of ctas) {
      expect(card).toMatch(
        /^<a[^>]*><span><svg\/><\/span><span>[^<]+<\/span><span aria-hidden="true">→<\/span><\/a>$/
      );
    }
    expect(ctas[3]).toContain(`>${i18n.t('nav.quota_management')}</span>`);
    expect(read('src/features/dashboard/DashboardPage.tsx')).not.toContain('description:');
  });
});

describe('system page', () => {
  test('the models card explains its two buttons in an InfoTip and a tooltip', async () => {
    await i18n.changeLanguage('en');
    const html = renderPage(SystemPage);
    expect(infoTipNames(html)).toEqual([
      `More information about ${i18n.t('system_info.models_title')}`,
    ]);
    expect(html).toContain(`hidden="">${i18n.t('system_info.models_desc')}</span>`);
    // No native titles on the two model buttons: the catalog hint moved into a Tooltip.
    const actions = html.slice(
      html.indexOf(i18n.t('system_info.models_title')),
      html.indexOf(i18n.t('system_info.models_catalog_refresh_button'))
    );
    expect(actions).not.toMatch(/<button[^>]*title=/);
    expect(html).not.toContain(`title="${i18n.t('system_info.models_catalog_refresh_hint')}"`);
    expect(read('src/pages/SystemPage.tsx')).toMatch(
      /<Tooltip content=\{t\('system_info\.models_catalog_refresh_hint'\)\}>\s*<Button/
    );
  });

  test('quick links are titles only; the destructive clear-login note stays visible', async () => {
    await i18n.changeLanguage('en');
    const html = stripSvg(renderPage(SystemPage));
    const quickLinks = html.slice(html.indexOf(i18n.t('system_info.quick_links_title')));
    const cards = quickLinks.match(/<a href="https:[\s\S]*?<\/a>/g) ?? [];
    expect(cards).toHaveLength(3);
    for (const card of cards) {
      // Icon, then a content block holding only the title line.
      expect(card).toMatch(/<div[^>]*><svg\/><\/div><div><div>[^<]+<svg\/><\/div><\/div><\/a>$/);
    }
    // The title row directly precedes the links: no description paragraph between them.
    expect(quickLinks).toMatch(/^[^<]*<\/div><\/div><div><a href=/);
    expect(html).toMatch(new RegExp(`<p[^>]*>${i18n.t('system_info.clear_login_desc')}</p>`));
  });
});
