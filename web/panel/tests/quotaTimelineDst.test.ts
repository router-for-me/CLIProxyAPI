import { expect, test } from 'bun:test';

// Separate processes keep timezone changes out of the other rendering suites.
const cases: { zone: string; date: number[]; hours: number; startHour?: number }[] = [
  { zone: 'Europe/Paris', date: [2026, 9, 25], hours: 25 },
  { zone: 'Europe/Paris', date: [2026, 2, 29], hours: 23 },
  { zone: 'America/New_York', date: [2026, 10, 1], hours: 25 },
  { zone: 'America/New_York', date: [2026, 2, 8], hours: 23 },
  { zone: 'Australia/Lord_Howe', date: [2026, 3, 5], hours: 24.5 },
  { zone: 'Australia/Lord_Howe', date: [2026, 9, 4], hours: 23.5 },
  { zone: 'UTC', date: [2026, 9, 25], hours: 24 },
  { zone: 'America/Santiago', date: [2026, 8, 6], hours: 23, startHour: 1 },
];

for (const { zone, date, hours, startHour = 0 } of cases) {
  test(`calendar columns and rendered labels across ${zone} ${date.join('-')}`, () => {
    const result = Bun.spawnSync({
      cmd: [
        process.execPath,
        '--eval',
        `
          import { createElement } from 'react';
          import { renderToStaticMarkup } from 'react-dom/server';
          import './src/i18n/index';
          import { QuotaTimeline } from './src/features/quota/components/QuotaTimeline';
          import { timelineSpan, timelineCells, projectLane, HOUR_MS }
            from './src/features/quota/quotaTimelineModel';
          const now = new Date(${date.join(',')}, 12).getTime();
          const lane = {
            name: 'fixture.json', displayName: 'Fixture', provider: 'codex',
            anchorMs: new Date(${date[0]}, ${date[1]}, ${date[2] + 1}).getTime(),
            periodHours: 168, remaining: 100, limits: [], resetCredits: [],
          };
          const output = ['weekly', 'session'].map((mode) => {
            const span = timelineSpan(mode, 0, now);
            const cells = timelineCells(mode, span);
            const props = {
              entries: [{ file: { name: 'fixture.json', type: 'codex' }, type: 'codex' }],
              displayNameFor: (name) => name, resolvedTheme: 'light', now, initialMode: mode,
              quotaFor: () => ({ status: 'success', windows: [{
                id: mode === 'weekly' ? 'weekly' : 'five-hour', label: 'Limit',
                usedPercent: 0, resetAtMs: lane.anchorMs,
                periodHours: mode === 'weekly' ? 168 : 5,
              }] }),
            };
            const markup = renderToStaticMarkup(createElement(QuotaTimeline, props));
            const previousMarkup = renderToStaticMarkup(createElement(QuotaTimeline, {
              ...props, initialOffset: -2,
            }));
            return {
              mode, markup, previousMarkup, days: span.days,
              endHour: new Date(span.endMs).getHours(),
              offsetHours: [-1, 1].map((offset) =>
                new Date(timelineSpan(mode, offset, now).startMs).getHours()),
              hours: cells.map((cell) => cell.durationMs / HOUR_MS),
              localHours: cells.map((cell) => new Date(cell.at).getHours()),
              dates: cells.map((cell) => new Date(cell.at).getDate()),
              coveredMs: cells.reduce((sum, cell) => sum + cell.durationMs, 0),
              spanMs: span.endMs - span.startMs,
              midnightPercent: cells[0].durationMs / (span.endMs - span.startMs) * 100,
              windowEndPercent: projectLane(lane, span.startMs, span.endMs, now, 'weekly')
                .find((window) => window.endMs === lane.anchorMs).widthPercent,
            };
          });
          console.log(JSON.stringify(output));
        `,
      ],
      cwd: new URL('..', import.meta.url).pathname,
      env: { ...process.env, TZ: zone },
      stdout: 'pipe',
      stderr: 'pipe',
    });

    expect(result.stderr.toString()).toBe('');
    expect(result.exitCode).toBe(0);
    const [weekly, session] = JSON.parse(result.stdout.toString());

    expect(weekly.hours).toHaveLength(14);
    expect(weekly.hours[0]).toBe(hours);
    expect(weekly.localHours).toEqual([startHour, ...Array(13).fill(0)]);
    expect(new Set(weekly.dates).size).toBe(14);
    expect(weekly.windowEndPercent).toBeCloseTo(weekly.midnightPercent, 8);
    expect(weekly.markup).toContain(`--timeline-columns:minmax(0, ${hours}fr)`);

    expect(session.hours).toHaveLength(12);
    expect(session.hours[0]).toBe(hours - 18);
    expect(session.localHours).toEqual([
      startHour,
      6,
      12,
      18,
      ...Array(2).fill([0, 6, 12, 18]).flat(),
    ]);

    for (const output of [weekly, session]) {
      expect(output.coveredMs).toBe(output.spanMs);
      expect(output.endHour).toBe(0);
      expect(output.offsetHours).toEqual([0, 0]);
      const dayLabels = [
        ...output.markup.matchAll(
          /data-daystart="1"><span[^>]*>[^<]*<\/span><span[^>]*>([^<]*)<\/span>/g
        ),
      ].map((match) => match[1]);
      expect(dayLabels).toHaveLength(output.days);
      expect(new Set(dayLabels).size).toBe(output.days);
      if (date[1] === 2 && date[2] === 29 && output.mode === 'session') {
        expect(output.previousMarkup).toContain('03/27 – 03/29');
      }
      if (zone === 'America/Santiago' && output.mode === 'session') {
        expect(output.markup).toContain('09/06 – 09/08');
      }
    }
  });
}
