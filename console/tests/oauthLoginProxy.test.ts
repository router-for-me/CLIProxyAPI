import { afterEach, describe, expect, spyOn, test } from 'bun:test';
import { readFileSync } from 'node:fs';
import axios, { AxiosError, type AxiosResponse, type InternalAxiosRequestConfig } from 'axios';
import { createElement } from 'react';
import { renderToStaticMarkup } from 'react-dom/server';
import { MemoryRouter } from 'react-router-dom';
import { I18nextProvider } from 'react-i18next';
import { createInstance } from 'i18next';
import { apiClient } from '@/services/api/client';
import { oauthApi } from '@/services/api/oauth';
import { OAuthPage } from '@/features/oauth/OAuthPage';
import { LoginProxyField } from '@/features/oauth/components/LoginProxyField';
import { OAuthFlowDialog } from '@/features/oauth/components/OAuthFlowDialog';
import type { ProviderFlowState } from '@/features/oauth/hooks/useOAuthFlows';
import {
  checkLoginProxyUrl,
  isInvalidProxyUrlError,
  maskProxyUrl,
  planLoginProxy,
  shouldShowLoginProxyError,
} from '@/features/oauth/loginProxy';
import en from '@/i18n/locales/en.json';
import zhCN from '@/i18n/locales/zh-CN.json';
import zhTW from '@/i18n/locales/zh-TW.json';
import ru from '@/i18n/locales/ru.json';
import vi from '@/i18n/locales/vi.json';
import ko from '@/i18n/locales/ko.json';
import tr from '@/i18n/locales/tr.json';

const PROXY = 'socks5://user:secret@127.0.0.1:1080';

const restorers: Array<() => void> = [];
afterEach(() => {
  while (restorers.length) restorers.pop()!();
});

/**
 * Route apiClient requests through the real Axios pipeline into a capturing adapter, so the
 * assertions see the final method, URL (including the serialized query string) and body.
 */
function captureTransport(respond: (config: InternalAxiosRequestConfig) => AxiosResponse) {
  const sent: Array<{ method: string; uri: string; params: unknown; body: unknown }> = [];
  const adapter = async (config: InternalAxiosRequestConfig) => {
    sent.push({
      method: String(config.method).toUpperCase(),
      uri: axios.getUri(config),
      params: config.params,
      body: typeof config.data === 'string' ? JSON.parse(config.data) : config.data,
    });
    return respond(config);
  };
  const realGet = apiClient.get.bind(apiClient);
  const realPost = apiClient.post.bind(apiClient);
  const get = spyOn(apiClient, 'get').mockImplementation((url, config) =>
    realGet(url, { ...config, adapter })
  );
  const post = spyOn(apiClient, 'post').mockImplementation((url, data, config) =>
    realPost(url, data, { ...config, adapter })
  );
  restorers.push(
    () => get.mockRestore(),
    () => post.mockRestore()
  );
  return { sent, get, post };
}

const ok = (config: InternalAxiosRequestConfig): AxiosResponse => ({
  config,
  data: { status: 'ok', url: 'https://auth.example/authorize', state: 'st' },
  status: 200,
  statusText: 'OK',
  headers: {},
});

