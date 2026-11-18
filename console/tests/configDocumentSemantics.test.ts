import { describe, expect, test } from 'bun:test';
import { parse as parseYaml, parseDocument } from 'yaml';
import { buildConfigSaveDraft } from '../src/features/config/hooks/useConfigDocument';
import { DEFAULT_VISUAL_VALUES } from '../src/types/visualConfig';
import { runVisualConfig } from './helpers/visualConfig';

describe('YAML document editing semantics', () => {
  const numericFields = [
    ['port', 'server.port', '8317'],
    ['errorLogsMaxFiles', 'observability.logs.error-logs-max-files', '10'],
    ['logsMaxTotalSizeMb', 'observability.logs.logs-max-total-size-mb', '100'],
    [
      'redisUsageQueueRetentionSeconds',
      'observability.usage.redis-usage-queue-retention-seconds',
      '60',
    ],
    ['requestRetry', 'routing.retry.request-retry', '3'],
    ['maxRetryCredentials', 'routing.retry.max-retry-credentials', '2'],
    ['maxRetryInterval', 'routing.retry.max-retry-interval', '30'],
    ['authAutoRefreshWorkers', 'oauth.auth-auto-refresh-workers', '16'],
  ] as const;

  for (const [field, yamlKey, initial] of numericFields) {
    test(`clearing ${yamlKey} removes the key, not replaces it with zero or a placeholder`, () => {
      const doc = parseDocument('# unrelated setting\nfuture-option: keep\n');
      doc.setIn(yamlKey.split('.'), Number(initial));
      const yaml = doc.toString();
      const config = runVisualConfig(yaml, [{ [field]: '' }]);
      expect(config.visualDirtyFields.has(field)).toBe(true);
      const output = config.applyVisualChangesToYaml(yaml);
      expect(parseYaml(output)).toEqual({ 'future-option': 'keep' });
      expect(output).toContain('# unrelated setting');
      expect(runVisualConfig(output).visualValues[field]).toBe('');
    });
  }

  for (const [field, yamlKey] of [
    ['keepaliveSeconds', 'keepalive-seconds'],
    ['bootstrapRetries', 'bootstrap-retries'],
  ] as const) {
    test(`clearing streaming.${yamlKey} preserves unmanaged siblings`, () => {
      const yaml = `requests:
  streaming:
    ${yamlKey}: 2
    future-option: keep
`;
      const config = runVisualConfig(yaml, [
        { streaming: { ...DEFAULT_VISUAL_VALUES.streaming, [field]: '' } },
      ]);
      expect(parseYaml(config.applyVisualChangesToYaml(yaml))).toEqual({
        requests: { streaming: { 'future-option': 'keep' } },
      });
    });
  }

  test('clearing nonstream keepalive deletes the top-level key, not the streaming block', () => {
    const yaml =
      'requests:\n  nonstream-keepalive-interval: 2\n  streaming:\n    future-option: keep\n';
    const config = runVisualConfig(yaml, [
      { streaming: { ...DEFAULT_VISUAL_VALUES.streaming, nonstreamKeepaliveInterval: '' } },
    ]);
    expect(parseYaml(config.applyVisualChangesToYaml(yaml))).toEqual({
      requests: { streaming: { 'future-option': 'keep' } },
    });
  });

  test('clearing the last managed streaming value removes the empty block', () => {
    const yaml = 'requests:\n  streaming:\n    keepalive-seconds: 2\n';
    const config = runVisualConfig(yaml, [{ streaming: { ...DEFAULT_VISUAL_VALUES.streaming } }]);
    expect(parseYaml(config.applyVisualChangesToYaml(yaml))).toEqual({});
  });

  test('retains existing empty string keys rather than treating all blanks as numeric resets', () => {
    const yaml =
      'server:\n  tls:\n    cert: fixture.pem\n    key: fixture.key\nrequests:\n  proxy-url: http://proxy.example\n';
    const config = runVisualConfig(yaml, [{ proxyUrl: '', tlsCert: '' }]);
    expect(parseYaml(config.applyVisualChangesToYaml(yaml))).toEqual({
      requests: { 'proxy-url': '' },
      server: { tls: { cert: '', key: 'fixture.key' } },
    });
  });

  test('does not normalize untouched file values or drop unknown plugin settings', () => {
    const yaml = `# file values, not a runtime snapshot
plugins:
  configs:
    fixture:
      enabled: false
      custom-option: keep
multimedia:
  gpt-image-2-base-model: custom-invalid-model
observability:
  usage:
    redis-usage-queue-retention-seconds: 5000
`;
    const config = runVisualConfig(yaml, [{ debug: true }]);
    const output = config.applyVisualChangesToYaml(yaml);
    expect(parseYaml(output)).toEqual({
      ...parseYaml(yaml),
      observability: { ...parseYaml(yaml).observability, logs: { debug: true } },
    });
    expect(output).toContain('# file values, not a runtime snapshot');
    // The existing bounded validation still blocks invalid visual saves.
    expect(config.visualValidationErrors.redisUsageQueueRetentionSeconds).toBe(
      'integer_range_1_3600'
    );
  });

  test('preserves raw source drafts and keeps invalid raw Payload validation', () => {
    const source = `requests:
  payload:
    default-raw:
      - models:
          - name: fixture-model
        params:
          fixture: not-valid-json
`;
    const config = runVisualConfig(source);
    expect(config.visualHasPayloadValidationErrors).toBe(true);
    expect(config.applyVisualChangesToYaml(source)).toBe(source);
    expect(
      buildConfigSaveDraft(source, source, true, 'source', () => {
        throw new Error('A source draft must not be rebuilt from visual or runtime values');
      })
    ).toBe(source);
  });
});
