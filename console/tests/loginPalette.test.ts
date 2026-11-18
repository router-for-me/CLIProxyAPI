import { describe, expect, test } from 'bun:test';

const palette = await Bun.file('src/pages/login/_palette.scss').text();
const veil = await Bun.file('src/components/common/LoginRevealVeil.scss').text();

/** 取出某个 mixin 的 --login-* 变量表 */
const tokens = (mixin: string): Record<string, string> => {
  const start = palette.indexOf(`@mixin ${mixin} {`);
  if (start < 0) throw new Error(`mixin ${mixin} not found`);
  const body = palette.slice(start, palette.indexOf('\n}', start));
  return Object.fromEntries(
    [...body.matchAll(/--(login-[\w-]+):\s*([^;]+);/g)].map((match) => [match[1], match[2].trim()])
  );
};

const hexToRgb = (hex: string) => {
  const value = hex.replace('#', '');
  return [0, 2, 4].map((offset) => parseInt(value.slice(offset, offset + 2), 16));
};

const luminance = (hex: string) => {
  const [r, g, b] = hexToRgb(hex).map((channel) => {
    const c = channel / 255;
    return c <= 0.03928 ? c / 12.92 : ((c + 0.055) / 1.055) ** 2.4;
  });
  return 0.2126 * r + 0.7152 * g + 0.0722 * b;
};

const contrast = (a: string, b: string) => {
  const [hi, lo] = [luminance(a), luminance(b)].sort((x, y) => y - x);
  return (hi + 0.05) / (lo + 0.05);
};

const light = tokens('login-tokens-light');
const dark = tokens('login-tokens-dark');
const backgrounds = {
  light: [light['login-bg'], '#ffffff'], // 暖纸底 + 纯白主题
  dark: [dark['login-bg']],
};

describe('login palette', () => {
  test('light and dark define the same set of tokens', () => {
    expect(Object.keys(light).sort()).toEqual(Object.keys(dark).sort());
  });

  test('white theme only swaps the background to pure white', () => {
    expect(palette).toMatch(
      /html\[data-theme='white'\]\) & \{\s*--login-bg: #ffffff;\s*--bg-primary: #ffffff;\s*\}/
    );
    expect(palette).toMatch(/html\[data-theme='dark'\]\) & \{\s*@include login-tokens-dark;/);
  });

  for (const [name, set, bgs] of [
    ['light', light, backgrounds.light],
    ['dark', dark, backgrounds.dark],
  ] as const) {
    test(`${name}: body copy, title and errors meet WCAG AA (4.5:1)`, () => {
      for (const bg of bgs) {
        expect(contrast(set['login-text'], bg)).toBeGreaterThanOrEqual(4.5);
        expect(contrast(set['login-text-muted'], bg)).toBeGreaterThanOrEqual(4.5);
        expect(contrast(set['login-title'], bg)).toBeGreaterThanOrEqual(4.5);
        expect(contrast(set['login-error'], bg)).toBeGreaterThanOrEqual(4.5);
      }
    });

    test(`${name}: placeholders/icons and the primary button stay legible`, () => {
      for (const bg of bgs) {
        expect(contrast(set['login-text-faint'], bg)).toBeGreaterThanOrEqual(3);
      }
      expect(contrast(set['login-submit-fg'], set['login-submit-bg'])).toBeGreaterThanOrEqual(7);
    });
  }

  test('the transition veil matches the login background in every theme', () => {
    expect(veil).toContain(`background: ${light['login-bg']};`);
    expect(veil).toMatch(
      /html\[data-theme='white'\] \.login-reveal-veil \{\s*background: #ffffff;/
    );
    expect(veil).toMatch(
      new RegExp(
        `html\\[data-theme='dark'\\] \\.login-reveal-veil \\{\\s*background: ${dark['login-bg']};`
      )
    );
  });

  test('login styles carry no hard-coded dark-only colors', async () => {
    for (const path of [
      'src/pages/LoginPage.module.scss',
      'src/pages/login/LoginForm.module.scss',
      'src/pages/login/DotMatrixHero.module.scss',
    ]) {
      const css = (await Bun.file(path).text()).replace(/\/\/.*$/gm, '');
      expect(css).not.toMatch(/rgba\(255, 255, 255/);
      expect(css).not.toMatch(/#(?:000000|ffffff|f5f5f4|0a0a0a|111111)\b/i);
    }
  });

  test('the dot canvas takes its color from the theme and repaints on theme change', async () => {
    const hero = await Bun.file('src/pages/login/DotMatrixHero.tsx').text();
    expect(hero).toContain("getPropertyValue('--login-dot-color')");
    expect(hero).toContain("attributeFilter: ['data-theme']");
  });
});
