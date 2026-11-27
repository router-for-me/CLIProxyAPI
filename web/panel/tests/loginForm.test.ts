import { afterAll, beforeAll, describe, expect, test } from 'bun:test';
import { createElement } from 'react';
import { renderToStaticMarkup } from 'react-dom/server';
import i18n from '@/i18n';
import { LoginForm, type LoginFormProps } from '@/pages/login/LoginForm';
import { LANGUAGE_ORDER } from '@/utils/constants';

const noop = () => undefined;

const baseProps: LoginFormProps = {
  apiBase: '',
  detectedBase: 'http://localhost:8317',
  editingBase: false,
  managementKey: '',
  showKey: false,
  rememberPassword: false,
  loading: false,
  error: '',
  onApiBaseChange: noop,
  onEditingBaseChange: noop,
  onManagementKeyChange: noop,
  onShowKeyChange: noop,
  onRememberPasswordChange: noop,
  onSubmit: noop,
};

const render = (overrides: Partial<LoginFormProps> = {}) =>
  renderToStaticMarkup(createElement(LoginForm, { ...baseProps, ...overrides }));

describe('LoginForm', () => {
  let original = 'zh-CN';
  beforeAll(async () => {
    original = i18n.language;
    await i18n.changeLanguage('en');
  });
  afterAll(async () => {
    await i18n.changeLanguage(original);
  });

  test('submits through a native form so Enter logs in', () => {
    const markup = render();
    expect(markup).toContain('<form');
    expect(markup).toContain('type="submit"');
    expect(markup).toMatch(/autocomplete="current-password"/i);
  });

  test('shows the detected URL read-only until the user chooses to change it', () => {
    const markup = render();
    expect(markup).toContain('http://localhost:8317');
    expect(markup).not.toContain('type="url"');
    expect(markup).toContain('aria-expanded="false"');
    expect(markup).toContain(`>${i18n.t('login.connection_edit')}<`);
  });

  test('prefers the stored/custom URL over the detected one', () => {
    const markup = render({ apiBase: 'https://proxy.example.com' });
    expect(markup).toContain('https://proxy.example.com');
    expect(markup).not.toContain('http://localhost:8317');
  });

  test('edits the URL in place with a labelled input', () => {
    const markup = render({ editingBase: true, apiBase: 'https://proxy.example.com' });
    expect(markup).toContain('type="url"');
    expect(markup).toContain('value="https://proxy.example.com"');
    expect(markup).toContain('aria-expanded="true"');
    expect(markup).toContain(`>${i18n.t('login.connection_done')}<`);
    expect(markup).toContain(i18n.t('login.custom_connection_label'));
  });

  test('toggles key visibility with an accessible pressed button', () => {
    expect(render()).toContain('type="password"');
    expect(render()).toContain(`aria-label="${i18n.t('login.show_key')}"`);
    const revealed = render({ showKey: true });
    expect(revealed).toContain('aria-pressed="true"');
    expect(revealed).toContain(`aria-label="${i18n.t('login.hide_key')}"`);
    expect(revealed).not.toContain('type="password"');
  });

  test('links the error to the key field and announces it', () => {
    const markup = render({ error: 'HTTP 401: bad key' });
    expect(markup).toContain('role="alert"');
    expect(markup).toContain('aria-invalid="true"');
    expect(markup).toMatch(/aria-describedby="[^"]+"/);
    expect(markup).toContain('HTTP 401: bad key');
  });

  test('locks the submit button while connecting', () => {
    const markup = render({ loading: true });
    expect(markup).toContain('aria-busy="true"');
    expect(markup).toMatch(/type="submit"[^>]*disabled=""/);
    expect(markup).toContain(i18n.t('login.submitting'));
  });
});

describe('login copy', () => {
  test('every locale keeps field labels colon-free', async () => {
    for (const language of LANGUAGE_ORDER) {
      const { login } = await Bun.file(`src/i18n/locales/${language}.json`).json();
      expect(login.management_key_label).not.toMatch(/[:：]\s*$/);
      expect(login.custom_connection_label).not.toMatch(/[:：]\s*$/);
    }
  });
});
