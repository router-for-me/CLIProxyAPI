import { describe, expect, test } from 'bun:test';
import { parse as parseYaml } from 'yaml';
import { buildConfigSaveDraft } from '../src/features/config/hooks/useConfigDocument';
import { DEFAULT_VISUAL_VALUES } from '../src/types/visualConfig';
import { runVisualConfig } from './helpers/visualConfig';

describe('YAML document editing semantics', () => {
  const numericFields = [
    ['port', 'port', '8317'],
    ['errorLogsMaxFiles', 'error-logs-max-files', '10'],
    ['logsMaxTotalSizeMb', 'logs-max-total-size-mb', '100'],
    ['redisUsageQueueRetentionSeconds', 'redis-usage-queue-retention-seconds', '60'],
    ['requestRetry', 'request-retry', '3'],
    ['maxRetryCredentials', 'max-retry-credentials', '2'],
    ['maxRetryInterval', 'max-retry-interval', '30'],
    ['authAutoRefreshWorkers', 'auth-auto-refresh-workers', '16'],
  ] as const;

  for (const [field, yamlKey, initial] of numericFields) {
    test(`clearing ${yamlKey} removes the key, not replaces it with zero or a placeholder`, () => {
      const yaml = `# unrelated setting\nfuture-option: keep\n${yamlKey}: ${initial}\n`;
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
      const yaml = `streaming:\n  ${yamlKey}: 2\n  future-option: keep\n`;
      const config = runVisualConfig(yaml, [
        { streaming: { ...DEFAULT_VISUAL_VALUES.streaming, [field]: '' } },
      ]);
      expect(parseYaml(config.applyVisualChangesToYaml(yaml))).toEqual({
        streaming: { 'future-option': 'keep' },
      });
    });
  }

  test('clearing nonstream keepalive deletes the top-level key, not the streaming block', () => {
    const yaml = 'nonstream-keepalive-interval: 2\nstreaming:\n  future-option: keep\n';
    const config = runVisualConfig(yaml, [
      { streaming: { ...DEFAULT_VISUAL_VALUES.streaming, nonstreamKeepaliveInterval: '' } },
    ]);
    expect(parseYaml(config.applyVisualChangesToYaml(yaml))).toEqual({
      streaming: { 'future-option': 'keep' },
    });
  });

  test('clearing the last managed streaming value removes the empty block', () => {
    const yaml = 'streaming:\n  keepalive-seconds: 2\n';
    const config = runVisualConfig(yaml, [{ streaming: { ...DEFAULT_VISUAL_VALUES.streaming } }]);
    expect(parseYaml(config.applyVisualChangesToYaml(yaml))).toEqual({});
  });

  test('retains existing empty string keys rather than treating all blanks as numeric resets', () => {
    const yaml = 'proxy-url: http://proxy.example\ntls:\n  cert: fixture.pem\n  key: fixture.key\n';
    const config = runVisualConfig(yaml, [{ proxyUrl: '', tlsCert: '' }]);
    expect(parseYaml(config.applyVisualChangesToYaml(yaml))).toEqual({
      'proxy-url': '',
      tls: { cert: '', key: 'fixture.key' },
    });
  });

  test('does not normalize untouched file values or drop unknown plugin settings', () => {
    const yaml = `# file values, not a runtime snapshot
redis-usage-queue-retention-seconds: 5000
gpt-image-2-base-model: custom-invalid-model
plugins:
  configs:
    fixture:
      enabled: false
      custom-option: keep
`;
    const config = runVisualConfig(yaml, [{ debug: true }]);
    const output = config.applyVisualChangesToYaml(yaml);
    expect(parseYaml(output)).toEqual({ ...parseYaml(yaml), debug: true });
    expect(output).toContain('# file values, not a runtime snapshot');
    // The existing bounded validation still blocks invalid visual saves.
    expect(config.visualValidationErrors.redisUsageQueueRetentionSeconds).toBe(
      'integer_range_1_3600'
    );
  });

  test('preserves raw source drafts and keeps invalid raw Payload validation', () => {
    const source = `payload:
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
