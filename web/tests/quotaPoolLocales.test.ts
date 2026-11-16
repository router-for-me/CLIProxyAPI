import { describe, expect, test } from 'bun:test';
import en from '../src/i18n/locales/en.json';
import ko from '../src/i18n/locales/ko.json';
import ru from '../src/i18n/locales/ru.json';
import vi from '../src/i18n/locales/vi.json';
import zhCN from '../src/i18n/locales/zh-CN.json';
import zhTW from '../src/i18n/locales/zh-TW.json';

const locales = { en, 'zh-CN': zhCN, 'zh-TW': zhTW, ru, ko, vi };

const poolKeys = [
  'metric_label',
  'metric_remaining',
  'metric_drain',
  'burn_label',
  'burn_window',
  'future_label',
  'future_same',
  'future_half',
  'future_double',
  'future_none',
  'accounts_toggle',
  'range',
  'range_prev',
  'range_next',
  'range_today',
  'accounts_count_one',
  'accounts_count_other',
  'no_data_count',
  'of_capacity',
  'stat_pool_empty',
  'stat_first_empty',
  'stat_next_reset',
  'stat_burn',
  'not_by',
  'empty_now',
  'in_duration',
  'now_short',
  'gain',
  'gain_at_least',
  'burn_value',
  'burn_approx',
  'burn_approx_hint',
  'needs_history',
  'unknown',
  'duration_dh',
  'duration_hm',
  'duration_m',
  'day_label',
  'chart_now',
  'chart_empty',
  'tooltip_remaining',
  'tooltip_drain',
  'tooltip_partial',
  'tooltip_no_data',
  'kind_recorded',
  'kind_projected',
  'legend_pool',
  'legend_recorded',
  'legend_projected',
  'legend_partial',
  'history_empty',
  'history_unsupported',
  'history_disabled',
  'history_error',
  'history_retry',
  'chart_label',
  'chart_summary',
  'chart_hint',
  'account_n',
] as const;

const removedQuotaKeyPatterns = [
  /^windows_/,
  /^weekday_/,
  /^summary_/,
  /^ledger_/,
  /^view_(label|ledger|cards)$/,
  /^no_reset_pending$/,
];

const tokens = (value: string): string[] => (value.match(/\{\{[^}]+\}\}/g) ?? []).sort();

describe('quota pool locales', () => {
  for (const [name, locale] of Object.entries(locales)) {
    describe(name, () => {
      test('has every quota_pool key with the English interpolation tokens', () => {
        for (const key of poolKeys) {
          const value = (locale.quota_pool as Record<string, string>)[key];
          expect(typeof value).toBe('string');
          expect(tokens(value)).toEqual(tokens(en.quota_pool[key]));
        }
      });

      test('has the email visibility labels', () => {
        expect(locale.quota_management.show_emails).toBeTruthy();
        expect(locale.quota_management.hide_emails).toBeTruthy();
      });

      test('uses the prox brand and no sidebar subtitle', () => {
        expect(locale.title.abbr).toBe('prox');
        expect(locale.title.login).toBe('prox');
        expect(locale.splash.title).toBe('prox');
        expect(locale.splash.subtitle).toBe('');
        expect(locale.system_info.about_title).toBe('prox');
        expect(locale.sidebar).not.toHaveProperty('subtitle');
      });

      test('no longer carries the removed ledger, timeline and summary keys', () => {
        const stale = Object.keys(locale.quota_management).filter((key) =>
          removedQuotaKeyPatterns.some((pattern) => pattern.test(key))
        );
        expect(stale).toEqual([]);
      });
    });
  }

  test('ru has the few and many accounts plurals', () => {
    expect(tokens(ru.quota_pool.accounts_count_few)).toEqual(['{{count}}']);
    expect(tokens(ru.quota_pool.accounts_count_many)).toEqual(['{{count}}']);
  });

  test('en keeps the compact burn window label', () => {
    expect(en.quota_pool.burn_window).toBe('{{hours}}h');
  });
});