describe('oauthApi.startAuth login proxy contract', () => {
  test('a proxy moves the whole request into a POST body with a trimmed proxy_url', async () => {
    const signal = new AbortController().signal;
    const { sent, get, post } = captureTransport(ok);

    await oauthApi.startAuth('codex', signal, { proxyUrl: `  ${PROXY}\n` });
    expect(post).toHaveBeenCalledWith(
      '/oauth/auth-url',
      { provider: 'codex', is_webui: true, proxy_url: PROXY },
      { signal }
    );
    expect(get).not.toHaveBeenCalled();

    // Device-flow providers have no is_webui; the anthropic alias still maps to claude.
    await oauthApi.startAuth('kimi', undefined, { proxyUrl: PROXY });
    await oauthApi.startAuth('anthropic', undefined, { proxyUrl: PROXY });

    expect(sent.map(({ method, body }) => [method, body])).toEqual([
      ['POST', { provider: 'codex', is_webui: true, proxy_url: PROXY }],
      ['POST', { provider: 'kimi', proxy_url: PROXY }],
      ['POST', { provider: 'claude', is_webui: true, proxy_url: PROXY }],
    ]);
    for (const request of sent) {
      // No query string at all: the backend rejects proxy_url there.
      expect(request.params).toBeUndefined();
      expect(request.uri).toBe('/oauth/auth-url');
      expect(request.uri).not.toMatch(/proxy/i);
    }
  });

  for (const blank of [undefined, '', '   ', '\t\n']) {
    test(`a blank proxy (${JSON.stringify(blank)}) keeps the original GET request`, async () => {
      const signal = new AbortController().signal;
      const { sent, get, post } = captureTransport(ok);

      await oauthApi.startAuth(
        'codex',
        signal,
        blank === undefined ? undefined : { proxyUrl: blank }
      );
      await oauthApi.startAuth('meta', undefined, blank === undefined ? {} : { proxyUrl: blank });

      expect(post).not.toHaveBeenCalled();
      expect(get).toHaveBeenNthCalledWith(1, '/oauth/auth-url', {
        params: { provider: 'codex', is_webui: true },
        signal,
      });
      expect(get).toHaveBeenNthCalledWith(2, '/oauth/auth-url', {
        params: { provider: 'meta' },
      });
      expect(sent.map(({ method, uri, body }) => [method, uri, body])).toEqual([
        ['GET', '/oauth/auth-url?provider=codex&is_webui=true', undefined],
        ['GET', '/oauth/auth-url?provider=meta', undefined],
      ]);
    });
  }

  test("maps the backend's 400 invalid proxy_url to a recognizable error", async () => {
    const originalWindow = Object.getOwnPropertyDescriptor(globalThis, 'window');
    Object.defineProperty(globalThis, 'window', {
      configurable: true,
      value: { dispatchEvent: () => true },
    });
    restorers.push(() => {
      if (originalWindow) Object.defineProperty(globalThis, 'window', originalWindow);
      else Reflect.deleteProperty(globalThis, 'window');
    });
    const reject = (status: number, error: string) => (config: InternalAxiosRequestConfig) => {
      const response: AxiosResponse = {
        config,
        data: { error },
        status,
        statusText: String(status),
        headers: {},
      };
      throw new AxiosError('Request failed', 'ERR_BAD_REQUEST', config, undefined, response);
    };

    captureTransport(reject(400, 'invalid proxy_url'));
    const invalid = await oauthApi
      .startAuth('codex', undefined, { proxyUrl: PROXY })
      .catch((e) => e);
    expect(isInvalidProxyUrlError(invalid)).toBe(true);
    while (restorers.length > 1) restorers.pop()!();

    captureTransport(reject(400, 'invalid provider'));
    const other = await oauthApi.startAuth('codex', undefined, { proxyUrl: PROXY }).catch((e) => e);
    expect(isInvalidProxyUrlError(other)).toBe(false);
    expect(other.message).toBe('invalid provider');
  });

  test('only a 400 carrying invalid proxy_url counts as a proxy rejection', () => {
    expect(isInvalidProxyUrlError({ status: 400, apiCode: 'invalid proxy_url' })).toBe(true);
    expect(isInvalidProxyUrlError({ status: 400, message: 'invalid proxy_url' })).toBe(true);
    expect(isInvalidProxyUrlError({ status: 500, message: 'invalid proxy_url' })).toBe(false);
    expect(isInvalidProxyUrlError({ status: 400, message: 'invalid is_webui' })).toBe(false);
    expect(isInvalidProxyUrlError(new Error('invalid proxy_url'))).toBe(false);
    expect(isInvalidProxyUrlError(null)).toBe(false);
  });
});

/*
 * Mirrors validateOAuthSessionProxyURL (CLIProxyAPI internal/api/handlers/management/
 * auth_files_v8.go) over proxyutil.Parse / ValidRequestProxy (sdk/proxyutil/proxy.go) and Go
 * 1.26 net/url.Parse. These verdicts were cross-checked against the Go implementation.
 */
