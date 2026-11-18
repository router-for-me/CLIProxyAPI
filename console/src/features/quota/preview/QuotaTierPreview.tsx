/**
 * DEV-ONLY preview: plan tiers (standard / premium=gold / flagship=Hetian jade with gold bezel).
 *
 * Registered in App.tsx only when `import.meta.env.DEV` is true, outside the
 * auth guard, so it can be opened without a backend or real accounts:
 *   http://localhost:5173/#/__preview/quota-tiers
 * Renders the real QuotaCard with mocked quota states. Copy is intentionally
 * not localized — this page never ships.
 */

import { useState } from 'react';
import { useThemeStore } from '@/stores';
import type {
  AntigravityQuotaState,
  AuthFileItem,
  ClaudeQuotaState,
  CodexQuotaState,
  CodexQuotaWindow,
  KimiQuotaState,
  Theme,
  XaiBillingSummary,
  XaiQuotaState,
} from '@/types';
import { HOUR_MS, DAY_MS } from '@/utils/time/durations';
import { QuotaCard } from '../components/QuotaCard';
import type { QuotaFileEntry } from '../logic';
import type { QuotaCardState } from '../providers';
import pageStyles from '../QuotaPage.module.scss';
import styles from './QuotaTierPreview.module.scss';

interface MockAccount {
  entry: QuotaFileEntry;
  quota: QuotaCardState;
}

const THEMES: { value: Theme; label: string }[] = [
  { value: 'light', label: '纸感' },
  { value: 'white', label: '纯白' },
  { value: 'dark', label: '暗色' },
];

const CARD_STAGGER_MS = 50;
const SIMULATED_REFRESH_MS = 1200;

const file = (name: string, type: string, extra: Partial<AuthFileItem> = {}): AuthFileItem => ({
  name,
  type,
  provider: type,
  ...extra,
});

const codexWindow = (
  id: string,
  label: string,
  usedPercent: number,
  resetInMs: number,
  periodHours: number
): CodexQuotaWindow => ({
  id,
  label,
  usedPercent,
  resetLabel: '',
  resetAtMs: Date.now() + resetInMs,
  periodHours,
});

const codexQuota = (planType: string, used: [number, number], soon = false): CodexQuotaState => ({
  status: 'success',
  planType,
  subscriptionActiveUntil: new Date(Date.now() + 26 * DAY_MS).toISOString(),
  windows: [
    codexWindow('primary', '5 小时', used[0], soon ? 0.4 * HOUR_MS : 3 * HOUR_MS, 5),
    codexWindow('secondary', '每周', used[1], 4 * DAY_MS, 168),
  ],
});

const xaiBilling = (
  planLabel: string,
  planTier: XaiBillingSummary['planTier'],
  usedPercent: number,
  limitCents = 150_000
): XaiBillingSummary => ({
  mode: 'billing',
  periodType: 'monthly',
  usagePercent: null,
  productUsage: [],
  monthlyLimitCents: limitCents,
  usedCents: limitCents * (usedPercent / 100),
  includedUsedCents: limitCents * (usedPercent / 100),
  onDemandCapCents: null,
  onDemandUsedCents: null,
  onDemandUsedPercent: null,
  usedPercent,
  planLabel,
  planTier,
  billingPeriodEnd: new Date(Date.now() + 6 * DAY_MS).toISOString(),
});

const antigravityQuota = (plan: string, remaining: [number, number]): AntigravityQuotaState => ({
  status: 'success',
  subscription: { plan, tierName: null, tierId: null },
  groups: [
    {
      id: 'gemini',
      label: 'Gemini 3 Pro',
      buckets: [
        {
          id: 'gemini',
          label: 'Gemini 3 Pro',
          remainingFraction: remaining[0],
          resetTime: new Date(Date.now() + 6 * HOUR_MS).toISOString(),
        },
      ],
    },
    {
      id: 'claude',
      label: 'Claude Sonnet',
      buckets: [
        {
          id: 'claude',
          label: 'Claude Sonnet',
          remainingFraction: remaining[1],
          resetTime: new Date(Date.now() + 6 * HOUR_MS).toISOString(),
        },
      ],
    },
  ],
});

const claudeQuota = (planType: string, used: [number, number]): ClaudeQuotaState => ({
  status: 'success',
  planType,
  observedAt: new Date().toISOString(),
  windows: [
    {
      id: 'five-hour',
      label: '5 小时',
      usedPercent: used[0],
      resetLabel: '',
      resetAtMs: Date.now() + 2 * HOUR_MS,
      periodHours: 5,
    },
    {
      id: 'seven-day',
      label: '每周 · 全部模型',
      usedPercent: used[1],
      resetLabel: '',
      resetAtMs: Date.now() + 3 * DAY_MS,
      periodHours: 168,
    },
  ],
});

