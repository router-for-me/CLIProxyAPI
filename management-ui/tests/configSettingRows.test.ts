// 设置行原语：开关 / 输入 / 下拉 / 区块编辑器共用一副骨架，
// 两列网格的落单补整行逻辑，以及每种行的标签与描述关联。

import { describe, expect, test } from 'bun:test';
import { createElement, Fragment } from 'react';
import { renderToStaticMarkup } from 'react-dom/server';
import {
  BlockSetting,
  SelectSetting,
  SettingList,
  TextSetting,
  ToggleSetting,
} from '@/features/config/components/fields/FieldPrimitives';
import { resolveRowSpans } from '@/features/config/components/fields/rowSpans';

const noop = () => {};
const render = (element: ReturnType<typeof createElement>) => renderToStaticMarkup(element);
const attr = (tag: string, name: string) => tag.match(new RegExp(`\\b${name}="([^"]*)"`))?.[1];

/** 回显 SettingList 注入的 wide，用来观察补整行结果。 */
function Probe({ name, wide }: { name: string; wide?: boolean }) {
  return createElement('i', { 'data-name': name, 'data-wide': String(Boolean(wide)) });
}
const probe = (name: string, wide?: boolean) => createElement(Probe, { key: name, name, wide });
const spansOf = (markup: string) =>
  [...markup.matchAll(/data-name="([^"]+)" data-wide="(true|false)"/g)].map(
    ([, name, wide]) => `${name}:${wide}`
  );

describe('two-column row spans', () => {
  test('pairs rows and lets a trailing orphan fill its row', () => {
    expect(resolveRowSpans([false, false])).toEqual([false, false]);
    expect(resolveRowSpans([false, false, false])).toEqual([false, false, true]);
    expect(resolveRowSpans([false])).toEqual([true]);
    expect(resolveRowSpans([])).toEqual([]);
  });

  test('a row left alone before a full-width row spans instead of leaving a hole', () => {
    // a b / c [wide] / d → c 与 d 都会落单
    expect(resolveRowSpans([false, false, false, true, false])).toEqual([
      false,
      false,
      true,
      true,
      true,
    ]);
    // [wide] a b → 显式整行后重新从行首配对
    expect(resolveRowSpans([true, false, false])).toEqual([true, false, false]);
  });

  test('SettingList flattens fragments and injects wide only into orphans', () => {
    const markup = render(
      createElement(
        SettingList,
        null,
        probe('a'),
        probe('b'),
        createElement(Fragment, null, probe('c'), probe('d', true)),
        null,
        false,
        probe('e')
      )
    );
    expect(spansOf(markup)).toEqual(['a:false', 'b:false', 'c:true', 'd:true', 'e:true']);
  });

  test('renders an optional group heading above the list', () => {
    const markup = render(
      createElement(SettingList, { title: 'Codex', description: 'Group hint' }, probe('a'))
    );
    expect(markup).toContain('<h3');
    expect(markup).toContain('Codex');
    expect(markup).toContain('Group hint');
    expect(render(createElement(SettingList, null, probe('a')))).not.toContain('<h3');
  });
});

describe('setting row accessibility', () => {
  test('toggle rows label the switch and describe it with the hint', () => {
    const markup = render(
      createElement(ToggleSetting, {
        fieldId: 'debug',
        label: 'Debug',
        description: 'Verbose logs',
        checked: true,
        onChange: noop,
      })
    );
    expect(markup).toContain('id="cfg-field-debug"');
    const input = markup.match(/<input\b[^>]*type="checkbox"[^>]*>/)?.[0] ?? '';
    expect(input).toContain('checked=""');
    const id = attr(input, 'id');
    expect(id).toBeTruthy();
    expect(markup).toContain(`for="${id}"`);
    expect(attr(input, 'aria-describedby')).toBe(`${id}-hint`);
    expect(markup).toContain(`id="${id}-hint"`);
  });

  test('text rows put the error before the hint and flag the input invalid', () => {
    const markup = render(
      createElement(TextSetting, {
        fieldId: 'port',
        label: 'Port',
        description: 'Listen port',
        error: 'Invalid port',
        type: 'number',
        value: '',
        onChange: noop,
        disabled: true,
      })
    );
    const input = markup.match(/<input\b[^>]*>/)?.[0] ?? '';
    const id = attr(input, 'id');
    expect(markup).toContain(`for="${id}"`);
    expect(attr(input, 'aria-describedby')).toBe(`${id}-error ${id}-hint`);
    expect(input).toContain('aria-invalid="true"');
    expect(input).toContain('type="number"');
    expect(input).toContain('disabled=""');
    expect(input).not.toContain('min=');
    expect(markup).toContain('Invalid port');
  });

  test('adornments render inside the control without replacing the value', () => {
    const markup = render(
      createElement(TextSetting, {
        fieldId: 'streamingKeepaliveSeconds',
        label: 'Keepalive',
        value: '0',
        adornment: 'Disabled',
        onChange: noop,
      })
    );
    expect(markup).toContain('value="0"');
    expect(markup).toContain('Disabled');
    expect(markup).not.toContain('aria-describedby');
  });

  test('select rows label the trigger button by id and by aria-labelledby', () => {
    const markup = render(
      createElement(SelectSetting, {
        fieldId: 'routingStrategy',
        label: 'Strategy',
        value: 'a',
        options: [
          { value: 'a', label: 'Alpha' },
          { value: 'b', label: 'Beta' },
        ],
        onChange: noop,
      })
    );
    const button = markup.match(/<button\b[^>]*aria-haspopup="listbox"[^>]*>/)?.[0] ?? '';
    const id = attr(button, 'id');
    expect(markup).toContain(`for="${id}"`);
    const labelId = attr(button, 'aria-labelledby');
    expect(labelId).toContain('routingStrategy-label');
    expect(markup).toContain(`id="${labelId}"`);
    expect(markup).toContain('Alpha');
  });

  test('block rows wrap the editor in a named group and hand describedBy to it', () => {
    let received: { describedBy?: string; invalid: boolean } | undefined;
    const markup = render(
      createElement(BlockSetting, {
        fieldId: 'trustedProxies',
        label: 'Trusted proxies',
        description: 'CIDR list',
        error: 'Bad CIDR',
        children: (a11y: { describedBy?: string; invalid: boolean }) => {
          received = a11y;
          return createElement('textarea', { 'aria-describedby': a11y.describedBy });
        },
      })
    );
    const group = markup.match(/<div\b[^>]*role="group"[^>]*>/)?.[0] ?? '';
    const labelId = attr(group, 'aria-labelledby') ?? '';
    expect(labelId).toContain('trustedProxies-label');
    expect(markup).toContain(`id="${labelId}"`);
    expect(group).toContain('aria-invalid="true"');
    expect(received?.invalid).toBe(true);
    const ids = received?.describedBy?.split(' ') ?? [];
    expect(ids).toHaveLength(2);
    for (const id of ids) expect(markup).toContain(`id="${id}"`);
    expect(attr(group, 'aria-describedby')).toBe(received?.describedBy);
  });
});
