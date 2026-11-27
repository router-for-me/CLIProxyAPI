import { describe, expect, test } from 'bun:test';

function findUndefinedReads(files: [string, string][]) {
  const defined = new Set<string>();
  const required = new Map<string, string>();

  for (const [path, source] of files) {
    const definitions = /\.s?css$/.test(path)
      ? [/(?<![\w-])(--[\w-]+)\s*:/g]
      : [/['"](--[\w-]+)['"]\s*:/g, /\.setProperty\(\s*['"`](--[\w-]+)['"`]\s*,/g];
    for (const pattern of definitions) {
      for (const [, name] of source.matchAll(pattern)) defined.add(name);
    }
    // Only calls without a fallback require a definition. Nested var() calls are
    // matched independently, so a required variable inside a fallback is still checked.
    for (const [, name] of source.matchAll(/var\(\s*(--[\w-]+)\s*\)/g)) {
      if (!required.has(name)) required.set(name, path);
    }
  }
  return [...required].filter(([name]) => !defined.has(name));
}

describe('CSS custom properties', () => {
  test('allows optional properties but checks required reads inside fallbacks', () => {
    expect(
      findUndefinedReads([
        [
          'a.css',
          'a { color: var(--optional, currentColor); background: var(--bg, var(--missing)); }',
        ],
      ])
    ).toEqual([['--missing', 'a.css']]);
  });

  test('counts runtime writes but not reads or removals as definitions', () => {
    expect(
      findUndefinedReads([
        [
          'a.scss',
          'a { --local: red; color: var(--local); padding: var(--inline); top: var(--set); left: var(--typo); }',
        ],
        [
          'a.tsx',
          `const style = { '--inline': 1 }; el.style.setProperty('--set', '0'); el.style.getPropertyValue('--typo'); el.style.removeProperty('--typo');`,
        ],
      ])
    ).toEqual([['--typo', 'a.scss']]);
  });

  test('required var() reads in src have definitions', async () => {
    const files: [string, string][] = [];
    for await (const path of new Bun.Glob('src/**/*.{scss,css,ts,tsx}').scan('.')) {
      files.push([path, await Bun.file(path).text()]);
    }
    expect(findUndefinedReads(files)).toEqual([]);
  });
});
