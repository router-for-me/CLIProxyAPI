// Plugin config sheet: plugin-declared field descriptions are explained the same way as the
// built-in config page, through an InfoTip after the field name, and every control stays
// described for assistive tech. Static markup only (closed tooltips); hover, focus and tap
// behaviour need a browser.

import { afterAll, describe, expect, test } from 'bun:test';
import { createElement } from 'react';
import { renderToStaticMarkup } from 'react-dom/server';
import i18n from '../src/i18n/index';
import { PluginConfigSheet } from '../src/features/plugins/components/PluginConfigSheet';
import { buildPluginConfigDraft } from '../src/features/plugins/pluginConfigDraft';
import type { PluginConfigField, PluginListEntry } from '../src/types';

const FIELDS: PluginConfigField[] = [
  { name: 'verbose', type: 'boolean', enumValues: [], description: 'Log every request body' },
  { name: 'mode', type: 'enum', enumValues: ['fast', 'safe'], description: 'Which pipeline runs' },
  { name: 'headers', type: 'object', enumValues: [], description: 'Extra upstream headers' },
  { name: 'endpoint', type: 'string', enumValues: [], description: 'Upstream base URL' },
  { name: 'quiet', type: 'boolean', enumValues: [], description: '' },
];

const PLUGIN: PluginListEntry = {
  id: 'example.plugin',
  path: '/plugins/example.so',
  configured: true,
  registered: true,
  enabled: true,
  effectiveEnabled: true,
  supportsOAuth: false,
  supportsQuota: false,
  logo: '',
  configFields: FIELDS,
  menus: [],
  metadata: null,
};

const initialLanguage = i18n.language;
afterAll(async () => {
  await i18n.changeLanguage(initialLanguage);
});

const renderSheet = () =>
  renderToStaticMarkup(
    createElement(PluginConfigSheet, {
      plugin: PLUGIN,
      draft: buildPluginConfigDraft(PLUGIN, {}),
      saving: false,
      onClose: () => {},
      onSave: () => {},
      onDraftChange: () => {},
    })
  );

const infoTips = (html: string) => html.match(/<button\b[^>]*data-info-tip=""[^>]*>/g) ?? [];
const attr = (tag: string, name: string) => tag.match(new RegExp(`\\b${name}="([^"]*)"`))?.[1];
/** Text of the element carrying `id`, whatever its tag. */
const textOf = (html: string, id: string) =>
  html.match(new RegExp(`id="${id}"[^>]*>([^<]*)<`))?.[1] ?? null;

describe('plugin config sheet field descriptions', () => {
  test('every described field gets one InfoTip named after the field', async () => {
    await i18n.changeLanguage('en');
    const html = renderSheet();
    const names = infoTips(html).map((tip) => attr(tip, 'aria-label'));
    expect(names).toEqual([
      'More information about Enabled',
      'More information about verbose',
      'More information about mode',
      'More information about headers',
      'More information about endpoint',
    ]);
  });

  test('descriptions are not printed as visible lines', async () => {
    await i18n.changeLanguage('en');
    const html = renderSheet();
    for (const text of [
      'Log every request body',
      'Which pipeline runs',
      'Extra upstream headers',
      i18n.t('plugin_management.enabled_hint'),
    ]) {
      // Only the hidden copy the control and the InfoTip point at.
      const occurrences = html.split(text).length - 1;
      expect(occurrences).toBe(1);
      expect(html).toMatch(new RegExp(`<span id="[^"]+-desc" hidden="">${text}</span>`));
    }
    // The string field goes through the shared Input: its hint is visually hidden.
    expect(html).toContain('class="hint visually-hidden-focusable">Upstream base URL</div>');
  });

  test('each control is described by its field description', async () => {
    await i18n.changeLanguage('en');
    const html = renderSheet();
    const control = (id: string) =>
      html.match(new RegExp(`<(?:input|textarea|button)\\b[^>]*id="${id}"[^>]*>`))?.[0] ?? '';

    const cases: Array<[string, string]> = [
      ['plugin-config-enabled', i18n.t('plugin_management.enabled_hint')],
      ['plugin-field-verbose', 'Log every request body'],
      ['plugin-field-mode', 'Which pipeline runs'],
      ['plugin-field-headers', 'Extra upstream headers'],
      ['plugin-field-endpoint', 'Upstream base URL'],
    ];
    for (const [id, text] of cases) {
      const describedBy = attr(control(id), 'aria-describedby') ?? '';
      expect(describedBy.split(' ').some((ref) => textOf(html, ref) === text)).toBe(true);
    }
    // No description declared: no InfoTip and no dangling reference.
    expect(attr(control('plugin-field-quiet'), 'aria-describedby')).toBeUndefined();
  });

  test('the plugin id stays visible as the sheet description', async () => {
    await i18n.changeLanguage('en');
    expect(renderSheet()).toContain('>example.plugin</span>');
  });
});
