import { beforeAll, describe, expect, test } from 'bun:test';
import { readFileSync } from 'node:fs';
import { createElement } from 'react';
import { renderToStaticMarkup } from 'react-dom/server';
import i18n from '@/i18n';
import { PoolChart } from '@/features/quota/components/PoolChart';
import { tooltipText } from '@/features/quota/components/poolChartScene';
import type { PoolProvider } from '@/features/quota/pool/constants';
import { buildPoolCardModel } from '@/features/quota/pool/model';
import type { PoolModelInput } from '@/features/quota/pool/types';
import { HOUR_MS } from '@/utils/time/durations';
import {
  CLAUDE_RESETS,
  NOW,
  TZ,
  claudeFiles,
  claudeName,
  claudeOutageFiles,
  historyCredential,
  historyWithOutage,
  okHistory,
  poolModelInput,
} from './fixtures/quotaPoolFixture';

const chartSource = readFileSync('src/features/quota/components/PoolChart.tsx', 'utf8');
const chartStyles = readFileSync('src/features/quota/components/PoolChart.module.scss', 'utf8');

const renderChart = (
  provider: PoolProvider,
  {
    metric = 'remaining',
    width = 1100,
    ...overrides
  }: Partial<PoolModelInput> & { width?: number } = {}
) => {
  const model = buildPoolCardModel(poolModelInput(provider, { metric, ...overrides }));

  if (!model) throw new Error(`no ${provider} pool model`);

  return renderToStaticMarkup(
    createElement(PoolChart, { locale: 'en', metric, model, timeZone: TZ, width })
  );
};

const textY = (markup: string, text: string): number => {
  const match = markup.match(
    new RegExp(`<text[^>]* y="([\\d.]+)"[^>]*>${text.replace('+', '\\+')}</text>`)
  );

  if (!match) throw new Error(`no <text> reading ${text}`);

  return Number(match[1]);
};

const dayLabels = (markup: string) =>
  [...markup.matchAll(/<text[^>]*>((?:Mon|Tue|Wed|Thu|Fri|Sat|Sun) \d+)<\/text>/g)].map(
    (match) => match[1]
  );

const ruleBody = (selector: string) =>
  chartStyles.match(new RegExp(`^${selector.replace('.', '\\.')} \\{([^}]*)\\}`, 'm'))?.[1] ?? '';

beforeAll(async () => {
  await i18n.changeLanguage('en');
});

describe('PoolChart', () => {
  test('draws recorded and dashed projected series with markers and axes', () => {
    const markup = renderChart('claude');

    expect(markup).toContain('role="img"');
    expect(markup).toContain('aria-label="Claude pooled quota chart"');
    expect(markup).toMatch(/<path[^>]*data-series="recorded"/);
    expect(markup).toMatch(/<path[^>]*data-series="projected" stroke-dasharray="8 4"/);
    expect(markup).toContain('>Now</text>');
    expect(markup).toContain('data-marker="now"');
    expect(markup).not.toContain('data-marker="empty"');

    for (const label of ['600%', '300%', '0%']) {
      expect(markup).toContain(`>${label}</text>`);
    }
  });

  test('labels all 14 local days', () => {
    const labels = dayLabels(renderChart('claude'));

    expect(labels).toHaveLength(14);
    expect(labels[0]).toBe('Fri 23');
    expect(labels[13]).toBe('Thu 5');
  });

  test('places step labels in the band above the plot, or mid-riser when the riser is tall', () => {
    const claude = renderChart('claude');
    const codex = renderChart('codex');
    const claudeY = textY(claude, '+48%');

    expect(claude).toContain('>+100%</text>');
    expect(claudeY).toBeLessThan(30);
    expect(claudeY).toBeGreaterThanOrEqual(12);
    expect(textY(codex, '+300%')).toBeGreaterThan(30);
  });

  test('marks the Codex empty point', () => {
    const markup = renderChart('codex');

    expect(markup).toContain('data-marker="empty"');
    expect(markup).toContain('>Empty Sun 4:39 PM</text>');
    expect(markup).toContain('>300%</text>');
    expect(markup).toContain('>150%</text>');
  });

  test('renders the legend and a screen-reader summary', () => {
    const markup = renderChart('claude');

    expect(markup).toContain('<figcaption');
    for (const text of ['Pool', 'Recorded', 'Projected']) {
      expect(markup).toMatch(new RegExp(`>${text}</span>|</span>${text}</span>`));
    }
    expect(markup).not.toContain('Partial data');
    expect(markup).toContain('Claude: 555% of 600% left. Burn 3.7%/h. Pool empty: Not by Nov 9.');
    expect(markup).toContain('role="group" tabindex="0"');
  });

  test('styles legend items apart from the Pool label', () => {
    expect(chartSource).toContain(
      "<span className={styles.legendLabel}>{t('quota_pool.legend_pool')}"
    );
    expect(chartSource.match(/className=\{styles\.legendItem\}/g)).toHaveLength(3);
    expect(ruleBody('.legendLabel')).toContain('color: var(--text-secondary);');
    expect(ruleBody('.legendItem')).toContain('color: var(--text-tertiary);');
  });

  test('draws no recorded series without history', () => {
    const markup = renderChart('claude', { history: null });

    expect(markup).not.toContain('data-series="recorded"');
    expect(markup).not.toContain('Recorded');
    expect(markup).not.toContain('data-series="projected"');
    expect(markup).toContain('data-marker="now"');
  });

  test('switches to rates without step labels or the empty marker in drain view', () => {
    const markup = renderChart('codex', { metric: 'drain' });

    expect(markup).toMatch(/>[\d.]+%\/h<\/text>/);
    expect(markup).not.toContain('>+300%</text>');
    expect(markup).not.toContain('data-marker="empty"');
    expect(markup).toMatch(/data-series="projected" stroke-dasharray="8 4"/);
  });

  test('thins day labels on a phone-width chart', () => {
    const labels = dayLabels(renderChart('claude', { width: 390 }));

    expect(labels.length).toBeGreaterThanOrEqual(1);
    expect(labels.length).toBeLessThan(14);
    expect(labels[0]).toBe('Fri 23');
  });
});

describe('tooltipText', () => {
  const tooltipAt = (overrides: Partial<PoolModelInput>, cursor: number) => {
    const model = buildPoolCardModel(poolModelInput('claude', overrides));

    if (!model) throw new Error('no claude pool model');

    return tooltipText(model, 'remaining', cursor, i18n.t, 'en', TZ);
  };

  test('reports coverage only when some accounts have no reading yet', () => {
    const files = claudeFiles().slice(0, 2);
    const history = okHistory([
      historyCredential(claudeName('a'), 'claude', [
        { resetAt: CLAUDE_RESETS[0], t: NOW - 6 * HOUR_MS, used: 0.3 },
        { resetAt: CLAUDE_RESETS[0], t: NOW - 3 * HOUR_MS, used: 0.3 },
      ]),
      historyCredential(claudeName('b'), 'claude', [
        { resetAt: CLAUDE_RESETS[1], t: NOW - 3 * HOUR_MS, used: 0 },
      ]),
    ]);

    expect(tooltipAt({ files, history }, NOW - 4 * HOUR_MS)).toEndWith(' · 1 of 2 accounts');
  });

  test('omits the coverage note on a stale stretch where every account is known', () => {
    const text = tooltipAt(
      { files: claudeOutageFiles(), history: historyWithOutage() },
      NOW - 24 * HOUR_MS
    );

    expect(text).toContain('% left · recorded');
    expect(text).not.toContain('accounts');
  });
});
