import type { TFunction } from 'i18next';
import type { RoutingSettings } from '@/types/routing';

export function formatRoutingDuration(t: TFunction, seconds: number): string {
  if (seconds < 60) return t('routing.duration_seconds', { count: Math.max(1, seconds) });
  if (seconds < 3600) return t('routing.duration_minutes', { count: Math.ceil(seconds / 60) });
  return t('routing.duration_hours', { count: Math.ceil(seconds / 3600) });
}

export const SETTING_LABEL_KEYS: Record<keyof RoutingSettings, string> = {
  strategy: 'routing.strategy_label',
  sessionAffinity: 'routing.affinity_label',
  requestRetry: 'routing.request_retry',
  maxRetryCredentials: 'routing.max_retry_credentials',
};

export function formatSettingValue(
  t: TFunction,
  field: keyof RoutingSettings,
  value: RoutingSettings[keyof RoutingSettings]
): string {
  if (field === 'strategy') return t(`routing.strategy_${String(value).replace(/-/g, '_')}`);
  if (typeof value === 'boolean') return t(value ? 'routing.value_on' : 'routing.value_off');
  if (field === 'maxRetryCredentials' && value === 0) return t('routing.unlimited');
  return String(value);
}

export function formatWeightValue(t: TFunction, value: number | null): string {
  return value === null ? t('routing.value_default') : String(value);
}
