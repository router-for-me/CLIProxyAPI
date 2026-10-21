import { describe, expect, test } from 'bun:test';
import en from '../src/i18n/locales/en.json';
import zhCN from '../src/i18n/locales/zh-CN.json';
import zhTW from '../src/i18n/locales/zh-TW.json';
import ru from '../src/i18n/locales/ru.json';

const keys = [
  'preview_too_large',
  'read_status_live',
  'read_status_paused',
  'read_status_catching_up',
  'last_updated',
  'buffer_scope',
  'buffer_evicted',
  'cursor_reset_notice',
  'history_evicted',
  'resume_following',
  'level_filter',
  'all_levels',
  'view_request',
  'copy_line',
  'request_log_missing',
  'download_cached',
  'clear_application_confirm',
  'clear_search',
  'reading_enabled',
  'filter_load_more',
] as const;

describe('log interaction translations', () => {
  for (const [locale, document] of Object.entries({ en, zhCN, zhTW, ru })) {
    test(`${locale} includes all new actions, status labels and matching interpolation`, () => {
      for (const key of keys) {
        expect(document.logs[key].trim().length).toBeGreaterThan(0);
        expect(document.logs[key].match(/\{\{\w+\}\}/g) ?? []).toEqual(
          en.logs[key].match(/\{\{\w+\}\}/g) ?? []
        );
      }
    });
  }
});