const buildAccounts = (): MockAccount[] => [
  ...['Allegro', 'Allegretto', 'Moderato', 'Andante'].map((planName): MockAccount => ({
    entry: { type: 'kimi', file: file(`kimi-${planName.toLowerCase()}.json`, 'kimi') },
    quota: {
      status: 'success',
      planName,
      rows: [
        {
          id: 'five-hour',
          labelKey: 'kimi_quota.limit_window',
          labelParams: { duration: '5h' },
          used: 25,
          limit: 100,
          resetAtMs: Date.now() + 2 * HOUR_MS,
          periodHours: 5,
        },
      ],
    } as KimiQuotaState,
  })),
  {
    entry: { type: 'codex', file: file('codex-pro500.json', 'codex') },
    quota: codexQuota('promax', [6, 21]),
  },
  {
    entry: { type: 'claude', file: file('claude-max20.json', 'claude') },
    quota: claudeQuota('plan_max20', [36, 72]),
  },
  {
    entry: { type: 'xai', file: file('grok-heavy.json', 'xai') },
    quota: {
      status: 'success',
      billing: xaiBilling('SuperGrok Heavy', 'elite', 43),
    } as XaiQuotaState,
  },
  {
    entry: { type: 'antigravity', file: file('antigravity-ultra.json', 'antigravity') },
    quota: antigravityQuota('ultra', [0.91, 0.27]),
  },
  {
    entry: { type: 'codex', file: file('codex-pro200.json', 'codex') },
    quota: codexQuota('pro', [18, 54]),
  },
  {
    entry: { type: 'xai', file: file('grok-supergrok.json', 'xai') },
    quota: {
      status: 'success',
      billing: xaiBilling('SuperGrok', 'premium', 61, 15_000),
    } as XaiQuotaState,
  },
  {
    entry: { type: 'codex', file: file('codex-pro100.json', 'codex') },
    quota: codexQuota('prolite', [82, 48], true),
  },
  {
    entry: { type: 'claude', file: file('claude-max5.json', 'claude') },
    quota: claudeQuota('plan_max5', [12, 40]),
  },
  {
    entry: { type: 'antigravity', file: file('antigravity-ultra-lite.json', 'antigravity') },
    quota: antigravityQuota('ultra-lite', [0.64, 0.12]),
  },
  {
    entry: { type: 'codex', file: file('codex-plus.json', 'codex') },
    quota: codexQuota('plus', [0, 29]),
  },
  {
    entry: { type: 'codex', file: file('codex-business.json', 'codex') },
    quota: codexQuota('self_serve_business_prolite', [40, 33]),
  },
  {
    entry: { type: 'codex', file: file('codex-team.json', 'codex') },
    quota: { status: 'error', windows: [], error: 'HTTP 401' } as CodexQuotaState,
  },
  {
    // Idle card: no plan styling until quota data arrives.
    entry: {
      type: 'codex',
      file: file('codex-pro500-idle.json', 'codex', { plan_type: 'promax' }),
    },
    quota: { status: 'idle', windows: [] } as CodexQuotaState,
  },
];

export function QuotaTierPreview() {
  const theme = useThemeStore((state) => state.theme);
  const resolvedTheme = useThemeStore((state) => state.resolvedTheme);
  const setTheme = useThemeStore((state) => state.setTheme);
  const [accounts] = useState(buildAccounts);
  const [refreshing, setRefreshing] = useState(false);
  const [replayKey, setReplayKey] = useState(0);

  const simulateRefresh = () => {
    setRefreshing(true);
    window.setTimeout(() => setRefreshing(false), SIMULATED_REFRESH_MS);
  };

  return (
    <div className={styles.page}>
      <header className={styles.toolbar}>
        <div className={styles.titleBlock}>
          <h1 className={styles.title}>额度卡片档位</h1>
          <p className={styles.subtitle}>
            开发环境预览，模拟数据。套餐档位：标准 → 金卡 → 和田玉金镶玉。
          </p>
        </div>
        <div className={styles.controls}>
          <div className={styles.segmented} role="group" aria-label="主题">
            {THEMES.map((option) => (
              <button
                key={option.value}
                type="button"
                className={styles.segment}
                aria-pressed={theme === option.value}
                onClick={() => setTheme(option.value)}
              >
                {option.label}
              </button>
            ))}
          </div>
          <button
            type="button"
            className={styles.replay}
            onClick={simulateRefresh}
            disabled={refreshing}
          >
            模拟刷新（档位应保持）
          </button>
          <button
            type="button"
            className={styles.replay}
            onClick={() => setReplayKey((key) => key + 1)}
          >
            重播入场
          </button>
        </div>
      </header>

      <section className={styles.section}>
        <div key={replayKey} className={pageStyles.grid}>
          {accounts.map((account, index) => (
            <QuotaCard
              key={account.entry.file.name}
              entry={account.entry}
              quota={
                refreshing && account.quota.status === 'success'
                  ? { status: 'loading' }
                  : account.quota
              }
              resolvedTheme={resolvedTheme}
              canRefresh
              resetting={false}
              entranceDelayMs={index * CARD_STAGGER_MS}
              onRefresh={simulateRefresh}
              onReset={() => undefined}
            />
          ))}
        </div>
      </section>
    </div>
  );
}
