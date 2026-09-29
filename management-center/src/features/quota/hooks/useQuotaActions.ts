/**
 * 单卡额度操作：刷新 + Codex 重置积分。
 * 流程 1:1 移植旧 QuotaSection（confirm modal、resetting 再入守卫、
 * generation-guarded commit、成功/失败通知），仅把 config 换成 adapter。
 */

import { useCallback, useState } from 'react';
import { useTranslation } from 'react-i18next';
import {
  captureQuotaCacheGeneration,
  commitIfQuotaCacheCurrent,
  useNotificationStore,
} from '@/stores';
import type { AuthFileItem } from '@/types';
import { getStatusFromError } from '@/utils/quota';
import { getQuotaCacheKey } from '@/utils/quota/identity';
import { getQuotaMap, getQuotaSetter, type QuotaAdapter, type QuotaCardState } from '../providers';

const getQuotaState = (adapter: QuotaAdapter, file: AuthFileItem): QuotaCardState | undefined =>
  getQuotaMap(adapter)[getQuotaCacheKey(file)];

export function useQuotaActions(disableControls: boolean) {
  const { t } = useTranslation();
  const showNotification = useNotificationStore((state) => state.showNotification);
  const showConfirmation = useNotificationStore((state) => state.showConfirmation);
  const [resettingQuotaName, setResettingQuotaName] = useState<string | null>(null);

  const refreshQuota = useCallback(
    async (file: AuthFileItem, adapter: QuotaAdapter) => {
      if (disableControls || file.disabled) return;
      const cacheKey = getQuotaCacheKey(file);
      if (resettingQuotaName === cacheKey) return;
      if (getQuotaState(adapter, file)?.status === 'loading') return;
      const cacheGeneration = captureQuotaCacheGeneration(file.name);
      const setQuota = getQuotaSetter(adapter);

      setQuota((prev) => ({
        ...prev,
        [cacheKey]: adapter.buildLoadingState(),
      }));

      try {
        const data = await adapter.fetchQuota(file, t);
        commitIfQuotaCacheCurrent(cacheGeneration, () => {
          setQuota((prev) => ({
            ...prev,
            [cacheKey]: adapter.buildSuccessState(data),
          }));
          showNotification(t('auth_files.quota_refresh_success', { name: file.name }), 'success');
        });
      } catch (err: unknown) {
        const message = err instanceof Error ? err.message : t('common.unknown_error');
        const status = getStatusFromError(err);
        commitIfQuotaCacheCurrent(cacheGeneration, () => {
          setQuota((prev) => ({
            ...prev,
            [cacheKey]: adapter.buildErrorState(message, status),
          }));
          showNotification(
            t('auth_files.quota_refresh_failed', { name: file.name, message }),
            'error'
          );
        });
      }
    },
    [disableControls, resettingQuotaName, showNotification, t]
  );

  const resetQuota = useCallback(
    (file: AuthFileItem, adapter: QuotaAdapter) => {
      const resetQuotaFn = adapter.resetQuota;
      if (!resetQuotaFn) return;
      if (disableControls || file.disabled) return;
      const cacheKey = getQuotaCacheKey(file);
      if (getQuotaState(adapter, file)?.status === 'loading') return;
      if (resettingQuotaName === cacheKey) return;

      showConfirmation({
        title: t('codex_quota.reset_confirm_title'),
        message: t('codex_quota.reset_confirm_message', { name: file.name }),
        confirmText: t('codex_quota.reset_confirm_button'),
        variant: 'primary',
        onConfirm: async () => {
          const cacheGeneration = captureQuotaCacheGeneration(file.name);
          const setQuota = getQuotaSetter(adapter);
          setResettingQuotaName(cacheKey);
          try {
            const data = await resetQuotaFn(file, t);
            commitIfQuotaCacheCurrent(cacheGeneration, () => {
              setQuota((prev) => ({
                ...prev,
                [cacheKey]: adapter.buildSuccessState(data),
              }));
              showNotification(t('codex_quota.reset_success', { name: file.name }), 'success');
            });
          } catch (err: unknown) {
            const message = err instanceof Error ? err.message : t('common.unknown_error');
            commitIfQuotaCacheCurrent(cacheGeneration, () => {
              showNotification(
                t('codex_quota.reset_failed', { name: file.name, message }),
                'error'
              );
            });
          } finally {
            setResettingQuotaName((current) => (current === cacheKey ? null : current));
          }
        },
      });
    },
    [disableControls, resettingQuotaName, showConfirmation, showNotification, t]
  );

  return { resettingQuotaName, refreshQuota, resetQuota };
}