describe('login proxy validation mirrors the backend', () => {
  const accepted = [
    'socks5://user:pass@127.0.0.1:1080',
    'socks5h://proxy.example.com:1080',
    'SOCKS5://Host:1080', // Go lowercases the scheme
    'http://proxy.example.com', // http(s) may omit the port
    'http://proxy.example.com:8080/', // a bare trailing slash is not a path
    'https://u:p@proxy.example.com:8443',
    'http://host:80?', // a lone "?" is an empty query
    'http://host:80#', // and a lone "#" an empty fragment
    'http://host:65535',
    'http://host:0080',
    'http://[::1]:8080',
    'socks5://[::ffff:1.2.3.4]:1080',
    'http://[fe80::1%25en0]:80',
    'http://u:p@ss@host:80', // Go splits userinfo at the last "@"
    'http://u%40x:p%3Ass@host:80',
    'socks5://a:1:2', // non-http schemes split the port at the last colon
    'http://exa_mple.com:80',
    'http://例子.测试:8080',
    'http://ex%c3%a9mple:80',
  ];
  const rejected = [
    'ftp://host:21', // scheme: only http, https, socks5, socks5h
    'socks4://host:1080',
    'direct', // "direct"/"none" are not a concrete proxy
    'none',
    'proxy.example.com', // no scheme
    'host:8080',
    '//host:80',
    'http:host:80', // no "//" authority
    'http://', // no host
    'http://:8080',
    'http://user@:80',
    'socks5://host', // socks5(h) requires a port
    'socks5h://host',
    'http://host:', // dangling colon
    'http://[::1]:',
    'http://host:0', // port must be 1-65535
    'http://host:65536',
    'http://host:abc',
    'http://host/path', // no path
    'http://host:80/%2F',
    'http://host:80?a=1', // no query
    'http://host:80#frag', // no fragment
    'socks5://user:p#ss@host:1080',
    'http://a:1:2', // Go 1.26 rejects extra colons for http(s)
    'http://[1.2.3.4]:80', // brackets need IPv6
    'http://[::1',
    'http://ho st:80', // whitespace or control characters
    'http://h\u0001ost:80',
    'http://u p@host:80',
    'http://ü:p@host:80', // non-ASCII userinfo must be escaped
    'http://u%zz:p@host:80',
    'http://ex|ample:80',
    'http://ex%41mple:80', // hosts may only escape non-ASCII bytes
    'http://%c2%a0:80', // blank hostname after unescaping
  ];

  for (const raw of accepted) {
    test(`accepts ${JSON.stringify(raw)}`, () => {
      expect(checkLoginProxyUrl(raw)).toEqual({ kind: 'valid', url: raw });
    });
  }
  for (const raw of rejected) {
    test(`rejects ${JSON.stringify(raw)}`, () => {
      expect(checkLoginProxyUrl(raw)).toEqual({ kind: 'invalid' });
    });
  }

  test('blank input means no proxy, and valid input is sent trimmed', () => {
    expect(checkLoginProxyUrl('')).toEqual({ kind: 'empty' });
    expect(checkLoginProxyUrl(' \t\n ')).toEqual({ kind: 'empty' });
    expect(checkLoginProxyUrl(`  ${PROXY}  `)).toEqual({ kind: 'valid', url: PROXY });
  });
});

describe('login proxy request planning', () => {
  test('all providers send a valid proxy and refuse an invalid one', () => {
    expect(planLoginProxy('')).toEqual({ kind: 'none' });
    expect(planLoginProxy('   ')).toEqual({ kind: 'none' });
    expect(planLoginProxy(` ${PROXY} `)).toEqual({ kind: 'proxy', url: PROXY });
    // Never silently degrade to a login without the proxy, including plugin logins.
    expect(planLoginProxy('socks5://host')).toEqual({ kind: 'invalid' });
  });

  test('plugin provider IDs use POST with a proxy and retain GET without one', async () => {
    const { sent } = captureTransport(ok);
    const signal = new AbortController().signal;
    for (const value of [` ${PROXY} `, '']) {
      const plan = planLoginProxy(value);
      await oauthApi.startAuth(
        'custom-sso',
        signal,
        plan.kind === 'proxy' ? { proxyUrl: plan.url } : undefined
      );
    }
    expect(sent).toEqual([
      {
        method: 'POST',
        uri: '/oauth/auth-url',
        params: undefined,
        body: { provider: 'custom-sso', proxy_url: PROXY },
      },
      {
        method: 'GET',
        uri: '/oauth/auth-url?provider=custom-sso',
        params: { provider: 'custom-sso' },
        body: undefined,
      },
    ]);
    const hook = readFileSync('src/features/oauth/hooks/useOAuthFlows.ts', 'utf8');
    expect(hook).toContain('const proxyPlan = planLoginProxy(loginProxy);');
  });

  test('the inline error appears after blur or a refused login, and tracks the refused value', () => {
    const state = (touched: boolean, rejectedValue: string | null) => ({ touched, rejectedValue });
    expect(shouldShowLoginProxyError('socks5://host', state(false, null))).toBe(false);
    expect(shouldShowLoginProxyError('socks5://host', state(true, null))).toBe(true);
    expect(shouldShowLoginProxyError('socks5://host', state(false, 'socks5://host'))).toBe(true);
    expect(shouldShowLoginProxyError('', state(true, null))).toBe(false);
    expect(shouldShowLoginProxyError(PROXY, state(true, null))).toBe(false);
    // Locally valid but refused by the backend: flagged until edited.
    expect(shouldShowLoginProxyError(` ${PROXY} `, state(false, PROXY))).toBe(true);
    expect(shouldShowLoginProxyError('http://other:8080', state(false, PROXY))).toBe(false);
  });
});

