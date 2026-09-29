import { describe, expect, test } from 'bun:test';
import {
  buildHeaderMeta,
  countSectionErrors,
  countTotalErrors,
  readSavedMode,
  readSavedSection,
  resolveDirtyTabs,
  resolveStatus,
  type ConfigStatusInput,
} from '@/features/config/uiState';
import type { VisualConfigValidationErrors } from '@/types/visualConfig';

const statusInput = (overrides: Partial<ConfigStatusInput> = {}): ConfigStatusInput => ({
  disconnected: false,
  loading: false,
  loadFailed: false,
  yamlError: false,
  validationBlocked: false,
  saving: false,
  dirty: false,
  ...overrides,
});

describe('resolveStatus', () => {
  test('follows the legacy precedence chain top-down', () => {
    // disconnected > loading > load_failed > yaml_error > validation_blocked > saving > dirty > synced
    expect(resolveStatus(statusInput({ disconnected: true, loading: true })).key).toBe(
      'disconnected'
    );
    expect(resolveStatus(statusInput({ loading: true, loadFailed: true })).key).toBe('loading');
    expect(resolveStatus(statusInput({ loadFailed: true, yamlError: true })).key).toBe(
      'load_failed'
    );
    expect(resolveStatus(statusInput({ yamlError: true, validationBlocked: true })).key).toBe(
      'yaml_error'
    );
    expect(resolveStatus(statusInput({ validationBlocked: true, saving: true })).key).toBe(
      'validation_blocked'
    );
    expect(resolveStatus(statusInput({ saving: true, dirty: true })).key).toBe('saving');
    expect(resolveStatus(statusInput({ dirty: true })).key).toBe('dirty');
    expect(resolveStatus(statusInput()).key).toBe('synced');
  });

  test('validation_blocked short key lives at config_management top level (regression: old .visual. path bug)', () => {
    const status = resolveStatus(statusInput({ validationBlocked: true }));
    expect(status.shortLabelKey).toBe('config_management.validation_blocked_short');
    expect(status.labelKey).toBe('config_management.visual.validation.validation_blocked');
    expect(status.tone).toBe('error');
  });

  test('every status resolves label keys that exist in all four locales', async () => {
    const locales = ['en', 'zh-CN', 'zh-TW', 'ru'];
    const inputs: Partial<ConfigStatusInput>[] = [
      { disconnected: true },
      { loading: true },
      { loadFailed: true },
      { yamlError: true },
      { validationBlocked: true },
      { saving: true },
      { dirty: true },
      {},
    ];
    for (const locale of locales) {
      const json = (await Bun.file(`src/i18n/locales/${locale}.json`).json()) as Record<
        string,
        unknown
      >;
      const resolveKey = (path: string): unknown =>
        path.split('.').reduce<unknown>((node, part) => {
          if (node && typeof node === 'object') return (node as Record<string, unknown>)[part];
          return undefined;
        }, json);
      for (const overrides of inputs) {
        const status = resolveStatus(statusInput(overrides));
        expect(typeof resolveKey(status.labelKey)).toBe('string');
        expect(typeof resolveKey(status.shortLabelKey)).toBe('string');
      }
    }
  });
});

describe('countSectionErrors', () => {
  test('buckets field errors by section and mirrors common-tab fields', () => {
    const errors: VisualConfigValidationErrors = {
      port: 'port_range',
      requestRetry: 'non_negative_integer',
      'streaming.keepaliveSeconds': 'non_negative_integer',
    };
    const counts = countSectionErrors(errors, false);
    expect(counts.connectivity).toBe(1);
    expect(counts.network).toBe(1);
    expect(counts.streaming).toBe(1);
    expect(counts.logging).toBe(0);
    expect(counts.quota).toBe(0);
    expect(counts.advanced).toBe(0);
    expect(counts.payload).toBe(0);
    // port 由常用 tab 渲染，同一错误在两个 tab 都要可见
    expect(counts.common).toBe(1);
  });

  test('payload flag adds one to the payload tab only', () => {
    const counts = countSectionErrors(undefined, true);
    expect(counts.payload).toBe(1);
    expect(counts.common).toBe(0);
    expect(counts.connectivity).toBe(0);
  });

  test('undefined error entries do not count', () => {
    const errors: VisualConfigValidationErrors = { port: undefined };
    const counts = countSectionErrors(errors, false);
    expect(counts.connectivity).toBe(0);
    expect(counts.common).toBe(0);
  });
});

