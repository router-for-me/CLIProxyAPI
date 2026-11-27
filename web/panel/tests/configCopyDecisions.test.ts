// 配置页文案取舍（安静界面）：哪些说明删掉、哪些收进 InfoTip、哪些必须常驻可见。
// 静态标记只能验证结构与关联，不能验证悬停/聚焦行为。

import { describe, expect, test } from 'bun:test';
import { createElement, type ComponentType } from 'react';
import { renderToStaticMarkup } from 'react-dom/server';
import { I18nextProvider } from 'react-i18next';
import i18n from '@/i18n';
import { DEFAULT_VISUAL_VALUES, type PluginStoreAuthRule } from '@/types/visualConfig';
import { PluginStoreAuthEditor } from '@/features/config/components/blocks/PluginStoreAuthEditor';
import { SectionAdvanced } from '@/features/config/components/sections/SectionAdvanced';
import { SectionCommon } from '@/features/config/components/sections/SectionCommon';
import { SectionConnectivity } from '@/features/config/components/sections/SectionConnectivity';
import { SectionLogging } from '@/features/config/components/sections/SectionLogging';
import { SectionNetwork } from '@/features/config/components/sections/SectionNetwork';
import { SectionPayload } from '@/features/config/components/sections/SectionPayload';
import { SectionStreaming } from '@/features/config/components/sections/SectionStreaming';
import { SPONSORS } from '@/features/config/sponsors';

const translations = i18n.cloneInstance({ lng: 'en' });
const render = (element: ReturnType<typeof createElement>) =>
  renderToStaticMarkup(createElement(I18nextProvider, { i18n: translations }, element));
const escapeText = (value: string) =>
  renderToStaticMarkup(createElement('span', null, value)).slice(6, -7);
const t = (key: string) => escapeText(translations.t(`config_management.visual.${key}`));
const props = {
  values: DEFAULT_VISUAL_VALUES,
  disabled: false,
  onChange: () => {},
};
const renderSection = (Section: ComponentType<Record<string, unknown>>) =>
  render(createElement(Section, props));
/** Markup of one setting row, from its anchor to the next anchor. */
const rowOf = (markup: string, fieldId: string) =>
  markup.split(`id="cfg-field-${fieldId}"`)[1].split('id="cfg-field-')[0];
/** The section/group header that contains `title`. */
const headerWith = (markup: string, title: string) =>
  (markup.match(/<header\b.*?<\/header>/g) ?? []).find((header) => header.includes(title)) ?? '';

describe('section headers', () => {
  const sections = {
    common: SectionCommon,
    connectivity: SectionConnectivity,
    network: SectionNetwork,
    logging: SectionLogging,
    streaming: SectionStreaming,
    advanced: SectionAdvanced,
    payload: SectionPayload,
  } as Record<string, ComponentType<Record<string, unknown>>>;

  for (const [id, Section] of Object.entries(sections)) {
    test(`${id}: the title stands alone, without a subtitle or InfoTip`, () => {
      const markup = renderSection(Section);
      const header = markup.match(/<header\b.*?<\/header>/)?.[0] ?? '';
      expect(header).toContain(t(`sections.${id}.title`));
      expect(header).not.toMatch(/<p\b/);
      expect(header).not.toContain('data-info-tip');
      expect(markup).not.toContain(t(`sections.${id}.description`));
    });
  }
});

describe('proxy URL sponsor', () => {
  test('stays a visible, keyboard-reachable link outside the input description', () => {
    const sponsor = SPONSORS[0];
    expect(sponsor).toBeDefined();
    for (const Section of [SectionCommon, SectionNetwork]) {
      const row = rowOf(
        renderSection(Section as ComponentType<Record<string, unknown>>),
        'proxyUrl'
      );
      const link = row.match(/<a\b[^>]*>/)?.[0] ?? '';
      expect(link).toContain(`href="${escapeText(sponsor.url)}"`);
      expect(link).toContain('rel="noopener noreferrer sponsored"');
      expect(row).toContain(t('sections.network.proxy_url_sponsor_hint'));
      // No tooltip holding a link, and nothing visually hidden around it.
      expect(row).not.toContain('data-info-tip');
      expect(row).not.toContain('visually-hidden');
      const input = row.match(/<input\b[^>]*>/)?.[0] ?? '';
      expect(input).not.toContain('aria-describedby');
    }
  });
});

