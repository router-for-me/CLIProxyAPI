import { describe, expect, test } from 'bun:test';
import { createElement } from 'react';
import { renderToStaticMarkup } from 'react-dom/server';
import { createInstance } from 'i18next';
import { I18nextProvider } from 'react-i18next';
import { ModelEntriesEditor } from '@/features/providers/sheets/forms/ModelEntriesEditor';
import { AuthFilePolicyFields } from '@/features/authFiles/components/AuthFilePolicyFields';
import { readCredentialPolicy } from '@/features/authFiles/credentialPolicy';
import en from '@/i18n/locales/en.json';
import zhCN from '@/i18n/locales/zh-CN.json';
import zhTW from '@/i18n/locales/zh-TW.json';
import ru from '@/i18n/locales/ru.json';

const i18n = createInstance();
await i18n.init({ lng: 'en', resources: { en: { translation: en } } });
const original = {
  model_aliases: [{ name: 'upstream', alias: 'public', fork: false, 'force-mapping': true }],
  request_scoped_errors: [
    {
      status: 429,
      match: ['quota'],
      'match-regexr': ['(?i)limit'],
      action: 'continue-and-cooldown',
    },
    { status: 503, match: ['busy'], action: 'stop' },
  ],
};
const render = (json: Record<string, unknown> = original, disabled = false) =>
  renderToStaticMarkup(
    createElement(
      I18nextProvider,
      { i18n },
      createElement(AuthFilePolicyFields, {
        draft: readCredentialPolicy(json),
        disabled,
        onChange: () => {},
      })
    )
  );

describe('credential policy structured forms', () => {
  test('keeps optional aliases for the original Provider editor by default', () => {
    const html = renderToStaticMarkup(
      createElement(
        I18nextProvider,
        { i18n },
        createElement(ModelEntriesEditor, {
          models: [{ name: 'model', alias: '' }],
          supportsImage: false,
          supportsThinking: true,
          mutating: false,
          removeDisabled: false,
          onAdd: () => {},
          onRemove: () => {},
          onUpdate: () => {},
        })
      )
    );
    expect(html).toContain('Alias (optional)');
    expect(html).not.toContain('required=""');
  });

  test('uses Provider-style model rows and the same rule/condition controls, not JSON editors', () => {
    const html = render();
    expect(html).toContain('value="upstream"');
    expect(html).toContain('value="public"');
    expect(html).toContain('Add model');
    expect(html).toContain('Remove model');
    expect(html).toContain('Rule 1');
    expect(html).toContain('Rule 2');
    expect(html).toContain('Status code');
    expect(html).toContain('Add rule');
    expect(html).toContain('Add condition');
    expect(html).toContain('Move rule up');
    expect(html).toContain('Move rule down');
    expect(html).toContain('Continue and cool down');
    expect(html).toContain('Contains text');
    expect(html).toContain('Regular expression');
    expect(html).not.toContain('(JSON)');
    expect(html).not.toContain('modelAliasesText');
    expect(html).not.toContain('errorRulesText');
    // Textareas remain only for individual literal/regex conditions.
    expect(html.match(/<textarea\b/g)).toHaveLength(3);
    expect(html).toContain('aria-label="Alias" required=""');
    expect(html).not.toContain('Alias (optional)');
  });

  test('disables all mutation controls while saving or disconnected', () => {
    const controls = [
      ...render(original, true).matchAll(/<(?:input|textarea|select|button)\b[^>]*>/g),
    ];
    expect(controls.length).toBeGreaterThan(12);
    for (const control of controls) expect(control[0]).toContain('disabled=""');
  });

  test('provides unique label targets and accessible action buttons', () => {
    const html = render();
    const ids = [...html.matchAll(/\bid="([^"]+)"/g)].map((match) => match[1]);
    expect(new Set(ids).size).toBe(ids.length);
    for (const label of html.matchAll(/\bfor="([^"]+)"/g)) expect(ids).toContain(label[1]);
    expect(html).toContain('aria-haspopup="listbox"');
    expect(html).toContain('aria-expanded="false"');
  });

  test('inherited errors do not pretend to override provider rules', () => {
    const html = render({});
    expect(html).toContain('Inherit');
    expect(html).not.toContain('Add rule');
    expect(html).not.toContain('Add model');
  });

  test('all locales describe structured fields without asking users for JSON', () => {
    for (const messages of [en, zhCN, zhTW, ru]) {
      for (const [key, value] of Object.entries(messages.auth_files)) {
        if (!key.startsWith('policy_')) continue;
        expect(value.trim().length).toBeGreaterThan(0);
        expect(value).not.toContain('JSON');
      }
    }
  });
});
