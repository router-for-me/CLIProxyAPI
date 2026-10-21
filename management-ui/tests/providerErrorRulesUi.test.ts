import { describe, expect, test } from 'bun:test';
import { createElement } from 'react';
import { renderToStaticMarkup } from 'react-dom/server';
import { createInstance } from 'i18next';
import { I18nextProvider } from 'react-i18next';
import { ErrorRulesEditor } from '@/features/providers/sheets/forms/ErrorRulesEditor';
import { RuntimePolicyEditor } from '@/features/providers/sheets/forms/RuntimePolicyEditor';
import { readErrorRules, type ErrorRuleDraft } from '@/features/providers/errorRules';
import { readRuntimePolicy } from '@/features/providers/runtimePolicy';
import en from '@/i18n/locales/en.json';
import zhCN from '@/i18n/locales/zh-CN.json';
import zhTW from '@/i18n/locales/zh-TW.json';
import ru from '@/i18n/locales/ru.json';

const i18n = createInstance();
await i18n.init({ lng: 'en', resources: { en: { translation: en } } });
const render = (component: ReturnType<typeof createElement>) =>
  renderToStaticMarkup(createElement(I18nextProvider, { i18n }, component));
const rows = readErrorRules([
  { status: 429, match: ['quota'], matchRegex: ['(?i)limit'], action: 'continue-and-cooldown' },
  { status: 500, match: ['busy'], action: 'stop' },
]);
const renderRows = (rules: ErrorRuleDraft[] = rows, disabled = false) =>
  render(createElement(ErrorRulesEditor, { rules, onChange: () => {}, disabled }));

describe('error-rule editor UI', () => {
  test('renders rows, named controls, localized actions and match conditions without JSON editing', () => {
    const html = renderRows();
    expect(html).toContain('Rule 1');
    expect(html).toContain('Rule 2');
    expect(html).toContain('role="group"');
    expect(html).toContain('Status code');
    expect(html).toContain('Continue and cool down');
    expect(html).toContain('Contains text');
    expect(html).toContain('Regular expression');
    expect(html).toContain('Go regular expression');
    expect(html).toContain('Add rule');
    expect(html).toContain('Add condition');
    expect(html).not.toContain('JSON');
    expect(html).not.toContain('<select');
    expect(html).toContain('aria-haspopup="listbox"');
    const ids = [...html.matchAll(/\bid="([^"]+)"/g)].map((match) => match[1]);
    expect(new Set(ids).size).toBe(ids.length);
    for (const label of html.matchAll(/\bfor="([^"]+)"/g)) expect(ids).toContain(label[1]);
  });
  test('keeps list order and disables boundary moves', () => {
    const html = renderRows();
    const ups = [...html.matchAll(/<button\b[^>]*aria-label="Move rule up"[^>]*>/g)];
    const downs = [...html.matchAll(/<button\b[^>]*aria-label="Move rule down"[^>]*>/g)];
    expect(ups).toHaveLength(2);
    expect(downs).toHaveLength(2);
    expect(ups[0][0]).toContain('disabled=""');
    expect(ups[1][0]).not.toContain('disabled=""');
    expect(downs[0][0]).not.toContain('disabled=""');
    expect(downs[1][0]).toContain('disabled=""');
  });
  test('all controls are disabled while saving or disconnected', () => {
    const controls = [
      ...renderRows(rows, true).matchAll(/<(?:input|textarea|select|button)\b[^>]*>/g),
    ];
    expect(controls.length).toBeGreaterThan(10);
    for (const control of controls) expect(control[0]).toContain('disabled=""');
  });
  test('an empty override retains its add action and inherited mode hides rows', () => {
    expect(renderRows([])).toContain('No rules.');
    expect(renderRows([])).toContain('Add rule');
    const policy = { ...readRuntimePolicy(), errorRules: rows };
    const inherited = render(
      createElement(RuntimePolicyEditor, { value: policy, onChange: () => {}, disabled: false })
    );
    expect(inherited).not.toContain('Add rule');
    const overridden = render(
      createElement(RuntimePolicyEditor, {
        value: { ...policy, errorsMode: 'override' },
        onChange: () => {},
        disabled: false,
      })
    );
    expect(overridden).toContain('Add rule');
    const vertex = render(
      createElement(RuntimePolicyEditor, {
        value: { ...policy, errorsMode: 'override' },
        onChange: () => {},
        disabled: false,
        supportsErrors: false,
      })
    );
    expect(vertex).not.toContain('Add rule');
    expect(vertex).not.toContain('Request error rules');
  });
  test('unrecognized imported actions are displayed safely instead of silently selected away', () => {
    const html = renderRows([{ ...rows[0], action: '<unknown>' }]);
    expect(html).toContain('&lt;unknown&gt;');
    expect(html).not.toContain('<unknown>');
    expect(html).toContain('>&lt;unknown&gt;</span>');
    expect(html).toContain('aria-label="Action"');
  });
  test('every locale contains complete row labels, errors, actions and no stale JSON guidance', () => {
    for (const messages of [en, zhCN, zhTW, ru]) {
      const section = messages.providersPage.errorRules;
      expect(Object.keys(section).sort()).toEqual(Object.keys(en.providersPage.errorRules).sort());
      expect(Object.keys(section.actions).sort()).toEqual([
        'continue',
        'continue-and-cooldown',
        'stop',
        'stop-and-cooldown',
      ]);
      for (const [key, value] of Object.entries(section)) {
        if (key === 'actions') continue;
        expect(typeof value === 'string' && value.trim().length > 0).toBe(true);
      }
      expect(Object.values(section.actions).every((value) => value.trim().length > 0)).toBe(true);
      expect(JSON.stringify(messages.providersPage.runtimePolicy)).not.toContain('JSON');
      expect(messages.providersPage.runtimePolicy).not.toHaveProperty('errorsJson');
    }
  });
});
