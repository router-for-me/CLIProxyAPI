import { describe, expect, test } from 'bun:test';
import { existsSync, readdirSync, readFileSync } from 'node:fs';

const readSource = (path: string) => readFileSync(new URL(`../${path}`, import.meta.url), 'utf8');

const themes = readSource('src/styles/themes.scss');

const THEME_TOKENS = [
  '--bg-secondary',
  '--bg-primary',
  '--bg-tertiary',
  '--bg-hover',
  '--bg-selected',
  '--bg-quinary',
  '--floating-surface',
  '--floating-shadow',
  '--text-primary',
  '--text-secondary',
  '--text-tertiary',
  '--text-quaternary',
  '--border-color',
  '--border-primary',
  '--border-hover',
  '--primary-color',
  '--primary-hover',
  '--primary-active',
  '--primary-contrast',
  '--primary-gradient',
  '--secondary-gradient',
  '--count-badge-bg',
  '--count-badge-text',
  '--shadow',
  '--shadow-lg',
  '--radius-sm',
  '--focus-ring-color',
  '--accent-color',
  '--viz-claude',
  '--viz-codex',
  '--viz-grid',
  '--viz-grid-day',
  '--viz-axis',
  '--viz-now',
  '--viz-area-recorded-opacity',
  '--viz-area-projected-opacity',
  '--viz-crosshair-opacity',
];

const THEME_BLOCKS = [
  { name: 'light', selector: ':root', colorScheme: 'light' },
  { name: 'white', selector: "[data-theme='white']", colorScheme: 'light' },
  { name: 'dark', selector: "[data-theme='dark']", colorScheme: 'dark' },
];

const escapeRegExp = (text: string) => text.replace(/[.*+?^${}()|[\]\\]/g, '\\$&');

const topLevelBlocks = (source: string, selector: string) =>
  [...source.matchAll(new RegExp(`^${escapeRegExp(selector)} \\{\\n([\\s\\S]*?)^\\}`, 'gm'))].map(
    (match) => match[1]
  );

const themeBlock = (selector: string) => topLevelBlocks(themes, selector)[0] ?? '';

describe('theme tokens', () => {
  for (const { name, selector, colorScheme } of THEME_BLOCKS) {
    test(`${name} declares every themed token`, () => {
      const block = themeBlock(selector);
      const missing = THEME_TOKENS.filter((token) => !block.includes(`\n  ${token}: `));

      expect(missing).toEqual([]);
      expect(block).toContain(`color-scheme: ${colorScheme};`);
    });
  }

  test('dark uses the sampled mock surfaces', () => {
    const dark = themeBlock("[data-theme='dark']");

    expect(dark).toContain('--bg-secondary: #000000;');
    expect(dark).toContain('--bg-primary: #0e0e0e;');
    expect(dark).toContain('--bg-selected: #1f1f1f;');
  });

  test('no legacy warm palette remains', () => {
    for (const hex of ['#151412', '#1d1b18', '#faf9f5', '#8b8680', '#f0eee8']) {
      expect(themes.toLowerCase()).not.toContain(hex);
    }
  });
});

describe('hard-coded colors', () => {
  test('the SCSS primary color is neutral', () => {
    expect(readSource('src/styles/variables.scss')).toContain('$primary-color: #8f8f8f;');
  });

  test('dark theme does not force white button text', () => {
    const components = readSource('src/styles/components.scss');

    for (const block of topLevelBlocks(components, "[data-theme='dark']")) {
      expect(block).not.toContain('color: #fff;');
    }
  });

  test('the shell has no warm tooltip or blur tint', () => {
    const layout = readSource('src/styles/layout.scss');

    expect(layout).not.toContain('#24211c');
    expect(layout).not.toContain('#0d0c0a');
  });

  test('pool modules use tokens only', () => {
    const directory = 'src/features/quota/components';
    const directoryUrl = new URL(`../${directory}`, import.meta.url);
    const modules = existsSync(directoryUrl)
      ? readdirSync(directoryUrl).filter(
          (file) => /^Pool\w*\.module\.scss$/.test(file) || file === 'MaskedName.module.scss'
        )
      : [];

    for (const file of modules) {
      expect(readSource(`${directory}/${file}`)).not.toMatch(/#[0-9a-fA-F]{3,8}\b/);
    }
  });
});

describe('top scrim', () => {
  test('follows the document scroller at phone width, where .content does not scroll', () => {
    const layout = readSource('src/styles/layout.scss');
    const mobile = layout.slice(
      layout.indexOf('@media (max-width: $breakpoint-mobile) {\n  :root')
    );
    const scrim = mobile.match(/\n {2}\.top-gradient-blur \{([^}]*)\}/)?.[1] ?? '';

    expect(layout).toContain('animation-timeline: --content-scroll;');
    expect(scrim).toContain('animation-timeline: scroll(root block);');
  });
});