describe('advanced section copy', () => {
  const markup = renderSection(SectionAdvanced);

  test('Devin warning about deleting whole prompt lines stays visible', () => {
    const row = rowOf(markup, 'devinSensitiveWords');
    expect(row).toContain(t('sections.system.devin_sensitive_words_hint'));
    expect(row).not.toContain('data-info-tip');
    expect(row).not.toContain('visually-hidden');
    // The group's scope explanation moved into the group title's InfoTip.
    const header = headerWith(markup, t('sections.system.devin_sensitive_words'));
    expect(header).toContain('data-info-tip');
    expect(header).toContain(t('sections.system.devin_sensitive_words_desc'));
  });

  test('plugin store auth: "env var names only" is visible under the group title', () => {
    const header = headerWith(markup, t('sections.system.plugin_store_auth'));
    expect(header).toMatch(/<p\b[^>]*>[^<]*<\/p>/);
    expect(header).toContain(t('sections.system.plugin_store_auth_hint'));
    expect(header).not.toContain('data-info-tip');
    expect(markup).not.toContain(t('sections.system.plugin_store_auth_desc'));
  });

  test('antigravity sensitive words: one InfoTip carries both what and how', () => {
    const header = headerWith(markup, t('sections.system.antigravity_sensitive_words'));
    expect(header).not.toContain('data-info-tip');
    const row = rowOf(markup, 'antigravitySensitiveWords');
    expect(row.match(/data-info-tip/g)).toHaveLength(1);
    expect(row).toContain(t('sections.system.antigravity_sensitive_words_desc'));
    expect(row).toContain(t('sections.system.antigravity_sensitive_words_hint'));
  });

  test('restating copy is gone: plugin source rows, header defaults summary', () => {
    expect(markup).not.toContain(t('sections.system.plugin_store_sources_hint'));
    expect(headerWith(markup, t('sections.system.plugin_store_sources'))).toContain(
      'data-info-tip'
    );
    expect(markup).not.toContain(t('sections.headers.description'));
  });
});

describe('payload rule groups', () => {
  test('collapsible summaries carry only their titles', () => {
    const markup = renderSection(SectionPayload as ComponentType<Record<string, unknown>>);
    const summaries = markup.match(/<summary\b.*?<\/summary>/g) ?? [];
    expect(summaries).toHaveLength(6);
    for (const key of [
      'default_rules',
      'default_raw_rules',
      'override_rules',
      'override_raw_rules',
      'filter_rules',
    ]) {
      expect(markup).toContain(t(`sections.payload.${key}`));
    }
  });
});

describe('plugin store auth rule editor', () => {
  test('"apply to" is a named checkbox group described by its InfoTip text', () => {
    const rule: PluginStoreAuthRule = {
      id: 'rule-1',
      match: '',
      applyTo: [],
      type: 'bearer',
      tokenEnv: '',
      usernameEnv: '',
      passwordEnv: '',
      headerName: '',
      headerValueEnv: '',
      allowInsecure: false,
    };
    const markup = render(
      createElement(PluginStoreAuthEditor, { value: [rule], onChange: () => {} })
    );
    expect(markup).not.toContain('<small');
    const group = markup.match(/<div\b[^>]*role="group"[^>]*>/)?.[0] ?? '';
    const labelId = group.match(/aria-labelledby="([^"]+)"/)?.[1];
    const hintId = group.match(/aria-describedby="([^"]+)"/)?.[1];
    expect(markup).toMatch(
      new RegExp(`<span id="${labelId}">${t('sections.system.store_auth_apply_to')}</span>`)
    );
    expect(markup).toContain(
      `<span id="${hintId}" hidden="">${t('sections.system.store_auth_apply_to_hint')}</span>`
    );
    const tip = markup.match(/<button\b[^>]*data-info-tip=""[^>]*>/)?.[0] ?? '';
    expect(tip).toContain(`aria-describedby="${hintId}"`);
    // Exactly one copy of the hint: the InfoTip reuses the group's hidden description.
    expect(markup.split(t('sections.system.store_auth_apply_to_hint'))).toHaveLength(2);
  });
});

describe('api keys block', () => {
  test('the header is just the title and the add action', () => {
    const markup = renderSection(SectionCommon);
    const cell = rowOf(markup, 'apiKeys');
    expect(cell).toContain(t('api_keys.label'));
    expect(cell).not.toContain(t('api_keys.hint'));
  });
});
