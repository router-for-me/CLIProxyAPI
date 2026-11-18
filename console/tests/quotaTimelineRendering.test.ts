import { describe, expect, test } from 'bun:test';
import { createElement } from 'react';
import { renderToStaticMarkup } from 'react-dom/server';
import i18n from '../src/i18n/index';
import { QuotaTimeline } from '../src/features/quota/components/QuotaTimeline';
import type { QuotaFileEntry } from '../src/features/quota/logic';
import { buildKimiQuotaRows } from '../src/utils/quota';

const entries: QuotaFileEntry[] = [
  {
    file: { name: 'weekly-only.json', type: 'claude' },
    type: 'claude',
  },
];

const baseProps = {
  entries,
  displayNameFor: (name: string) => name,
  resolvedTheme: 'light' as const,
  now: new Date(2026, 6, 29, 12).getTime(),
};

describe('QuotaTimeline rendering', () => {
  test('shows the selected period date instead of always labelling it Today', () => {
    const markup = renderToStaticMarkup(
      createElement(QuotaTimeline, {
        ...baseProps,
        initialOffset: 1,
        quotaFor: () => ({
          status: 'success',
          windows: [
            {
              label: '7-day',
              usedPercent: 25,
              resetAtMs: new Date(2026, 7, 1, 12).getTime(),
              periodHours: 168,
            },
          ],
        }),
      })
    );

    // The next weekly period starts on Sunday 08/02. The button remains the
    // shortcut back to Today (aria-label/title), but its visible label now
    // reflects the period selected with the previous/next arrows.
    expect(markup).toMatch(
      /<button type="button" aria-label="[^"]+" title="[^"]+">08\/02<\/button>/
    );
  });

  test('keeps the panel and controls visible when 5-hour mode has no matching lanes', () => {
    const weeklyOnlyQuota = {
      status: 'success' as const,
      windows: [
        {
          label: '7-day',
          usedPercent: 25,
          resetAtMs: new Date(2026, 7, 1, 12).getTime(),
          periodHours: 168,
        },
      ],
    };

    const markup = renderToStaticMarkup(
      createElement(QuotaTimeline, {
        ...baseProps,
        initialMode: 'session',
        quotaFor: () => weeklyOnlyQuota,
      })
    );

    expect(markup).toContain('<section');
    expect(markup).toContain('aria-pressed="true"');
    expect(markup).toContain('role="status"');
  });

  test('renders a Kimi 5-hour lane from the protobuf-style time unit', () => {
    const rows = buildKimiQuotaRows({
      usage: {
        used: '1',
        limit: '100',
        resetTime: '2099-08-06T13:59:23.136523Z',
      },
      limits: [
        {
          window: { duration: 300, timeUnit: 'TIME_UNIT_MINUTE' },
          detail: {
            used: '2',
            limit: '100',
            resetTime: '2099-07-31T06:59:23.136523Z',
          },
        },
      ],
    });

    const markup = renderToStaticMarkup(
      createElement(QuotaTimeline, {
        entries: [
          {
            file: { name: 'kimi-real-response.json', type: 'kimi' },
            type: 'kimi',
          },
        ],
        displayNameFor: (name: string) => name,
        resolvedTheme: 'light',
        now: new Date('2099-07-31T04:40:00Z').getTime(),
        initialMode: 'session',
        quotaFor: () => ({ status: 'success', rows }),
      })
    );

    expect(markup).toContain('kimi-real-response.json');
    expect(markup).not.toContain('role="status"');
  });

  test('renders an unexpired Codex reset credit as an expiry tick', () => {
    const markup = renderToStaticMarkup(
      createElement(QuotaTimeline, {
        entries: [
          {
            file: { name: 'codex-credit.json', type: 'codex' },
            type: 'codex',
          },
        ],
        displayNameFor: (name: string) => name,
        resolvedTheme: 'light',
        now: new Date(2026, 6, 29, 12).getTime(),
        quotaFor: () => ({
          status: 'success',
          windows: [
            {
              label: '7-day',
              usedPercent: 90,
              resetAtMs: new Date(2026, 7, 1, 12).getTime(),
              periodHours: 168,
            },
          ],
          rateLimitResetCredits: [
            {
              id: 'credit-1',
              status: 'available',
              grantedAt: '2026-07-20T12:00:00Z',
              expiresAt: '2026-08-03T12:00:00Z',
            },
          ],
        }),
      })
    );

    expect(markup).toContain('role="img"');
    expect(markup).toContain('08/03 12:00');
  });

  test('stays hidden before any credential exposes a usable quota window', () => {
    const markup = renderToStaticMarkup(
      createElement(QuotaTimeline, {
        ...baseProps,
        quotaFor: () => undefined,
      })
    );

    expect(markup).toBe('');
  });
});

describe('QuotaTimeline chart note', () => {
  const render = (initialMode: 'weekly' | 'session', periodHours: number, resetAtMs: number) =>
    renderToStaticMarkup(
      createElement(QuotaTimeline, {
        ...baseProps,
        initialMode,
        quotaFor: () => ({
          status: 'success',
          windows: [{ label: 'window', usedPercent: 25, resetAtMs, periodHours }],
        }),
      })
    );

  test('explains the bars through an InfoTip after the title instead of a legend sentence', async () => {
    await i18n.changeLanguage('en');
    const markup = render('weekly', 168, new Date(2026, 7, 1, 12).getTime());
    const title = i18n.t('quota_management.windows_title');
    expect(markup).toContain(`>${title}</h2><button type="button" data-info-tip=""`);
    expect(markup).toContain(`aria-label="${i18n.t('common.more_info_about', { label: title })}"`);
    // The sentence lives only in the InfoTip's hidden description, not in the legend footer.
    const note = i18n.t('quota_management.windows_note_weekly');
    expect(markup).toContain(`hidden="">${note}</span>`);
    expect(markup.split(note)).toHaveLength(2);
    const legend = markup.slice(markup.lastIndexOf('<footer'));
    expect(legend).toContain(i18n.t('quota_management.windows_legend_current'));
    expect(legend).not.toContain(note);
  });

  test('follows the mode, and has nothing to explain when no lane is drawn', async () => {
    await i18n.changeLanguage('en');
    const session = render('session', 5, baseProps.now + 2 * 60 * 60 * 1000);
    expect(session).toContain(
      `hidden="">${i18n.t('quota_management.windows_note_session')}</span>`
    );
    expect(session).not.toContain(i18n.t('quota_management.windows_note_weekly'));

    // Weekly-only data in 5-hour mode: the empty state shows, so no chart note either.
    const empty = render('session', 168, new Date(2026, 7, 1, 12).getTime());
    expect(empty).toContain(i18n.t('quota_management.windows_empty_session'));
    expect(empty).not.toContain('data-info-tip');
  });
});
