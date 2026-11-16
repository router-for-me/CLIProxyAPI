import { describe, expect, test } from 'bun:test';
import { createElement } from 'react';
import { renderToStaticMarkup } from 'react-dom/server';
import {
  SegmentedControl,
  type SegmentedControlProps,
  type SegmentedOption,
} from '@/components/ui/SegmentedControl';
import { segmentKeyTarget } from '@/components/ui/segmentKeyTarget';

const OPTIONS: readonly SegmentedOption<string>[] = [
  { value: 'remaining', label: 'Remaining' },
  { value: 'drain', label: 'Drain rate' },
  { value: 'other', label: 'Other', ariaLabel: 'Other metric', disabled: true },
];

const renderControl = (props: Partial<SegmentedControlProps<string>> = {}) =>
  renderToStaticMarkup(
    createElement(SegmentedControl<string>, {
      ariaLabel: 'Metric',
      onChange: () => undefined,
      options: OPTIONS,
      value: 'drain',
      ...props,
    })
  );

const buttonsOf = (markup: string) => markup.match(/<button[^>]*>[^<]*<\/button>/g) ?? [];

describe('SegmentedControl', () => {
  test('renders a labelled radio group', () => {
    const markup = renderControl();

    expect(markup).toMatch(/^<div role="radiogroup" aria-label="Metric"/);
    expect(buttonsOf(markup)).toHaveLength(3);
    expect(markup.match(/role="radio"/g)).toHaveLength(3);
    expect(markup).toContain('>Remaining</button>');
    expect(markup).toContain('>Drain rate</button>');
    expect(markup).toContain('aria-label="Other metric"');
  });

  test('checks one option and makes it the only tab stop', () => {
    const buttons = buttonsOf(renderControl());
    const checked = buttons.filter((button) => button.includes('aria-checked="true"'));
    const tabStops = buttons.filter((button) => button.includes('tabindex="0"'));

    expect(checked).toHaveLength(1);
    expect(checked[0]).toContain('>Drain rate<');
    expect(checked[0]).toContain('data-checked="true"');
    expect(tabStops).toEqual(checked);
    expect(buttons.filter((button) => button.includes('tabindex="-1"'))).toHaveLength(2);
  });

  test('disables only the disabled option', () => {
    const disabled = buttonsOf(renderControl()).filter((button) => button.includes('disabled=""'));

    expect(disabled).toHaveLength(1);
    expect(disabled[0]).toContain('>Other<');
  });

  test('disables every option when the group is disabled', () => {
    const buttons = buttonsOf(renderControl({ disabled: true }));

    expect(buttons.every((button) => button.includes('disabled=""'))).toBe(true);
  });

  test('falls back to the first enabled option as the tab stop', () => {
    const buttons = buttonsOf(renderControl({ value: 'missing' }));

    expect(buttons.some((button) => button.includes('aria-checked="true"'))).toBe(false);
    expect(buttons.filter((button) => button.includes('tabindex="0"'))).toHaveLength(1);
    expect(buttons[0]).toContain('tabindex="0"');
  });

  test('supports aria-labelledby and numeric values', () => {
    const markup = renderToStaticMarkup(
      createElement(SegmentedControl<number>, {
        ariaLabelledBy: 'burn-label',
        onChange: () => undefined,
        options: [
          { value: 1, label: '1h' },
          { value: 3, label: '3h' },
        ],
        value: 3,
      })
    );

    expect(markup).toContain('aria-labelledby="burn-label"');
    expect(markup).not.toContain('aria-label=');
    expect(markup).toMatch(/aria-checked="true"[^>]*>3h</);
  });

  test('moves with the radio-group keys, wrapping and skipping disabled options', () => {
    const isEnabled = (index: number) => !OPTIONS[index].disabled;
    const target = (key: string, index: number) =>
      segmentKeyTarget(key, index, OPTIONS.length, isEnabled);

    expect(target('ArrowDown', 0)).toBe(1);
    expect(target('ArrowRight', 0)).toBe(1);
    expect(target('ArrowDown', 1)).toBe(0);
    expect(target('ArrowUp', 1)).toBe(0);
    expect(target('ArrowLeft', 0)).toBe(1);
    expect(target('ArrowUp', 0)).toBe(1);
    expect(target('Home', 1)).toBe(0);
    expect(target('End', 0)).toBe(1);
    expect(target('Enter', 0)).toBe(-1);
    expect(segmentKeyTarget('ArrowDown', 0, OPTIONS.length, () => false)).toBe(-1);
  });
});