describe('countTotalErrors', () => {
  test('sums field errors plus the payload flag', () => {
    const errors: VisualConfigValidationErrors = {
      port: 'port_range',
      maxRetryInterval: 'non_negative_integer',
      logsMaxTotalSizeMb: undefined,
    };
    expect(countTotalErrors(errors, false)).toBe(2);
    expect(countTotalErrors(errors, true)).toBe(3);
    expect(countTotalErrors(undefined, false)).toBe(0);
  });
});

describe('resolveDirtyTabs', () => {
  test('maps dirty value keys to their canonical sections', () => {
    const tabs = resolveDirtyTabs(new Set(['rmSecretKey', 'streaming.bootstrapRetries']));
    expect(tabs.has('connectivity')).toBe(true);
    expect(tabs.has('streaming')).toBe(true);
    expect(tabs.has('common')).toBe(false);
    expect(tabs.size).toBe(2);
  });

  test('a common field lights both the common tab and its canonical section', () => {
    const tabs = resolveDirtyTabs(new Set(['apiKeysText']));
    expect(tabs.has('common')).toBe(true);
    expect(tabs.has('connectivity')).toBe(true);
    expect(tabs.size).toBe(2);

    const quotaTabs = resolveDirtyTabs(new Set(['quotaSwitchProject']));
    expect(quotaTabs.has('common')).toBe(true);
    expect(quotaTabs.has('quota')).toBe(true);
  });

  test('unknown keys are ignored instead of crashing', () => {
    const tabs = resolveDirtyTabs(new Set(['not-a-real-key']));
    expect(tabs.size).toBe(0);
  });
});

describe('buildHeaderMeta', () => {
  const base = {
    fieldCount: 58,
    status: resolveStatus(statusInput()),
    dirtyCount: 0,
    sourceDirty: false,
    errorCount: 0,
  };

  test('blocking statuses come directly from the page status machine', () => {
    for (const key of ['disconnected', 'loading', 'load_failed'] as const) {
      const status = resolveStatus(
        statusInput({
          disconnected: key === 'disconnected',
          loading: key === 'loading',
          loadFailed: key === 'load_failed',
        })
      );
      const meta = buildHeaderMeta({ ...base, status, dirtyCount: 3 });
      expect(meta.map((segment) => segment.key)).toEqual(['fields', key]);
    }
  });

  test('clean state ends with a synced segment', () => {
    const meta = buildHeaderMeta(base);
    expect(meta.map((segment) => segment.key)).toEqual(['fields', 'synced']);
    expect(meta[0].count).toBe(58);
  });

  test('dirty and errors stack after the field count', () => {
    const status = resolveStatus(statusInput({ validationBlocked: true, dirty: true }));
    const meta = buildHeaderMeta({ ...base, status, dirtyCount: 3, errorCount: 2 });
    expect(meta.map((segment) => segment.key)).toEqual(['fields', 'dirty', 'errors']);
    expect(meta[1].count).toBe(3);
    expect(meta[1].tone).toBe('warning');
    expect(meta[2].count).toBe(2);
    expect(meta[2].tone).toBe('error');
  });

  test('source dirty supersedes the visual dirty count', () => {
    const status = resolveStatus(statusInput({ dirty: true }));
    const meta = buildHeaderMeta({ ...base, status, dirtyCount: 3, sourceDirty: true });
    expect(meta.map((segment) => segment.key)).toEqual(['fields', 'dirty_source']);
  });

  test('yaml error shows without a synced tail', () => {
    const status = resolveStatus(statusInput({ yamlError: true }));
    const meta = buildHeaderMeta({ ...base, status });
    expect(meta.map((segment) => segment.key)).toEqual(['fields', 'yaml_error']);
  });
});

describe('localStorage readers', () => {
  test('readSavedMode falls back to visual on unknown values', () => {
    expect(readSavedMode('source')).toBe('source');
    expect(readSavedMode('visual')).toBe('visual');
    expect(readSavedMode('full')).toBe('visual'); // 旧「简单/完整」值域不再合法
    expect(readSavedMode(null)).toBe('visual');
  });

  test('readSavedSection falls back to common on stale values', () => {
    expect(readSavedSection('payload')).toBe('payload');
    expect(readSavedSection('common')).toBe('common');
    expect(readSavedSection('server')).toBe('common'); // 历史分区 id 不再存在
    expect(readSavedSection(null)).toBe('common');
  });
});
