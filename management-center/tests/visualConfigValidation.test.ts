import { describe, expect, test } from 'bun:test';
import { createElement } from 'react';
import { renderToStaticMarkup } from 'react-dom/server';
import { parse as parseYaml } from 'yaml';
import i18n from '../src/i18n';
import { SectionStreaming } from '../src/features/config/components/sections/SectionStreaming';
import { getVisualConfigValidationErrors } from '../src/hooks/useVisualConfig';
import { DEFAULT_VISUAL_VALUES } from '../src/types/visualConfig';
import { runVisualConfig } from './helpers/visualConfig';

const signedFields = [
  'maxRetryCredentials',
  'maxRetryInterval',
  'authAutoRefreshWorkers',
  'streaming.keepaliveSeconds',
  'streaming.bootstrapRetries',
  'streaming.nonstreamKeepaliveInterval',
] as const;

function withSignedValues(value: string) {
  return {
    ...structuredClone(DEFAULT_VISUAL_VALUES),
    maxRetryCredentials: value,
    maxRetryInterval: value,
    authAutoRefreshWorkers: value,
    streaming: {
      keepaliveSeconds: value,
      bootstrapRetries: value,
      nonstreamKeepaliveInterval: value,
    },
  };
}

describe('visual config validation', () => {
  for (const value of [
    '',
    '  ',
    '-1',
    ' -16 ',
    '0',
    '16',
    String(Number.MIN_SAFE_INTEGER),
    String(Number.MAX_SAFE_INTEGER),
  ]) {
    test(`accepts backend-supported integer sentinel values: ${JSON.stringify(value)}`, () => {
      const errors = getVisualConfigValidationErrors(withSignedValues(value));
      for (const field of signedFields) expect(errors[field]).toBeUndefined();
    });
  }

  for (const value of [
    '-1.5',
    '1.5',
    'abc',
    '1e3',
    'Infinity',
    '9'.repeat(400),
    '-9007199254740992',
    '9007199254740992',
    '-9007199254740993',
    '9007199254740993',
    '-9999999999999999999',
    '9999999999999999999',
  ]) {
    test(`rejects invalid integer input: ${value.slice(0, 20)}`, () => {
      const errors = getVisualConfigValidationErrors(withSignedValues(value));
      for (const field of signedFields) expect(errors[field]).toBe('integer');
    });
  }

  test('keeps non-negative and bounded fields strict', () => {
    const errors = getVisualConfigValidationErrors({
      ...structuredClone(DEFAULT_VISUAL_VALUES),
      requestRetry: '-1',
      logsMaxTotalSizeMb: '-1',
      errorLogsMaxFiles: '-1',
      port: '65536',
    });
    expect(errors.requestRetry).toBe('non_negative_integer');
    expect(errors.logsMaxTotalSizeMb).toBe('non_negative_integer');
    expect(errors.errorLogsMaxFiles).toBe('non_negative_integer');
    expect(errors.port).toBe('port_range');
  });

  test.each(['-9007199254740993', '9007199254740993', '-9999999999999999999'])(
    'does not serialize unsafe integers even if validation is bypassed: %s',
    (value) => {
      const yaml = 'max-retry-interval: 10\n';
      const config = runVisualConfig(yaml, [withSignedValues(value)]);
      for (const field of signedFields) {
        expect(config.visualValidationErrors[field]).toBe('integer');
      }
      expect(parseYaml(config.applyVisualChangesToYaml(yaml))).toEqual({
        'max-retry-interval': 10,
      });
    }
  );

  test.each([Number.MIN_SAFE_INTEGER, Number.MAX_SAFE_INTEGER])(
    'serializes safe integer boundaries exactly: %s',
    (value) => {
      const config = runVisualConfig('{}', [{ maxRetryInterval: String(value) }]);
      expect(config.visualValidationErrors.maxRetryInterval).toBeUndefined();
      expect(parseYaml(config.applyVisualChangesToYaml('{}'))).toEqual({
        'max-retry-interval': value,
      });
    }
  );

  test('rejects unsafe integers in non-negative fields', () => {
    const errors = getVisualConfigValidationErrors({
      ...structuredClone(DEFAULT_VISUAL_VALUES),
      requestRetry: '9007199254740993',
      logsMaxTotalSizeMb: '9007199254740993',
      errorLogsMaxFiles: '9007199254740993',
    });
    expect(errors.requestRetry).toBe('non_negative_integer');
    expect(errors.logsMaxTotalSizeMb).toBe('non_negative_integer');
    expect(errors.errorLogsMaxFiles).toBe('non_negative_integer');
  });

  test('loading negative sentinels does not block an unrelated visual edit', () => {
    const yaml = `max-retry-credentials: -1
max-retry-interval: -1
auth-auto-refresh-workers: -1
nonstream-keepalive-interval: -1
streaming:
  keepalive-seconds: -1
  bootstrap-retries: -1
`;
    const config = runVisualConfig(yaml, [{ debug: true }]);
    expect(Object.values(config.visualValidationErrors).some(Boolean)).toBe(false);
    expect(parseYaml(config.applyVisualChangesToYaml(yaml))).toEqual({
      ...parseYaml(yaml),
      debug: true,
    });
  });

  test('writes user-entered negative sentinels as YAML integers', () => {
    const config = runVisualConfig('{}', [withSignedValues('-1')]);
    expect(parseYaml(config.applyVisualChangesToYaml('{}'))).toEqual({
      'max-retry-credentials': -1,
      'max-retry-interval': -1,
      'auth-auto-refresh-workers': -1,
      'nonstream-keepalive-interval': -1,
      streaming: { 'keepalive-seconds': -1, 'bootstrap-retries': -1 },
    });
  });

  test('streaming badges recognize non-positive integers but not invalid input', () => {
    for (const [value, disabled] of [
      ['-1', true],
      ['0', true],
      ['', true],
      ['1', false],
      ['-1.5', false],
    ] as const) {
      const values = withSignedValues(value);
      const markup = renderToStaticMarkup(
        createElement(SectionStreaming, {
          values,
          validationErrors: getVisualConfigValidationErrors(values),
          disabled: false,
          onChange: () => {},
        })
      );
      const label = i18n.t('config_management.visual.sections.streaming.disabled');
      expect(markup.split(`>${label}</span>`).length - 1).toBe(disabled ? 2 : 0);
    }
  });

  test('integer validation and sentinel hints exist in all four locales', async () => {
    for (const locale of ['en', 'zh-CN', 'zh-TW', 'ru']) {
      const json = await Bun.file(`src/i18n/locales/${locale}.json`).json();
      const visual = json.config_management.visual;
      expect(visual.validation.integer).toBeString();
      expect(visual.sections.network.max_retry_interval_hint).toBeString();
    }
  });

  test('requires Redis usage retention to be empty or within 1..3600', () => {
    const values = structuredClone(DEFAULT_VISUAL_VALUES);

    values.redisUsageQueueRetentionSeconds = '';
    expect(getVisualConfigValidationErrors(values).redisUsageQueueRetentionSeconds).toBeUndefined();

    values.redisUsageQueueRetentionSeconds = '0';
    expect(getVisualConfigValidationErrors(values).redisUsageQueueRetentionSeconds).toBe(
      'integer_range_1_3600'
    );

    values.redisUsageQueueRetentionSeconds = '3601';
    expect(getVisualConfigValidationErrors(values).redisUsageQueueRetentionSeconds).toBe(
      'integer_range_1_3600'
    );

    values.redisUsageQueueRetentionSeconds = '3600';
    expect(getVisualConfigValidationErrors(values).redisUsageQueueRetentionSeconds).toBeUndefined();
  });
});
