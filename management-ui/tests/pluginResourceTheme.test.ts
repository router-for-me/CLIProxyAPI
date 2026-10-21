import { describe, expect, test } from 'bun:test';
import { readFileSync } from 'node:fs';

const readSource = (path: string) => readFileSync(new URL(`../${path}`, import.meta.url), 'utf8');

// Source contracts only: these do not verify iframe painting in a browser.
describe('plugin resource theme backgrounds', () => {
  const page = readSource('src/features/plugins/PluginResourcePage.module.scss');
  const layout = readSource('src/styles/layout.scss');

  for (const selector of ['page', 'frame']) {
    test(`${selector} uses the theme background`, () => {
      const block = page.match(new RegExp(`\\.${selector}\\s*\\{([^}]+)\\}`))?.[1];
      expect(block).toContain('background: var(--bg-secondary);');
      expect(block).not.toMatch(/#fff(?:fff)?\b/i);
    });
  }

  test('plugin shell and transition layers use the theme background', () => {
    const block = layout.match(/&\.main-content-plugin-resource\s*\{([\s\S]*?)\n {2}\}/)?.[1];
    expect(block).toBeDefined();
    expect(block?.match(/background: var\(--bg-secondary\);/g)).toHaveLength(2);
    expect(block).not.toMatch(/#fff(?:fff)?\b/i);
  });
});
