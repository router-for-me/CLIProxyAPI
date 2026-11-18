import { afterEach, beforeAll, describe, expect, test } from 'bun:test';
import { createElement } from 'react';
import { renderToStaticMarkup } from 'react-dom/server';
import i18n from '@/i18n';
import { LoginRevealVeil, LoginRevealVeilLayer } from '@/components/common/LoginRevealVeil';
import { LoginForm, type LoginFormProps } from '@/pages/login/LoginForm';
import {
  LOGIN_TRANSITION_TIMINGS,
  LOGIN_TRANSITION_TIMINGS_REDUCED,
  getLoginTransitionTimings,
  isArrivingFromLogin,
  useLoginTransitionStore,
} from '@/stores/useLoginTransitionStore';

const noop = () => undefined;
const formProps: LoginFormProps = {
  apiBase: '',
  detectedBase: 'http://localhost:8317',
  editingBase: false,
  managementKey: 'sk-demo',
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

/** 读取 CSS 中 `animation: <name> <dur>ms ... [<delay>ms] ...;` 的总时长（时长 + 延迟） */
const animationTotal = (css: string, name: string) => {
  const declaration = css.match(new RegExp(`animation:\\s*${name}\\s[^;]*;`))?.[0];
  if (!declaration) throw new Error(`animation ${name} not found`);
  const [duration = 0, delay = 0] = [...declaration.matchAll(/(\d+)ms/g)].map((m) => Number(m[1]));
  return duration + delay;
};

beforeAll(async () => {
  await i18n.changeLanguage('en');
});

afterEach(() => {
  useLoginTransitionStore.setState({ phase: 'idle', revealId: 0 });
});

describe('login transition store', () => {
  test('starts and finishes a reveal round', () => {
    expect(isArrivingFromLogin()).toBe(false);
    useLoginTransitionStore.getState().startReveal();
    expect(useLoginTransitionStore.getState()).toMatchObject({ phase: 'revealing', revealId: 1 });
    expect(isArrivingFromLogin()).toBe(true);
    useLoginTransitionStore.getState().startReveal();
    expect(useLoginTransitionStore.getState().revealId).toBe(2);
    useLoginTransitionStore.getState().finishReveal();
    expect(useLoginTransitionStore.getState().phase).toBe('idle');
  });

  test('keeps the whole hand-off brisk, and shorter still with reduced motion', () => {
    const total = (t: typeof LOGIN_TRANSITION_TIMINGS) => t.successHold + t.exit + t.reveal;
    expect(LOGIN_TRANSITION_TIMINGS.exit).toBeLessThan(LOGIN_TRANSITION_TIMINGS.reveal);
    expect(total(LOGIN_TRANSITION_TIMINGS)).toBeLessThanOrEqual(1300);
    expect(total(LOGIN_TRANSITION_TIMINGS_REDUCED)).toBeLessThan(total(LOGIN_TRANSITION_TIMINGS));
    expect(LOGIN_TRANSITION_TIMINGS_REDUCED.successHold).toBeGreaterThan(0);
    expect(getLoginTransitionTimings(true)).toBe(LOGIN_TRANSITION_TIMINGS_REDUCED);
    expect(getLoginTransitionTimings(false)).toBe(LOGIN_TRANSITION_TIMINGS);
  });

  test('CSS durations stay in sync with the orchestration timings', async () => {
    const veil = await Bun.file('src/components/common/LoginRevealVeil.scss').text();
    const page = await Bun.file('src/pages/LoginPage.module.scss').text();
    const layout = await Bun.file('src/styles/layout.scss').text();

    expect(animationTotal(veil, 'loginVeilOut')).toBe(LOGIN_TRANSITION_TIMINGS.reveal);
    expect(animationTotal(page, 'exitToBase')).toBe(LOGIN_TRANSITION_TIMINGS.exit);
    expect(animationTotal(page, 'exitLift')).toBe(LOGIN_TRANSITION_TIMINGS.exit);
    // 外壳入场必须在揭幕结束前落定，否则撤销类名时会跳变
    for (const name of ['shellSidebarIn', 'shellChromeIn', 'shellFadeIn']) {
      expect(animationTotal(layout, name)).toBeLessThanOrEqual(LOGIN_TRANSITION_TIMINGS.reveal);
    }
  });
});

describe('LoginRevealVeil', () => {
  test('renders nothing outside a reveal', () => {
    expect(renderToStaticMarkup(createElement(LoginRevealVeil))).toBe('');
  });

  test('covers the screen without trapping pointer or screen reader focus while revealing', () => {
    const markup = renderToStaticMarkup(createElement(LoginRevealVeilLayer, { onDone: noop }));
    expect(markup).toContain('class="login-reveal-veil"');
    expect(markup).toContain('aria-hidden="true"');
  });

  test('is mounted outside the routes so it survives the route switch', async () => {
    const app = await Bun.file('src/App.tsx').text();
    expect(app).toMatch(/<Outlet \/>\s*<LoginRevealVeil \/>/);
  });
});

describe('LoginForm success state', () => {
  test('confirms in place on the button and locks the form', () => {
    const markup = renderToStaticMarkup(createElement(LoginForm, { ...formProps, success: true }));
    expect(markup).toContain('data-state="success"');
    expect(markup).toContain('role="status"');
    expect(markup).toContain(i18n.t('common.connected_status'));
    expect(markup).toMatch(/type="submit"[^>]*disabled=""/);
    expect(markup).toContain('readOnly=""');
    expect(markup).not.toContain(i18n.t('login.submit_button') + '<');
  });
});

describe('login hand-off wiring', () => {
  test('login page orchestrates exit then reveal instead of jumping straight to the app', async () => {
    const source = await Bun.file('src/pages/LoginPage.tsx').text();
    expect(source).toContain("setPhase('success')");
    expect(source).toContain("setPhase('exiting')");
    expect(source).toMatch(/startReveal\(\);\s*navigate\(redirect, \{ replace: true \}\);/);
    expect(source).toContain("phase === 'idle'");
    expect(source).not.toContain("showNotification(t('common.connected_status')");
  });

  test('main layout marks the shell as arriving only during the reveal', async () => {
    const source = await Bun.file('src/components/layout/MainLayout.tsx').text();
    expect(source).toContain("state.phase === 'revealing'");
    expect(source).toContain('app-shell--arriving');
  });
});