describe('proxy display masking', () => {
  test('hides userinfo and keeps scheme, host and port', () => {
    expect(maskProxyUrl(PROXY)).toBe('socks5://***@127.0.0.1:1080');
    expect(maskProxyUrl('http://onlyuser@proxy.example.com:8080/')).toBe(
      'http://***@proxy.example.com:8080'
    );
    expect(maskProxyUrl('http://u:p@ss@host:80')).toBe('http://***@host:80');
    expect(maskProxyUrl('https://u:p@[::1]:8443')).toBe('https://***@[::1]:8443');
    expect(maskProxyUrl(' http://proxy.example.com:3128 ')).toBe('http://proxy.example.com:3128');
  });

  test('never leaks a credential even from malformed input', () => {
    for (const raw of ['socks5://user:p/ss@host:1', 'user:secret@host:1', 'http://a:b?c#d@h']) {
      const masked = maskProxyUrl(raw);
      expect(masked).not.toContain('secret');
      expect(masked).not.toMatch(/user|:p\/ss|a:b/);
    }
  });
});

const LOCALES = { en, 'zh-CN': zhCN, 'zh-TW': zhTW, ru, vi, ko, tr } as const;
const KEYS = [
  'login_proxy_toggle',
  'login_proxy_status_invalid',
  'login_proxy_label',
  'login_proxy_placeholder',
  'login_proxy_clear',
  'login_proxy_invalid',
  'login_proxy_in_use',
];

describe('login proxy locales', () => {
  test('every locale translates every key', () => {
    const english = en.auth_login as Record<string, string>;
    for (const [lng, translation] of Object.entries(LOCALES)) {
      const messages = translation.auth_login as Record<string, string>;
      for (const key of KEYS) {
        expect(typeof messages[key], `${lng}.${key}`).toBe('string');
        expect(messages[key].trim(), `${lng}.${key}`).not.toBe('');
        // The placeholder is a URL example; everything else must be a real translation.
        if (lng !== 'en' && key !== 'login_proxy_placeholder') {
          expect(messages[key], `${lng}.${key}`).not.toBe(english[key]);
        }
      }
    }
  });
});

const renderWith = async (lng: keyof typeof LOCALES, element: ReturnType<typeof createElement>) => {
  const i18n = createInstance();
  await i18n.init({ lng, resources: { [lng]: { translation: LOCALES[lng] } } });
  return {
    i18n,
    markup: renderToStaticMarkup(createElement(I18nextProvider, { i18n }, element)),
  };
};

