import { describe, expect, test } from 'bun:test';
import { resolveDropdownStyle } from '@/components/ui/selectPosition';

const css = await Bun.file('src/pages/LoginPage.module.scss').text();

/** 取出某个顶层规则块（不含嵌套块）的声明文本 */
const ruleBody = (selector: string) => {
  const start = css.indexOf(`\n${selector} {`);
  if (start < 0) throw new Error(`rule ${selector} not found`);
  let depth = 0;
  let body = '';
  for (let i = css.indexOf('{', start); i < css.length; i += 1) {
    const char = css[i];
    if (char === '{') depth += 1;
    if (char === '}') depth -= 1;
    if (depth === 1 && char !== '{') body += char;
    if (depth === 0) break;
  }
  return body;
};

describe('login layout on narrow screens (390×844)', () => {
  test('the form column can shrink below its content width instead of overflowing', () => {
    const panel = ruleBody('.formPanel');
    expect(panel).toContain('grid-template-columns: minmax(0, 1fr)');
    expect(panel).toContain('min-width: min(400px, 100%)');
    expect(ruleBody('.main')).toContain('min-width: 0');
  });

  test('the language trigger no longer reserves a fixed width', () => {
    expect(css).not.toMatch(/min-width:\s*168px/);
  });
});

describe('login layout on short screens (390×280)', () => {
  test('the panel scrolls as a fallback while the page itself stays fixed', () => {
    expect(ruleBody('.container')).toContain('overflow: hidden');
    const panel = ruleBody('.formPanel');
    expect(panel).toContain('overflow-y: auto');
    expect(panel).toContain('overflow-x: hidden');
    expect(panel).toContain('overscroll-behavior: contain');
    expect(panel).toContain('grid-template-rows: auto 1fr');
  });

  test('centering cannot push content above the scrollable area', () => {
    const main = ruleBody('.main');
    expect(main).not.toContain('align-items: center;\n  justify-content: center');
    expect(main).not.toMatch(/^\s*min-height: 0/m);
    expect(ruleBody('.column')).toContain('margin-block: auto');
  });
});

describe('Select dropdown positioning', () => {
  const viewport = { width: 390, height: 844 };
  const trigger = { left: 300, right: 366, top: 19, bottom: 51, width: 66 };

  test('keeps the trigger width when no minimum is requested', () => {
    const style = resolveDropdownStyle(trigger, viewport.width, viewport.height);
    expect(style.width).toBe(66);
    expect(style.left).toBe(300);
  });

  test('widens to the minimum and right-aligns with a trigger near the right edge', () => {
    const style = resolveDropdownStyle(trigger, viewport.width, viewport.height, 180);
    expect(style.width).toBe(180);
    expect(style.left).toBe(366 - 180);
  });

  test('never exceeds the viewport', () => {
    const style = resolveDropdownStyle(trigger, 160, viewport.height, 180);
    expect(style.width).toBe(160 - 16);
    expect(style.left).toBe(8);
  });

  test('left-aligns when there is room to the right', () => {
    const left = { left: 24, right: 90, top: 19, bottom: 51, width: 66 };
    const style = resolveDropdownStyle(left, viewport.width, viewport.height, 180);
    expect(style.left).toBe(24);
    expect(style.width).toBe(180);
  });
});
