import { beforeAll, describe, expect, test } from 'bun:test';
import { createElement } from 'react';
import { renderToStaticMarkup } from 'react-dom/server';
import i18n from '@/i18n';
import { PoolControls, type PoolControlsProps } from '@/features/quota/components/PoolControls';
import { poolRange } from '@/features/quota/pool/calendar';
import { NOW, TZ } from './fixtures/quotaPoolFixture';

const noop = () => undefined;

const renderControls = (overrides: Partial<PoolControlsProps> = {}) =>
  renderToStaticMarkup(
    createElement(PoolControls, {
      accounts: false,
      burnWindow: 3,
      futureUsage: 'same',
      locale: 'en',
      metric: 'remaining',
      onAccountsChange: noop,
      onBurnWindowChange: noop,
      onFutureUsageChange: noop,
      onMetricChange: noop,
      onNext: noop,
      onPrev: noop,
      onToday: noop,
      range: poolRange(NOW, 0, TZ),
      timeZone: TZ,
      ...overrides,
    })
  );

const buttonWith = (markup: string, text: string) =>
  markup.match(new RegExp(`<button[^>]*>(?:<[^>]+>)*${text}(?:<[^>]+>)*</button>`))?.[0] ?? '';

const buttonLabelled = (markup: string, label: string) =>
  markup.match(new RegExp(`<button[^>]*aria-label="${label}"[^>]*>`))?.[0] ?? '';

beforeAll(async () => {
  await i18n.changeLanguage('en');
});

describe('PoolControls', () => {
  test('renders the metric and burn window as radio groups with the defaults checked', () => {
    const markup = renderControls();

    expect(markup.match(/role="radiogroup"/g)).toHaveLength(2);
    expect(markup).toContain('aria-label="Chart metric"');
    expect(buttonWith(markup, 'Remaining')).toContain('aria-checked="true"');
    expect(buttonWith(markup, 'Drain rate')).toContain('aria-checked="false"');
    expect(buttonWith(markup, '3h')).toContain('aria-checked="true"');
    expect(buttonWith(markup, '1h')).toContain('aria-checked="false"');
  });

  test('labels the burn window and future usage controls by their visible text', () => {
    const markup = renderControls();
    const burnId = markup.match(/<span id="([^"]+)">Burn rate<\/span>/)?.[1];
    const futureId = markup.match(/<span id="([^"]+)">Future usage<\/span>/)?.[1];

    expect(burnId).toBeTruthy();
    expect(futureId).toBeTruthy();
    expect(markup).toContain(`role="radiogroup" aria-labelledby="${burnId}"`);
    expect(markup).toContain(`aria-labelledby="${futureId}"`);
    expect(markup).toContain('Same as now');
  });

  test('renders the Accounts toggle as a pressed state', () => {
    expect(buttonWith(renderControls(), 'Accounts')).toContain('aria-pressed="false"');
    expect(buttonWith(renderControls({ accounts: true }), 'Accounts')).toContain(
      'aria-pressed="true"'
    );
  });

  test('shows the range and limits navigation', () => {
    const current = renderControls();
    const earliest = renderControls({ range: poolRange(NOW, -3, TZ) });
    const latest = renderControls({ range: poolRange(NOW, 1, TZ) });

    expect(current).toContain('Oct 23 to Nov 5');
    expect(buttonWith(current, 'Today')).toContain('aria-current="date"');
    expect(buttonLabelled(current, 'Previous week')).not.toContain('disabled');
    expect(buttonLabelled(earliest, 'Previous week')).toContain('disabled=""');
    expect(buttonWith(earliest, 'Today')).not.toContain('aria-current');
    expect(buttonLabelled(latest, 'Next week')).toContain('disabled=""');
  });

  test('disables every control while disabled', () => {
    const markup = renderControls({ disabled: true });
    const buttons = markup.match(/<button[^>]*>/g) ?? [];

    expect(buttons.length).toBeGreaterThan(0);
    expect(buttons.every((button) => button.includes('disabled=""'))).toBe(true);
  });
});
