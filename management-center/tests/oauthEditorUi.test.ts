import { describe, expect, test } from 'bun:test';
import { createElement } from 'react';
import { renderToStaticMarkup } from 'react-dom/server';
import { I18nextProvider } from 'react-i18next';
import { createInstance } from 'i18next';
import { OAuthEditorProviderCard } from '../src/features/authFiles/components/OAuthEditorProviderCard';
import { OAuthAliasMappingRow } from '../src/features/authFiles/components/OAuthAliasMappingRow';
import { SecondaryScreenShell } from '../src/components/common/SecondaryScreenShell';
import en from '../src/i18n/locales/en.json';
import zhCN from '../src/i18n/locales/zh-CN.json';
import zhTW from '../src/i18n/locales/zh-TW.json';
import ru from '../src/i18n/locales/ru.json';

const i18n = createInstance();
await i18n.init({ lng: 'en', resources: { en: { translation: en } } });
const noop = () => {};
const render = (element: ReturnType<typeof createElement>) =>
  renderToStaticMarkup(createElement(I18nextProvider, { i18n }, element));

function renderMapping(overrides: { disabled?: boolean; canRemove?: boolean } = {}) {
  return render(
    createElement(OAuthAliasMappingRow, {
      entry: { name: 'source-model', alias: 'client-model', fork: true },
      index: 1,
      options: [{ value: 'source-model' }],
      disabled: false,
      canRemove: true,
      onChange: noop,
      onRemove: noop,
      ...overrides,
    })
  );
}

describe('OAuth editor UI', () => {
  test('allows scoped toolbar styling without losing the full title or actions', () => {
    const markup = render(
      createElement(SecondaryScreenShell, {
        title: 'Edit model aliases for a-long-provider-name',
        onBack: noop,
        topBarClassName: 'oauth-toolbar',
        rightAction: createElement('button', null, 'Save'),
      })
    );
    expect(markup).toContain('class="oauth-toolbar"');
    expect(markup).toContain('title="Edit model aliases for a-long-provider-name"');
    expect(markup).toContain('<button>Save</button>');
    expect(markup).toContain('aria-label="Back"');
  });

  test('provider shortcuts expose normalized selection and an associated input label', () => {
    const markup = render(
      createElement(OAuthEditorProviderCard, {
        provider: ' CODEX ',
        options: ['codex', 'claude', 'custom-provider'],
        onChange: noop,
        disabled: false,
        translationPrefix: 'oauth_excluded',
      })
    );
    expect(markup.match(/aria-pressed="true"/g)).toHaveLength(1);
    expect(markup.match(/aria-pressed="false"/g)).toHaveLength(2);
    const inputId = markup.match(/<input id="([^"]+)"/)?.[1];
    expect(inputId).toBeDefined();
    expect(markup).toContain(`for="${inputId}"`);
    expect(markup).toContain('Custom-provider');
  });

  test('disables provider input and every shortcut while saving or disconnected', () => {
    const markup = render(
      createElement(OAuthEditorProviderCard, {
        provider: 'codex',
        options: ['codex', 'claude'],
        onChange: noop,
        disabled: true,
        translationPrefix: 'oauth_model_alias',
      })
    );
    expect(markup.match(/disabled=""/g)).toHaveLength(3);
  });

  test('mapping fields have persistent associated labels and a contextual delete name', () => {
    const markup = renderMapping();
    expect(markup).toContain('aria-label="Alias mapping 2"');
    expect(markup).toContain('aria-label="Delete alias mapping 2"');
    expect(markup).toContain('value="source-model"');
    expect(markup).toContain('value="client-model"');
    for (const suffix of ['source', 'alias']) {
      const id = markup.match(new RegExp(`<input id="([^"]+-${suffix})"`))?.[1];
      expect(id).toBeDefined();
      expect(markup).toContain(`for="${id}"`);
    }
    expect(markup).toContain('checked=""');
  });

  test('preserves the last-row delete guard and disabled editing state', () => {
    const lastRow = renderMapping({ canRemove: false });
    expect(lastRow.match(/disabled=""/g)).toHaveLength(1);
    const disabledRow = renderMapping({ disabled: true });
    expect(disabledRow.match(/disabled=""/g)).toHaveLength(4);
  });

  test('all four locales include editor guidance and accessible row labels', () => {
    for (const locale of [en, zhCN, zhTW, ru]) {
      expect(locale.oauth_excluded.editor_description.length).toBeGreaterThan(0);
      expect(locale.oauth_excluded.models_hint.length).toBeGreaterThan(0);
      expect(locale.oauth_model_alias.editor_description.length).toBeGreaterThan(0);
      expect(locale.oauth_model_alias.mapping_hint.length).toBeGreaterThan(0);
      expect(locale.oauth_model_alias.edit_title).toContain('{{provider}}');
      expect(locale.oauth_model_alias.mapping_row).toContain('{{number}}');
      expect(locale.oauth_model_alias.remove_mapping).toContain('{{number}}');
    }
  });
});