describe('login proxy field', () => {
  for (const lng of Object.keys(LOCALES) as Array<keyof typeof LOCALES>) {
    test(`the OAuth page renders it collapsed by default (${lng})`, async () => {
      const { i18n, markup } = await renderWith(
        lng,
        createElement(MemoryRouter, null, createElement(OAuthPage))
      );
      const toggle = /<button type="button"[^>]*aria-expanded="false" aria-controls="([^"]+)"/.exec(
        markup
      );
      expect(toggle).not.toBeNull();
      expect(markup).toContain(`id="${toggle![1]}"`);
      expect(markup).toMatch(new RegExp(`id="${toggle![1]}"[^>]*hidden=""`));
      const text = markup.replace(/&#x27;/g, "'").replace(/&amp;/g, '&');
      expect(text).toContain(i18n.t('auth_login.login_proxy_toggle'));
      // An empty proxy shows no status: the collapsed chip already reads as optional, so the
      // toggle's only text is its label.
      const toggleText = /aria-expanded="false"[^>]*>([\s\S]*?)<\/button>/
        .exec(markup)![1]
        .replace(/<[^>]+>/g, '')
        .replace(/&#x27;/g, "'");
      expect(toggleText).toBe(i18n.t('auth_login.login_proxy_toggle'));
      expect(markup).not.toContain('data-active');
      // It sits between the header and the gallery.
      expect(markup.indexOf('</header>')).toBeLessThan(toggle!.index);
      expect(markup.indexOf('aria-haspopup="dialog"')).toBeGreaterThan(toggle!.index);
      expect(markup).not.toMatch(/auth_login\./);
    });
  }

  test('the input is labelled and opts out of autocomplete and spellcheck', async () => {
    const { markup } = await renderWith(
      'en',
      createElement(LoginProxyField, { value: '', rejectedValue: null, onChange: () => {} })
    );
    const input = /<input[^>]*>/.exec(markup)![0];
    // React's static renderer keeps the camel-cased attribute names.
    expect(input).toContain('autoComplete="off"');
    expect(input).toContain('spellCheck="false"');
    expect(input).toContain('placeholder="socks5://user:pass@host:port"');
    expect(input).toContain('aria-invalid="false"');
    const inputId = /id="([^"]+)"/.exec(input)![1];
    expect(markup).toContain(`for="${inputId}"`);
    expect(input).not.toContain('aria-describedby');
    expect(markup).not.toContain('Not stored in the browser');
    // No clear button without a value.
    expect(markup).not.toContain('>Clear<');
  });

  test('a set proxy stays visible while collapsed, masked', async () => {
    const { markup } = await renderWith(
      'en',
      createElement(LoginProxyField, { value: PROXY, rejectedValue: null, onChange: () => {} })
    );
    expect(markup).toContain('aria-expanded="false"');
    expect(markup).toContain('data-active="true"');
    expect(markup).toContain('socks5://***@127.0.0.1:1080');
    // The raw value only lives in the input itself, never in the visible status.
    expect(markup.split('secret').length - 1).toBe(1);
    expect(markup).toContain('>Clear<');
  });

  test('a refused proxy expands the field and ties the error to the input', async () => {
    const { markup } = await renderWith(
      'en',
      createElement(LoginProxyField, {
        value: 'socks5://host',
        rejectedValue: 'socks5://host',
        onChange: () => {},
      })
    );
    expect(markup).toContain('data-invalid="true"');
    expect(markup).toContain('aria-expanded="true"');
    expect(markup).not.toContain('hidden=""');
    const input = /<input[^>]*>/.exec(markup)![0];
    expect(input).toContain('aria-invalid="true"');
    const errorId = /aria-describedby="([^"]+)"/.exec(input)![1];
    expect(markup).toMatch(new RegExp(`id="${errorId}"[^>]*role="alert"`));
    expect(markup).toContain(en.auth_login.login_proxy_invalid);
  });
});

describe('OAuth dialog proxy line', () => {
  const renderDialog = async (state: ProviderFlowState) =>
    (
      await renderWith(
        'en',
        createElement(OAuthFlowDialog, {
          open: true,
          providerId: 'codex',
          heading: { title: 'Codex OAuth', caption: 'Browser sign-in', icon: '' },
          state,
          text: (suffix: string) => `codex_${suffix}`,
          supportsCallback: true,
          onStart: () => {},
          onCancel: () => {},
          onCallbackChange: () => {},
          onCallbackSubmit: () => {},
          onViewAuthFiles: () => {},
          onClose: () => {},
        })
      )
    ).markup;

  test('shows the masked proxy of the running attempt', async () => {
    const markup = await renderDialog({
      status: 'waiting',
      url: 'https://auth.example/authorize',
      proxyUrl: PROXY,
    });
    expect(markup).toContain(en.auth_login.login_proxy_in_use);
    expect(markup).toContain('socks5://***@127.0.0.1:1080');
    expect(markup).not.toContain('secret');
  });

  test('says nothing about a proxy when the attempt has none', async () => {
    const markup = await renderDialog({ status: 'waiting', url: 'https://auth.example/a' });
    expect(markup).not.toContain(en.auth_login.login_proxy_in_use);
  });
});

describe('login proxy state stays in memory', () => {
  test('is never persisted and is cleared with the connection', () => {
    const sources = [
      'src/features/oauth/hooks/useOAuthFlows.ts',
      'src/features/oauth/loginProxy.ts',
      'src/features/oauth/components/LoginProxyField.tsx',
    ].map((path) => readFileSync(path, 'utf8'));
    for (const source of sources) {
      expect(source).not.toMatch(/localStorage|sessionStorage|persist\(|searchParams|location\./);
    }
    const hook = sources[0];
    const subscription = hook.slice(hook.indexOf('useAuthStore.subscribe'));
    const reset = subscription.slice(0, subscription.indexOf('return () =>'));
    expect(reset).toContain("setLoginProxyValue('')");
    expect(reset).toContain('setLoginProxyRejected(null)');
  });
});
